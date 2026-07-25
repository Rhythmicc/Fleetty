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

const refreshInterval = 2 * time.Second

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
	collector *metricsCollector
	admin     *adminController
	user      string
	remote    string
	width     int
	height    int
	snapshot  monitorSnapshot
	loadErr   error
	screen    screen
	password  string
	selected  *adminAction
	status    string
	busy      bool
}

type screen int

const (
	screenMonitor screen = iota
	screenPassword
	screenAdmin
	screenConfirm
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
		status:    "Live data refreshes every 2 seconds.",
	}
}

func (m *monitorModel) Init() tea.Cmd {
	return tea.Batch(m.collect(), tick())
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

func (m *monitorModel) collect() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.collector.collect()
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m *monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.snapshot = msg.snapshot
		m.loadErr = msg.err
	case tickMsg:
		return m, tea.Batch(m.collect(), tick())
	case actionResultMsg:
		m.busy = false
		if msg.err != nil {
			m.status = fmt.Sprintf("%s failed: %v", msg.action.label, msg.err)
		} else {
			m.status = fmt.Sprintf("%s requested%s", msg.action.label, compactOutput(msg.output))
		}
		m.screen = screenAdmin
		m.selected = nil
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m, m.handleClick(msg.Mouse().X, msg.Mouse().Y)
		}
	case tea.KeyMsg:
		return m, m.handleKey(msg)
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
			return m.collect()
		}
	case screenPassword:
		switch key {
		case "esc":
			m.screen, m.password, m.status = screenMonitor, "", "Management mode cancelled."
		case "enter":
			if m.admin.authenticate(m.password) {
				m.screen, m.password, m.status = screenAdmin, "", "Management mode enabled. Select an action."
			} else {
				m.password, m.status = "", "Incorrect password. Try again or press Esc."
			}
		case "backspace", "delete":
			m.password = trimLastRune(m.password)
		default:
			if text := msg.Key().Text; text != "" && len([]rune(m.password)) < 128 {
				m.password += text
			}
		}
	case screenAdmin:
		switch key {
		case "esc", "q", "m":
			m.screen, m.status = screenMonitor, "Returned to read-only monitor."
		case "1", "2":
			m.selectAction(int(key[0] - '1'))
		}
	case screenConfirm:
		switch key {
		case "esc", "n":
			m.screen, m.selected, m.status = screenAdmin, nil, "Action cancelled."
		case "y", "enter":
			if m.selected != nil && !m.busy {
				m.busy = true
				m.status = "Running " + m.selected.label + "…"
				return m.runAction(*m.selected)
			}
		}
	}
	return nil
}

func (m *monitorModel) handleClick(x, y int) tea.Cmd {
	if m.screen == screenAdmin {
		// Each two-line card has a border, so it occupies four terminal rows.
		if x >= 2 && y >= 3 && y <= 6 {
			m.selectAction(0)
		}
		if x >= 2 && y >= 8 && y <= 11 {
			m.selectAction(1)
		}
	}
	if m.screen == screenConfirm {
		if x >= 2 && y >= 8 && y <= 11 && m.selected != nil && !m.busy {
			m.busy = true
			m.status = "Running " + m.selected.label + "…"
			return m.runAction(*m.selected)
		}
		if x >= 2 && y >= 13 && y <= 16 {
			m.screen, m.selected, m.status = screenAdmin, nil, "Action cancelled."
		}
	}
	return nil
}

func (m *monitorModel) selectAction(index int) {
	if index < 0 || index >= len(m.admin.actions) {
		return
	}
	action := m.admin.actions[index]
	m.selected = &action
	m.screen = screenConfirm
	m.status = "Review the confirmation before running this action."
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
	meta := dimStyle.Render("LIVE  ·  READ-ONLY  ·  2s REFRESH")
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
	lines := []string{sectionStyle.Render("TOP PROCESSES") + "  " + dimStyle.Render("read-only · sorted by CPU"), processHeaderStyle.Render(format.header())}
	for i, p := range m.snapshot.Processes {
		if i >= layout.processRows {
			break
		}
		lines = append(lines, format.row(p))
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
		dimStyle.Render("[enter] continue  [esc] return to monitor"),
		warningStyle.Render(m.status),
	}, "\n")
	return centeredPanel(w, content)
}

func (m *monitorModel) adminView() string {
	w := usableWidth(m.width)
	first, second := m.admin.actions[0], m.admin.actions[1]
	return strings.Join([]string{
		titleStyle.Render("MANAGEMENT MODE") + "  " + accentStyle.Render("AUTHORIZED"),
		dimStyle.Render("Actions are fixed by server configuration. Click a card or press its number."),
		"",
		actionCard(w, "1", first.label, first.description, false),
		"",
		actionCard(w, "2", second.label, second.description, true),
		"",
		helpStyle.Render("[1/2] select  [esc] return to read-only monitor") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

func (m *monitorModel) confirmView() string {
	w := usableWidth(m.width)
	if m.selected == nil {
		m.screen = screenAdmin
		return m.adminView()
	}
	confirmLabel := "Confirm action"
	if m.busy {
		confirmLabel = "Running…"
	}
	return strings.Join([]string{
		titleStyle.Render("CONFIRM MANAGEMENT ACTION"),
		warningStyle.Render("This runs on the host immediately: " + m.selected.label),
		"",
		panelStyle(w).Render(sectionStyle.Render(m.selected.label) + "\n" + m.selected.description),
		"",
		actionCard(w, "Y", confirmLabel, "Click to execute", true),
		"",
		actionCard(w, "N", "Cancel", "Return without making a change", false),
		"",
		helpStyle.Render("[y/enter] confirm  [n/esc] cancel") + "  " + dimStyle.Render(m.status),
	}, "\n")
}

type metricCard struct{ title, value, detail string }

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
			items = append(items, render(card))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(items, " ")))
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
		return processFormat{mode: processFull, commandWidth: max(16, width-66)}
	}
	if width >= 78 {
		return processFormat{mode: processMedium, commandWidth: max(14, width-46)}
	}
	return processFormat{mode: processCompact, commandWidth: max(10, width-29)}
}

func (f processFormat) header() string {
	switch f.mode {
	case processFull:
		return "PID       USER          CPU     MEM     RSS       ELAPSED   COMMAND"
	case processMedium:
		return "PID       USER          CPU     MEM     RSS       COMMAND"
	default:
		return "PID       CPU     MEM     COMMAND"
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

func actionCard(width int, key, title, detail string, dangerous bool) string {
	style := clickableStyle(width)
	label := accentStyle.Render("["+key+"] ") + title
	if dangerous {
		label = dangerStyle.Render("["+key+"] ") + title
	}
	return style.Render(label + "\n" + dimStyle.Render(detail))
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
	PID           int
	User, Command string
	CPU, Memory   float64
	RSS, Elapsed  uint64
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
	output, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,user=,pcpu=,pmem=,rss=,etimes=,comm=", "--sort=-pcpu").Output()
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		memory, _ := strconv.ParseFloat(fields[3], 64)
		rss, _ := strconv.ParseUint(fields[4], 10, 64)
		elapsed, _ := strconv.ParseUint(fields[5], 10, 64)
		// ps can briefly appear as the busiest process because it is sampling
		// the whole process table. It is collector noise, not a useful host task.
		if fields[6] == "ps" {
			continue
		}
		processes = append(processes, processInfo{PID: pid, User: fields[1], CPU: cpu, Memory: memory, RSS: rss * 1024, Elapsed: elapsed, Command: strings.Join(fields[6:], " ")})
	}
	sort.SliceStable(processes, func(i, j int) bool { return processes[i].CPU > processes[j].CPU })
	return processes, nil
}

var (
	titleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	sectionStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#9EE493")).Bold(true)
	valueStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Bold(true)
	accentStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#B9A4FF")).Bold(true)
	dangerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8A80")).Bold(true)
	dimStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3BC"))
	warningStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD180"))
	helpStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDB4FF")).Bold(true)
	processHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3BC")).Bold(true)
	inputStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#24283B")).Padding(0, 1)
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
