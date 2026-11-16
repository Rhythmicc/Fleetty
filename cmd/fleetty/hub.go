package main

import (
	stdbytes "bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/x/ansi"
)

const (
	hubConfigVersion          = 1
	hubGroupStyleGPU          = "gpu"
	hubGroupStyleCPU          = "cpu"
	hubGroupStyleNetwork      = "network"
	hubGroupStyleProcess      = "process"
	defaultHubRefreshInterval = time.Second
	hubSparkCap               = 44 // also the widest rack history chart
	hubOfflineRetryInitial    = 5 * time.Second
	hubOfflineRetryMaximum    = 30 * time.Second
)

type hubConfig struct {
	Version                           int                  `json:"version"`
	Name                              string               `json:"name,omitempty"`
	RefreshSeconds                    int                  `json:"refresh_seconds,omitempty"`
	InsecureSkipHostKey               bool                 `json:"insecure_skip_host_key,omitempty"`
	InsecureAllowUnauthenticatedNodes bool                 `json:"insecure_allow_unauthenticated_nodes,omitempty"`
	Groups                            []hubGroupConfig     `json:"groups,omitempty"`
	Nodes                             []hubNodeConfig      `json:"nodes"`
	SlurmClusters                     []slurmClusterConfig `json:"slurm_clusters,omitempty"`
}

type hubGroupConfig struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Style string `json:"style"`
}

type hubNodeConfig struct {
	Name                 string `json:"name"`
	Group                string `json:"group"`
	Address              string `json:"address"`
	Description          string `json:"description,omitempty"`
	Profile              string `json:"profile,omitempty"`
	SlurmCluster         string `json:"slurm_cluster,omitempty"`
	SlurmNode            string `json:"slurm_node,omitempty"`
	HostKey              string `json:"host_key,omitempty"`
	IdentityFile         string `json:"identity_file,omitempty"`
	InsecureSkipHostKey  bool   `json:"-"`
	AllowUnauthenticated bool   `json:"-"`
}

func loadHubConfig(path string) (*hubConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config hubConfig
	decoder := json.NewDecoder(stdbytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: trailing JSON value", path)
	}
	if config.Version != hubConfigVersion {
		return nil, fmt.Errorf("unsupported hub configuration version %d; expected %d", config.Version, hubConfigVersion)
	}
	if len(config.Nodes) == 0 && len(config.SlurmClusters) == 0 {
		return nil, errors.New("hub configuration has no nodes or Slurm clusters")
	}
	config.Name = sanitizeTerminalText(config.Name)
	if config.Name == "" {
		config.Name = "Fleetty Hub"
	}
	groupIDs := make(map[string]struct{}, len(config.Groups))
	for index := range config.Groups {
		group := &config.Groups[index]
		group.ID = strings.TrimSpace(group.ID)
		group.Title = sanitizeTerminalText(group.Title)
		group.Style = strings.ToLower(strings.TrimSpace(group.Style))
		if !validHubGroupID(group.ID) {
			return nil, fmt.Errorf("hub group %d has invalid id %q; use lowercase letters, digits, hyphens, or underscores", index+1, group.ID)
		}
		if group.Title == "" {
			return nil, fmt.Errorf("hub group %q requires title", group.ID)
		}
		if !validHubGroupStyle(group.Style) {
			return nil, fmt.Errorf("hub group %q has invalid style %q", group.ID, group.Style)
		}
		if _, exists := groupIDs[group.ID]; exists {
			return nil, fmt.Errorf("duplicate hub group id %q", group.ID)
		}
		groupIDs[group.ID] = struct{}{}
	}
	if len(config.Nodes) > 0 && len(config.Groups) == 0 {
		return nil, errors.New("hub configuration with nodes requires groups")
	}
	seen := make(map[string]struct{}, len(config.Nodes))
	for index := range config.Nodes {
		node := &config.Nodes[index]
		node.Name = sanitizeTerminalText(node.Name)
		node.Group = strings.TrimSpace(node.Group)
		node.Description = sanitizeTerminalText(node.Description)
		node.SlurmCluster = sanitizeTerminalText(node.SlurmCluster)
		node.SlurmNode = sanitizeTerminalText(node.SlurmNode)
		rawProfile := strings.TrimSpace(node.Profile)
		node.Profile = normalizeMachineProfile(rawProfile)
		if rawProfile != "" && node.Profile == "" {
			return nil, fmt.Errorf("hub node %q has invalid profile %q", node.Name, rawProfile)
		}
		node.Address = strings.TrimSpace(node.Address)
		node.HostKey = strings.TrimSpace(node.HostKey)
		node.IdentityFile = strings.TrimSpace(node.IdentityFile)
		node.InsecureSkipHostKey = config.InsecureSkipHostKey
		node.AllowUnauthenticated = config.InsecureAllowUnauthenticatedNodes
		if node.Name == "" || node.Address == "" {
			return nil, fmt.Errorf("hub node %d requires name and address", index+1)
		}
		if _, exists := groupIDs[node.Group]; !exists {
			return nil, fmt.Errorf("hub node %q references unknown group %q", node.Name, node.Group)
		}
		if _, exists := seen[node.Name]; exists {
			return nil, fmt.Errorf("duplicate hub node name %q", node.Name)
		}
		seen[node.Name] = struct{}{}
		if node.HostKey == "" && !config.InsecureSkipHostKey {
			return nil, fmt.Errorf("hub node %q requires host_key", node.Name)
		}
		if node.IdentityFile == "" && !config.InsecureAllowUnauthenticatedNodes {
			return nil, fmt.Errorf(
				"hub node %q requires identity_file; use insecure_allow_unauthenticated_nodes only during migration",
				node.Name,
			)
		}
	}
	seenClusters := make(map[string]struct{}, len(config.SlurmClusters))
	for index := range config.SlurmClusters {
		cluster := &config.SlurmClusters[index]
		cluster.Name = sanitizeTerminalText(cluster.Name)
		cluster.Description = sanitizeTerminalText(cluster.Description)
		cluster.Transport = strings.ToLower(strings.TrimSpace(cluster.Transport))
		cluster.Address = strings.TrimSpace(cluster.Address)
		cluster.User = strings.TrimSpace(cluster.User)
		cluster.IdentityFile = strings.TrimSpace(cluster.IdentityFile)
		cluster.HostKey = strings.TrimSpace(cluster.HostKey)
		cluster.HostKeys = uniqueTerminalValues(cluster.HostKeys)
		if cluster.HostKey != "" {
			cluster.HostKeys = appendUnique(cluster.HostKeys, cluster.HostKey)
		}
		cluster.InsecureSkipHostKey = config.InsecureSkipHostKey
		if cluster.Transport == "" {
			if cluster.Address == "" {
				cluster.Transport = "local"
			} else {
				cluster.Transport = "ssh"
			}
		}
		if cluster.Name == "" {
			return nil, fmt.Errorf("Slurm cluster %d requires name", index+1)
		}
		if _, exists := seenClusters[cluster.Name]; exists {
			return nil, fmt.Errorf("duplicate Slurm cluster name %q", cluster.Name)
		}
		seenClusters[cluster.Name] = struct{}{}
		switch cluster.Transport {
		case "local":
		case "ssh":
			if cluster.Address == "" || cluster.User == "" || cluster.IdentityFile == "" {
				return nil, fmt.Errorf("Slurm cluster %q using ssh requires address, user, and identity_file", cluster.Name)
			}
			if len(cluster.HostKeys) == 0 && !config.InsecureSkipHostKey {
				return nil, fmt.Errorf("Slurm cluster %q requires host_key or host_keys", cluster.Name)
			}
		default:
			return nil, fmt.Errorf("Slurm cluster %q has invalid transport %q", cluster.Name, cluster.Transport)
		}
		cluster.Partitions = uniqueTerminalValues(cluster.Partitions)
	}
	for _, node := range config.Nodes {
		if (node.SlurmCluster == "") != (node.SlurmNode == "") {
			return nil, fmt.Errorf("hub node %q requires both slurm_cluster and slurm_node", node.Name)
		}
		if node.SlurmCluster != "" {
			if _, exists := seenClusters[node.SlurmCluster]; !exists {
				return nil, fmt.Errorf("hub node %q references unknown Slurm cluster %q", node.Name, node.SlurmCluster)
			}
		}
	}
	return &config, nil
}

func validHubGroupID(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		isLowerLetter := r >= 'a' && r <= 'z'
		isSuffixCharacter := index > 0 && ((r >= '0' && r <= '9') || r == '-' || r == '_')
		if isLowerLetter || isSuffixCharacter {
			continue
		}
		return false
	}
	return true
}

func validHubGroupStyle(value string) bool {
	switch value {
	case hubGroupStyleGPU, hubGroupStyleCPU, hubGroupStyleNetwork, hubGroupStyleProcess:
		return true
	default:
		return false
	}
}

func uniqueTerminalValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = sanitizeTerminalText(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (c hubConfig) displayName() string {
	if name := sanitizeTerminalText(c.Name); name != "" {
		return name
	}
	return "Fleetty Hub"
}

func (c hubConfig) refreshInterval() time.Duration {
	if c.RefreshSeconds < 1 {
		return defaultHubRefreshInterval
	}
	return time.Duration(min(c.RefreshSeconds, 60)) * time.Second
}

type hubSparkSample struct {
	At       time.Time // start of the Hub refresh interval
	CPU, GPU float64
	Net      uint64
}

type hubNodeState struct {
	Snapshot            monitorSnapshot
	Spark               []hubSparkSample
	Error               string
	Delay               string
	Warning             string
	Latency             time.Duration
	Checked             time.Time
	LastSeen            time.Time
	NextRetry           time.Time
	ConsecutiveFailures int
}

// hubService shares one recent overview snapshot between all connected Hub
// sessions. Per-node detail pages remain live and session-specific.
type hubService struct {
	config         hubConfig
	mu             sync.RWMutex
	slurmCollectMu sync.Mutex
	startOnce      sync.Once
	viewers        atomic.Int64
	nodeWake       []chan struct{}
	slurmWake      chan struct{}
	slurmRunners   []slurmCommandRunner
	states         []hubNodeState
	slurmStates    []slurmClusterState
}

func newHubService(config hubConfig) *hubService {
	nodeWake := make([]chan struct{}, len(config.Nodes))
	for i := range nodeWake {
		nodeWake[i] = make(chan struct{}, 1)
	}
	return &hubService{
		config: config, states: make([]hubNodeState, len(config.Nodes)),
		slurmStates:  make([]slurmClusterState, len(config.SlurmClusters)),
		slurmRunners: make([]slurmCommandRunner, len(config.SlurmClusters)),
		nodeWake:     nodeWake,
		slurmWake:    make(chan struct{}, 1),
	}
}

// Track interactive Hub sessions after the RPC and active-terminal checks.
// The handler returns on normal exit, disconnect, or terminal errors.
func (s *hubService) trackPanel(next ssh.Handler) ssh.Handler {
	if s == nil {
		return next
	}
	return func(session ssh.Session) {
		s.viewers.Add(1)
		defer s.viewers.Add(-1)
		next(session)
	}
}

// start owns the polling workers for the whole Hub process. Connected users
// only read its cached snapshots, so adding users never multiplies node RPCs.
func (s *hubService) start(ctx context.Context) {
	s.startOnce.Do(func() {
		for index := range s.config.Nodes {
			go s.runNode(ctx, index, fetchHubNodeSnapshot)
		}
		go s.runSlurm(ctx)
	})
}

func (s *hubService) runSlurm(ctx context.Context) {
	if len(s.config.SlurmClusters) == 0 {
		return
	}
	ticker := time.NewTicker(s.slurmRefreshInterval())
	defer ticker.Stop()
	defer s.closeSlurmRunners()
	for {
		s.collectSlurm()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.slurmWake:
		}
	}
}

func (s *hubService) slurmRefreshInterval() time.Duration {
	interval := s.config.SlurmClusters[0].refreshInterval()
	for _, cluster := range s.config.SlurmClusters[1:] {
		if candidate := cluster.refreshInterval(); candidate < interval {
			interval = candidate
		}
	}
	return interval
}

func (s *hubService) snapshot() ([]hubNodeState, []slurmClusterState) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]hubNodeState(nil), s.states...),
		append([]slurmClusterState(nil), s.slurmStates...)
}

func (s *hubService) requestCollect() {
	for _, wake := range s.nodeWake {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	select {
	case s.slurmWake <- struct{}{}:
	default:
	}
}

func (s *hubService) closeSlurmRunners() {
	s.slurmCollectMu.Lock()
	defer s.slurmCollectMu.Unlock()
	for index, runner := range s.slurmRunners {
		if runner != nil {
			_ = runner.Close()
			s.slurmRunners[index] = nil
		}
	}
}

func (s *hubService) collectSlurm() []slurmClusterState {
	if len(s.config.SlurmClusters) == 0 {
		return nil
	}
	s.slurmCollectMu.Lock()
	defer s.slurmCollectMu.Unlock()

	s.mu.RLock()
	previous := append([]slurmClusterState(nil), s.slurmStates...)
	s.mu.RUnlock()
	states := collectSlurmStates(s.config.SlurmClusters, previous, time.Now(), s.slurmRunners)
	s.mu.Lock()
	s.slurmStates = append([]slurmClusterState(nil), states...)
	s.mu.Unlock()
	return states
}

func hubOfflineRetryDelay(failures int) time.Duration {
	delay := hubOfflineRetryInitial
	for attempt := 1; attempt < failures && delay < hubOfflineRetryMaximum; attempt++ {
		delay *= 2
		if delay > hubOfflineRetryMaximum {
			delay = hubOfflineRetryMaximum
		}
	}
	return delay
}

func (s *hubService) retryOfflineNow() {
	s.mu.Lock()
	for index := range s.slurmStates {
		if s.slurmStates[index].ConsecutiveFailures > 0 {
			s.slurmStates[index].NextRetry = time.Time{}
		}
	}
	s.mu.Unlock()
	s.requestCollect()
}

type hubSnapshotsMsg struct {
	States      []hubNodeState
	SlurmStates []slurmClusterState
}

type hubTickMsg struct{}

type hubModel struct {
	service         *hubService
	config          hubConfig
	states          []hubNodeState
	width           int
	height          int
	cursor          int
	collecting      bool
	colorMode       colorMode
	theme           terminalTheme
	status          string
	detail          *monitorModel
	queueExpanded   bool
	slurmFilter     int
	slurmOffset     int
	slurmCursor     int
	slurmExplain    bool
	slurmJobID      string
	slurmJobCluster string
	slurmStates     []slurmClusterState
	detailFixture   func(int) *monitorModel
}

type hubNodeGroup struct {
	title string
	style string
	nodes []int
}

func newHubModel(service *hubService, width, height int) *hubModel {
	states, slurmStates := service.snapshot()
	return &hubModel{
		service:       service,
		config:        service.config,
		states:        states,
		slurmStates:   slurmStates,
		slurmFilter:   -1,
		queueExpanded: len(service.config.Nodes) == 0 && len(service.config.SlurmClusters) > 0,
		width:         width,
		height:        height,
		colorMode:     parseColorMode(os.Getenv("DEFAULT_THEME")),
		theme:         newTerminalTheme(os.Getenv("DEFAULT_THEME")),
		status:        "Select a server to open its live dashboard.",
	}
}

func (m *hubModel) Init() tea.Cmd {
	return tea.Batch(m.theme.init(), m.startCollect(), hubTick(m.config.refreshInterval()))
}

func hubTick(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg { return hubTickMsg{} })
}

func (m *hubModel) startCollect() tea.Cmd {
	if m.collecting {
		return nil
	}
	m.collecting = true
	service := m.service
	cached := hubSnapshotsMsg{States: m.states, SlurmStates: m.slurmStates}
	return func() tea.Msg {
		if service != nil {
			states, slurmStates := service.snapshot()
			return hubSnapshotsMsg{States: states, SlurmStates: slurmStates}
		}
		return cached
	}
}

func (m *hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.theme.update(msg, &m.colorMode) {
		if m.detail != nil {
			m.detail.colorMode, m.detail.theme = m.colorMode, m.theme
		}
		return m, nil
	}
	// Hub refresh messages belong to the parent model even while a node
	// dashboard is open. Forwarding either message to the child drops the
	// refresh timer chain and can leave collecting stuck at true.
	switch msg := msg.(type) {
	case hubSnapshotsMsg:
		m.collecting = false
		m.states = msg.States
		m.slurmStates = msg.SlurmStates
		m.clampSlurmCursor()
		if m.detail != nil {
			m.detail.slurmQueue = m.nodeSlurmQueue(m.cursor)
		} else {
			m.updateOnlineStatus()
		}
		return m, nil
	case hubTickMsg:
		return m, tea.Batch(m.startCollect(), hubTick(m.config.refreshInterval()))
	}

	if m.detail != nil {
		if size, ok := msg.(tea.WindowSizeMsg); ok {
			m.width, m.height = size.Width, size.Height
		}
		if key, ok := msg.(tea.KeyPressMsg); ok && m.detail.screen == screenMonitor {
			if key.String() == "esc" || key.String() == "q" || key.String() == "Q" {
				m.colorMode = m.detail.colorMode
				m.theme = m.detail.theme
				m.detail.adminCredential = ""
				m.detail = nil
				m.status = "Returned to the server overview."
				return m, m.startCollect()
			}
		}
		updated, command := m.detail.Update(msg)
		if child, ok := updated.(*monitorModel); ok {
			m.detail = child
			m.colorMode, m.theme = child.colorMode, child.theme
		}
		return m, command
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampCursor()
		if m.slurmExplain && m.hubQueueHeight() < 4 {
			m.slurmExplain = false
		}
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if m.queueExpanded {
				if index, ok := m.slurmJobAt(msg.Mouse().X, msg.Mouse().Y); ok {
					m.slurmExplain = m.slurmCursor == index && m.hubQueueHeight() >= 4
					m.slurmCursor = index
					m.rememberSlurmSelection()
				}
				return m, nil
			}
			if index, ok := m.nodeAt(msg.Mouse().X, msg.Mouse().Y); ok {
				if m.cursor == index {
					return m, m.openSelected()
				}
				m.cursor = index
				return m, nil
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q", "Q":
			if m.queueExpanded {
				if m.slurmExplain {
					m.slurmExplain = false
					return m, nil
				}
				if len(m.config.Nodes) > 0 {
					m.queueExpanded = false
					return m, nil
				}
			}
			return m, tea.Quit
		case "t", "T":
			m.toggleColorMode()
		case "r":
			m.status = "Refreshing servers and Slurm queues…"
			if m.service != nil {
				m.service.retryOfflineNow()
			}
			return m, m.startCollect()
		case "s", "S":
			if len(m.config.SlurmClusters) > 0 && len(m.config.Nodes) > 0 {
				m.queueExpanded = !m.queueExpanded
				m.slurmExplain = false
				if m.queueExpanded {
					m.clampSlurmCursor()
					m.rememberSlurmSelection()
				}
			}
		case "esc":
			if m.slurmExplain {
				m.slurmExplain = false
				break
			}
			if m.queueExpanded && len(m.config.Nodes) > 0 {
				m.queueExpanded = false
				break
			}
			return m, tea.Quit
		case "a", "A":
			if m.queueExpanded && !m.slurmExplain {
				m.moveSlurmFilter(-len(m.config.SlurmClusters))
			}
		case "left", "h":
			if m.queueExpanded {
				if m.slurmExplain {
					break
				}
				m.moveSlurmFilter(-1)
				break
			}
			m.moveCursorVertical(-1)
		case "right", "l":
			if m.queueExpanded {
				if m.slurmExplain {
					break
				}
				m.moveSlurmFilter(1)
				break
			}
			m.moveCursorVertical(1)
		case "up", "k":
			if m.queueExpanded {
				m.moveSlurmCursor(-1)
				break
			}
			m.moveCursorVertical(-1)
		case "down", "j":
			if m.queueExpanded {
				m.moveSlurmCursor(1)
				break
			}
			m.moveCursorVertical(1)
		case "pgup":
			if m.queueExpanded {
				m.moveSlurmCursor(-m.hubQueuePageSize())
			} else {
				m.moveCursorVertical(-max(1, m.height-5))
			}
		case "pgdown":
			if m.queueExpanded {
				m.moveSlurmCursor(m.hubQueuePageSize())
			} else {
				m.moveCursorVertical(max(1, m.height-5))
			}
		case "enter":
			if m.queueExpanded {
				if m.hubQueueHeight() >= 4 && len(m.selectedSlurmJobs()) > 0 {
					m.slurmExplain = !m.slurmExplain
					m.rememberSlurmSelection()
				}
				break
			}
			return m, m.openSelected()
		}
	}
	return m, nil
}

func (m *hubModel) updateOnlineStatus() {
	live := 0
	for _, state := range m.states {
		if state.Error == "" && state.Delay == "" && !state.Snapshot.CollectedAt.IsZero() {
			live++
		}
	}
	slurmOnline := 0
	for _, state := range m.slurmStates {
		if state.Error == "" && !state.Snapshot.CollectedAt.IsZero() {
			slurmOnline++
		}
	}
	if len(m.slurmStates) > 0 {
		m.status = fmt.Sprintf("%d/%d servers live · %d/%d Slurm sources online.",
			live, len(m.states), slurmOnline, len(m.slurmStates))
		return
	}
	m.status = fmt.Sprintf("%d/%d servers live.", live, len(m.states))
}

func (m *hubModel) openSelected() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.config.Nodes) {
		return nil
	}
	node := m.config.Nodes[m.cursor]
	if m.cursor < len(m.states) && m.states[m.cursor].Error != "" {
		m.status = fmt.Sprintf("%s is offline. Press r to retry now.", node.Name)
		return nil
	}
	if m.detailFixture != nil {
		m.detail = m.detailFixture(m.cursor)
	} else {
		m.detail = newRemoteMonitorModel(node, m.width, m.height, m.colorMode)
	}
	m.detail.theme = m.theme
	m.detail.slurmQueue = m.nodeSlurmQueue(m.cursor)
	m.detail.status = fmt.Sprintf("Connected through Hub to %s. Esc returns to the server list.", node.Name)
	if m.detailFixture != nil {
		m.detail.status = "Simulated node. Esc returns to the server list."
		return nil
	}
	return tea.Batch(m.detail.startCollect(), tick())
}

func (m *hubModel) nodeGroups() []hubNodeGroup {
	groups := make([]hubNodeGroup, len(m.config.Groups))
	groupIndexes := make(map[string]int, len(m.config.Groups))
	for index, configured := range m.config.Groups {
		groups[index] = hubNodeGroup{title: configured.Title, style: configured.Style}
		groupIndexes[configured.ID] = index
	}
	for index, node := range m.config.Nodes {
		if groupIndex, exists := groupIndexes[node.Group]; exists {
			groups[groupIndex].nodes = append(groups[groupIndex].nodes, index)
		}
	}
	filtered := groups[:0]
	for _, group := range groups {
		if len(group.nodes) > 0 {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func (m *hubModel) nodeOrder() []int {
	order := make([]int, 0, len(m.config.Nodes))
	for _, group := range m.nodeGroups() {
		order = append(order, group.nodes...)
	}
	return order
}

func (m *hubModel) View() tea.View {
	if m.detail != nil {
		view := m.detail.View()
		view.WindowTitle = m.config.displayName() + " · " + m.config.Nodes[m.cursor].Name
		return view
	}
	body := m.hubView()
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	view := tea.NewView(body)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = m.config.displayName()
	view.BackgroundColor, view.ForegroundColor = m.theme.colors(m.colorMode)
	return view
}

func hubNodeAddressLabel(address string) string {
	if strings.TrimSpace(address) == "" {
		return "IP --"
	}
	host, _, err := net.SplitHostPort(normalizeNodeAddress(address))
	if err != nil {
		return "HOST " + sanitizeTerminalText(address)
	}
	label := "IP "
	if _, err := netip.ParseAddr(host); err != nil {
		label = "HOST "
	}
	return label + sanitizeTerminalText(host)
}

func hubShortError(err string, width int) string {
	err = strings.TrimSpace(sanitizeTerminalText(err))
	if err == "" || width <= 0 {
		return ""
	}
	lower := strings.ToLower(err)
	for _, reason := range []string{
		"i/o timeout", "connection refused", "connection reset by peer",
		"no route to host", "network is unreachable", "no such host",
	} {
		if strings.Contains(lower, reason) && lipgloss.Width(reason) <= width {
			return reason
		}
	}
	if lipgloss.Width(err) <= width {
		return err
	}
	if split := strings.LastIndex(err, ": "); split >= 0 {
		tail := strings.TrimSpace(err[split+2:])
		if tail != "" && lipgloss.Width(tail) <= width && strings.Trim(tail, "0123456789") != "" {
			return tail
		}
	}
	return truncate(err, width)
}

func appendSpark(existing []hubSparkSample, sample hubSparkSample) []hubSparkSample {
	// A manual refresh can update this interval, but must not consume an
	// extra column. Never mutate history shared with open Hub sessions.
	end := len(existing)
	if end > 0 && existing[end-1].At.Equal(sample.At) {
		end--
	}
	start := max(0, end+1-hubSparkCap)
	next := make([]hubSparkSample, end-start+1)
	copy(next, existing[start:end])
	next[len(next)-1] = sample
	return next
}

func (node hubNodeConfig) profileFor(snapshot monitorSnapshot) string {
	if strings.TrimSpace(node.Profile) == "" {
		return normalizeMachineProfile(snapshot.Profile)
	}
	return normalizeMachineProfile(node.Profile)
}

func hubSparkFrom(snapshot monitorSnapshot) hubSparkSample {
	gpu, _, _, _ := hubGPUStats(snapshot.GPUs)
	return hubSparkSample{
		CPU: snapshot.CPUPercent,
		GPU: gpu,
		Net: hubTotalNetworkRate(snapshot),
	}
}

type sparkField int

const (
	sparkCPU sparkField = iota
	sparkGPU
	sparkNet
)

func hubSparkline(samples []hubSparkSample, field sparkField, width int, interval time.Duration, end time.Time) string {
	if width <= 0 || len(samples) == 0 || interval <= 0 {
		return hubFixedSparkline(nil, width)
	}
	values := make([]float64, width)
	for i := range values {
		values[i] = math.NaN()
	}
	latest := end.Truncate(interval)
	scale := float64(1 << 20) // Do not magnify idle network chatter to a full bar.
	for _, sample := range samples {
		age := latest.Sub(sample.At.Truncate(interval))
		index := width - 1 - int(age/interval)
		if age < 0 || index < 0 {
			continue
		}
		switch field {
		case sparkNet:
			values[index] = float64(sample.Net)
			scale = math.Max(scale, values[index])
		case sparkGPU:
			values[index] = sample.GPU
		default:
			values[index] = sample.CPU
		}
	}
	if field == sparkNet {
		for index := range values {
			values[index] = values[index] / scale * 100
		}
		// Height is relative to this window, so its peak is not an alarm.
		return hubSparklineColumns(values, width, true)
	}
	return hubFixedSparkline(values, width)
}

// hubFixedSparkline draws one column per sample on a fixed 0–100 scale.
// Samples not collected yet are a faint dot on the left. A real sample is at
// least a dim baseline, so an idle row still reads as present.
func hubFixedSparkline(values []float64, width int) string {
	return hubSparklineColumns(values, width, false)
}

var sparklineBlockGlyphs = [...]string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

func hubSparklineColumns(values []float64, width int, network bool) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	parts := make([]string, width)
	missing := width - len(values)
	for index := 0; index < width; index++ {
		if index < missing || math.IsNaN(values[index-missing]) {
			parts[index] = barTrackStyle.Render("·")
			continue
		}
		value := values[index-missing]
		if value < 0 {
			value = 0
		}
		if value > 100 {
			value = 100
		}
		level := int(value / 100 * float64(len(sparklineBlockGlyphs)))
		if level < 0 {
			level = 0
		}
		if level >= len(sparklineBlockGlyphs) {
			level = len(sparklineBlockGlyphs) - 1
		}
		// The flat baseline stays fainter than a real column.
		if level == 0 {
			parts[index] = barTrackStyle.Render(sparklineBlockGlyphs[0])
			continue
		}
		style := networkRXStyle
		if !network {
			style = loadBandStyle(percentBand(value), true)
		}
		parts[index] = style.Render(sparklineBlockGlyphs[level])
	}
	return strings.Join(parts, "")
}

func hubTotalNetworkRate(snapshot monitorSnapshot) uint64 {
	return snapshot.NetworkRX + snapshot.NetworkTX
}

func hubNetworkRates(snapshot monitorSnapshot, width int) string {
	link := hubLinkBytesPerSec(snapshot)
	rx := networkRXStyle.Render("↓") + loadBandStyle(throughputBand(snapshot.NetworkRX, link), false).Render(hubCompactRate(snapshot.NetworkRX))
	tx := networkTXStyle.Render("↑") + loadBandStyle(throughputBand(snapshot.NetworkTX, link), false).Render(hubCompactRate(snapshot.NetworkTX))
	return ansi.Truncate(rx+" "+tx, width, "")
}

func vramBand(used, total uint64) loadBand {
	if total == 0 {
		return loadNeutral
	}
	return percentBand(float64(used) / float64(total) * 100)
}

func hubLinkBytesPerSec(snapshot monitorSnapshot) uint64 {
	if len(snapshot.NetworkInterfaces) == 0 {
		return 0
	}
	var mbps uint64
	for _, iface := range snapshot.NetworkInterfaces {
		if iface.SpeedMbps == 0 {
			return 0
		}
		mbps += iface.SpeedMbps
	}
	return mbps * 1000 * 1000 / 8
}

func hubCompactRate(value uint64) string {
	const unit = 1024.0
	if value < 1024 {
		return fmt.Sprintf("%d", value)
	}
	scaled := float64(value)
	labels := []string{"", "K", "M", "G", "T", "P", "E"}
	index := 0
	for scaled >= unit && index < len(labels)-1 {
		scaled /= unit
		index++
	}
	return fmt.Sprintf("%.0f%s", math.Round(scaled), labels[index])
}

func hubVRAM(used, total uint64) string {
	if total == 0 {
		return "—"
	}
	const gi = 1024 * 1024 * 1024
	return fmt.Sprintf("%d/%dG", int(math.Round(float64(used)/float64(gi))), int(math.Round(float64(total)/float64(gi))))
}

func (m *hubModel) hubOccupant(index int, snapshot monitorSnapshot) string {
	var users []string
	seen := map[string]bool{}
	add := func(user string) {
		user = strings.TrimSpace(sanitizeTerminalText(user))
		if user == "" || seen[user] {
			return
		}
		seen[user] = true
		users = append(users, user)
	}
	node := m.config.Nodes[index]
	if node.SlurmNode != "" {
		for cluster, config := range m.config.SlurmClusters {
			if config.Name != node.SlurmCluster || cluster >= len(m.slurmStates) || m.slurmStates[cluster].Error != "" {
				continue
			}
			for _, job := range m.slurmStates[cluster].Snapshot.Jobs {
				if isSlurmRunning(job.State) && slurmNodeListContains(job.NodeList, node.SlurmNode) {
					add(job.User)
				}
			}
			break
		}
	}
	for _, gpu := range snapshot.GPUs {
		for _, workload := range gpu.Workloads {
			name := hubBaseName(workload.Name)
			if name == "" || strings.EqualFold(name, "[No data]") {
				continue
			}
			add(workload.User)
		}
	}
	for _, process := range topProcesses(snapshot.Processes, 3) {
		if process.CPU < 5 {
			continue
		}
		if hubBaseName(process.Command) == "" {
			continue
		}
		add(process.User)
	}
	if len(users) == 0 {
		return ""
	}
	text := users[0]
	if extra := len(users) - 1; extra > 0 {
		text += fmt.Sprintf(" +%d", extra)
	}
	return text
}

func hubBaseName(value string) string {
	value = strings.TrimSpace(value)
	if split := strings.LastIndexAny(value, "/\\"); split >= 0 {
		value = value[split+1:]
	}
	return value
}

// Compare the configured filesystems with the host's root disk.
func hubNASDiskUsage(snapshot monitorSnapshot) (mount string, usage float64) {
	mount, usage = "/", percent(snapshot.DiskUsed, snapshot.DiskTotal)
	for _, filesystem := range snapshot.Filesystems {
		if used := percent(filesystem.Used, filesystem.Total); used > usage {
			mount, usage = filesystem.Mount, used
		}
	}
	return mount, usage
}

func hubStorageWarning(snapshot monitorSnapshot) string {
	mount, usage := hubNASDiskUsage(snapshot)
	if usage >= stateThresholds.Warn {
		return fmt.Sprintf("Disk %s %.0f%% used", sanitizeTerminalText(mount), usage)
	}
	for _, filesystem := range snapshot.Filesystems {
		if filesystem.Error != "" {
			return "Disk " + sanitizeTerminalText(filesystem.Mount) + " unavailable"
		}
	}
	return ""
}

func renderNASServiceLine(snapshot monitorSnapshot, width int) string {
	healthyHTTP := 0
	for _, service := range snapshot.Services {
		if service.Healthy {
			healthyHTTP++
		}
	}
	healthyContainers := 0
	for _, container := range snapshot.Containers {
		if dockerContainerHealthy(container) {
			healthyContainers++
		}
	}
	containerHealth := compactHealthCount(healthyContainers, len(snapshot.Containers))
	if snapshot.DockerError != "" {
		containerHealth = dangerStyle.Render("ERR")
	}
	healthyPM2 := 0
	for _, process := range snapshot.PM2Processes {
		if strings.EqualFold(process.Status, "online") {
			healthyPM2++
		}
	}
	pm2Health := compactHealthCount(healthyPM2, len(snapshot.PM2Processes))
	if snapshot.PM2Error != "" {
		pm2Health = dangerStyle.Render("ERR")
	}
	line := dimStyle.Render("HTTP ") + compactHealthCount(healthyHTTP, len(snapshot.Services)) +
		dimStyle.Render("  CTR ") + containerHealth +
		dimStyle.Render("  PM2 ") + pm2Health
	return ansi.Truncate(line, width, "")
}

func compactHealthCount(healthy, total int) string {
	label := fmt.Sprintf("%d/%d", healthy, total)
	if total == 0 {
		return dimStyle.Render("—")
	}
	if healthy == total {
		return okStyle.Render(label)
	}
	if healthy == 0 {
		return dangerStyle.Render(label)
	}
	return warningStyle.Render(label)
}

func hubAge(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Round(time.Second)
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(duration.Minutes()), int(duration.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(duration.Hours()), int(duration.Minutes())%60)
	}
}

func hubGPUStats(gpus []gpuInfo) (maxUtil float64, memoryUsed, memoryTotal uint64, maxTemperature int) {
	for _, gpu := range gpus {
		if gpu.Utilization > maxUtil {
			maxUtil = gpu.Utilization
		}
		memoryUsed += gpu.MemoryUsed
		memoryTotal += gpu.MemoryTotal
		if gpu.Temperature > maxTemperature {
			maxTemperature = gpu.Temperature
		}
	}
	return maxUtil, memoryUsed, memoryTotal, maxTemperature
}

func (m *hubModel) toggleColorMode() {
	m.theme.manual()
	if m.colorMode == colorModeLight {
		m.colorMode = colorModeDark
	} else {
		m.colorMode = colorModeLight
	}
	m.status = fmt.Sprintf("%s theme enabled for this Hub session.", strings.ToUpper(m.colorMode.String()))
}

const (
	hubOverviewRPCTimeout  = 3 * time.Second
	hubNodeOfflineFailures = 3
)

func fetchHubNodeSnapshot(ctx context.Context, node hubNodeConfig) (nodeRPCResponse, error) {
	client := sharedRPCClientRegistry.clientFor(node)
	return client.pool.call(ctx, nodeRPCRequest{
		Version: nodeRPCVersion, Operation: rpcSnapshot,
		IncludeSlurmProgress: node.SlurmCluster != "" && node.SlurmNode != "",
	})
}

// Each node owns one poll at a time and publishes it without waiting for peers.
// Cadence is measured from the start of an attempt, not from its completion.
func (s *hubService) runNode(ctx context.Context, index int, fetch func(context.Context, hubNodeConfig) (nodeRPCResponse, error)) {
	node, interval := s.config.Nodes[index], s.config.refreshInterval()
	timer := time.NewTimer(0)
	defer timer.Stop()
	var state hubNodeState
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.nodeWake[index]:
			// Manual refresh bypasses this node's current backoff.
		}
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		requestCtx, cancel := context.WithTimeout(ctx, hubOverviewRPCTimeout)
		response, err := fetch(requestCtx, node)
		cancel()
		if ctx.Err() != nil {
			return
		}
		state = updateHubNodeState(state, response, err, started, time.Now(), interval)
		s.mu.Lock()
		s.states[index] = state
		s.mu.Unlock()

		next := started.Add(interval)
		if state.NextRetry.After(next) {
			next = state.NextRetry
		}
		timer.Reset(time.Until(next))
	}
}

func updateHubNodeState(state hubNodeState, response nodeRPCResponse, err error, started, checked time.Time, interval time.Duration) hubNodeState {
	state.Latency = checked.Sub(started)
	state.Checked = checked
	if err != nil {
		state.ConsecutiveFailures++
		reason := sanitizeTerminalText(err.Error())
		if state.ConsecutiveFailures < hubNodeOfflineFailures {
			state.Delay = reason
			return state
		}
		state.Error, state.Delay = reason, ""
		state.NextRetry = checked.Add(hubOfflineRetryDelay(state.ConsecutiveFailures - hubNodeOfflineFailures + 1))
		return state
	}
	state.Snapshot = response.Snapshot
	sample := hubSparkFrom(response.Snapshot)
	sample.At = started.Truncate(interval)
	state.Spark = appendSpark(state.Spark, sample)
	state.Error, state.Delay = "", ""
	state.Warning = sanitizeTerminalText(response.Warning)
	state.LastSeen = checked
	state.NextRetry = time.Time{}
	state.ConsecutiveFailures = 0
	return state
}
