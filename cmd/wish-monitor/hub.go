package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	defaultHubRefreshInterval = time.Second
	hubCardHeight             = 7
)

type hubConfig struct {
	RefreshSeconds      int             `json:"refresh_seconds,omitempty"`
	InsecureSkipHostKey bool            `json:"insecure_skip_host_key,omitempty"`
	Nodes               []hubNodeConfig `json:"nodes"`
}

type hubNodeConfig struct {
	Name                string `json:"name"`
	Address             string `json:"address"`
	Description         string `json:"description,omitempty"`
	HostKey             string `json:"host_key,omitempty"`
	InsecureSkipHostKey bool   `json:"-"`
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
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(config.Nodes) == 0 {
		return nil, errors.New("hub configuration has no nodes")
	}
	seen := make(map[string]struct{}, len(config.Nodes))
	for index := range config.Nodes {
		node := &config.Nodes[index]
		node.Name = sanitizeTerminalText(node.Name)
		node.Description = sanitizeTerminalText(node.Description)
		node.Address = strings.TrimSpace(node.Address)
		node.HostKey = strings.TrimSpace(node.HostKey)
		node.InsecureSkipHostKey = config.InsecureSkipHostKey
		if node.Name == "" || node.Address == "" {
			return nil, fmt.Errorf("hub node %d requires name and address", index+1)
		}
		if _, exists := seen[node.Name]; exists {
			return nil, fmt.Errorf("duplicate hub node name %q", node.Name)
		}
		seen[node.Name] = struct{}{}
		if node.HostKey == "" && !config.InsecureSkipHostKey {
			return nil, fmt.Errorf("hub node %q requires host_key", node.Name)
		}
	}
	return &config, nil
}

func (c hubConfig) refreshInterval() time.Duration {
	if c.RefreshSeconds < 1 {
		return defaultHubRefreshInterval
	}
	return time.Duration(min(c.RefreshSeconds, 60)) * time.Second
}

type hubNodeState struct {
	Snapshot monitorSnapshot
	Error    string
	Warning  string
	Latency  time.Duration
	Checked  time.Time
}

// hubService shares one recent overview snapshot between all connected Hub
// sessions. Per-node detail pages remain live and session-specific.
type hubService struct {
	config      hubConfig
	mu          sync.RWMutex
	collectMu   sync.Mutex
	states      []hubNodeState
	collectedAt time.Time
}

func newHubService(config hubConfig) *hubService {
	return &hubService{config: config, states: make([]hubNodeState, len(config.Nodes))}
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

	states := collectHubNodeStates(s.config.Nodes)
	s.mu.Lock()
	s.states = append([]hubNodeState(nil), states...)
	s.collectedAt = time.Now()
	s.mu.Unlock()
	return states
}

func collectHubNodeStates(nodes []hubNodeConfig) []hubNodeState {
	states := make([]hubNodeState, len(nodes))
	var wait sync.WaitGroup
	for index := range nodes {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			started := time.Now()
			response, err := newNodeRPCClient(nodes[index]).Call(nodeRPCRequest{Operation: rpcSnapshot})
			states[index] = hubNodeState{
				Latency: time.Since(started),
				Checked: time.Now(),
			}
			if err != nil {
				states[index].Error = sanitizeTerminalText(err.Error())
				return
			}
			states[index].Snapshot = response.Snapshot
			states[index].Warning = sanitizeTerminalText(response.Warning)
		}(index)
	}
	wait.Wait()
	return states
}

type hubSnapshotsMsg struct {
	States []hubNodeState
}

type hubTickMsg struct{}

type hubModel struct {
	service    *hubService
	config     hubConfig
	states     []hubNodeState
	width      int
	height     int
	cursor     int
	offset     int
	collecting bool
	colorMode  colorMode
	status     string
	detail     *monitorModel
}

func newHubModel(service *hubService, _ ssh.Session, width, height int) *hubModel {
	return &hubModel{
		service:   service,
		config:    service.config,
		states:    make([]hubNodeState, len(service.config.Nodes)),
		width:     width,
		height:    height,
		colorMode: parseColorMode(os.Getenv("DEFAULT_THEME")),
		status:    "Select a server to open its live dashboard.",
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
	service := m.service
	return func() tea.Msg {
		if service != nil {
			return hubSnapshotsMsg{States: service.collect()}
		}
		return hubSnapshotsMsg{States: collectHubNodeStates(nodes)}
	}
}

func (m *hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.detail != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok && m.detail.screen == screenMonitor {
			if key.String() == "esc" || key.String() == "q" {
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
	case hubSnapshotsMsg:
		m.collecting = false
		m.states = msg.States
		online := 0
		for _, state := range m.states {
			if state.Error == "" {
				online++
			}
		}
		m.status = fmt.Sprintf("%d/%d servers online.", online, len(m.states))
	case hubTickMsg:
		return m, tea.Batch(m.startCollect(), hubTick(m.config.refreshInterval()))
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if index, ok := m.nodeAt(msg.Mouse().X, msg.Mouse().Y); ok {
				m.cursor = index
				return m, m.openSelected()
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "t", "T":
			m.toggleColorMode()
		case "r":
			m.status = "Refreshing all servers…"
			return m, m.startCollect()
		case "left", "h":
			if m.cursor > 0 {
				m.cursor--
				m.clampCursor()
			}
		case "right", "l":
			if m.cursor+1 < len(m.config.Nodes) {
				m.cursor++
				m.clampCursor()
			}
		case "up", "k":
			m.cursor = max(0, m.cursor-m.columns())
			m.clampCursor()
		case "down", "j":
			m.cursor = min(len(m.config.Nodes)-1, m.cursor+m.columns())
			m.clampCursor()
		case "enter":
			return m, m.openSelected()
		}
	}
	return m, nil
}

func (m *hubModel) openSelected() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.config.Nodes) {
		return nil
	}
	node := m.config.Nodes[m.cursor]
	m.detail = newRemoteMonitorModel(node, m.width, m.height, m.colorMode)
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

func (m *hubModel) visibleCapacity() int {
	rows := max(1, (max(10, m.height)-3)/hubCardHeight)
	return rows * m.columns()
}

func (m *hubModel) clampCursor() {
	if len(m.config.Nodes) == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	m.cursor = min(max(0, m.cursor), len(m.config.Nodes)-1)
	capacity := m.visibleCapacity()
	if m.cursor < m.offset {
		m.offset = (m.cursor / m.columns()) * m.columns()
	}
	if m.cursor >= m.offset+capacity {
		firstRow := m.cursor/m.columns() - capacity/m.columns() + 1
		m.offset = max(0, firstRow*m.columns())
	}
	maxOffset := max(0, ((len(m.config.Nodes)-1)/m.columns()-(capacity/m.columns())+1)*m.columns())
	m.offset = min(m.offset, maxOffset)
}

func (m *hubModel) nodeAt(x, y int) (int, bool) {
	if y < 1 {
		return 0, false
	}
	columns := m.columns()
	cardWidth := max(20, (usableWidth(m.width)-(columns-1))/columns)
	column := x / (cardWidth + 1)
	row := (y - 1) / hubCardHeight
	if column < 0 || column >= columns || row < 0 {
		return 0, false
	}
	index := m.offset + row*columns + column
	return index, index >= 0 && index < len(m.config.Nodes)
}

func (m *hubModel) View() tea.View {
	if m.detail != nil {
		view := m.detail.View()
		view.WindowTitle = "GPU Monitor Hub · " + m.config.Nodes[m.cursor].Name
		return view
	}
	body := m.hubView()
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	view := tea.NewView(body)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "GPU Monitor Hub"
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
	title := titleStyle.Render("GPU MONITOR HUB") + "  " + liveBadgeStyle.Render(fmt.Sprintf("%d/%d ONLINE", online, len(m.config.Nodes)))
	meta := dimStyle.Render(fmt.Sprintf("%ds  ·  %s", int(m.config.refreshInterval().Seconds()), strings.ToUpper(m.colorMode.String())))
	headerGap := max(2, width-lipgloss.Width(title)-lipgloss.Width(meta))
	header := title + strings.Repeat(" ", headerGap) + meta

	columns := m.columns()
	cardWidth := max(20, (width-(columns-1))/columns)
	capacity := m.visibleCapacity()
	end := min(len(m.config.Nodes), m.offset+capacity)
	var rows []string
	for start := m.offset; start < end; start += columns {
		var cards []string
		for index := start; index < min(end, start+columns); index++ {
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
	if len(m.config.Nodes) > capacity {
		rangeText = fmt.Sprintf("  %d-%d/%d", m.offset+1, end, len(m.config.Nodes))
	}
	footer := strings.Join([]string{
		keyHint("↑↓←→", "select"),
		keyHint("enter", "open"),
		keyHint("r", "refresh"),
		keyHint("t", "theme"),
		keyHint("q", "quit"),
	}, "  ")
	statusWidth := width - lipgloss.Width(footer) - 2
	if statusWidth > 10 {
		footer += "  " + dimStyle.Render(truncate(m.status+rangeText, statusWidth))
	}
	footer = ansi.Truncate(footer, width, "")
	return strings.Join([]string{header, strings.Join(rows, "\n"), footer}, "\n")
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
		content = []string{
			dangerStyle.Render("● UNREACHABLE"),
			dimStyle.Render(truncate(normalizeNodeAddress(node.Address), width-4)),
			warningStyle.Render(truncate(state.Error, width-4)),
			dimStyle.Render(strings.Repeat("·", max(1, width-4))),
			dimStyle.Render("Press r to retry"),
		}
	} else if !state.Snapshot.CollectedAt.IsZero() {
		meta = fmt.Sprintf("%dms", state.Latency.Milliseconds())
		snapshot := state.Snapshot
		memory := percent(snapshot.MemoryUsed, snapshot.MemoryTotal)
		disk := percent(snapshot.DiskUsed, snapshot.DiskTotal)
		gpuUtil, gpuMemoryUsed, gpuMemoryTotal, maxTemperature := hubGPUStats(snapshot.GPUs)
		_, gpuStyle := gpuLoadStatus(gpuUtil)
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
			bar(math.Max(snapshot.CPUPercent, gpuUtil), max(8, width-4)),
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
