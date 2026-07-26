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
	hubOverviewRPCTimeout     = 900 * time.Millisecond
	hubOfflineRetryInitial    = 5 * time.Second
	hubOfflineRetryMaximum    = 30 * time.Second
)

type hubConfig struct {
	Name                string          `json:"name,omitempty"`
	RefreshSeconds      int             `json:"refresh_seconds,omitempty"`
	InsecureSkipHostKey bool            `json:"insecure_skip_host_key,omitempty"`
	Nodes               []hubNodeConfig `json:"nodes"`
}

type hubNodeConfig struct {
	Name                string `json:"name"`
	Address             string `json:"address"`
	Description         string `json:"description,omitempty"`
	Profile             string `json:"profile,omitempty"`
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
	config.Name = sanitizeTerminalText(config.Name)
	if config.Name == "" {
		config.Name = "Machine Hub"
	}
	seen := make(map[string]struct{}, len(config.Nodes))
	for index := range config.Nodes {
		node := &config.Nodes[index]
		node.Name = sanitizeTerminalText(node.Name)
		node.Description = sanitizeTerminalText(node.Description)
		rawProfile := strings.TrimSpace(node.Profile)
		node.Profile = normalizeMachineProfile(rawProfile)
		if rawProfile != "" && node.Profile == "" {
			return nil, fmt.Errorf("hub node %q has invalid profile %q", node.Name, rawProfile)
		}
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

func (c hubConfig) displayName() string {
	if name := sanitizeTerminalText(c.Name); name != "" {
		return name
	}
	return "Machine Hub"
}

func (c hubConfig) refreshInterval() time.Duration {
	if c.RefreshSeconds < 1 {
		return defaultHubRefreshInterval
	}
	return time.Duration(min(c.RefreshSeconds, 60)) * time.Second
}

type hubNodeState struct {
	Snapshot            monitorSnapshot
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
			response, err := newNodeRPCClient(nodes[index]).CallWithTimeout(
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
	defer s.mu.Unlock()
	for index := range s.states {
		if s.states[index].Error != "" {
			s.states[index].NextRetry = time.Time{}
		}
	}
	s.collectedAt = time.Time{}
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

type hubNodeGroup struct {
	title   string
	profile string
	nodes   []int
}

type hubDisplayRow struct {
	title   string
	profile string
	count   int
	nodes   []int
}

type hubPage struct {
	rows []hubDisplayRow
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
	// Hub refresh messages belong to the parent model even while a node
	// dashboard is open. Forwarding either message to the child drops the
	// refresh timer chain and can leave collecting stuck at true.
	switch msg := msg.(type) {
	case hubSnapshotsMsg:
		m.collecting = false
		m.states = msg.States
		if m.detail == nil {
			m.updateOnlineStatus()
		}
		return m, nil
	case hubTickMsg:
		return m, tea.Batch(m.startCollect(), hubTick(m.config.refreshInterval()))
	}

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
			if m.service != nil {
				m.service.retryOfflineNow()
			}
			return m, m.startCollect()
		case "left", "h":
			m.moveCursor(-1)
		case "right", "l":
			m.moveCursor(1)
		case "up", "k":
			m.moveCursor(-m.columns())
		case "down", "j":
			m.moveCursor(m.columns())
		case "enter":
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
	groups := []hubNodeGroup{
		{title: "GPU COMPUTE", profile: machineProfileGPU},
		{title: "NAS & STORAGE", profile: machineProfileNAS},
		{title: "GENERAL SERVERS", profile: machineProfileGeneral},
	}
	for index, node := range m.config.Nodes {
		profile := normalizeMachineProfile(node.Profile)
		for groupIndex := range groups {
			if groups[groupIndex].profile == profile {
				groups[groupIndex].nodes = append(groups[groupIndex].nodes, index)
				break
			}
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

func (m *hubModel) moveCursor(delta int) {
	order := m.nodeOrder()
	if len(order) == 0 {
		return
	}
	position := 0
	for index, nodeIndex := range order {
		if nodeIndex == m.cursor {
			position = index
			break
		}
	}
	position = min(max(0, position+delta), len(order)-1)
	m.cursor = order[position]
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
				title: group.title, profile: group.profile, count: len(group.nodes),
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
			rows = append(rows, renderHubSectionTitle(row.title, row.profile, row.count, width))
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

func renderHubSectionTitle(title, profile string, count, width int) string {
	style := gpuTitleStyle
	if profile == machineProfileNAS {
		style = networkRXStyle
	} else if profile == machineProfileGeneral {
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
		profile := snapshot.Profile
		if profile == "" {
			profile = node.Profile
		}
		if profile == machineProfileNAS {
			content = renderNASHubCard(snapshot)
			if state.Warning != "" {
				content[4] = warningStyle.Render(truncate(state.Warning, width-4))
			}
			return btopPanel(width, node.Name, meta, strings.Join(content, "\n"), titleStyleForCard, border)
		}
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
