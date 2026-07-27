package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"image/color"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/log/v2"
	"charm.land/wish/v2"
	"charm.land/wish/v2/accesscontrol"
	"charm.land/wish/v2/activeterm"
	bubblewish "charm.land/wish/v2/bubbletea"
	"charm.land/wish/v2/logging"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/crypto/bcrypt"
)

const refreshInterval = time.Second

func main() {
	host := envString("SSH_HOST", "0.0.0.0")
	port := envString("SSH_PORT", "23234")
	hostKeyPath := envString("SSH_HOST_KEY_PATH", ".ssh/gpu-ssh-monitor_ed25519")
	machine, err := loadMachineConfig(os.Getenv("MACHINE_CONFIG_FILE"))
	if err != nil {
		log.Fatal("Could not load machine configuration", "error", err)
	}
	admin := newAdminController()
	rpc := newNodeRPCService(admin, machine)
	hub, err := loadHubConfig(os.Getenv("HUB_NODES_FILE"))
	if err != nil {
		log.Fatal("Could not load hub configuration", "error", err)
	}
	var hubRuntime *hubService
	if hub != nil {
		hubRuntime = newHubService(*hub)
	}

	server, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithMiddleware(
			bubblewish.Middleware(func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
				pty, _, ok := sess.Pty()
				if !ok {
					return nil, nil
				}
				if hubRuntime != nil {
					return newHubModel(hubRuntime, sess, pty.Window.Width, pty.Window.Height), nil
				}
				return newMonitorModel(admin, machine, sess, pty.Window.Width, pty.Window.Height), nil
			}),
			activeterm.Middleware(),
			nodeRPCMiddleware(rpc),
			accesscontrol.Middleware(nodeRPCCommand),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatal("Could not create SSH server", "error", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("Starting Go SSH monitor", "host", host, "port", port)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("Could not start SSH server", "error", err)
			done <- syscall.SIGTERM
		}
	}()

	<-done
	log.Info("Stopping SSH monitor")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("Could not stop SSH server", "error", err)
	}
}

// monitorModel is deliberately read-only: it never starts a shell or proxies
// terminal programs. Every SSH session owns one small collector and Bubble Tea
// program, so the monitor has no daemon process besides this Go binary.
type monitorModel struct {
	backend          monitorBackend
	admin            *adminController
	user             string
	remote           string
	nodeName         string
	profile          string
	width            int
	height           int
	snapshot         monitorSnapshot
	loadErr          error
	screen           screen
	password         string
	filter           string
	filtering        bool
	cursor           int
	processOffset    int
	selectedAction   *adminAction
	selectedProcess  *processInfo
	processDetail    *processDetail
	detailErr        error
	status           string
	busy             bool
	adminCredential  string
	collecting       bool
	cpuHistory       []float64
	networkRXHistory []float64
	networkTXHistory []float64
	colorMode        colorMode
	slurmQueue       *nodeSlurmQueue
}

type colorMode int

const (
	colorModeDark colorMode = iota
	colorModeLight
)

type screen int

const (
	screenMonitor screen = iota
	screenPassword
	screenAdmin
	screenConfirm
	screenProcessDetail
	screenProcessTerminateConfirm
)

func newMonitorModel(admin *adminController, machine machineConfig, sess ssh.Session, width, height int) *monitorModel {
	remote := "local"
	if addr := sess.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	return &monitorModel{
		backend:   newLocalMonitorBackend(admin, machine, sess.User(), remote),
		admin:     admin,
		user:      sess.User(),
		remote:    remote,
		nodeName:  machine.Name,
		profile:   machine.Profile,
		width:     width,
		height:    height,
		status:    "Live data refreshes every second.",
		colorMode: parseColorMode(os.Getenv("DEFAULT_THEME")),
	}
}

func newRemoteMonitorModel(node hubNodeConfig, width, height int, mode colorMode) *monitorModel {
	return &monitorModel{
		backend:   newRemoteMonitorBackend(node),
		admin:     newRemoteAdminController(),
		user:      "hub",
		remote:    node.Name,
		nodeName:  node.Name,
		profile:   normalizeMachineProfile(node.Profile),
		width:     width,
		height:    height,
		status:    "Remote data refreshes every second. Esc returns to the server list.",
		colorMode: mode,
	}
}

func (m *monitorModel) Init() tea.Cmd {
	return tea.Batch(m.startCollect(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}
type snapshotMsg struct {
	snapshot monitorSnapshot
	err      error
}
type authenticationResultMsg struct {
	password string
	ok       bool
	err      error
}
type actionResultMsg struct {
	action adminAction
	output string
	err    error
}
type processDetailMsg struct {
	pid    int
	detail processDetail
	err    error
}
type processTerminateResultMsg struct {
	pid int
	err error
}

func (m *monitorModel) collect() tea.Cmd {
	return func() tea.Msg {
		if m.backend == nil {
			return snapshotMsg{err: errors.New("monitor backend is unavailable")}
		}
		snapshot, err := m.backend.Collect()
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m *monitorModel) startCollect() tea.Cmd {
	if m.collecting {
		return nil
	}
	m.collecting = true
	return m.collect()
}

func (m *monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.collecting = false
		m.snapshot = msg.snapshot
		if msg.snapshot.Profile != "" {
			m.profile = msg.snapshot.Profile
		}
		m.loadErr = msg.err
		m.cpuHistory = appendHistory(m.cpuHistory, msg.snapshot.CPUPercent, 60)
		m.networkRXHistory = appendHistory(m.networkRXHistory, float64(msg.snapshot.NetworkRX), 60)
		m.networkTXHistory = appendHistory(m.networkTXHistory, float64(msg.snapshot.NetworkTX), 60)
		m.clampProcessCursor()
	case authenticationResultMsg:
		m.busy = false
		if msg.err != nil {
			m.password, m.status = "", "Authentication failed: "+msg.err.Error()
		} else if msg.ok {
			credential := ""
			if _, remote := m.backend.(*remoteMonitorBackend); remote {
				credential = msg.password
			}
			m.screen, m.password, m.adminCredential = screenAdmin, "", credential
			m.status = "Management mode enabled. Filter or select a process."
		} else {
			m.password, m.status = "", "Incorrect password. Try again or press Esc."
		}
	case tickMsg:
		return m, tea.Batch(m.startCollect(), tick())
	case actionResultMsg:
		m.busy = false
		if msg.err != nil {
			m.status = fmt.Sprintf("%s failed: %v", msg.action.label, msg.err)
		} else {
			m.status = fmt.Sprintf("%s requested%s", msg.action.label, compactOutput(msg.output))
		}
		m.screen = screenAdmin
		m.selectedAction = nil
	case processDetailMsg:
		if m.selectedProcess != nil && m.selectedProcess.PID == msg.pid {
			m.processDetail = &msg.detail
			m.detailErr = msg.err
		}
	case processTerminateResultMsg:
		m.busy = false
		if msg.err != nil {
			m.status = fmt.Sprintf("Could not terminate PID %d: %v", msg.pid, msg.err)
			m.screen = screenProcessDetail
		} else {
			m.status = fmt.Sprintf("SIGTERM sent to PID %d.", msg.pid)
			m.screen = screenAdmin
			m.selectedProcess, m.processDetail, m.detailErr = nil, nil, nil
			return m, m.startCollect()
		}
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m, m.handleClick(msg.Mouse().X, msg.Mouse().Y)
		}
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.PasteMsg:
		if m.screen == screenPassword {
			m.appendPassword(msg.Content)
		} else if m.screen == screenAdmin && m.filtering {
			m.appendFilter(msg.Content)
		}
	}
	return m, nil
}

func (m *monitorModel) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" || key == "q" && m.screen == screenMonitor {
		return tea.Quit
	}
	themeKey := key == "T" || key == "t" && (m.screen == screenMonitor || m.screen == screenAdmin)
	if themeKey && m.screen != screenPassword && !(m.screen == screenAdmin && m.filtering) {
		m.toggleColorMode()
		return nil
	}

	switch m.screen {
	case screenMonitor:
		switch key {
		case "m":
			if !m.admin.enabled() {
				m.status = "Management mode is disabled: configure ADMIN_PASSWORD_HASH."
				return nil
			}
			m.screen, m.password, m.status = screenPassword, "", "Enter the management password."
		case "r":
			m.status = "Refreshing now…"
			return m.startCollect()
		}
	case screenPassword:
		switch key {
		case "esc":
			m.screen, m.password, m.status = screenMonitor, "", "Management mode cancelled."
		case "enter":
			if !m.busy && m.password != "" {
				m.busy = true
				m.status = "Authenticating with the selected node…"
				return m.authenticate(m.password)
			}
		case "backspace", "delete":
			m.password = trimLastRune(m.password)
		default:
			text := msg.Key().Text
			// Some legacy SSH terminals provide Code/String but leave Text empty.
			if text == "" && len([]rune(key)) == 1 {
				text = key
			}
			m.appendPassword(text)
		}
	case screenAdmin:
		if m.filtering {
			switch key {
			case "esc":
				m.filtering = false
				m.status = "Filter editing cancelled."
			case "enter":
				m.filtering = false
				m.cursor, m.processOffset = 0, 0
				m.status = fmt.Sprintf("Filter applied: %q", m.filter)
			case "backspace", "delete":
				m.filter = trimLastRune(m.filter)
				m.cursor, m.processOffset = 0, 0
			default:
				text := msg.Key().Text
				if text == "" && len([]rune(key)) == 1 {
					text = key
				}
				m.appendFilter(text)
			}
			return nil
		}
		switch key {
		case "esc", "q", "m":
			m.screen, m.status, m.adminCredential = screenMonitor, "Returned to read-only monitor.", ""
		case "1", "2":
			m.selectAction(int(key[0] - '1'))
		case "/":
			m.filtering = true
			m.status = "Type a process name, user, or PID."
		case "c":
			m.filter, m.cursor, m.processOffset, m.status = "", 0, 0, "Process filter cleared."
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.clampProcessCursor()
			}
		case "down", "j":
			if m.cursor+1 < len(m.filteredProcesses()) {
				m.cursor++
				m.clampProcessCursor()
			}
		case "enter":
			return m.openProcess(m.cursor)
		case "r":
			return m.startCollect()
		}
	case screenConfirm:
		switch key {
		case "esc", "n":
			m.screen, m.selectedAction, m.status = screenAdmin, nil, "Action cancelled."
		case "y", "enter":
			if m.selectedAction != nil && !m.busy {
				m.busy = true
				m.status = "Running " + m.selectedAction.label + "…"
				return m.runAction(*m.selectedAction)
			}
		}
	case screenProcessDetail:
		switch key {
		case "esc", "q":
			m.screen, m.status = screenAdmin, "Returned to process manager."
		case "r":
			if m.selectedProcess != nil {
				return m.loadProcessDetail(m.selectedProcess.PID)
			}
		case "t":
			if m.selectedProcessCanTerminate() {
				m.screen = screenProcessTerminateConfirm
				m.status = "Confirm before sending SIGTERM."
			}
		}
	case screenProcessTerminateConfirm:
		switch key {
		case "esc", "n":
			m.screen, m.status = screenProcessDetail, "Process termination cancelled."
		case "y", "enter":
			if m.selectedProcess != nil && !m.busy {
				m.busy = true
				m.status = fmt.Sprintf("Sending SIGTERM to PID %d…", m.selectedProcess.PID)
				return m.terminateProcess(m.selectedProcess.PID, m.processDetail.StartTimeTicks)
			}
		}
	}
	return nil
}

func (m *monitorModel) appendPassword(text string) {
	if text == "" || len([]rune(m.password)) >= 128 {
		return
	}
	var accepted []rune
	for _, r := range text {
		if !unicode.IsControl(r) {
			accepted = append(accepted, r)
		}
	}
	remaining := 128 - len([]rune(m.password))
	if len(accepted) > remaining {
		accepted = accepted[:remaining]
	}
	m.password += string(accepted)
}

func (m *monitorModel) appendFilter(text string) {
	for _, r := range text {
		if !unicode.IsControl(r) && len([]rune(m.filter)) < 80 {
			m.filter += string(r)
		}
	}
	m.cursor, m.processOffset = 0, 0
}

func (m *monitorModel) handleClick(x, y int) tea.Cmd {
	if m.screen == screenAdmin {
		if y == 2 {
			firstLabel, secondLabel := m.adminActionLabels()
			firstWidth := compactButtonWidth("1", firstLabel)
			if x < firstWidth {
				m.selectAction(0)
			} else if x > firstWidth && x < firstWidth+compactButtonWidth("2", secondLabel)+1 {
				m.selectAction(1)
			}
		} else if y == 3 {
			m.filtering = true
			m.status = "Type a process name, user, or PID."
		} else if y >= adminProcessRowStart && y < adminProcessRowStart+m.visibleAdminProcessCount() {
			return m.openProcess(m.processOffset + y - adminProcessRowStart)
		}
	}
	if m.screen == screenConfirm {
		if x >= 2 && y >= 8 && y <= 11 && m.selectedAction != nil && !m.busy {
			m.busy = true
			m.status = "Running " + m.selectedAction.label + "…"
			return m.runAction(*m.selectedAction)
		}
		if x >= 2 && y >= 13 && y <= 16 {
			m.screen, m.selectedAction, m.status = screenAdmin, nil, "Action cancelled."
		}
	}
	if m.screen == screenProcessDetail && y == 2 && x < compactButtonWidth("T", "Terminate process") {
		if m.selectedProcessCanTerminate() {
			m.screen = screenProcessTerminateConfirm
			m.status = "Confirm before sending SIGTERM."
		}
	}
	if m.screen == screenProcessTerminateConfirm && y == 2 {
		confirmWidth := compactButtonWidth("Y", "Send SIGTERM")
		if x < confirmWidth && m.selectedProcess != nil && !m.busy {
			m.busy = true
			m.status = fmt.Sprintf("Sending SIGTERM to PID %d…", m.selectedProcess.PID)
			return m.terminateProcess(m.selectedProcess.PID, m.processDetail.StartTimeTicks)
		}
		if x > confirmWidth {
			m.screen, m.status = screenProcessDetail, "Process termination cancelled."
		}
	}
	return nil
}

func (m *monitorModel) selectAction(index int) {
	if index < 0 || index >= len(m.admin.actions) {
		return
	}
	action := m.admin.actions[index]
	m.selectedAction = &action
	m.screen = screenConfirm
	m.status = "Review the confirmation before running this action."
}

func (m *monitorModel) filteredProcesses() []processInfo {
	query := strings.ToLower(strings.TrimSpace(m.filter))
	if query == "" {
		return m.snapshot.Processes
	}
	filtered := make([]processInfo, 0, len(m.snapshot.Processes))
	for _, process := range m.snapshot.Processes {
		searchable := strings.ToLower(fmt.Sprintf("%d %s %s", process.PID, process.User, process.Command))
		if strings.Contains(searchable, query) {
			filtered = append(filtered, process)
		}
	}
	return filtered
}

func (m *monitorModel) clampProcessCursor() {
	processes := m.filteredProcesses()
	if len(processes) == 0 {
		m.cursor, m.processOffset = 0, 0
		return
	}
	if m.cursor >= len(processes) {
		m.cursor = len(processes) - 1
	}
	rows := m.adminVisibleProcessRows()
	if m.cursor < m.processOffset {
		m.processOffset = m.cursor
	}
	if m.cursor >= m.processOffset+rows {
		m.processOffset = m.cursor - rows + 1
	}
	maxOffset := max(0, len(processes)-rows)
	if m.processOffset > maxOffset {
		m.processOffset = maxOffset
	}
}

func (m *monitorModel) adminVisibleProcessRows() int {
	return max(3, m.height-9)
}

func (m *monitorModel) visibleAdminProcessCount() int {
	return min(m.adminVisibleProcessRows(), max(0, len(m.filteredProcesses())-m.processOffset))
}

func (m *monitorModel) adminActionLabels() (string, string) {
	if usableWidth(m.width) < 54 {
		return "Restart", "Reboot"
	}
	return m.admin.actions[0].label, m.admin.actions[1].label
}

func (m *monitorModel) openProcess(index int) tea.Cmd {
	processes := m.filteredProcesses()
	if index < 0 || index >= len(processes) {
		return nil
	}
	process := processes[index]
	m.cursor = index
	m.selectedProcess = &process
	m.processDetail, m.detailErr = nil, nil
	m.screen = screenProcessDetail
	m.status = fmt.Sprintf("Loading details for PID %d…", process.PID)
	return m.loadProcessDetail(process.PID)
}

func (m *monitorModel) loadProcessDetail(pid int) tea.Cmd {
	return func() tea.Msg {
		if m.backend == nil {
			return processDetailMsg{pid: pid, err: errors.New("monitor backend is unavailable")}
		}
		detail, err := m.backend.ProcessDetail(pid, m.adminCredential)
		return processDetailMsg{pid: pid, detail: detail, err: err}
	}
}

func (m *monitorModel) terminateProcess(pid int, expectedStartTicks uint64) tea.Cmd {
	return func() tea.Msg {
		if m.backend == nil {
			return processTerminateResultMsg{pid: pid, err: errors.New("monitor backend is unavailable")}
		}
		err := m.backend.TerminateProcess(pid, expectedStartTicks, m.adminCredential)
		return processTerminateResultMsg{pid: pid, err: err}
	}
}

func canTerminatePID(pid int) bool {
	return pid > 1 && pid != os.Getpid()
}

func (m *monitorModel) selectedProcessCanTerminate() bool {
	return m.selectedProcess != nil &&
		m.processDetail != nil &&
		m.detailErr == nil &&
		m.processDetail.StartTimeTicks != 0 &&
		canTerminatePID(m.selectedProcess.PID)
}

func (m *monitorModel) runAction(action adminAction) tea.Cmd {
	return func() tea.Msg {
		if m.backend == nil {
			return actionResultMsg{action: action, err: errors.New("monitor backend is unavailable")}
		}
		output, err := m.backend.RunAction(action.ID, m.adminCredential)
		return actionResultMsg{action: action, output: output, err: err}
	}
}

func (m *monitorModel) authenticate(password string) tea.Cmd {
	return func() tea.Msg {
		if m.backend == nil {
			if m.admin != nil {
				return authenticationResultMsg{password: password, ok: m.admin.authenticate(password)}
			}
			return authenticationResultMsg{password: password, err: errors.New("monitor backend is unavailable")}
		}
		ok, err := m.backend.Authenticate(password)
		return authenticationResultMsg{password: password, ok: ok, err: err}
	}
}

func (m *monitorModel) View() tea.View {
	var body string
	switch m.screen {
	case screenPassword:
		body = m.passwordView()
	case screenAdmin:
		body = m.adminView()
	case screenConfirm:
		body = m.confirmView()
	case screenProcessDetail:
		body = m.processDetailView()
	case screenProcessTerminateConfirm:
		body = m.processTerminateConfirmView()
	default:
		body = m.monitorView()
	}
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "GPU SSH Monitor"
	if m.profile == machineProfileNAS {
		v.WindowTitle = "NAS Monitor"
	}
	v.BackgroundColor, v.ForegroundColor = viewColors(m.colorMode)
	return v
}

func (m *monitorModel) monitorView() string {
	if m.profile == machineProfileNAS || m.snapshot.Profile == machineProfileNAS {
		return m.nasView()
	}
	layout := newDashboardLayout(m.width, m.height, len(m.snapshot.GPUs), m.snapshot.GPUError != "")
	w := layout.width
	header := dashboardHeader(w, m.snapshot.CollectedAt, m.colorMode, m.nodeName)
	if m.snapshot.CollectedAt.IsZero() {
		return strings.Join([]string{header, "", panelStyle(w).Render("Collecting system metrics…")}, "\n")
	}

	memoryPercent := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
	diskPercent := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
	metrics := []metricCard{
		{
			title: "CPU", value: fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent), detail: m.snapshot.LoadAverage,
			visual: metricVisualCPU, usage: m.snapshot.CPUPercent, primaryHistory: m.cpuHistory,
			titleStyle: cpuTitleStyle, borderColor: colorCPUBorder,
		},
		{
			title: "MEMORY", value: fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal)), detail: fmt.Sprintf("%.1f%% used", memoryPercent),
			visual: metricVisualMeter, usage: memoryPercent,
			titleStyle: memoryTitleStyle, borderColor: colorMemoryBorder,
		},
		{
			title: "DISK /", value: fmt.Sprintf("%s / %s", bytes(m.snapshot.DiskUsed), bytes(m.snapshot.DiskTotal)), detail: fmt.Sprintf("%.1f%% used", diskPercent),
			visual: metricVisualMeter, usage: diskPercent,
			titleStyle: diskTitleStyle, borderColor: colorDiskBorder,
		},
		{
			title: "NETWORK", value: fmt.Sprintf("↓ %s/s  ↑ %s/s", bytes(m.snapshot.NetworkRX), bytes(m.snapshot.NetworkTX)), detail: fmt.Sprintf("TOTAL ↓ %s  ↑ %s", bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal)),
			visual: metricVisualNetwork, primaryHistory: m.networkRXHistory, secondaryHistory: m.networkTXHistory,
			titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
		},
	}
	metricRows := renderMetricRows(metrics, layout)
	gpu := m.gpuPanel(layout)
	slurm := ""
	if m.slurmQueue != nil {
		queueRows := m.slurmQueueRows()
		queueLines := queueRows + 4
		if m.slurmQueue.Warning != "" {
			queueLines++
		}
		layout.processRows = max(0, layout.processRows-queueLines)
		slurm = m.slurmNodePanel(layout.width, queueRows)
	}
	processes := m.processPanel(layout)
	footer := renderFooter(w, m.status)
	if m.loadErr != nil {
		footer = warningStyle.Render("Metric warning: " + m.loadErr.Error())
	}
	sections := []string{header, metricRows, gpu}
	if slurm != "" {
		sections = append(sections, slurm)
	}
	sections = append(sections, processes, footer)
	return strings.Join(sections, "\n")
}

func dashboardHeader(width int, collectedAt time.Time, mode colorMode, nodeName string) string {
	return dashboardHeaderNamed("GPU SSH MONITOR", width, collectedAt, mode, nodeName)
}

func dashboardHeaderNamed(label string, width int, collectedAt time.Time, mode colorMode, nodeName string) string {
	title := titleStyle.Render(label)
	if nodeName != "" {
		title += dimStyle.Render(" / ") + accentStyle.Render(truncate(nodeName, max(8, width/3)))
	}
	live := liveBadgeStyle.Render("● LIVE")
	clock := "--:--:--"
	if !collectedAt.IsZero() {
		clock = collectedAt.Format("15:04:05")
	}
	modeLabel := strings.ToUpper(mode.String())
	metaLabel := "READ ONLY  ·  1s  ·  " + modeLabel
	if width < 80 {
		metaLabel = "1s  ·  " + modeLabel
	}
	meta := dimStyle.Render(metaLabel) + "  " + clockStyle.Render(clock)
	if width < 54 {
		return title + "  " + live + "\n" + meta
	}
	left := title + "  " + live
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(meta))
	return left + strings.Repeat(" ", gap) + meta
}

func (m *monitorModel) gpuPanel(layout dashboardLayout) string {
	w := layout.width
	if m.snapshot.GPUError != "" {
		return btopPanel(w, "GPU", "UNAVAILABLE", dimStyle.Render("nvidia-smi unavailable: "+m.snapshot.GPUError), gpuTitleStyle, colorGPUBorder)
	}
	lines := make([]string, 0, max(1, len(m.snapshot.GPUs)*3))
	memoryUsedWidth, memoryTotalWidth := gpuMemoryWidths(m.snapshot.GPUs)
	nameWidth := gpuNameWidth(m.snapshot.GPUs, w)
	for _, gpu := range m.snapshot.GPUs {
		memory := gpuMemoryText(gpu, memoryUsedWidth, memoryTotalWidth)
		status, statusStyle := gpuLoadStatus(gpu.Utilization)
		summary := fmt.Sprintf("%s  %-*s  %s",
			accentStyle.Render(fmt.Sprintf("GPU %d", gpu.Index)),
			nameWidth, truncate(gpu.Name, nameWidth),
			statusStyle.Render(fmt.Sprintf("%-6s", status)),
		)
		if layout.compactGPU {
			lines = append(lines,
				summary,
				bar(gpu.Utilization, max(8, w-lipgloss.Width(memory)-12))+fmt.Sprintf(" %3.0f%%  %s", gpu.Utilization, memory),
				statusStyle.Render(gpuTelemetry(gpu, false)),
			)
			continue
		}
		showPowerLimit := w >= 128
		telemetry := gpuTelemetry(gpu, showPowerLimit)
		barWidth := min(42, max(10, w-lipgloss.Width(memory)-lipgloss.Width(telemetry)-22))
		lines = append(lines,
			summary,
			fmt.Sprintf("       %s %3.0f%%  %s  %s",
				bar(gpu.Utilization, barWidth), gpu.Utilization,
				memory,
				statusStyle.Render(telemetry),
			),
		)
	}
	if len(m.snapshot.GPUs) == 0 {
		lines = append(lines, dimStyle.Render("No NVIDIA GPU was reported."))
	}
	meta := fmt.Sprintf("%d DEVICE", len(m.snapshot.GPUs))
	if len(m.snapshot.GPUs) != 1 {
		meta += "S"
	}
	return btopPanel(w, "GPU", meta, strings.Join(lines, "\n"), gpuTitleStyle, colorGPUBorder)
}

func gpuTelemetry(gpu gpuInfo, showPowerLimit bool) string {
	if showPowerLimit && gpu.PowerLimit > 0 {
		return fmt.Sprintf("CLK %4d MHz · PWR %3.0f/%3.0f W · %3d°C", gpu.ClockMHz, gpu.Power, gpu.PowerLimit, gpu.Temperature)
	}
	return fmt.Sprintf("CLK %4d MHz · PWR %3.0f W · %3d°C", gpu.ClockMHz, gpu.Power, gpu.Temperature)
}

func gpuMemoryWidths(gpus []gpuInfo) (usedWidth, totalWidth int) {
	for _, gpu := range gpus {
		usedWidth = max(usedWidth, lipgloss.Width(bytes(gpu.MemoryUsed)))
		totalWidth = max(totalWidth, lipgloss.Width(bytes(gpu.MemoryTotal)))
	}
	return max(usedWidth, 1), max(totalWidth, 1)
}

func gpuMemoryText(gpu gpuInfo, usedWidth, totalWidth int) string {
	return fmt.Sprintf("MEM %*s/%*s", usedWidth, bytes(gpu.MemoryUsed), totalWidth, bytes(gpu.MemoryTotal))
}

func gpuNameWidth(gpus []gpuInfo, width int) int {
	nameWidth := 8
	for _, gpu := range gpus {
		nameWidth = max(nameWidth, lipgloss.Width(gpu.Name))
	}
	return min(nameWidth, max(8, width-22))
}

func gpuLoadStatus(utilization float64) (string, lipgloss.Style) {
	switch {
	case utilization < 5:
		return "IDLE", gpuIdleStyle
	case utilization < 35:
		return "LIGHT", gpuLightStyle
	case utilization < 65:
		return "ACTIVE", gpuActiveStyle
	case utilization < 85:
		return "BUSY", gpuBusyStyle
	case utilization < 95:
		return "HIGH", gpuHighStyle
	default:
		return "MAX", gpuMaxStyle
	}
}

func (m *monitorModel) processPanel(layout dashboardLayout) string {
	w := layout.width
	format := newProcessFormat(w)
	lines := []string{
		processLegend(w),
		processTableHeader(format.header(), w-4),
	}
	for i, p := range m.snapshot.Processes {
		if i >= layout.processRows {
			break
		}
		lines = append(lines, processStateStyle(p.State).Render(format.row(p)))
	}
	if len(m.snapshot.Processes) == 0 {
		lines = append(lines, dimStyle.Render("No process data available."))
	}
	return btopPanel(w, "PROCESSES", "CPU ↓  ·  READ ONLY", strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
}

func (m *monitorModel) passwordView() string {
	w := usableWidth(m.width)
	masked := strings.Repeat("•", len([]rune(m.password)))
	if masked == "" {
		masked = dimStyle.Render("password")
	}
	content := strings.Join([]string{
		titleStyle.Render("MANAGEMENT MODE"),
		dimStyle.Render("Authentication is required before any host action."),
		"",
		"Password: " + inputStyle.Render(masked+" "),
		"",
		dimStyle.Render("Type or paste password  ·  [enter] continue  [esc] return"),
		warningStyle.Render(m.status),
	}, "\n")
	return centeredPanel(w, content)
}

func (m *monitorModel) adminView() string {
	w := usableWidth(m.width)
	processes := m.filteredProcesses()
	rows := m.adminVisibleProcessRows()
	end := min(len(processes), m.processOffset+rows)
	if m.processOffset > end {
		m.processOffset = 0
	}
	format := newProcessFormat(w)
	table := []string{
		processLegend(w),
		processTableHeader(format.header(), w-4),
	}
	for i := m.processOffset; i < end; i++ {
		row := format.row(processes[i])
		if i == m.cursor && !m.filtering {
			row = selectedRowStyle.Render(row)
		} else {
			row = processStateStyle(processes[i].State).Render(row)
		}
		table = append(table, row)
	}
	if len(processes) == 0 {
		table = append(table, warningStyle.Render("No processes match this filter."))
	}

	filterValue := m.filter
	if filterValue == "" {
		filterValue = "process name, user, or PID"
	}
	if m.filtering {
		filterValue += "█"
	}
	filterLine := accentStyle.Render("FILTER /") + " " + inputStyle.Width(max(12, w-14)).Render(truncate(filterValue, max(8, w-18)))
	firstLabel, secondLabel := m.adminActionLabels()
	actions := lipgloss.JoinHorizontal(lipgloss.Top,
		compactButton("1", firstLabel, false),
		" ",
		compactButton("2", secondLabel, true),
	)
	return strings.Join([]string{
		titleStyle.Render("MANAGEMENT MODE") + "  " + accentStyle.Render("AUTHORIZED"),
		dimStyle.Render(truncate("Click a process for details. Host actions remain fixed and require confirmation.", w)),
		actions,
		filterLine,
		btopPanel(w, "PROCESS MANAGER", fmt.Sprintf("%d/%d  ·  ROWS %d-%d", len(processes), len(m.snapshot.Processes), min(len(processes), m.processOffset+1), end), strings.Join(table, "\n"), processTitleStyle, colorProcessBorder),
		helpStyle.Render("[/] filter  [↑/↓] select  [enter/click] details  [c] clear  [esc] monitor") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

func (m *monitorModel) confirmView() string {
	w := usableWidth(m.width)
	if m.selectedAction == nil {
		m.screen = screenAdmin
		return m.adminView()
	}
	confirmLabel := "Confirm action"
	if m.busy {
		confirmLabel = "Running…"
	}
	return strings.Join([]string{
		titleStyle.Render("CONFIRM MANAGEMENT ACTION"),
		warningStyle.Render("This runs on the host immediately: " + m.selectedAction.label),
		"",
		panelStyle(w).Render(sectionStyle.Render(m.selectedAction.label) + "\n" + m.selectedAction.description),
		"",
		actionCard(w, "Y", confirmLabel, "Click to execute", true),
		"",
		actionCard(w, "N", "Cancel", "Return without making a change", false),
		"",
		helpStyle.Render("[y/enter] confirm  [n/esc] cancel") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

func (m *monitorModel) processDetailView() string {
	w := usableWidth(m.width)
	if m.selectedProcess == nil {
		m.screen = screenAdmin
		return m.adminView()
	}
	pid := m.selectedProcess.PID
	action := compactButton("T", "Terminate process", true)
	if !m.selectedProcessCanTerminate() {
		action = dimStyle.Render("Termination unavailable until details are loaded, or for protected processes")
	}
	body := "Loading process details…"
	if m.detailErr != nil {
		body = warningStyle.Render("Unable to read process details: " + m.detailErr.Error())
	} else if m.processDetail != nil {
		d := m.processDetail
		body = strings.Join([]string{
			sectionStyle.Render(fmt.Sprintf("%s  PID %d", d.Name, d.PID)),
			fmt.Sprintf("%s %d    %s %d    %s %s    %s %d", dimStyle.Render("PPID"), d.PPID, dimStyle.Render("UID"), d.UID, dimStyle.Render("USER"), d.User, dimStyle.Render("THREADS"), d.Threads),
			fmt.Sprintf("%s %s    %s %.1f%%    %s %.1f%%    %s %s", dimStyle.Render("STATE"), d.State, dimStyle.Render("CPU"), d.CPU, dimStyle.Render("MEM"), d.Memory, dimStyle.Render("RSS"), bytes(d.RSS)),
			fmt.Sprintf("%s %s", dimStyle.Render("ELAPSED"), elapsed(d.Elapsed)),
			"",
			dimStyle.Render("COMMAND") + "  " + truncate(d.CommandLine, max(12, w-14)),
			dimStyle.Render("EXEC") + "     " + truncate(d.Executable, max(12, w-14)),
			dimStyle.Render("CWD") + "      " + truncate(d.CWD, max(12, w-14)),
		}, "\n")
	}
	return strings.Join([]string{
		titleStyle.Render("PROCESS DETAILS") + "  " + accentStyle.Render(fmt.Sprintf("PID %d", pid)),
		dimStyle.Render("Inspect the selected process before taking action."),
		action + "  " + compactButton("Esc", "Back to process manager", false),
		panelStyle(w).Render(body),
		helpStyle.Render("[t/click] terminate  [r] reload  [esc] back") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

func (m *monitorModel) processTerminateConfirmView() string {
	w := usableWidth(m.width)
	if m.selectedProcess == nil {
		m.screen = screenAdmin
		return m.adminView()
	}
	p := m.selectedProcess
	confirm := compactButton("Y", "Send SIGTERM", true)
	if m.busy {
		confirm = compactButton("…", "Sending SIGTERM", true)
	}
	return strings.Join([]string{
		titleStyle.Render("CONFIRM PROCESS TERMINATION"),
		warningStyle.Render(fmt.Sprintf("PID %d (%s) will receive SIGTERM.", p.PID, p.Command)),
		confirm + "  " + compactButton("N", "Cancel", false),
		panelStyle(w).Render(strings.Join([]string{
			sectionStyle.Render(fmt.Sprintf("PID %d", p.PID)),
			fmt.Sprintf("%s %s", dimStyle.Render("USER"), p.User),
			fmt.Sprintf("%s %.1f%%    %s %.1f%%    %s %s", dimStyle.Render("CPU"), p.CPU, dimStyle.Render("MEM"), p.Memory, dimStyle.Render("RSS"), bytes(p.RSS)),
			dimStyle.Render("COMMAND") + "  " + truncate(p.Command, max(12, w-14)),
		}, "\n")),
		helpStyle.Render("[y/enter/click] confirm  [n/esc] cancel") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

type metricVisual int

const (
	metricVisualNone metricVisual = iota
	metricVisualCPU
	metricVisualMeter
	metricVisualNetwork
)

type metricCard struct {
	title, value, detail             string
	visual                           metricVisual
	usage                            float64
	primaryHistory, secondaryHistory []float64
	titleStyle                       lipgloss.Style
	borderColor                      color.Color
}

const adminProcessRowStart = 7

type dashboardLayout struct {
	width       int
	height      int
	metricCols  int
	processRows int
	compactGPU  bool
}

func newDashboardLayout(width, height, gpuCount int, gpuUnavailable bool) dashboardLayout {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	cols := 1
	switch {
	case width >= 132:
		cols = 4
	case width >= 52:
		cols = 2
	}
	compactGPU := width < 112
	metricLines := ((4 + cols - 1) / cols) * 5 // three content lines plus border
	gpuContentLines := 0
	if gpuUnavailable {
		gpuContentLines = 1
	} else if compactGPU {
		gpuContentLines = gpuCount * 3
	} else {
		gpuContentLines = gpuCount * 2
	}
	if !gpuUnavailable && gpuCount == 0 {
		gpuContentLines = 1
	}
	gpuLines := gpuContentLines + 2 // rounded border
	// Header, metric cards, GPU panel, process panel headings/border, and footer.
	headerLines := 1
	if width < 54 {
		headerLines = 2
	}
	reserved := headerLines + metricLines + gpuLines + 4 + 1
	processRows := max(0, height-reserved)
	return dashboardLayout{width: width, height: height, metricCols: cols, processRows: processRows, compactGPU: compactGPU}
}

func renderMetricRows(cards []metricCard, layout dashboardLayout) string {
	cardWidth := max(16, (layout.width-(layout.metricCols-1))/layout.metricCols)
	render := func(c metricCard) string {
		contentWidth := max(4, cardWidth-4)
		title := c.titleStyle
		if title.GetForeground() == nil {
			title = sectionStyle
		}
		border := c.borderColor
		if border == nil {
			border = colorPanelBorder
		}
		return btopPanel(cardWidth, c.title, "", strings.Join([]string{
			valueStyle.Render(truncate(c.value, cardWidth-4)),
			dimStyle.Render(truncate(c.detail, cardWidth-4)),
			renderMetricVisual(c, contentWidth),
		}, "\n"), title, border)
	}
	rows := make([]string, 0, (len(cards)+layout.metricCols-1)/layout.metricCols)
	for start := 0; start < len(cards); start += layout.metricCols {
		end := min(start+layout.metricCols, len(cards))
		items := make([]string, 0, end-start)
		for _, card := range cards[start:end] {
			if len(items) > 0 {
				items = append(items, " ")
			}
			items = append(items, render(card))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, items...))
	}
	return strings.Join(rows, "\n")
}

func renderMetricVisual(card metricCard, width int) string {
	switch card.visual {
	case metricVisualCPU:
		_, style := gpuLoadStatus(card.usage)
		return sparkline(card.primaryHistory, width, 100, style)
	case metricVisualMeter:
		return bar(card.usage, width)
	case metricVisualNetwork:
		graphWidth := max(2, (width-5)/2)
		ceiling := historyMax(card.primaryHistory, card.secondaryHistory)
		return networkRXStyle.Render("↓") +
			sparkline(card.primaryHistory, graphWidth, ceiling, networkRXStyle) +
			dimStyle.Render("  ") + networkTXStyle.Render("↑") +
			sparkline(card.secondaryHistory, graphWidth, ceiling, networkTXStyle)
	default:
		return dimStyle.Render(strings.Repeat("─", max(1, width)))
	}
}

type processFormat struct {
	mode         int
	commandWidth int
}

const (
	processFull = iota
	processMedium
	processCompact
)

func newProcessFormat(width int) processFormat {
	if width >= 112 {
		return processFormat{mode: processFull, commandWidth: max(16, width-66)}
	}
	if width >= 78 {
		return processFormat{mode: processMedium, commandWidth: max(14, width-56)}
	}
	return processFormat{mode: processCompact, commandWidth: max(10, width-32)}
}

func (f processFormat) header() string {
	switch f.mode {
	case processFull:
		return fmt.Sprintf("%-9s %-13s %6s  %6s  %-8s  %-8s  %s", "PID", "USER", "CPU", "MEM", "RSS", "ELAPSED", "COMMAND")
	case processMedium:
		return fmt.Sprintf("%-9s %-13s %6s  %6s  %-8s  %s", "PID", "USER", "CPU", "MEM", "RSS", "COMMAND")
	default:
		return fmt.Sprintf("%-9s %6s  %6s  %s", "PID", "CPU", "MEM", "COMMAND")
	}
}

func (f processFormat) row(p processInfo) string {
	switch f.mode {
	case processFull:
		return fmt.Sprintf("%-9d %-13s %5.1f%%  %5.1f%%  %-8s  %-8s  %s", p.PID, truncate(p.User, 12), p.CPU, p.Memory, bytes(p.RSS), elapsed(p.Elapsed), truncate(p.Command, f.commandWidth))
	case processMedium:
		return fmt.Sprintf("%-9d %-13s %5.1f%%  %5.1f%%  %-8s  %s", p.PID, truncate(p.User, 12), p.CPU, p.Memory, bytes(p.RSS), truncate(p.Command, f.commandWidth))
	default:
		return fmt.Sprintf("%-9d %5.1f%%  %5.1f%%  %s", p.PID, p.CPU, p.Memory, truncate(p.Command, f.commandWidth))
	}
}

func processLegend(width int) string {
	items := []struct {
		state string
		full  string
		short string
	}{
		{state: "R", full: "RUNNING", short: "RUN"},
		{state: "S", full: "SLEEPING", short: "SLEEP"},
		{state: "D", full: "WAITING", short: "WAIT"},
		{state: "T", full: "STOPPED", short: "STOP"},
		{state: "Z", full: "ZOMBIE", short: "ZOMBIE"},
		{state: "I", full: "IDLE", short: "IDLE"},
	}
	parts := []string{dimStyle.Render("STATE")}
	for _, item := range items {
		label := item.state
		switch {
		case width >= 108:
			label += " " + item.full
		case width >= 78:
			label += " " + item.short
		}
		parts = append(parts, processStateStyle(item.state).Render(label))
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

func processStateStyle(state string) lipgloss.Style {
	if state == "" {
		return processDefaultStyle
	}
	switch state[0] {
	case 'R':
		return processRunningStyle
	case 'S':
		return processSleepingStyle
	case 'D':
		return processWaitingStyle
	case 'T', 't':
		return processStoppedStyle
	case 'Z':
		return processZombieStyle
	case 'I':
		return processIdleStyle
	case 'X', 'x':
		return processDeadStyle
	default:
		return processDefaultStyle
	}
}

func actionCard(width int, key, title, detail string, dangerous bool) string {
	style := clickableStyle(width)
	label := accentStyle.Render("["+key+"] ") + title
	if dangerous {
		label = dangerStyle.Render("["+key+"] ") + title
	}
	return style.Render(label + "\n" + dimStyle.Render(detail))
}

func compactButton(key, label string, dangerous bool) string {
	style := compactButtonStyle
	keyStyle := accentStyle
	if dangerous {
		style = compactDangerButtonStyle
		keyStyle = dangerStyle
	}
	return style.Render(keyStyle.Render("["+key+"]") + " " + label)
}

func compactButtonWidth(key, label string) int {
	return lipgloss.Width(compactButton(key, label, false))
}

func centeredPanel(width int, content string) string {
	return "\n" + panelStyle(min(68, width)).Render(content)
}

type adminAction struct {
	ID          int
	label       string
	description string
	command     string
}

type adminController struct {
	password     string
	passwordHash string
	actions      []adminAction
}

func newAdminController() *adminController {
	return &adminController{
		password:     os.Getenv("ADMIN_PASSWORD"),
		passwordHash: os.Getenv("ADMIN_PASSWORD_HASH"),
		actions: []adminAction{
			{ID: 0, label: "Restart monitor service", description: "Restart the machine SSH monitor service.", command: envString("ADMIN_RESTART_MONITOR_CMD", "systemctl restart gpu-ssh-monitor.service")},
			{ID: 1, label: "Reboot machine", description: "Restart the entire host. Active workloads will be interrupted.", command: envString("ADMIN_REBOOT_CMD", "systemctl reboot")},
		},
	}
}

func newRemoteAdminController() *adminController {
	admin := newAdminController()
	admin.password = "remote"
	admin.passwordHash = ""
	return admin
}

func (a *adminController) enabled() bool { return a.password != "" || a.passwordHash != "" }

func (a *adminController) authenticate(password string) bool {
	if a.passwordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password)) == nil
	}
	return a.password != "" && subtle.ConstantTimeCompare([]byte(a.password), []byte(password)) == 1
}

type monitorSnapshot struct {
	CollectedAt             time.Time
	Profile                 string
	NodeName                string
	CPUPercent              float64
	LoadAverage             string
	MemoryUsed, MemoryTotal uint64
	DiskUsed, DiskTotal     uint64
	NetworkRX, NetworkTX    uint64
	NetworkRXTotal          uint64
	NetworkTXTotal          uint64
	NetworkInterfaces       []networkInterfaceInfo
	Filesystems             []filesystemInfo
	Services                []serviceHealth
	Containers              []containerInfo
	DockerError             string
	PM2Processes            []pm2ProcessInfo
	PM2Error                string
	GPUs                    []gpuInfo
	GPUError                string
	Processes               []processInfo
}

type gpuInfo struct {
	Index                   int
	Name                    string
	Utilization             float64
	MemoryUsed, MemoryTotal uint64
	Temperature             int
	ClockMHz                int
	Power, PowerLimit       float64
}

type processInfo struct {
	PID                  int
	User, State, Command string
	CPU, Memory          float64
	RSS, Elapsed         uint64
}

type processDetail struct {
	PID, PPID, UID, Threads int
	User, State, Name       string
	CPU, Memory             float64
	RSS, Elapsed            uint64
	CommandLine             string
	Executable              string
	CWD                     string
	StartTimeTicks          uint64
}

type cpuCounters struct{ total, idle uint64 }

type metricsCollector struct {
	config             machineConfig
	previousCPU        cpuCounters
	previousNet        netCounters
	previousInterfaces map[string]networkDeviceCounters
	haveCPU            bool
	haveNet            bool
	lastNetAt          time.Time
	lastServiceAt      time.Time
	cachedServices     []serviceHealth
	cachedContainers   []containerInfo
	cachedDockerError  string
	cachedPM2Processes []pm2ProcessInfo
	cachedPM2Error     string
}

type netCounters struct{ rx, tx uint64 }

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func newMetricsCollector(config machineConfig) *metricsCollector {
	return &metricsCollector{config: config, previousInterfaces: make(map[string]networkDeviceCounters)}
}

func (c *metricsCollector) collect() (monitorSnapshot, error) {
	return c.collectWithProcesses(true)
}

func (c *metricsCollector) collectSummary() (monitorSnapshot, error) {
	return c.collectWithProcesses(false)
}

func (c *metricsCollector) collectWithProcesses(includeProcesses bool) (monitorSnapshot, error) {
	s := monitorSnapshot{CollectedAt: time.Now()}
	var errs []string
	if counters, err := readCPUCounters(); err != nil {
		errs = append(errs, "cpu: "+err.Error())
	} else {
		if c.haveCPU {
			totalDelta := counters.total - c.previousCPU.total
			idleDelta := counters.idle - c.previousCPU.idle
			if totalDelta > 0 {
				s.CPUPercent = 100 * float64(totalDelta-idleDelta) / float64(totalDelta)
			}
		}
		c.previousCPU, c.haveCPU = counters, true
	}
	if used, total, err := readMemory(); err != nil {
		errs = append(errs, "memory: "+err.Error())
	} else {
		s.MemoryUsed, s.MemoryTotal = used, total
	}
	if used, total, err := readDisk(); err != nil {
		errs = append(errs, "disk: "+err.Error())
	} else {
		s.DiskUsed, s.DiskTotal = used, total
	}
	if err := c.collectNetwork(&s); err != nil {
		errs = append(errs, "network: "+err.Error())
	}
	s.LoadAverage = readLoadAverage()
	if c.config.Profile == machineProfileGPU {
		s.GPUs, s.GPUError = readGPUs()
	}
	c.collectMachineDetails(&s)
	if includeProcesses {
		if processes, err := readProcesses(); err != nil {
			errs = append(errs, "processes: "+err.Error())
		} else {
			s.Processes = processes
		}
	}
	if len(errs) > 0 {
		return s, errors.New(strings.Join(errs, "; "))
	}
	return s, nil
}

func readCPUCounters() (cpuCounters, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuCounters{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var c cpuCounters
		for _, field := range fields[1:] {
			value, _ := strconv.ParseUint(field, 10, 64)
			c.total += value
		}
		idle, _ := strconv.ParseUint(fields[4], 10, 64)
		if len(fields) > 5 {
			iowait, _ := strconv.ParseUint(fields[5], 10, 64)
			idle += iowait
		}
		c.idle = idle
		return c, nil
	}
	return cpuCounters{}, errors.New("cpu line missing")
}

func readMemory() (used, total uint64, err error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	total = values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, 0, errors.New("MemTotal missing")
	}
	return total - available, total, nil
}

func readDisk() (used, total uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, err
	}
	total = stat.Blocks * uint64(stat.Bsize)
	used = (stat.Blocks - stat.Bavail) * uint64(stat.Bsize)
	return used, total, nil
}

func readNetwork() (netCounters, error) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return netCounters{}, err
	}
	var total netCounters
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		total.rx += rx
		total.tx += tx
	}
	return total, nil
}

func readLoadAverage() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "load unavailable"
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return "load unavailable"
	}
	return "load " + strings.Join(fields[:3], " · ")
}

func readGPUs() ([]gpuInfo, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,clocks.current.graphics,power.draw,power.limit", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, compactCommandError(err)
	}
	return parseGPUs(output), ""
}

func parseGPUs(output []byte) []gpuInfo {
	var gpus []gpuInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 9 {
			continue
		}
		index, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		util, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		memUsed, _ := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
		memTotal, _ := strconv.ParseUint(strings.TrimSpace(parts[4]), 10, 64)
		temp, _ := strconv.Atoi(strings.TrimSpace(parts[5]))
		clockMHz, _ := strconv.Atoi(strings.TrimSpace(parts[6]))
		power, _ := strconv.ParseFloat(strings.TrimSpace(parts[7]), 64)
		powerLimit, _ := strconv.ParseFloat(strings.TrimSpace(parts[8]), 64)
		gpus = append(gpus, gpuInfo{
			Index: index, Name: strings.TrimSpace(parts[1]), Utilization: util,
			MemoryUsed: memUsed * 1024 * 1024, MemoryTotal: memTotal * 1024 * 1024,
			Temperature: temp, ClockMHz: clockMHz, Power: power, PowerLimit: powerLimit,
		})
	}
	return gpus
}

func readProcesses() ([]processInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,user=,stat=,pcpu=,pmem=,rss=,etimes=,comm=", "--sort=-pcpu").Output()
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		memory, _ := strconv.ParseFloat(fields[4], 64)
		rss, _ := strconv.ParseUint(fields[5], 10, 64)
		elapsed, _ := strconv.ParseUint(fields[6], 10, 64)
		// ps can briefly appear as the busiest process because it is sampling
		// the whole process table. It is collector noise, not a useful host task.
		if fields[7] == "ps" {
			continue
		}
		processes = append(processes, processInfo{
			PID: pid, User: sanitizeTerminalText(fields[1]), State: sanitizeTerminalText(fields[2]), CPU: cpu, Memory: memory,
			RSS: rss * 1024, Elapsed: elapsed, Command: sanitizeTerminalText(strings.Join(fields[7:], " ")),
		})
	}
	sort.SliceStable(processes, func(i, j int) bool { return processes[i].CPU > processes[j].CPU })
	return processes, nil
}

func readProcessDetail(pid int) (processDetail, error) {
	if pid <= 0 {
		return processDetail{}, errors.New("invalid PID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,uid=,user=,stat=,pcpu=,pmem=,rss=,etimes=,nlwp=,comm=").Output()
	if err != nil {
		return processDetail{}, fmt.Errorf("process no longer exists")
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) < 11 {
		return processDetail{}, errors.New("incomplete process information")
	}
	detail := processDetail{}
	detail.PID, _ = strconv.Atoi(fields[0])
	detail.PPID, _ = strconv.Atoi(fields[1])
	detail.UID, _ = strconv.Atoi(fields[2])
	detail.User = sanitizeTerminalText(fields[3])
	detail.State = sanitizeTerminalText(fields[4])
	detail.CPU, _ = strconv.ParseFloat(fields[5], 64)
	detail.Memory, _ = strconv.ParseFloat(fields[6], 64)
	rss, _ := strconv.ParseUint(fields[7], 10, 64)
	detail.RSS = rss * 1024
	detail.Elapsed, _ = strconv.ParseUint(fields[8], 10, 64)
	detail.Threads, _ = strconv.Atoi(fields[9])
	detail.Name = sanitizeTerminalText(strings.Join(fields[10:], " "))

	procRoot := fmt.Sprintf("/proc/%d", pid)
	if commandLine, readErr := os.ReadFile(procRoot + "/cmdline"); readErr == nil {
		detail.CommandLine = sanitizeTerminalText(strings.ReplaceAll(string(commandLine), "\x00", " "))
	}
	if detail.CommandLine == "" {
		detail.CommandLine = detail.Name
	}
	if executable, readErr := os.Readlink(procRoot + "/exe"); readErr == nil {
		detail.Executable = sanitizeTerminalText(executable)
	} else {
		detail.Executable = "unavailable"
	}
	if cwd, readErr := os.Readlink(procRoot + "/cwd"); readErr == nil {
		detail.CWD = sanitizeTerminalText(cwd)
	} else {
		detail.CWD = "unavailable"
	}
	detail.StartTimeTicks, _ = readProcessStartTicks(pid)
	return detail, nil
}

func readProcessStartTicks(pid int) (uint64, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseProcessStartTicks(stat)
}

func parseProcessStartTicks(stat []byte) (uint64, error) {
	// The command name in field 2 may contain spaces. Everything after its
	// closing parenthesis starts at field 3; starttime is field 22.
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+2 >= len(stat) {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(stat[closing+2:]))
	if len(fields) <= 19 {
		return 0, errors.New("incomplete process stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func sanitizeTerminalText(value string) string {
	var safe strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if !unicode.IsControl(r) {
			safe.WriteRune(r)
		}
	}
	return safe.String()
}

var (
	colorPanelBorder         = lipgloss.Color("#40516B")
	colorCPUBorder           = lipgloss.Color("#315F57")
	colorMemoryBorder        = lipgloss.Color("#514A78")
	colorDiskBorder          = lipgloss.Color("#6B5438")
	colorNetworkBorder       = lipgloss.Color("#315B72")
	colorGPUBorder           = lipgloss.Color("#53517A")
	colorProcessBorder       = lipgloss.Color("#465572")
	titleStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	sectionStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	valueStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Bold(true)
	accentStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#B9A4FF")).Bold(true)
	cpuTitleStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#7EE2B8")).Bold(true)
	memoryTitleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4A7E7")).Bold(true)
	diskTitleStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#F6C177")).Bold(true)
	networkTitleStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#70D6FF")).Bold(true)
	gpuTitleStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#B9A4FF")).Bold(true)
	processTitleStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#9FC3FF")).Bold(true)
	dangerStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8A80")).Bold(true)
	dimStyle                 = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3BC"))
	warningStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD180"))
	helpStyle                = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDB4FF")).Bold(true)
	clockStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#F6C177")).Bold(true)
	liveBadgeStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#7EE2B8")).Bold(true).Padding(0, 1)
	keycapStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#B9A4FF")).Bold(true).Padding(0, 1)
	hintLabelStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#C9CEDA"))
	processHeaderStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#9FC3FF")).Bold(true)
	inputStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#24283B")).Padding(0, 1)
	selectedRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#B9A4FF")).Bold(true)
	networkRXStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#70D6FF")).Bold(true)
	networkTXStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4A7E7")).Bold(true)
	processRunningStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	processSleepingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9FC3FF"))
	processWaitingStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")).Bold(true)
	processStoppedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD180"))
	processZombieStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF7B72")).Bold(true)
	processIdleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#8C91A8"))
	processDeadStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5370")).Bold(true)
	processDefaultStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#D7DAE0"))
	gpuIdleStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#8C91A8"))
	gpuLightStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#70D6FF")).Bold(true)
	gpuActiveStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	gpuBusyStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD166")).Bold(true)
	gpuHighStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9F43")).Bold(true)
	gpuMaxStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5370")).Bold(true)
	compactButtonStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#30374A")).Padding(0, 1)
	compactDangerButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFF4F2")).Background(lipgloss.Color("#5A3037")).Padding(0, 1)
)

func panelStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5B6B8A")).Padding(0, 1).Width(max(20, width-2))
}
func clickableStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#B9A4FF")).Padding(0, 1).Width(max(20, width-2))
}

func btopPanel(width int, title, meta, content string, titleStyle lipgloss.Style, borderColor color.Color) string {
	width = max(20, width)
	innerWidth := width - 2
	contentWidth := width - 4
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)

	titleText := " " + truncate(title, max(4, innerWidth-5)) + " "
	metaText := ""
	if meta != "" {
		metaText = " " + meta + " "
	}
	fillWidth := innerWidth - 1 - lipgloss.Width(titleText) - lipgloss.Width(metaText)
	if fillWidth < 1 {
		metaText = ""
		fillWidth = max(1, innerWidth-1-lipgloss.Width(titleText))
	}
	top := borderStyle.Render("╭─") +
		titleStyle.Render(titleText) +
		borderStyle.Render(strings.Repeat("─", fillWidth)) +
		dimStyle.Render(metaText) +
		borderStyle.Render("╮")

	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	rendered := make([]string, 0, len(lines)+2)
	rendered = append(rendered, top)
	lineStyle := lipgloss.NewStyle().Width(contentWidth)
	for _, line := range lines {
		line = ansi.Truncate(line, contentWidth, "…")
		rendered = append(rendered,
			borderStyle.Render("│")+" "+lineStyle.Render(line)+" "+borderStyle.Render("│"),
		)
	}
	rendered = append(rendered, borderStyle.Render("╰"+strings.Repeat("─", innerWidth)+"╯"))
	return strings.Join(rendered, "\n")
}

func processTableHeader(header string, width int) string {
	return processHeaderStyle.Width(max(1, width)).MaxWidth(max(1, width)).Render(header)
}

func renderFooter(width int, status string) string {
	hints := strings.Join([]string{
		keyHint("m", "management"),
		keyHint("t", "theme"),
		keyHint("r", "refresh"),
		keyHint("q", "quit"),
	}, "  ")
	remaining := width - lipgloss.Width(hints) - 2
	if remaining < 12 || strings.TrimSpace(status) == "" {
		return hints
	}
	return hints + "  " + dimStyle.Render(truncate(status, remaining))
}

func keyHint(key, label string) string {
	return keycapStyle.Render(key) + " " + hintLabelStyle.Render(label)
}

func parseColorMode(value string) colorMode {
	if strings.EqualFold(strings.TrimSpace(value), "light") {
		return colorModeLight
	}
	return colorModeDark
}

func (mode colorMode) String() string {
	if mode == colorModeLight {
		return "light"
	}
	return "dark"
}

func (m *monitorModel) toggleColorMode() {
	if m.colorMode == colorModeLight {
		m.colorMode = colorModeDark
	} else {
		m.colorMode = colorModeLight
	}
	m.status = fmt.Sprintf("%s theme enabled for this SSH session.", strings.ToUpper(m.colorMode.String()))
}

func viewColors(mode colorMode) (background, foreground color.Color) {
	if mode == colorModeLight {
		return lipgloss.Color("#F4F1EA"), lipgloss.Color("#263244")
	}
	return lipgloss.Color("#0E1117"), lipgloss.Color("#D7DAE0")
}

type themeColorSwap struct{ dark, light string }

var lightThemeColorSwaps = []themeColorSwap{
	{"#10131A", "#F8FAFC"},
	{"#0E1117", "#F4F1EA"},
	{"#9EE493", "#1F6F50"},
	{"#F5F7FF", "#172033"},
	{"#B9A4FF", "#5A3FA3"},
	{"#7EE2B8", "#166B4B"},
	{"#C4A7E7", "#704E91"},
	{"#F6C177", "#945E13"},
	{"#70D6FF", "#0B668A"},
	{"#9FC3FF", "#315D91"},
	{"#FF8A80", "#B42318"},
	{"#9CA3BC", "#5B6475"},
	{"#FFD180", "#8A5A00"},
	{"#CDB4FF", "#664A96"},
	{"#C9CEDA", "#384252"},
	{"#B8C0D9", "#334155"},
	{"#24283B", "#E4E9F0"},
	{"#FFB86C", "#A94B00"},
	{"#FF7B72", "#B42318"},
	{"#8C91A8", "#667085"},
	{"#FF5370", "#A80F2D"},
	{"#D7DAE0", "#2E3642"},
	{"#FFD166", "#8A5A00"},
	{"#FF9F43", "#B54708"},
	{"#30374A", "#D9E0EA"},
	{"#FFF4F2", "#8F1D18"},
	{"#5A3037", "#F4D8D5"},
	{"#40516B", "#7A889C"},
	{"#315F57", "#5E877D"},
	{"#514A78", "#756D99"},
	{"#6B5438", "#92795C"},
	{"#315B72", "#66869A"},
	{"#53517A", "#74739C"},
	{"#465572", "#697D9B"},
	{"#5B6B8A", "#7183A0"},
}

// Rendering always uses the immutable dark palette. Light mode rewrites only
// known ANSI RGB sequences in the completed view, keeping theme selection
// session-local without mutating shared styles across concurrent SSH sessions.
func applyLightTheme(rendered string) string {
	for _, swap := range lightThemeColorSwaps {
		dark := ansiRGB(swap.dark)
		light := ansiRGB(swap.light)
		rendered = strings.ReplaceAll(rendered, "38;2;"+dark, "38;2;"+light)
		rendered = strings.ReplaceAll(rendered, "48;2;"+dark, "48;2;"+light)
	}
	return rendered
}

func ansiRGB(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return ""
	}
	red, _ := strconv.ParseUint(hex[0:2], 16, 8)
	green, _ := strconv.ParseUint(hex[2:4], 16, 8)
	blue, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return fmt.Sprintf("%d;%d;%d", red, green, blue)
}

func usableWidth(width int) int {
	if width <= 0 {
		return 80
	}
	return max(36, width)
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func percent(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}
func truncate(s string, width int) string {
	r := []rune(s)
	if width <= 1 {
		return ""
	}
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
func bytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(value)
	i := 0
	for f >= float64(unit) && i < len(units)-1 {
		f /= float64(unit)
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
func elapsed(seconds uint64) string {
	d := time.Duration(seconds) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func appendHistory(history []float64, value float64, limit int) []float64 {
	history = append(history, value)
	if len(history) > limit {
		history = append([]float64(nil), history[len(history)-limit:]...)
	}
	return history
}

func historyMax(histories ...[]float64) float64 {
	maxValue := 0.0
	for _, history := range histories {
		for _, value := range history {
			if value > maxValue {
				maxValue = value
			}
		}
	}
	if maxValue <= 0 {
		return 1
	}
	return maxValue
}

func sparkline(history []float64, width int, ceiling float64, style lipgloss.Style) string {
	const levels = "▁▂▃▄▅▆▇█"
	width = max(1, width)
	if ceiling <= 0 {
		ceiling = 1
	}
	if len(history) > width {
		history = history[len(history)-width:]
	}
	var graph strings.Builder
	for _, value := range history {
		ratio := value / ceiling
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		index := int(ratio * float64(len([]rune(levels))-1))
		graph.WriteRune([]rune(levels)[index])
	}
	padding := width - len(history)
	return dimStyle.Render(strings.Repeat("·", padding)) + style.Render(graph.String())
}

func bar(value float64, width int) string {
	filled := int(value / 100 * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	_, loadStyle := gpuLoadStatus(value)
	return loadStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", width-filled))
}
func compactCommandError(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "command not found"
	}
	return "not available"
}
func compactOutput(output string) string {
	output = strings.TrimSpace(strings.ReplaceAll(output, "\n", " "))
	if output == "" {
		return "."
	}
	return ": " + truncate(output, 70)
}
func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
