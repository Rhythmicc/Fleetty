package main

import (
	stdbytes "bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
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
	hubCardHeight             = 7
	hubOverviewRPCTimeout     = 900 * time.Millisecond
	hubHistoryRPCTimeout      = 900 * time.Millisecond
	hubHistoryRefreshInterval = time.Minute
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

type hubNodeState struct {
	Snapshot            monitorSnapshot
	History             []historySample
	historyAt           time.Time
	Error               string
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
	collectMu      sync.Mutex
	slurmCollectMu sync.Mutex
	startOnce      sync.Once
	nodeWake       chan struct{}
	slurmWake      chan struct{}
	slurmRunners   []slurmCommandRunner
	states         []hubNodeState
	slurmStates    []slurmClusterState
	collectedAt    time.Time
}

func newHubService(config hubConfig) *hubService {
	return &hubService{
		config: config, states: make([]hubNodeState, len(config.Nodes)),
		slurmStates:  make([]slurmClusterState, len(config.SlurmClusters)),
		slurmRunners: make([]slurmCommandRunner, len(config.SlurmClusters)),
		nodeWake:     make(chan struct{}, 1),
		slurmWake:    make(chan struct{}, 1),
	}
}

// start owns the one polling loop for the whole Hub process. Connected users
// only read its cached snapshots, so adding users never multiplies node RPCs.
func (s *hubService) start(ctx context.Context) {
	s.startOnce.Do(func() {
		go s.runNodes(ctx)
		go s.runSlurm(ctx)
	})
}

func (s *hubService) runNodes(ctx context.Context) {
	ticker := time.NewTicker(s.config.refreshInterval())
	defer ticker.Stop()
	for {
		s.collect()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.nodeWake:
		}
	}
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
	select {
	case s.nodeWake <- struct{}{}:
	default:
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

func (s *hubService) collect() []hubNodeState {
	s.collectMu.Lock()
	defer s.collectMu.Unlock()

	s.mu.RLock()
	if !s.collectedAt.IsZero() && time.Since(s.collectedAt) < s.config.refreshInterval()/2 {
		cached := append([]hubNodeState(nil), s.states...)
		s.mu.RUnlock()
		return cached
	}
	s.mu.RUnlock()

	s.mu.RLock()
	previous := append([]hubNodeState(nil), s.states...)
	s.mu.RUnlock()
	states := collectHubNodeStatesWithPrevious(s.config.Nodes, previous, time.Now())
	s.mu.Lock()
	s.states = append([]hubNodeState(nil), states...)
	s.collectedAt = time.Now()
	s.mu.Unlock()
	return states
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

func collectHubNodeStates(nodes []hubNodeConfig) []hubNodeState {
	return collectHubNodeStatesWithPrevious(nodes, nil, time.Now())
}

func collectHubNodeStatesWithPrevious(nodes []hubNodeConfig, previous []hubNodeState, now time.Time) []hubNodeState {
	states := make([]hubNodeState, len(nodes))
	var wait sync.WaitGroup
	for index := range nodes {
		if index < len(previous) {
			states[index] = previous[index]
			if states[index].Error != "" && now.Before(states[index].NextRetry) {
				continue
			}
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			started := time.Now()
			client := sharedRPCClientRegistry.clientFor(nodes[index])
			response, err := client.CallWithTimeout(
				nodeRPCRequest{Operation: rpcSnapshot},
				hubOverviewRPCTimeout,
			)
			checked := time.Now()
			state := states[index]
			state.Latency = time.Since(started)
			state.Checked = checked
			if err != nil {
				state.Error = sanitizeTerminalText(err.Error())
				state.Warning = ""
				state.ConsecutiveFailures++
				state.NextRetry = checked.Add(hubOfflineRetryDelay(state.ConsecutiveFailures))
				states[index] = state
				return
			}
			state.Snapshot = response.Snapshot
			state.Error = ""
			state.Warning = sanitizeTerminalText(response.Warning)
			state.LastSeen = checked
			state.NextRetry = time.Time{}
			state.ConsecutiveFailures = 0
			if state.History == nil ||
				checked.Sub(state.historyAt) >= hubHistoryRefreshInterval {
				historyResponse, historyErr := client.CallWithTimeout(
					nodeRPCRequest{Operation: rpcHistory, HistoryMinutes: 60},
					hubHistoryRPCTimeout,
				)
				// History is a best-effort supplement to the live card; a
				// failure keeps the previous curve and retries next minute.
				if historyErr == nil {
					state.History = historyResponse.History
				}
				state.historyAt = checked
			}
			states[index] = state
		}(index)
	}
	wait.Wait()
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
	for index := range s.states {
		if s.states[index].Error != "" {
			s.states[index].NextRetry = time.Time{}
		}
	}
	for index := range s.slurmStates {
		if s.slurmStates[index].ConsecutiveFailures > 0 {
			s.slurmStates[index].NextRetry = time.Time{}
		}
	}
	s.collectedAt = time.Time{}
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
	offset          int
	collecting      bool
	colorMode       colorMode
	status          string
	detail          *monitorModel
	slurmView       bool
	slurmFilter     int
	slurmOffset     int
	slurmCursor     int
	slurmExplain    bool
	slurmJobID      string
	slurmJobCluster string
	slurmStates     []slurmClusterState
}

type hubNodeGroup struct {
	title string
	style string
	nodes []int
}

type hubDisplayRow struct {
	title string
	style string
	count int
	nodes []int
}

type hubPage struct {
	rows []hubDisplayRow
}

func newHubModel(service *hubService, _ ssh.Session, width, height int) *hubModel {
	states, slurmStates := service.snapshot()
	return &hubModel{
		service:     service,
		config:      service.config,
		states:      states,
		slurmStates: slurmStates,
		slurmFilter: -1,
		slurmView:   len(service.config.Nodes) == 0 && len(service.config.SlurmClusters) > 0,
		width:       width,
		height:      height,
		colorMode:   parseColorMode(os.Getenv("DEFAULT_THEME")),
		status:      "Select a server to open its live dashboard.",
	}
}

func (m *hubModel) Init() tea.Cmd {
	return tea.Batch(m.startCollect(), hubTick(m.config.refreshInterval()))
}

func hubTick(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg { return hubTickMsg{} })
}

func (m *hubModel) startCollect() tea.Cmd {
	if m.collecting {
		return nil
	}
	m.collecting = true
	nodes := append([]hubNodeConfig(nil), m.config.Nodes...)
	clusters := append([]slurmClusterConfig(nil), m.config.SlurmClusters...)
	service := m.service
	return func() tea.Msg {
		if service != nil {
			states, slurmStates := service.snapshot()
			return hubSnapshotsMsg{States: states, SlurmStates: slurmStates}
		}
		return hubSnapshotsMsg{
			States:      collectHubNodeStates(nodes),
			SlurmStates: collectSlurmStatesWithPrevious(clusters, nil, time.Now()),
		}
	}
}

func (m *hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if key, ok := msg.(tea.KeyPressMsg); ok && m.detail.screen == screenMonitor {
			if key.String() == "esc" || key.String() == "q" || key.String() == "Q" {
				m.colorMode = m.detail.colorMode
				m.detail.adminCredential = ""
				m.detail = nil
				m.status = "Returned to the server overview."
				return m, m.startCollect()
			}
		}
		updated, command := m.detail.Update(msg)
		if child, ok := updated.(*monitorModel); ok {
			m.detail = child
		}
		return m, command
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampCursor()
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if m.slurmView {
				if m.slurmExplain {
					m.slurmExplain = false
					m.slurmJobID, m.slurmJobCluster = "", ""
					return m, nil
				}
				if index, ok := m.slurmClusterAt(msg.Mouse().X, msg.Mouse().Y); ok {
					m.slurmFilter = index
					m.slurmOffset = 0
					m.slurmCursor = 0
					m.slurmJobID, m.slurmJobCluster = "", ""
					m.status = "Showing jobs from " + m.config.SlurmClusters[index].Name + "."
				} else if index, ok := m.slurmJobAt(msg.Mouse().X, msg.Mouse().Y); ok {
					m.slurmCursor = index
					m.slurmExplain = true
					m.rememberSlurmSelection()
				}
				return m, nil
			}
			if index, ok := m.nodeAt(msg.Mouse().X, msg.Mouse().Y); ok {
				m.cursor = index
				return m, m.openSelected()
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q", "Q":
			if m.slurmView {
				if m.slurmExplain {
					m.slurmExplain = false
					m.slurmJobID, m.slurmJobCluster = "", ""
					m.status = "Returned to the Slurm queue."
					return m, nil
				}
				m.slurmView = false
				m.slurmOffset = 0
				m.status = "Returned to the server overview."
				return m, nil
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
		case "tab":
			if len(m.config.SlurmClusters) > 0 {
				m.slurmView = !m.slurmView
				m.slurmOffset = 0
				m.slurmCursor = 0
				m.slurmExplain = false
				m.slurmJobID, m.slurmJobCluster = "", ""
				if m.slurmView {
					m.status = "Slurm queue overview."
				} else {
					m.status = "Server overview."
				}
			}
		case "s", "S":
			if len(m.config.SlurmClusters) > 0 {
				m.slurmView = true
				m.slurmOffset = 0
				m.slurmCursor = 0
				m.slurmExplain = false
				m.slurmJobID, m.slurmJobCluster = "", ""
				m.status = "Slurm queue overview."
			}
		case "esc":
			if m.slurmView {
				if m.slurmExplain {
					m.slurmExplain = false
					m.slurmJobID, m.slurmJobCluster = "", ""
					m.status = "Returned to the Slurm queue."
					break
				}
				m.slurmView = false
				m.status = "Server overview."
			} else {
				return m, tea.Quit
			}
		case "a", "A":
			if m.slurmView {
				m.slurmFilter = -1
				m.slurmOffset = 0
				m.slurmCursor = 0
				m.slurmJobID, m.slurmJobCluster = "", ""
				m.status = "Showing jobs from all Slurm clusters."
			}
		case "left", "h":
			if m.slurmView {
				if m.slurmExplain {
					break
				}
				m.moveSlurmFilter(-1)
				break
			}
			m.moveCursorHorizontal(-1)
		case "right", "l":
			if m.slurmView {
				if m.slurmExplain {
					break
				}
				m.moveSlurmFilter(1)
				break
			}
			m.moveCursorHorizontal(1)
		case "up", "k":
			if m.slurmView {
				m.moveSlurmCursor(-1)
				break
			}
			m.moveCursorVertical(-1)
		case "down", "j":
			if m.slurmView {
				m.moveSlurmCursor(1)
				break
			}
			m.moveCursorVertical(1)
		case "pgup":
			if m.slurmView {
				m.moveSlurmCursor(-max(1, m.height/2))
			}
		case "pgdown":
			if m.slurmView {
				m.moveSlurmCursor(max(1, m.height/2))
			}
		case "enter":
			if m.slurmView {
				if len(m.selectedSlurmJobs()) > 0 {
					m.slurmExplain = !m.slurmExplain
					if m.slurmExplain {
						m.rememberSlurmSelection()
					} else {
						m.slurmJobID, m.slurmJobCluster = "", ""
					}
				}
				break
			}
			return m, m.openSelected()
		}
	}
	return m, nil
}

func (m *hubModel) updateOnlineStatus() {
	online := 0
	for _, state := range m.states {
		if state.Error == "" {
			online++
		}
	}
	slurmOnline := 0
	for _, state := range m.slurmStates {
		if state.Error == "" && !state.Snapshot.CollectedAt.IsZero() {
			slurmOnline++
		}
	}
	if len(m.slurmStates) > 0 {
		m.status = fmt.Sprintf("%d/%d servers · %d/%d Slurm clusters online.",
			online, len(m.states), slurmOnline, len(m.slurmStates))
		return
	}
	m.status = fmt.Sprintf("%d/%d servers online.", online, len(m.states))
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
	m.detail = newRemoteMonitorModel(node, m.width, m.height, m.colorMode)
	m.detail.slurmQueue = m.nodeSlurmQueue(m.cursor)
	m.detail.status = fmt.Sprintf("Connected through Hub to %s. Esc returns to the server list.", node.Name)
	return tea.Batch(m.detail.startCollect(), tick())
}

func (m *hubModel) columns() int {
	switch {
	case usableWidth(m.width) >= 132:
		return 3
	case usableWidth(m.width) >= 76:
		return 2
	default:
		return 1
	}
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

// pageNodeRows returns the node-bearing rows of one page, skipping group
// titles, so cursor movement follows the visual card grid instead of the
// flat grouped node list.
func (m *hubModel) pageNodeRows(page hubPage) [][]int {
	rows := make([][]int, 0, len(page.rows))
	for _, row := range page.rows {
		if len(row.nodes) > 0 {
			rows = append(rows, row.nodes)
		}
	}
	return rows
}

// cursorCell locates the cursor in the visual grid of the page that contains
// it and returns that page index, row index, column index and node rows.
func (m *hubModel) cursorCell() (int, int, int, [][]int) {
	for pageIndex, page := range m.pages() {
		rows := m.pageNodeRows(page)
		for rowIndex, row := range rows {
			for column, nodeIndex := range row {
				if nodeIndex == m.cursor {
					return pageIndex, rowIndex, column, rows
				}
			}
		}
	}
	return 0, 0, 0, nil
}

func (m *hubModel) moveCursorVertical(delta int) {
	pages := m.pages()
	pageIndex, rowIndex, column, rows := m.cursorCell()
	if len(rows) == 0 || len(pages) == 0 {
		return
	}
	targetRow := rowIndex
	targetColumn := column
	if delta < 0 {
		if rowIndex > 0 {
			targetRow = rowIndex - 1
		} else {
			if pageIndex == 0 {
				return
			}
			previousRows := m.pageNodeRows(pages[pageIndex-1])
			if len(previousRows) == 0 {
				return
			}
			rows, targetRow = previousRows, len(previousRows)-1
		}
	} else {
		if rowIndex < len(rows)-1 {
			targetRow = rowIndex + 1
		} else {
			if pageIndex == len(pages)-1 {
				return
			}
			nextRows := m.pageNodeRows(pages[pageIndex+1])
			if len(nextRows) == 0 {
				return
			}
			rows, targetRow = nextRows, 0
		}
	}
	if targetColumn >= len(rows[targetRow]) {
		targetColumn = len(rows[targetRow]) - 1
	}
	m.cursor = rows[targetRow][targetColumn]
	m.clampCursor()
}

func (m *hubModel) moveCursorHorizontal(delta int) {
	pages := m.pages()
	pageIndex, rowIndex, column, rows := m.cursorCell()
	if len(rows) == 0 || len(pages) == 0 {
		return
	}
	row := rows[rowIndex]
	if delta < 0 {
		switch {
		case column > 0:
			m.cursor = row[column-1]
		case rowIndex > 0:
			previous := rows[rowIndex-1]
			m.cursor = previous[len(previous)-1]
		case pageIndex > 0:
			previousRows := m.pageNodeRows(pages[pageIndex-1])
			if len(previousRows) == 0 {
				return
			}
			last := previousRows[len(previousRows)-1]
			m.cursor = last[len(last)-1]
		default:
			return
		}
	} else {
		switch {
		case column < len(row)-1:
			m.cursor = row[column+1]
		case rowIndex < len(rows)-1:
			next := rows[rowIndex+1]
			m.cursor = next[0]
		case pageIndex < len(pages)-1:
			nextRows := m.pageNodeRows(pages[pageIndex+1])
			if len(nextRows) == 0 {
				return
			}
			m.cursor = nextRows[0][0]
		default:
			return
		}
	}
	m.clampCursor()
}

func (m *hubModel) pages() []hubPage {
	columns := m.columns()
	heightBudget := max(hubCardHeight+1, max(10, m.height)-2)
	var pages []hubPage
	current := hubPage{}
	usedHeight := 0
	flush := func() {
		if len(current.rows) == 0 {
			return
		}
		pages = append(pages, current)
		current = hubPage{}
		usedHeight = 0
	}

	for _, group := range m.nodeGroups() {
		nodeRows := make([][]int, 0, (len(group.nodes)+columns-1)/columns)
		for start := 0; start < len(group.nodes); start += columns {
			end := min(start+columns, len(group.nodes))
			nodeRows = append(nodeRows, group.nodes[start:end])
		}
		for len(nodeRows) > 0 {
			if len(current.rows) > 0 && heightBudget-usedHeight < hubCardHeight+1 {
				flush()
			}
			current.rows = append(current.rows, hubDisplayRow{
				title: group.title, style: group.style, count: len(group.nodes),
			})
			usedHeight++
			for len(nodeRows) > 0 && heightBudget-usedHeight >= hubCardHeight {
				current.rows = append(current.rows, hubDisplayRow{nodes: nodeRows[0]})
				nodeRows = nodeRows[1:]
				usedHeight += hubCardHeight
			}
			if len(nodeRows) > 0 {
				flush()
			}
		}
	}
	flush()
	if len(pages) == 0 {
		pages = []hubPage{{}}
	}
	return pages
}

func (m *hubModel) clampCursor() {
	order := m.nodeOrder()
	if len(order) == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	found := false
	for _, index := range order {
		if index == m.cursor {
			found = true
			break
		}
	}
	if !found {
		m.cursor = order[0]
	}
	pages := m.pages()
	for pageIndex, page := range pages {
		for _, row := range page.rows {
			for _, nodeIndex := range row.nodes {
				if nodeIndex == m.cursor {
					m.offset = pageIndex
					return
				}
			}
		}
	}
	m.offset = min(max(0, m.offset), len(pages)-1)
}

func (m *hubModel) nodeAt(x, y int) (int, bool) {
	if y < 1 {
		return 0, false
	}
	pages := m.pages()
	if m.offset < 0 || m.offset >= len(pages) {
		return 0, false
	}
	columns := m.columns()
	cardWidth := max(20, (usableWidth(m.width)-(columns-1))/columns)
	rowY := 1
	for _, row := range pages[m.offset].rows {
		if row.title != "" {
			if y == rowY {
				return 0, false
			}
			rowY++
			continue
		}
		if y >= rowY && y < rowY+hubCardHeight {
			if x < 0 {
				return 0, false
			}
			column := x / (cardWidth + 1)
			if column >= columns || x%(cardWidth+1) >= cardWidth || column >= len(row.nodes) {
				return 0, false
			}
			return row.nodes[column], true
		}
		rowY += hubCardHeight
	}
	return 0, false
}

func (m *hubModel) View() tea.View {
	if m.detail != nil {
		view := m.detail.View()
		view.WindowTitle = m.config.displayName() + " · " + m.config.Nodes[m.cursor].Name
		return view
	}
	body := m.hubView()
	if m.slurmView {
		body = m.slurmQueueView()
	}
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	view := tea.NewView(body)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = m.config.displayName()
	view.BackgroundColor, view.ForegroundColor = viewColors(m.colorMode)
	return view
}

func (m *hubModel) hubView() string {
	width := usableWidth(m.width)
	online := 0
	for _, state := range m.states {
		if state.Error == "" && !state.Snapshot.CollectedAt.IsZero() {
			online++
		}
	}
	title := titleStyle.Render(m.config.displayName()) + "  " + liveBadgeStyle.Render(fmt.Sprintf("%d/%d ONLINE", online, len(m.config.Nodes)))
	meta := dimStyle.Render(fmt.Sprintf("%ds  ·  %s", int(m.config.refreshInterval().Seconds()), strings.ToUpper(m.colorMode.String())))
	headerGap := max(2, width-lipgloss.Width(title)-lipgloss.Width(meta))
	header := title + strings.Repeat(" ", headerGap) + meta

	columns := m.columns()
	cardWidth := max(20, (width-(columns-1))/columns)
	pages := m.pages()
	pageIndex := min(max(0, m.offset), len(pages)-1)
	var rows []string
	for _, row := range pages[pageIndex].rows {
		if row.title != "" {
			rows = append(rows, renderHubSectionTitle(row.title, row.style, row.count, width))
			continue
		}
		var cards []string
		for _, index := range row.nodes {
			if len(cards) > 0 {
				cards = append(cards, " ")
			}
			cards = append(cards, m.renderNodeCard(index, cardWidth))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	if len(rows) == 0 {
		rows = []string{panelStyle(width).Render(warningStyle.Render("No servers are configured."))}
	}
	rangeText := ""
	if len(pages) > 1 {
		rangeText = fmt.Sprintf("  PAGE %d/%d", pageIndex+1, len(pages))
	}
	hints := []string{
		keyHint("↑↓←→", "select"),
		keyHint("enter", "open"),
	}
	if len(m.config.SlurmClusters) > 0 {
		hints = append(hints, keyHint("s", "queues"))
	}
	hints = append(hints,
		keyHint("r", "refresh"),
		keyHint("t", "theme"),
		keyHint("q", "quit"),
	)
	footer := strings.Join(hints, "  ")
	statusWidth := width - lipgloss.Width(footer) - 2
	if statusWidth > 10 {
		footer += "  " + dimStyle.Render(truncate(m.status+rangeText, statusWidth))
	}
	footer = ansi.Truncate(footer, width, "")
	body := strings.Join([]string{header, strings.Join(rows, "\n")}, "\n")
	return terminalFrame(body, footer, width, m.height)
}

func renderHubSectionTitle(title, styleName string, count, width int) string {
	style := gpuTitleStyle
	if styleName == hubGroupStyleCPU {
		style = cpuTitleStyle
	} else if styleName == hubGroupStyleNetwork {
		style = networkRXStyle
	} else if styleName == hubGroupStyleProcess {
		style = processTitleStyle
	}
	label := style.Render(" " + title + " ")
	unit := "NODES"
	if count == 1 {
		unit = "NODE"
	}
	countLabel := dimStyle.Render(fmt.Sprintf("%d %s ", count, unit))
	line := strings.Repeat("─", max(0, width-lipgloss.Width(label)-lipgloss.Width(countLabel)))
	return label + dimStyle.Render(line) + countLabel
}

func (m *hubModel) renderNodeCard(index, width int) string {
	node := m.config.Nodes[index]
	state := hubNodeState{}
	if index < len(m.states) {
		state = m.states[index]
	}
	selected := index == m.cursor
	titleStyleForCard := processTitleStyle
	border := colorPanelBorder
	if selected {
		titleStyleForCard = accentStyle
		border = lipgloss.Color("#B9A4FF")
	}
	meta := "CHECKING"
	content := []string{
		dimStyle.Render(truncate(node.Description, width-4)),
		dimStyle.Render(truncate(normalizeNodeAddress(node.Address), width-4)),
		"Waiting for the first snapshot…",
		dimStyle.Render(strings.Repeat("·", max(1, width-4))),
		dimStyle.Render("Enter or click to open"),
	}
	if state.Error != "" {
		meta = "OFFLINE"
		lastSeen := "Never seen online"
		if !state.LastSeen.IsZero() {
			lastSeen = "Last seen " + hubAge(time.Since(state.LastSeen)) + " ago"
		}
		retry := "retrying now"
		if remaining := time.Until(state.NextRetry); remaining > 0 {
			retry = "auto retry in " + hubAge(remaining)
		}
		content = []string{
			dangerStyle.Render("● OFFLINE / UNREACHABLE"),
			dimStyle.Render(truncate(normalizeNodeAddress(node.Address), width-4)),
			dimStyle.Render(truncate(lastSeen, width-4)),
			warningStyle.Render(truncate(state.Error, width-4)),
			dimStyle.Render(truncate("[r] retry now · "+retry, width-4)),
		}
	} else if !state.Snapshot.CollectedAt.IsZero() {
		meta = fmt.Sprintf("%dms", state.Latency.Milliseconds())
		snapshot := state.Snapshot
		profile := normalizeMachineProfile(node.Profile)
		if strings.TrimSpace(node.Profile) == "" && snapshot.Profile != "" {
			profile = normalizeMachineProfile(snapshot.Profile)
		}
		if profile == machineProfileNAS {
			content = renderNASHubCard(snapshot)
			if state.Warning != "" {
				content[4] = warningStyle.Render(truncate(state.Warning, width-4))
			}
			return btopPanel(width, node.Name, meta, strings.Join(content, "\n"), titleStyleForCard, border)
		}
		if profile == machineProfileCPU {
			content = renderCPUHubCard(snapshot, width)
			if state.Warning != "" {
				content[4] = warningStyle.Render(truncate(state.Warning, width-4))
			}
			return btopPanel(width, node.Name, meta, strings.Join(content, "\n"), titleStyleForCard, border)
		}
		memory := percent(snapshot.MemoryUsed, snapshot.MemoryTotal)
		disk := percent(snapshot.DiskUsed, snapshot.DiskTotal)
		gpuUtil, gpuMemoryUsed, gpuMemoryTotal, maxTemperature := hubGPUStats(snapshot.GPUs)
		_, gpuStyle := gpuLoadStatus(gpuUtil)
		// Hub cards are status summaries. Their layout must not depend on whether
		// a node happens to have history persistence enabled or reachable: that
		// produced a sparkline for some nodes and a load bar for others. Keep the
		// current peak load as the one consistent visual here; trends remain in
		// the live node detail view.
		loadVisual := bar(math.Max(snapshot.CPUPercent, gpuUtil), max(8, width-4))
		content = []string{
			fmt.Sprintf("%s %5.1f%%   %s %5.1f%%   %s %5.1f%%",
				cpuTitleStyle.Render("CPU"), snapshot.CPUPercent,
				memoryTitleStyle.Render("MEM"), memory,
				diskTitleStyle.Render("DSK"), disk),
			fmt.Sprintf("%s %d  %s %5.1f%%  %s %s/%s",
				gpuTitleStyle.Render("GPU"), len(snapshot.GPUs),
				gpuStyle.Render("LOAD"), gpuUtil,
				dimStyle.Render("VRAM"), bytes(gpuMemoryUsed), bytes(gpuMemoryTotal)),
			gpuStyle.Render(fmt.Sprintf("MAX %3.0f%% · %d°C", gpuUtil, maxTemperature)) +
				"  " + dimStyle.Render(fmt.Sprintf("↓%s/s ↑%s/s", bytes(snapshot.NetworkRX), bytes(snapshot.NetworkTX))),
			loadVisual,
			dimStyle.Render("Enter or click to open live details"),
		}
		if state.Warning != "" {
			content[4] = warningStyle.Render(truncate(state.Warning, width-4))
		}
	}
	if node.Description == "" && strings.HasPrefix(content[0], "\x1b") {
		// Live and offline cards already use all five lines. Waiting cards keep
		// a stable height even when no optional description was configured.
		content[0] = strings.TrimSpace(content[0])
	}
	return btopPanel(width, node.Name, meta, strings.Join(content, "\n"), titleStyleForCard, border)
}

func renderCPUHubCard(snapshot monitorSnapshot, width int) []string {
	memory := percent(snapshot.MemoryUsed, snapshot.MemoryTotal)
	disk := percent(snapshot.DiskUsed, snapshot.DiskTotal)
	load := strings.TrimSpace(strings.TrimPrefix(snapshot.LoadAverage, "load"))
	if load == "" {
		load = "--"
	}
	return []string{
		fmt.Sprintf("%s %5.1f%%   %s %5.1f%%   %s %5.1f%%",
			cpuTitleStyle.Render("CPU"), snapshot.CPUPercent,
			memoryTitleStyle.Render("MEM"), memory,
			diskTitleStyle.Render("DSK"), disk),
		fmt.Sprintf("%s %d  %s %s",
			cpuTitleStyle.Render("CORES"), snapshot.CPUCores,
			dimStyle.Render("LOAD"), dimStyle.Render(load)),
		fmt.Sprintf("%s %s/s   %s %s/s",
			networkRXStyle.Render("↓"), bytes(snapshot.NetworkRX),
			networkTXStyle.Render("↑"), bytes(snapshot.NetworkTX)),
		bar(snapshot.CPUPercent, max(8, width-4)),
		dimStyle.Render("Enter or click to open live details"),
	}
}

func renderNASHubCard(snapshot monitorSnapshot) []string {
	filesystems := snapshot.Filesystems
	if len(filesystems) == 0 {
		filesystems = []filesystemInfo{{Mount: "/", Used: snapshot.DiskUsed, Total: snapshot.DiskTotal}}
	}
	worst := filesystems[0]
	worstUsage := percent(worst.Used, worst.Total)
	for _, filesystem := range filesystems[1:] {
		if usage := percent(filesystem.Used, filesystem.Total); usage > worstUsage {
			worst, worstUsage = filesystem, usage
		}
	}
	storageStyle := dimStyle
	if worstUsage >= 95 {
		storageStyle = dangerStyle
	} else if worstUsage >= 85 {
		storageStyle = warningStyle
	}
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
	return []string{
		fmt.Sprintf("%s %s/s   %s %s/s",
			networkRXStyle.Render("↓"), bytes(snapshot.NetworkRX),
			networkTXStyle.Render("↑"), bytes(snapshot.NetworkTX)),
		dimStyle.Render(fmt.Sprintf("TOTAL ↓ %s  ↑ %s", bytes(snapshot.NetworkRXTotal), bytes(snapshot.NetworkTXTotal))),
		fmt.Sprintf("%s %d  %s",
			diskTitleStyle.Render("DISK"), len(filesystems),
			storageStyle.Render(fmt.Sprintf("MAX %.1f%% %s", worstUsage, truncate(worst.Mount, 12)))),
		fmt.Sprintf("%s %s  %s %s  %s %s",
			processTitleStyle.Render("HTTP"), compactHealthCount(healthyHTTP, len(snapshot.Services)),
			accentStyle.Render("CTR"), containerHealth,
			gpuTitleStyle.Render("PM2"), pm2Health),
		dimStyle.Render("Enter or click to open NAS details"),
	}
}

func compactHealthCount(healthy, total int) string {
	label := fmt.Sprintf("%d/%d", healthy, total)
	if total == 0 {
		return dimStyle.Render("—")
	}
	if healthy == total {
		return processRunningStyle.Render(label)
	}
	return dangerStyle.Render(label)
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
	if m.colorMode == colorModeLight {
		m.colorMode = colorModeDark
	} else {
		m.colorMode = colorModeLight
	}
	m.status = fmt.Sprintf("%s theme enabled for this Hub session.", strings.ToUpper(m.colorMode.String()))
}
