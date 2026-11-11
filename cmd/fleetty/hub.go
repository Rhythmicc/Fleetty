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
	hubCardHeight             = 5
	hubPreferredCardWidth     = 46
	hubMinimumCardWidth       = 36
	hubOverviewRPCTimeout     = 900 * time.Millisecond
	hubHistoryRPCTimeout      = 900 * time.Millisecond
	hubHistoryRefreshInterval = time.Minute
	// One sparkline column is one hub refresh. Keep enough samples for the
	// widest card; a narrower card draws the newest tail.
	hubSparkCap            = 96
	hubSparkFloor          = 14
	hubFactColumnWidth     = 14
	hubOfflineRetryInitial = 5 * time.Second
	hubOfflineRetryMaximum = 30 * time.Second
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
	CPU, Mem, Disk, GPU float64
}

type hubNodeState struct {
	Snapshot            monitorSnapshot
	History             []historySample
	Spark               []hubSparkSample
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
			state.Spark = appendSpark(state.Spark, hubSparkFrom(response.Snapshot))
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
	theme           terminalTheme
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
	headings []hubGroupSpan
	nodes    []int
}

type hubGroupSpan struct {
	group int
	span  int
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
		theme:       newTerminalTheme(os.Getenv("DEFAULT_THEME")),
		status:      "Select a server to open its live dashboard.",
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
	m.detail.theme = m.theme
	m.detail.slurmQueue = m.nodeSlurmQueue(m.cursor)
	m.detail.status = fmt.Sprintf("Connected through Hub to %s. Esc returns to the server list.", node.Name)
	return tea.Batch(m.detail.startCollect(), tick())
}

func (m *hubModel) columns() int {
	width := usableWidth(m.width)
	stride := hubPreferredCardWidth + 1
	// Pick the nearest column count, rather than leaving almost a whole card
	// empty when the terminal is just below a fixed-width breakpoint.
	preferred := max(1, (width+1+stride/2)/stride)
	columns := min(preferred, max(1, (width+1)/(hubMinimumCardWidth+1)))
	// Prefer another column over a wider card. Drop one only when the
	// sparkline would be a stub and the wider card stays near the preferred
	// width: 140 stays at 3, and ~220 settles at 4.
	for columns > 1 && hubSparkColumns(hubContentWidth(width, columns)) < hubSparkFloor {
		wider := (width - (columns - 1) + 1) / (columns - 1)
		if wider > hubPreferredCardWidth+8 {
			break
		}
		columns--
	}
	return columns
}

func hubContentWidth(termWidth, columns int) int {
	return (termWidth-columns+1)/columns - 4
}

func hubSparkColumns(contentWidth int) int {
	left, _ := hubColumnWidths(contentWidth)
	// "GPU×2", a space, the sparkline, a space, and "100%".
	width := left - 5 - 2 - 4
	if width < 0 {
		return 0
	}
	return width
}

func (m *hubModel) cardWidth() int {
	columns := m.columns()
	return (usableWidth(m.width) - columns + 1) / columns
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
	// Each group owns its rows. A new group always starts on the next row, so
	// two headers never share a line and a wrapped tail is not mixed in.
	var grid []hubDisplayRow
	row := hubDisplayRow{}
	flushRow := func() {
		if len(row.nodes) == 0 {
			return
		}
		grid = append(grid, row)
		row = hubDisplayRow{}
	}
	for groupIndex, group := range m.nodeGroups() {
		nodes := group.nodes
		if len(nodes) == 0 {
			continue
		}
		flushRow()
		for len(nodes) > 0 {
			take := min(columns, len(nodes))
			row.headings = append(row.headings, hubGroupSpan{group: groupIndex, span: take})
			row.nodes = append(row.nodes, nodes[:take]...)
			nodes = nodes[take:]
			flushRow()
		}
	}
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

	var previous []hubGroupSpan
	for _, row := range grid {
		heading := len(current.rows) == 0 || len(row.headings) > 1 || len(previous) != 1 || previous[0].group != row.headings[0].group
		needed := hubCardHeight
		if heading {
			needed++
		}
		if usedHeight+needed > heightBudget {
			flush()
			heading = true
		}
		if heading {
			current.rows = append(current.rows, hubDisplayRow{headings: row.headings})
			usedHeight++
		}
		current.rows = append(current.rows, hubDisplayRow{nodes: row.nodes})
		usedHeight += hubCardHeight
		previous = row.headings
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
	cardWidth := m.cardWidth()
	rowY := 1
	for _, row := range pages[m.offset].rows {
		if len(row.headings) > 0 {
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
	view.BackgroundColor, view.ForegroundColor = m.theme.colors(m.colorMode)
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
	badge := liveBadgeStyle
	if len(m.config.Nodes) > 0 && online == 0 {
		badge = critBadgeStyle
	} else if online < len(m.config.Nodes) {
		badge = warnBadgeStyle
	}
	title := titleStyle.Render(m.config.displayName()) + "  " + badge.Render(fmt.Sprintf("%d/%d ONLINE", online, len(m.config.Nodes)))
	meta := dimStyle.Render(fmt.Sprintf("%ds  ·  %s", int(m.config.refreshInterval().Seconds()), strings.ToUpper(m.colorMode.String())))
	headerGap := max(2, width-lipgloss.Width(title)-lipgloss.Width(meta))
	header := ansi.Truncate(title+strings.Repeat(" ", headerGap)+meta, width, "")

	cardWidth := m.cardWidth()
	pages := m.pages()
	pageIndex := min(max(0, m.offset), len(pages)-1)
	var rows []string
	groups := m.nodeGroups()
	for _, row := range pages[pageIndex].rows {
		if len(row.headings) > 0 {
			var headings []string
			for _, section := range row.headings {
				group := groups[section.group]
				headings = append(headings, renderHubSectionTitle(group.title, group.style, len(group.nodes), section.span*(cardWidth+1)-1))
			}
			rows = append(rows, strings.Join(headings, " "))
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

func renderHubSectionTitle(title, _ string, count, width int) string {
	unit := "NODES"
	if count == 1 {
		unit = "NODE"
	}
	label := sectionStyle.Render(truncate(strings.TrimSpace(title), max(1, width)))
	countLabel := dimStyle.Render(fmt.Sprintf("%d %s", count, unit))
	if lipgloss.Width(label)+1+lipgloss.Width(countLabel) > width {
		return ansi.Truncate(label, width, "")
	}
	gap := strings.Repeat(" ", width-lipgloss.Width(label)-lipgloss.Width(countLabel))
	return label + gap + countLabel
}

func (m *hubModel) renderNodeCard(index, width int) string {
	node := m.config.Nodes[index]
	state := hubNodeState{}
	if index < len(m.states) {
		state = m.states[index]
	}
	selected := index == m.cursor
	title := node.Name
	titleStyleForCard := valueStyle
	border := colorBorder
	if selected {
		title = "▸ " + title
		titleStyleForCard = accentStyle
		border = colorBorderFocus
	}
	status := "CHECKING"
	temp := 0
	if state.Error != "" {
		status = "OFFLINE"
	} else if !state.Snapshot.CollectedAt.IsZero() {
		status = fmt.Sprintf("%dms", state.Latency.Milliseconds())
		if len(state.Snapshot.GPUs) > 0 {
			_, _, _, temp = hubGPUStats(state.Snapshot.GPUs)
		}
	}
	meta := hubCardMeta(hubMetaBudget(width, title), hubNodeAddressLabel(node.Address), status, temp, state.Warning != "")
	contentWidth := max(1, width-4)
	var content []string
	switch {
	case state.Error != "":
		content = renderOfflineHubCard(state, contentWidth)
	case !state.Snapshot.CollectedAt.IsZero():
		snapshot := state.Snapshot
		profile := normalizeMachineProfile(node.Profile)
		if strings.TrimSpace(node.Profile) == "" && snapshot.Profile != "" {
			profile = normalizeMachineProfile(snapshot.Profile)
		}
		content = m.renderLiveHubCard(index, state.Spark, snapshot, profile, contentWidth)
	default:
		content = renderWaitingHubCard(node.Description, contentWidth)
	}
	return btopPanel(width, title, meta, strings.Join(content, "\n"), titleStyleForCard, border)
}

func hubMetaBudget(width int, title string) int {
	inner := max(0, width-2)
	titleText := " " + truncate(title, max(4, inner-5)) + " "
	return max(0, inner-lipgloss.Width(titleText)-2)
}

func hubCardMeta(budget int, address, status string, temp int, warn bool) string {
	if budget <= 0 {
		return ""
	}
	var core []string
	if status != "" {
		core = append(core, dimStyle.Render(status))
	}
	if temp > 0 {
		core = append(core, hubMeterStyle(temperatureLevel(temp)).Render(fmt.Sprintf("%d°C", temp)))
	}
	if warn {
		core = append(core, warningStyle.Render("WARN"))
	}
	join := func(parts []string) string {
		return strings.Join(parts, dimStyle.Render("  "))
	}
	withAddress := append([]string{dimStyle.Render(address)}, core...)
	if full := join(withAddress); lipgloss.Width(full) <= budget {
		return full
	}
	if short := join(core); lipgloss.Width(short) <= budget {
		return short
	}
	return ""
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

func renderWaitingHubCard(description string, width int) []string {
	lines := []string{dimStyle.Render(truncate("Waiting for the first snapshot…", width))}
	if strings.TrimSpace(description) != "" {
		lines = append(lines, dimStyle.Render(truncate(description, width)))
	}
	return fitHubLines(lines)
}

func renderOfflineHubCard(state hubNodeState, width int) []string {
	lastSeen := "Never seen online"
	if !state.LastSeen.IsZero() {
		lastSeen = "Last seen " + hubAge(time.Since(state.LastSeen)) + " ago"
	}
	retry := "retrying now"
	if remaining := time.Until(state.NextRetry); remaining > 0 {
		retry = "retry in " + hubAge(remaining)
	}
	leftWidth := min(12, max(10, width/3))
	rightWidth := max(1, width-leftWidth-1)
	return joinHubColumns([]string{
		dangerStyle.Render(truncate("● OFFLINE", leftWidth)),
	}, []string{
		dimStyle.Render(truncate(lastSeen, rightWidth)),
		dimStyle.Render(truncate(retry, rightWidth)),
		dimStyle.Render(hubShortError(state.Error, rightWidth)),
	}, leftWidth, rightWidth)
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

func (m *hubModel) renderLiveHubCard(index int, spark []hubSparkSample, snapshot monitorSnapshot, profile string, width int) []string {
	if profile == machineProfileNAS {
		return renderNASHubCard(spark, snapshot, width)
	}
	leftWidth, rightWidth := hubColumnWidths(width)
	memory := percent(snapshot.MemoryUsed, snapshot.MemoryTotal)
	disk := percent(snapshot.DiskUsed, snapshot.DiskTotal)
	gpuNode := len(snapshot.GPUs) > 0 && profile != machineProfileCPU
	var rows []struct {
		label  string
		series []float64
		value  float64
	}
	if gpuNode {
		gpuUtil, _, _, _ := hubGPUStats(snapshot.GPUs)
		rows = []struct {
			label  string
			series []float64
			value  float64
		}{
			{"CPU", sparkSeries(spark, sparkCPU), snapshot.CPUPercent},
			{"MEM", sparkSeries(spark, sparkMem), memory},
			{fmt.Sprintf("GPU×%d", len(snapshot.GPUs)), sparkSeries(spark, sparkGPU), gpuUtil},
		}
	} else {
		rows = []struct {
			label  string
			series []float64
			value  float64
		}{
			{"CPU", sparkSeries(spark, sparkCPU), snapshot.CPUPercent},
			{"MEM", sparkSeries(spark, sparkMem), memory},
			{"DSK", sparkSeries(spark, sparkDisk), disk},
		}
	}
	left := hubMetricRows(rows, leftWidth)
	return hubColumns(left, m.hubFacts(index, snapshot, gpuNode, rightWidth), width)
}

func hubColumnWidths(width int) (left, right int) {
	if width < 16 {
		return max(1, width/2), max(1, width-width/2)
	}
	// Narrow facts: "↓86M ↑14M", "vram 59/96G", "r40M w8M".
	right = hubFactColumnWidth
	if width < 32 {
		right = max(8, width*2/5)
	}
	if right >= width-8 {
		right = max(8, width/3)
	}
	left = max(1, width-right-1)
	return left, right
}

func hubColumns(left, right []string, width int) []string {
	leftWidth, rightWidth := hubColumnWidths(width)
	return joinHubColumns(left, right, leftWidth, rightWidth)
}

func joinHubColumns(left, right []string, leftWidth, rightWidth int) []string {
	lines := make([]string, 3)
	for index := range lines {
		var line, extra string
		if index < len(left) {
			line = left[index]
		}
		if index < len(right) {
			extra = right[index]
		}
		line = ansi.Truncate(line, leftWidth, "")
		extra = ansi.Truncate(extra, rightWidth, "")
		pad := leftWidth - lipgloss.Width(line)
		if pad < 0 {
			pad = 0
		}
		lines[index] = line + strings.Repeat(" ", pad) + " " + extra
	}
	return lines
}

func appendSpark(existing []hubSparkSample, sample hubSparkSample) []hubSparkSample {
	next := append(append([]hubSparkSample(nil), existing...), sample)
	if len(next) > hubSparkCap {
		next = append([]hubSparkSample(nil), next[len(next)-hubSparkCap:]...)
	}
	return next
}

func hubSparkFrom(snapshot monitorSnapshot) hubSparkSample {
	gpu, _, _, _ := hubGPUStats(snapshot.GPUs)
	return hubSparkSample{
		CPU:  snapshot.CPUPercent,
		Mem:  percent(snapshot.MemoryUsed, snapshot.MemoryTotal),
		Disk: percent(snapshot.DiskUsed, snapshot.DiskTotal),
		GPU:  gpu,
	}
}

type sparkField int

const (
	sparkCPU sparkField = iota
	sparkMem
	sparkDisk
	sparkGPU
)

func sparkSeries(samples []hubSparkSample, field sparkField) []float64 {
	values := make([]float64, len(samples))
	for index, sample := range samples {
		switch field {
		case sparkMem:
			values[index] = sample.Mem
		case sparkDisk:
			values[index] = sample.Disk
		case sparkGPU:
			values[index] = sample.GPU
		default:
			values[index] = sample.CPU
		}
	}
	return values
}

// hubFixedSparkline draws one column per sample on a fixed 0–100 scale.
// Samples not collected yet are a faint dot on the left. A real sample is at
// least a dim baseline, so an idle row still reads as present.
func hubFixedSparkline(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	if len(values) > width {
		values = values[len(values)-width:]
	}
	parts := make([]string, width)
	missing := width - len(values)
	for index := 0; index < width; index++ {
		if index < missing {
			parts[index] = dimStyle.Render("·")
			continue
		}
		value := values[index-missing]
		if value < 0 {
			value = 0
		}
		if value > 100 {
			value = 100
		}
		level := int(value / 100 * float64(len(blocks)))
		if level < 0 {
			level = 0
		}
		if level >= len(blocks) {
			level = len(blocks) - 1
		}
		style := dimStyle
		switch usageLevel(value) {
		case stateWarn:
			style = warningStyle
		case stateCrit:
			style = dangerStyle
		}
		parts[index] = style.Render(string(blocks[level]))
	}
	return strings.Join(parts, "")
}

type hubMetric struct {
	label  string
	series []float64
	value  float64
}

func hubMetricRows(rows []struct {
	label  string
	series []float64
	value  float64
}, width int) []string {
	labelWidth := 3
	for _, row := range rows {
		labelWidth = max(labelWidth, lipgloss.Width(row.label))
	}
	lines := make([]string, len(rows))
	for index, row := range rows {
		label := row.label + strings.Repeat(" ", labelWidth-lipgloss.Width(row.label))
		value := row.value
		if value < 0 {
			value = 0
		}
		if value > 100 {
			value = 100
		}
		percentText := fmt.Sprintf("%3.0f%%", value)
		sparkWidth := width - lipgloss.Width(label) - 1 - 1 - lipgloss.Width(percentText)
		if sparkWidth < 1 {
			lines[index] = ansi.Truncate(dimStyle.Render(label)+" "+hubMeterStyle(usageLevel(value)).Render(percentText), width, "")
			continue
		}
		lines[index] = dimStyle.Render(label) + " " +
			hubFixedSparkline(row.series, sparkWidth) + " " +
			hubMeterStyle(usageLevel(value)).Render(percentText)
	}
	return lines
}

func (m *hubModel) hubFacts(index int, snapshot monitorSnapshot, gpu bool, width int) []string {
	third := hubRateLine("r", "w", snapshot.DiskRead, snapshot.DiskWrite, width)
	if gpu {
		_, used, total, _ := hubGPUStats(snapshot.GPUs)
		third = ansi.Truncate(dimStyle.Render("vram ")+hubMetricStyle.Render(hubVRAM(used, total)), width, "")
	}
	return []string{
		m.hubOccupant(index, snapshot, width),
		hubRateLine("↓", "↑", snapshot.NetworkRX, snapshot.NetworkTX, width),
		third,
	}
}

func hubRateLine(leftLabel, rightLabel string, left, right uint64, width int) string {
	line := dimStyle.Render(leftLabel) + hubMetricStyle.Render(hubCompactRate(left)) + " " +
		dimStyle.Render(rightLabel) + hubMetricStyle.Render(hubCompactRate(right))
	return ansi.Truncate(line, width, "")
}

func hubCompactRate(value uint64) string {
	const unit = 1024.0
	if value < 1024 {
		return fmt.Sprintf("%d", value)
	}
	scaled := float64(value)
	labels := []string{"", "K", "M", "G", "T"}
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

func (m *hubModel) hubOccupant(index int, snapshot monitorSnapshot, width int) string {
	var users []string
	seen := map[string]bool{}
	add := func(user string) {
		user = strings.TrimSpace(user)
		if user == "" || seen[user] {
			return
		}
		seen[user] = true
		users = append(users, user)
	}
	if m != nil {
		if queue := m.nodeSlurmQueue(index); queue != nil {
			for _, display := range queue.Jobs {
				if isSlurmRunning(display.Job.State) {
					add(display.Job.User)
				}
			}
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
		return dimStyle.Render("idle")
	}
	text := users[0]
	if extra := len(users) - 1; extra > 0 {
		text += fmt.Sprintf(" +%d", extra)
	}
	return hubMetricStyle.Render(truncate(text, width))
}

func hubBaseName(value string) string {
	value = strings.TrimSpace(value)
	if split := strings.LastIndexAny(value, "/\\"); split >= 0 {
		value = value[split+1:]
	}
	return value
}

func renderNASHubCard(spark []hubSparkSample, snapshot monitorSnapshot, width int) []string {
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
	leftWidth, _ := hubColumnWidths(width)
	left := hubMetricRows([]struct {
		label  string
		series []float64
		value  float64
	}{
		{"DSK", sparkSeries(spark, sparkDisk), worstUsage},
	}, leftWidth)
	left = append(left, dimStyle.Render(truncate(worst.Mount, leftWidth)))
	lines := hubColumns(left, []string{
		dimStyle.Render("↓") + hubMetricStyle.Render(hubCompactRate(snapshot.NetworkRX)),
		dimStyle.Render("↑") + hubMetricStyle.Render(hubCompactRate(snapshot.NetworkTX)),
	}, width)
	lines[2] = ansi.Truncate(renderNASServiceLine(snapshot, width), width, "")
	return lines
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

func fitHubLines(lines []string) []string {
	if len(lines) > 3 {
		return lines[:3]
	}
	for len(lines) < 3 {
		lines = append(lines, "")
	}
	return lines
}

func hubCapacityStyle(usage float64, _ lipgloss.Style) lipgloss.Style {
	return stateStyle(usageLevel(usage))
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
