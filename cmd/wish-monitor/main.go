package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
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
	"golang.org/x/crypto/bcrypt"
)

const refreshInterval = time.Second

func main() {
	host := envString("SSH_HOST", "0.0.0.0")
	port := envString("SSH_PORT", "23234")
	hostKeyPath := envString("SSH_HOST_KEY_PATH", ".ssh/gpu-ssh-monitor_ed25519")
	admin := newAdminController()

	server, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithMiddleware(
			bubblewish.Middleware(func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
				pty, _, ok := sess.Pty()
				if !ok {
					return nil, nil
				}
				return newMonitorModel(admin, sess, pty.Window.Width, pty.Window.Height), nil
			}),
			activeterm.Middleware(),
			accesscontrol.Middleware(),
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
	collector       *metricsCollector
	admin           *adminController
	user            string
	remote          string
	width           int
	height          int
	snapshot        monitorSnapshot
	loadErr         error
	screen          screen
	password        string
	filter          string
	filtering       bool
	cursor          int
	processOffset   int
	selectedAction  *adminAction
	selectedProcess *processInfo
	processDetail   *processDetail
	detailErr       error
	status          string
	busy            bool
	collecting      bool
}

type screen int

const (
	screenMonitor screen = iota
	screenPassword
	screenAdmin
	screenConfirm
	screenProcessDetail
	screenProcessTerminateConfirm
)

func newMonitorModel(admin *adminController, sess ssh.Session, width, height int) *monitorModel {
	remote := "local"
	if addr := sess.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	return &monitorModel{
		collector: newMetricsCollector(),
		admin:     admin,
		user:      sess.User(),
		remote:    remote,
		width:     width,
		height:    height,
		status:    "Live data refreshes every second.",
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
		snapshot, err := m.collector.collect()
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
		m.loadErr = msg.err
		m.clampProcessCursor()
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
			if m.admin.authenticate(m.password) {
				m.screen, m.password, m.status = screenAdmin, "", "Management mode enabled. Filter or select a process."
			} else {
				m.password, m.status = "", "Incorrect password. Try again or press Esc."
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
			m.screen, m.status = screenMonitor, "Returned to read-only monitor."
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
		detail, err := readProcessDetail(pid)
		return processDetailMsg{pid: pid, detail: detail, err: err}
	}
}

func (m *monitorModel) terminateProcess(pid int, expectedStartTicks uint64) tea.Cmd {
	user, remote := m.user, m.remote
	return func() tea.Msg {
		if !canTerminatePID(pid) {
			return processTerminateResultMsg{pid: pid, err: errors.New("protected process")}
		}
		currentStartTicks, err := readProcessStartTicks(pid)
		if err != nil {
			return processTerminateResultMsg{pid: pid, err: errors.New("process no longer exists")}
		}
		if expectedStartTicks == 0 || currentStartTicks != expectedStartTicks {
			return processTerminateResultMsg{pid: pid, err: errors.New("process identity changed; reload details")}
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			err = process.Signal(syscall.SIGTERM)
		}
		log.Info("Process termination requested", "pid", pid, "signal", "SIGTERM", "user", user, "remote", remote, "error", err)
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
	user, remote := m.user, m.remote
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "sh", "-c", action.command).CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out")
		}
		log.Info("Management action requested", "action", action.label, "user", user, "remote", remote, "error", err)
		return actionResultMsg{action: action, output: string(output), err: err}
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
	v := tea.NewView(body)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "GPU SSH Monitor"
	return v
}

func (m *monitorModel) monitorView() string {
	layout := newDashboardLayout(m.width, m.height, len(m.snapshot.GPUs), m.snapshot.GPUError != "")
	w := layout.width
	header := dashboardHeader(w)
	if m.snapshot.CollectedAt.IsZero() {
		return strings.Join([]string{header, "", panelStyle(w).Render("Collecting system metrics…")}, "\n")
	}

	metrics := []metricCard{
		{"CPU", fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent), m.snapshot.LoadAverage},
		{"MEMORY", fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal)), fmt.Sprintf("%.1f%% used", percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal))},
		{"DISK /", fmt.Sprintf("%s / %s", bytes(m.snapshot.DiskUsed), bytes(m.snapshot.DiskTotal)), fmt.Sprintf("%.1f%% used", percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal))},
		{"NETWORK", fmt.Sprintf("↓ %s/s", bytes(m.snapshot.NetworkRX)), fmt.Sprintf("↑ %s/s", bytes(m.snapshot.NetworkTX))},
	}
	metricRows := renderMetricRows(metrics, layout)
	gpu := m.gpuPanel(layout)
	processes := m.processPanel(layout)
	footer := helpStyle.Render("[m] management  [r] refresh  [q] quit") + "  " + dimStyle.Render(m.status)
	if m.loadErr != nil {
		footer = warningStyle.Render("Metric warning: " + m.loadErr.Error())
	}
	return strings.Join([]string{header, metricRows, gpu, processes, footer}, "\n")
}

func dashboardHeader(width int) string {
	title := titleStyle.Render("GPU SSH MONITOR")
	meta := dimStyle.Render("LIVE  ·  READ-ONLY  ·  1s REFRESH")
	if width < 68 {
		return title + "\n" + meta
	}
	return title + dimStyle.Render("  /  ") + meta
}

func (m *monitorModel) gpuPanel(layout dashboardLayout) string {
	w := layout.width
	if m.snapshot.GPUError != "" {
		return panelStyle(w).Render(sectionStyle.Render("GPU") + "\n" + dimStyle.Render("nvidia-smi unavailable: "+m.snapshot.GPUError))
	}
	lines := []string{sectionStyle.Render("GPU")}
	for _, gpu := range m.snapshot.GPUs {
		if layout.compactGPU {
			lines = append(lines,
				accentStyle.Render(fmt.Sprintf("GPU %d", gpu.Index))+"  "+truncate(gpu.Name, w-12),
				bar(gpu.Utilization, max(8, w-43))+fmt.Sprintf(" %3.0f%%  MEM %s/%s", gpu.Utilization, bytes(gpu.MemoryUsed), bytes(gpu.MemoryTotal)),
			)
			continue
		}
		nameWidth := min(28, max(14, w/5))
		barWidth := min(42, max(10, w-nameWidth-66))
		lines = append(lines, fmt.Sprintf("%s  %-*s  %s %3.0f%%  %s  %s",
			accentStyle.Render(fmt.Sprintf("GPU %d", gpu.Index)),
			nameWidth, truncate(gpu.Name, nameWidth),
			bar(gpu.Utilization, barWidth), gpu.Utilization,
			fmt.Sprintf("MEM %s/%s", bytes(gpu.MemoryUsed), bytes(gpu.MemoryTotal)),
			dimStyle.Render(fmt.Sprintf("%d°C · %.0fW", gpu.Temperature, gpu.Power)),
		))
	}
	if len(m.snapshot.GPUs) == 0 {
		lines = append(lines, dimStyle.Render("No NVIDIA GPU was reported."))
	}
	return panelStyle(w).Render(strings.Join(lines, "\n"))
}

func (m *monitorModel) processPanel(layout dashboardLayout) string {
	w := layout.width
	format := newProcessFormat(w)
	lines := []string{sectionStyle.Render("TOP PROCESSES") + "  " + dimStyle.Render("read-only · sorted by CPU · colored by STAT"), processHeaderStyle.Render(format.header())}
	for i, p := range m.snapshot.Processes {
		if i >= layout.processRows {
			break
		}
		lines = append(lines, processStateStyle(p.State).Render(format.row(p)))
	}
	if len(m.snapshot.Processes) == 0 {
		lines = append(lines, dimStyle.Render("No process data available."))
	}
	return panelStyle(w).Render(strings.Join(lines, "\n"))
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
		sectionStyle.Render("PROCESS MANAGER") + "  " + dimStyle.Render(fmt.Sprintf("%d / %d processes · rows %d-%d · colored by STAT", len(processes), len(m.snapshot.Processes), min(len(processes), m.processOffset+1), end)),
		processHeaderStyle.Render(format.header()),
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
		panelStyle(w).Render(strings.Join(table, "\n")),
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

type metricCard struct{ title, value, detail string }

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
	case width >= 76:
		cols = 2
	}
	compactGPU := width < 96
	metricLines := ((4 + cols - 1) / cols) * 4 // two content lines plus border
	gpuContentLines := 1
	if gpuUnavailable {
		gpuContentLines = 2
	} else if compactGPU {
		gpuContentLines += gpuCount * 2
	} else {
		gpuContentLines += gpuCount
	}
	gpuLines := gpuContentLines + 2 // rounded border
	// Header, metric cards, GPU panel, process panel headings/border, and footer.
	headerLines := 1
	if width < 68 {
		headerLines = 2
	}
	reserved := headerLines + metricLines + gpuLines + 4 + 1
	processRows := max(2, height-reserved)
	return dashboardLayout{width: width, height: height, metricCols: cols, processRows: processRows, compactGPU: compactGPU}
}

func renderMetricRows(cards []metricCard, layout dashboardLayout) string {
	cardWidth := max(16, (layout.width-(layout.metricCols-1))/layout.metricCols)
	render := func(c metricCard) string {
		line := valueStyle.Render(truncate(c.value, cardWidth-4)) + "  " + dimStyle.Render(truncate(c.detail, max(6, cardWidth-lipgloss.Width(c.value)-7)))
		return metricStyle(cardWidth).Render(sectionStyle.Render(c.title) + "\n" + line)
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
		return processFormat{mode: processFull, commandWidth: max(16, width-72)}
	}
	if width >= 78 {
		return processFormat{mode: processMedium, commandWidth: max(14, width-52)}
	}
	return processFormat{mode: processCompact, commandWidth: max(10, width-35)}
}

func (f processFormat) header() string {
	switch f.mode {
	case processFull:
		return fmt.Sprintf("%-9s %-13s %-5s %6s  %6s  %-8s  %-8s  %s", "PID", "USER", "STAT", "CPU", "MEM", "RSS", "ELAPSED", "COMMAND")
	case processMedium:
		return fmt.Sprintf("%-9s %-13s %-5s %6s  %6s  %-8s  %s", "PID", "USER", "STAT", "CPU", "MEM", "RSS", "COMMAND")
	default:
		return fmt.Sprintf("%-9s %-5s %6s  %6s  %s", "PID", "STAT", "CPU", "MEM", "COMMAND")
	}
}

func (f processFormat) row(p processInfo) string {
	switch f.mode {
	case processFull:
		return fmt.Sprintf("%-9d %-13s %-5s %5.1f%%  %5.1f%%  %-8s  %-8s  %s", p.PID, truncate(p.User, 12), truncate(p.State, 5), p.CPU, p.Memory, bytes(p.RSS), elapsed(p.Elapsed), truncate(p.Command, f.commandWidth))
	case processMedium:
		return fmt.Sprintf("%-9d %-13s %-5s %5.1f%%  %5.1f%%  %-8s  %s", p.PID, truncate(p.User, 12), truncate(p.State, 5), p.CPU, p.Memory, bytes(p.RSS), truncate(p.Command, f.commandWidth))
	default:
		return fmt.Sprintf("%-9d %-5s %5.1f%%  %5.1f%%  %s", p.PID, truncate(p.State, 5), p.CPU, p.Memory, truncate(p.Command, f.commandWidth))
	}
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
			{label: "Restart monitor service", description: "Restart the GPU SSH monitor service.", command: envString("ADMIN_RESTART_MONITOR_CMD", "systemctl restart gpu-tui-monitor.service")},
			{label: "Reboot machine", description: "Restart the entire host. Active workloads will be interrupted.", command: envString("ADMIN_REBOOT_CMD", "systemctl reboot")},
		},
	}
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
	CPUPercent              float64
	LoadAverage             string
	MemoryUsed, MemoryTotal uint64
	DiskUsed, DiskTotal     uint64
	NetworkRX, NetworkTX    uint64
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
	Power                   float64
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
	previousCPU cpuCounters
	previousNet netCounters
	haveCPU     bool
	haveNet     bool
	lastNetAt   time.Time
}

type netCounters struct{ rx, tx uint64 }

func newMetricsCollector() *metricsCollector { return &metricsCollector{} }

func (c *metricsCollector) collect() (monitorSnapshot, error) {
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
	if net, err := readNetwork(); err != nil {
		errs = append(errs, "network: "+err.Error())
	} else if c.haveNet {
		delta := time.Since(c.lastNetAt).Seconds()
		if delta > 0 {
			s.NetworkRX = uint64(float64(net.rx-c.previousNet.rx) / delta)
			s.NetworkTX = uint64(float64(net.tx-c.previousNet.tx) / delta)
		}
		c.previousNet, c.lastNetAt = net, time.Now()
	} else {
		c.previousNet, c.haveNet, c.lastNetAt = net, true, time.Now()
	}
	s.LoadAverage = readLoadAverage()
	s.GPUs, s.GPUError = readGPUs()
	if processes, err := readProcesses(); err != nil {
		errs = append(errs, "processes: "+err.Error())
	} else {
		s.Processes = processes
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
	output, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, compactCommandError(err)
	}
	var gpus []gpuInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 7 {
			continue
		}
		index, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		util, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		memUsed, _ := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
		memTotal, _ := strconv.ParseUint(strings.TrimSpace(parts[4]), 10, 64)
		temp, _ := strconv.Atoi(strings.TrimSpace(parts[5]))
		power, _ := strconv.ParseFloat(strings.TrimSpace(parts[6]), 64)
		gpus = append(gpus, gpuInfo{Index: index, Name: strings.TrimSpace(parts[1]), Utilization: util, MemoryUsed: memUsed * 1024 * 1024, MemoryTotal: memTotal * 1024 * 1024, Temperature: temp, Power: power})
	}
	return gpus, ""
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
	titleStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	sectionStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	valueStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Bold(true)
	accentStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#B9A4FF")).Bold(true)
	dangerStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8A80")).Bold(true)
	dimStyle                 = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3BC"))
	warningStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD180"))
	helpStyle                = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDB4FF")).Bold(true)
	processHeaderStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3BC")).Bold(true)
	inputStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#24283B")).Padding(0, 1)
	selectedRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#B9A4FF")).Bold(true)
	processRunningStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	processSleepingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9FC3FF"))
	processWaitingStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")).Bold(true)
	processStoppedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD180"))
	processZombieStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF7B72")).Bold(true)
	processIdleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#8C91A8"))
	processDeadStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5370")).Bold(true)
	processDefaultStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#D7DAE0"))
	compactButtonStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#30374A")).Padding(0, 1)
	compactDangerButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFF4F2")).Background(lipgloss.Color("#5A3037")).Padding(0, 1)
)

func panelStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5B6B8A")).Padding(0, 1).Width(max(20, width-2))
}
func metricStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3D506F")).Padding(0, 1).Width(width - 2)
}
func clickableStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#B9A4FF")).Padding(0, 1).Width(max(20, width-2))
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
func bar(value float64, width int) string {
	filled := int(value / 100 * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return accentStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", width-filled))
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
