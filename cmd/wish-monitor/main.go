package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/log/v2"
	"charm.land/wish/v2"
	"charm.land/wish/v2/accesscontrol"
	"charm.land/wish/v2/activeterm"
	"charm.land/wish/v2/logging"
	"github.com/charmbracelet/ssh"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	host := envString("SSH_HOST", "0.0.0.0")
	port := envString("SSH_PORT", "23234")
	hostKeyPath := envString("SSH_HOST_KEY_PATH", ".ssh/gpu-ssh-monitor_ed25519")
	dashboard := newDashboardDaemon()
	admin := newAdminController()

	server, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithMiddleware(
			dashboardMiddleware(dashboard, admin),
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

	log.Info("Starting SSH monitor", "host", host, "port", port)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("Could not start SSH server", "error", err)
			done <- nil
		}
	}()

	<-done
	log.Info("Stopping SSH monitor")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("Could not stop SSH server", "error", err)
	}
	dashboard.stop()
}

type dashboardDaemon struct {
	mu          sync.Mutex
	workdir     string
	scriptPath  string
	command     string
	socketPath  string
	cmd         *exec.Cmd
	done        chan error
	lastStartAt time.Time
	active      int
}

func newDashboardDaemon() *dashboardDaemon {
	workdir := envString("DASHBOARD_WORKDIR", mustGetwd())
	return &dashboardDaemon{
		workdir:    workdir,
		scriptPath: envString("DASHBOARD_SCRIPT", filepath.Join(workdir, "ssh-dashboard.cjs")),
		command:    envString("DASHBOARD_CMD", envString("NODE_CMD", "node")),
		socketPath: envString("SSH_DASHBOARD_SOCKET", filepath.Join(os.TempDir(), "gpu-ssh-monitor.sock")),
	}
}

func (d *dashboardDaemon) ensureStarted() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.isRunning() && d.socketReady(100*time.Millisecond) {
		return nil
	}
	if d.cmd != nil {
		d.stopLocked()
	}

	if since := time.Since(d.lastStartAt); since > 0 && since < time.Second {
		time.Sleep(time.Second - since)
	}
	d.lastStartAt = time.Now()

	_ = os.Remove(d.socketPath)
	cmd := exec.Command(d.command, d.scriptPath, "--server")
	cmd.Dir = d.workdir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"SSH_DASHBOARD_SOCKET="+d.socketPath,
	)

	if err := cmd.Start(); err != nil {
		return err
	}

	d.cmd = cmd
	d.done = make(chan error, 1)
	go func() {
		d.done <- cmd.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d.socketReady(100 * time.Millisecond) {
			return nil
		}
		select {
		case err := <-d.done:
			d.cmd = nil
			d.done = nil
			return fmt.Errorf("dashboard exited before socket was ready: %w", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}

	d.stopLocked()
	return fmt.Errorf("dashboard socket did not become ready: %s", d.socketPath)
}

func (d *dashboardDaemon) isRunning() bool {
	if d.cmd == nil || d.cmd.Process == nil {
		return false
	}
	select {
	case <-d.done:
		d.cmd = nil
		d.done = nil
		return false
	default:
		return true
	}
}

func (d *dashboardDaemon) socketReady(timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", d.socketPath, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (d *dashboardDaemon) dial() (net.Conn, error) {
	d.clientConnected()
	if err := d.ensureStarted(); err != nil {
		d.clientDisconnected()
		return nil, err
	}
	conn, err := net.DialTimeout("unix", d.socketPath, 2*time.Second)
	if err != nil {
		d.clientDisconnected()
		return nil, err
	}
	return conn, nil
}

func (d *dashboardDaemon) clientConnected() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active++
}

func (d *dashboardDaemon) clientDisconnected() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active > 0 {
		d.active--
	}
	if d.active == 0 {
		d.stopLocked()
	}
}

func (d *dashboardDaemon) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
}

func (d *dashboardDaemon) stopLocked() {
	if d.cmd == nil || d.cmd.Process == nil {
		_ = os.Remove(d.socketPath)
		return
	}

	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
		<-d.done
	}
	d.cmd = nil
	d.done = nil
	_ = os.Remove(d.socketPath)
}

func dashboardMiddleware(dashboard *dashboardDaemon, admin *adminController) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(session ssh.Session) {
			_ = next

			conn, err := dashboard.dial()
			if err != nil {
				_, _ = fmt.Fprintf(session, "failed to connect to dashboard: %v\r\n", err)
				_ = session.Exit(1)
				return
			}
			defer conn.Close()
			defer dashboard.clientDisconnected()
			output := newPausableWriter(session)
			defer output.Resume()

			copyDone := make(chan error, 1)
			go func() {
				_, err := io.Copy(output, conn)
				copyDone <- err
			}()

			inputs := make(chan byte, 64)
			go watchSessionInput(session, inputs)
			adminSession := newAdminSession(admin, session, output, conn)
			_, windowChanges, hasPTY := session.Pty()
			if !hasPTY {
				windowChanges = nil
			}

			signals := make(chan ssh.Signal, 8)
			session.Signals(signals)
			defer session.Signals(nil)
			signalDone := make(chan struct{})
			go watchExitSignals(signals, signalDone)

			for {
				select {
				case <-session.Context().Done():
					_ = conn.Close()
					return
				case input, ok := <-inputs:
					if !ok {
						continue
					}
					action := adminSession.Handle(input)
					switch action {
					case actionRefresh:
						_, _ = conn.Write([]byte("r"))
					case actionExit:
						_ = conn.Close()
						restoreTerminal(session)
						_ = session.Exit(0)
						return
					}
				case <-signalDone:
					_ = conn.Close()
					restoreTerminal(session)
					_ = session.Exit(0)
					return
				case err := <-copyDone:
					if err != nil {
						_ = session.Exit(1)
					}
					return
				case window, ok := <-windowChanges:
					if !ok {
						windowChanges = nil
						continue
					}
					adminSession.Resize(window.Width)
				}
			}
		}
	}
}

type sessionAction int

const (
	actionExit sessionAction = iota
	actionRefresh
	actionNone
)

func restoreTerminal(writer io.Writer) {
	_, _ = writer.Write([]byte("\x1b[?25h\x1b[?1049l"))
}

func watchExitSignals(signals <-chan ssh.Signal, done chan<- struct{}) {
	for sig := range signals {
		if sig == ssh.SIGINT || sig == ssh.SIGTERM || sig == ssh.SIGQUIT || sig == ssh.SIGKILL {
			close(done)
			return
		}
	}
}

func watchSessionInput(reader io.Reader, inputs chan<- byte) {
	defer close(inputs)
	buf := make([]byte, 32)
	for {
		n, err := reader.Read(buf)
		if err != nil {
			return
		}

		for _, b := range buf[:n] {
			inputs <- b
		}
	}
}

// pausableWriter stops dashboard frames while an administrative prompt owns the
// terminal. It deliberately serializes the final frame before the prompt is
// drawn, so monitor output cannot overwrite a password or confirmation prompt.
type pausableWriter struct {
	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
	writer io.Writer
}

func newPausableWriter(writer io.Writer) *pausableWriter {
	result := &pausableWriter{writer: writer}
	result.cond = sync.NewCond(&result.mu)
	return result
}

func (w *pausableWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.paused {
		w.cond.Wait()
	}
	return w.writer.Write(data)
}

func (w *pausableWriter) Pause() {
	w.mu.Lock()
	w.paused = true
	w.mu.Unlock()
}

func (w *pausableWriter) Resume() {
	w.mu.Lock()
	w.paused = false
	w.cond.Broadcast()
	w.mu.Unlock()
}

type adminAction struct {
	key     byte
	label   string
	command string
	kind    adminActionKind
}

type adminActionKind int

const (
	adminActionCommand adminActionKind = iota
	adminActionProcessManager
)

type adminController struct {
	password     string
	passwordHash string
	actions      []adminAction
}

func newAdminController() *adminController {
	controller := &adminController{
		password:     os.Getenv("ADMIN_PASSWORD"),
		passwordHash: os.Getenv("ADMIN_PASSWORD_HASH"),
		actions: []adminAction{
			{key: '1', label: "Restart gpu-ssh-monitor service", command: os.Getenv("ADMIN_RESTART_MONITOR_CMD"), kind: adminActionCommand},
			{key: '2', label: "Reboot this machine", command: os.Getenv("ADMIN_REBOOT_CMD"), kind: adminActionCommand},
			{key: '3', label: "Power off this machine", command: os.Getenv("ADMIN_POWEROFF_CMD"), kind: adminActionCommand},
			{key: '4', label: "Manage processes", kind: adminActionProcessManager},
		},
	}
	if controller.actions[0].command == "" {
		controller.actions[0].command = "systemctl restart gpu-ssh-monitor.service"
	}
	if controller.actions[1].command == "" {
		controller.actions[1].command = "systemctl reboot"
	}
	if controller.actions[2].command == "" {
		controller.actions[2].command = "systemctl poweroff"
	}
	return controller
}

func (a *adminController) enabled() bool {
	return a.password != "" || a.passwordHash != ""
}

func (a *adminController) validPassword(password string) bool {
	if a.passwordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password)) == nil
	}
	if a.password == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.password), []byte(password)) == 1
}

type adminMode int

const (
	adminModeMonitor adminMode = iota
	adminModeDisabled
	adminModePassword
	adminModeMenu
	adminModeConfirm
	adminModeProcessList
	adminModeProcessDetailPID
	adminModeProcessDetail
	adminModeProcessTerminatePID
	adminModeProcessTerminateConfirm
)

type adminSession struct {
	model   *adminModel
	session ssh.Session
	output  *pausableWriter
	conn    net.Conn
}

func newAdminSession(controller *adminController, session ssh.Session, output *pausableWriter, conn net.Conn) *adminSession {
	width := 130
	if pty, _, ok := session.Pty(); ok && pty.Window.Width > 0 {
		width = pty.Window.Width
	}
	return &adminSession{
		model:   &adminModel{controller: controller, width: width},
		session: session,
		output:  output,
		conn:    conn,
	}
}

func (a *adminSession) Resize(width int) {
	if width <= 0 {
		return
	}
	a.model.width = width
	if a.model.mode != adminModeMonitor {
		a.draw()
	}
}

func (a *adminSession) Handle(input byte) sessionAction {
	if input == 0x03 {
		return actionExit
	}
	wasMonitor := a.model.mode == adminModeMonitor
	_, _ = a.model.Update(keyMessage(input))
	if a.model.exit {
		return actionExit
	}

	if wasMonitor && a.model.mode != adminModeMonitor {
		a.output.Pause()
	}
	if a.model.pending != nil {
		a.runPendingAction()
	}
	if a.model.processTask != processTaskNone {
		a.runProcessTask()
	}
	if !wasMonitor && a.model.mode == adminModeMonitor {
		a.leave()
		return actionNone
	}
	if a.model.mode != adminModeMonitor {
		a.draw()
	}
	if wasMonitor && a.model.mode == adminModeMonitor && (input == 'r' || input == 'R') {
		return actionRefresh
	}
	return actionNone
}

func (a *adminSession) runPendingAction() {
	action := *a.model.pending
	a.model.pending = nil
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", action.command)
	command.Stdout = a.session
	command.Stderr = a.session
	err := command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		a.model.status = "Operation timed out."
	} else if err != nil {
		a.model.status = fmt.Sprintf("Operation failed: %v", err)
	} else {
		a.model.status = "Operation requested successfully."
	}
	log.Info("administrative action requested", "user", a.session.User(), "remote_addr", a.session.RemoteAddr().String(), "action", action.label, "error", err)
}

func (a *adminSession) runProcessTask() {
	task := a.model.processTask
	pid := a.model.processPIDValue
	a.model.processTask = processTaskNone

	switch task {
	case processTaskList:
		processes, err := listProcesses()
		if err != nil {
			a.model.status = fmt.Sprintf("Could not list processes: %v", err)
			return
		}
		a.model.processOutput = processes
	case processTaskDetail:
		details, startTime, err := processDetails(pid)
		if err != nil {
			a.model.status = fmt.Sprintf("Could not inspect PID %d: %v", pid, err)
			if a.model.mode == adminModeProcessTerminateConfirm {
				a.model.mode = adminModeProcessList
			}
			return
		}
		a.model.processOutput = details
		a.model.processStartTime = startTime
	case processTaskTerminate:
		if err := terminateProcess(pid, a.model.processStartTime); err != nil {
			a.model.status = fmt.Sprintf("Could not terminate PID %d: %v", pid, err)
		} else {
			a.model.status = fmt.Sprintf("SIGTERM sent to PID %d.", pid)
		}
		processes, err := listProcesses()
		if err == nil {
			a.model.processOutput = processes
		}
	}
}

func (a *adminSession) draw() {
	_, _ = io.WriteString(a.session, "\x1b[2J\x1b[H\x1b[?25h"+a.model.View().Content)
}

func (a *adminSession) leave() {
	a.output.Resume()
	_, _ = a.conn.Write([]byte("r"))
}

// adminModel uses Bubble Tea's Model/Update/View contract. We keep the outer
// SSH loop as the program driver because the dashboard already owns the same
// terminal; running a second tea.Program would make the two renderers race.
type adminModel struct {
	controller       *adminController
	mode             adminMode
	password         []byte
	attempts         int
	selected         adminAction
	pending          *adminAction
	status           string
	exit             bool
	processPID       []byte
	processPIDValue  int
	processStartTime uint64
	processOutput    string
	processTask      processTask
	width            int
}

var (
	adminTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")).Bold(true)
	adminLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7")).Bold(true)
	adminMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6ADC8"))
	adminKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#CBA6F7")).Bold(true)
	adminValueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	adminPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#A6E3A1")).
			Padding(0, 2)
	adminWidePanelStyle   = adminPanelStyle.Width(118)
	adminDangerPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("#F9E2AF")).
				Padding(0, 2)
	adminInfoPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("#CBA6F7")).
				Padding(0, 2)
	adminDestructivePanelStyle = lipgloss.NewStyle().
					Border(lipgloss.NormalBorder()).
					BorderForeground(lipgloss.Color("#F38BA8")).
					Padding(0, 2)
	adminDangerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF")).Bold(true)
	adminDestructiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true)
	adminTableHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")).Bold(true)
)

type processTask int

const (
	processTaskNone processTask = iota
	processTaskList
	processTaskDetail
	processTaskTerminate
)

func (m *adminModel) Init() tea.Cmd {
	return nil
}

func (m *adminModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	input := key.Key().Code

	if m.mode == adminModeMonitor {
		switch input {
		case 0x03, 'q', 'Q':
			m.exit = true
		case 'm', 'M':
			m.password = nil
			m.attempts = 0
			m.status = ""
			if m.controller.enabled() {
				m.mode = adminModePassword
			} else {
				m.mode = adminModeDisabled
			}
		}
		return m, nil
	}

	switch m.mode {
	case adminModeDisabled:
		m.mode = adminModeMonitor
	case adminModePassword:
		m.updatePassword(input)
	case adminModeMenu:
		m.updateMenu(input)
	case adminModeConfirm:
		m.updateConfirmation(input)
	case adminModeProcessList:
		m.updateProcessList(input)
	case adminModeProcessDetailPID:
		m.updateProcessPID(input, false)
	case adminModeProcessDetail:
		m.mode = adminModeProcessList
	case adminModeProcessTerminatePID:
		m.updateProcessPID(input, true)
	case adminModeProcessTerminateConfirm:
		m.updateProcessTermination(input)
	}
	return m, nil
}

func (m *adminModel) updatePassword(input rune) {
	switch input {
	case 0x03, tea.KeyEscape:
		m.mode = adminModeMonitor
	case tea.KeyEnter, '\n':
		password := string(m.password)
		m.password = nil
		if m.controller.validPassword(password) {
			m.mode = adminModeMenu
			m.status = ""
			return
		}
		m.attempts++
		if m.attempts >= 3 {
			m.mode = adminModeMonitor
			return
		}
		m.status = "Incorrect password."
	case tea.KeyBackspace, 0x08:
		if len(m.password) > 0 {
			m.password = m.password[:len(m.password)-1]
		}
	default:
		if input >= 0x20 && input <= 0x7e && len(m.password) < 256 {
			m.password = append(m.password, byte(input))
		}
	}
}

func (m *adminModel) updateMenu(input rune) {
	if input == 0x03 || input == tea.KeyEscape || input == 'q' || input == 'Q' {
		m.mode = adminModeMonitor
		return
	}
	for _, action := range m.controller.actions {
		if input == rune(action.key) {
			if action.kind == adminActionProcessManager {
				m.status = ""
				m.processTask = processTaskList
				m.mode = adminModeProcessList
				return
			}
			m.selected = action
			m.mode = adminModeConfirm
			return
		}
	}
}

func (m *adminModel) updateConfirmation(input rune) {
	if input == 'y' || input == 'Y' {
		selected := m.selected
		m.pending = &selected
		m.mode = adminModeMenu
		return
	}
	m.mode = adminModeMenu
}

func (m *adminModel) updateProcessList(input rune) {
	switch input {
	case 0x03:
		m.exit = true
	case tea.KeyEscape, 'q', 'Q':
		m.mode = adminModeMenu
	case 'r', 'R':
		m.status = ""
		m.processTask = processTaskList
	case 'd', 'D':
		m.status = ""
		m.processPID = nil
		m.mode = adminModeProcessDetailPID
	case 't', 'T':
		m.status = ""
		m.processPID = nil
		m.mode = adminModeProcessTerminatePID
	}
}

func (m *adminModel) updateProcessPID(input rune, terminate bool) {
	switch input {
	case 0x03:
		m.exit = true
	case tea.KeyEscape:
		m.mode = adminModeProcessList
	case tea.KeyEnter, '\n':
		pid, err := validateProcessPID(string(m.processPID))
		if err != nil {
			m.status = err.Error()
			return
		}
		m.processPIDValue = pid
		if terminate {
			m.mode = adminModeProcessTerminateConfirm
		} else {
			m.mode = adminModeProcessDetail
		}
		m.processTask = processTaskDetail
	case tea.KeyBackspace, 0x08:
		if len(m.processPID) > 0 {
			m.processPID = m.processPID[:len(m.processPID)-1]
		}
	default:
		if input >= '0' && input <= '9' && len(m.processPID) < 10 {
			m.processPID = append(m.processPID, byte(input))
		}
	}
}

func (m *adminModel) updateProcessTermination(input rune) {
	if input == 'y' || input == 'Y' {
		m.processTask = processTaskTerminate
		m.mode = adminModeProcessList
		return
	}
	m.mode = adminModeProcessList
}

func (m *adminModel) View() tea.View {
	switch m.mode {
	case adminModeDisabled:
		body := adminDangerStyle.Render("Management mode is disabled.") + "\n\n" + adminMutedStyle.Render("Set ADMIN_PASSWORD_HASH (recommended) or ADMIN_PASSWORD.") + "\n\n" + renderKeyHints("any", "return to monitor")
		return newAdminView(m.adminScreen("MANAGEMENT MODE", "ACCESS NOT CONFIGURED", body, false, false))
	case adminModePassword:
		body := adminLabelStyle.Render("Administrator password") + "\n" + adminMutedStyle.Render("The live dashboard is paused until you exit.") + "\n\n"
		if m.status != "" {
			body += adminDangerStyle.Render(m.status) + "\n\n"
		}
		body += adminValueStyle.Render("Password: ") + adminTitleStyle.Render(strings.Repeat("•", len(m.password))) + "\n\n" + renderKeyHints("esc", "cancel", "ctrl-c", "exit")
		return newAdminView(m.adminScreen("MANAGEMENT MODE", "SECURE ACCESS", body, false, false))
	case adminModeMenu:
		var body strings.Builder
		body.WriteString(adminMutedStyle.Render("Fixed administrative actions configured on this host."))
		body.WriteString("\n\n")
		if m.status != "" {
			body.WriteString(adminDangerStyle.Render(m.status))
			body.WriteString("\n\n")
		}
		for _, action := range m.controller.actions {
			fmt.Fprintf(&body, "  %s  %s\n", adminKeyStyle.Render("["+string(action.key)+"]"), adminValueStyle.Render(action.label))
		}
		body.WriteString("\n")
		body.WriteString(renderKeyHints("q", "return to monitor"))
		return newAdminView(m.adminScreen("MANAGEMENT MODE", "ADMIN CONSOLE", body.String(), false, false))
	case adminModeConfirm:
		body := adminDangerStyle.Render("Confirm administrative action") + "\n\n" + adminValueStyle.Render(m.selected.label) + "\n\n" + renderKeyHints("y", "confirm", "any key", "cancel")
		return newAdminView(m.adminScreen("MANAGEMENT MODE", "CONFIRM ACTION", body, false, true))
	case adminModeProcessList:
		var body strings.Builder
		if m.status != "" {
			body.WriteString(adminDangerStyle.Render(m.status))
			body.WriteString("\n\n")
		}
		body.WriteString(m.renderProcessWorkspace())
		return newAdminView(m.adminScreen("PROCESS MANAGEMENT", "LIVE PROCESS VIEW", body.String(), true, false))
	case adminModeProcessDetailPID:
		body := adminLabelStyle.Render("Inspect a process") + "\n" + adminMutedStyle.Render("Enter a numeric PID to view its current details.") + "\n\n" + adminValueStyle.Render("PID: ") + adminTitleStyle.Render(string(m.processPID))
		if m.status != "" {
			body += "\n\n" + adminDangerStyle.Render(m.status)
		}
		body += "\n\n" + renderKeyHints("enter", "inspect", "esc", "cancel")
		return newAdminView(m.adminScreen("PROCESS MANAGEMENT", "PROCESS DETAILS", body, false, false))
	case adminModeProcessDetail:
		body := renderProcessDetails(m.processOutput)
		if m.status != "" {
			body = adminDangerStyle.Render(m.status) + "\n\n" + body
		}
		body += "\n\n" + renderKeyHints("any key", "return to process list")
		return newAdminView(m.adminScreen("PROCESS MANAGEMENT", "PROCESS DETAILS", body, true, false))
	case adminModeProcessTerminatePID:
		body := adminDangerStyle.Render("Terminate with SIGTERM") + "\n" + adminMutedStyle.Render("The selected process receives a graceful termination request.") + "\n\n" + adminValueStyle.Render("PID: ") + adminTitleStyle.Render(string(m.processPID))
		if m.status != "" {
			body += "\n\n" + adminDangerStyle.Render(m.status)
		}
		body += "\n\n" + renderKeyHints("enter", "review process", "esc", "cancel")
		return newAdminView(m.adminScreen("PROCESS MANAGEMENT", "TERMINATE PROCESS", body, false, true))
	case adminModeProcessTerminateConfirm:
		body := renderProcessDetails(m.processOutput) + "\n\n" + adminDestructiveStyle.Render(fmt.Sprintf("Send SIGTERM to PID %d?", m.processPIDValue)) + "\n\n" + renderKeyHints("y", "terminate", "any key", "cancel")
		return newAdminView(m.adminScreen("PROCESS MANAGEMENT", "CONFIRM TERMINATION", body, true, true))
	}
	return tea.NewView("")
}

func newAdminView(content string) tea.View {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return tea.NewView(strings.ReplaceAll(content, "\n", "\r\n"))
}

func (m *adminModel) adminScreen(title, context, body string, wide, destructive bool) string {
	width := m.panelWidth(wide)
	header := adminTitleStyle.Render("GPU SSH MONITOR") + "  " + adminMutedStyle.Render("/ "+context) + "\n" + adminLabelStyle.Render(title) + "\n" + adminMutedStyle.Render(strings.Repeat("─", width))
	style := adminPanelStyle
	if wide {
		style = adminWidePanelStyle.Width(width)
	}
	if destructive {
		style = adminDestructivePanelStyle
		if wide {
			style = style.Width(width)
		}
	}
	if !wide {
		width = min(width, 82)
		style = style.Width(width)
	}
	content := header + "\n\n" + style.Render(body)
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, content)
}

func (m *adminModel) panelWidth(wide bool) int {
	width := m.width - 4
	if !wide {
		width = min(width, 78)
	}
	return max(width, 64)
}

func renderKeyHints(values ...string) string {
	var hints []string
	for index := 0; index+1 < len(values); index += 2 {
		hints = append(hints, adminKeyStyle.Render("["+values[index]+"]")+" "+adminMutedStyle.Render(values[index+1]))
	}
	return strings.Join(hints, "   ")
}

func renderProcessTable(value string) string {
	return renderProcessTableLimit(value, 12)
}

func renderProcessTableLimit(value string, limit int) string {
	lines := strings.Split(strings.TrimSuffix(value, "\r\n"), "\r\n")
	if len(lines) == 0 || lines[0] == "" {
		return adminMutedStyle.Render("No processes returned.")
	}
	if len(lines) > limit+1 {
		lines = lines[:limit+1]
	}
	return adminTableHeaderStyle.Render(lines[0]) + "\n" + adminValueStyle.Render(strings.Join(lines[1:], "\n"))
}

func (m *adminModel) renderProcessWorkspace() string {
	width := m.panelWidth(true)
	leftWidth := max(54, width*3/5-5)
	rightWidth := max(34, width-leftWidth-9)
	leftBody := adminLabelStyle.Render("PROCESSES  /  TOP CPU") + "\n\n" + renderProcessTable(m.processOutput)
	rightBody := adminLabelStyle.Render("PROCESS INSPECTION") + "\n\n" +
		adminMutedStyle.Render("Inspect any live PID on demand.") + "\n\n" +
		renderKeyHints("d", "enter PID and view details") + "\n\n" +
		adminLabelStyle.Render("SAFE TERMINATION") + "\n\n" +
		adminDangerStyle.Render("SIGTERM only") + "\n" +
		adminMutedStyle.Render("PID 1 and this monitor are protected.\nPID reuse is checked before sending a signal.") + "\n\n" +
		renderKeyHints("t", "review and terminate a PID")
	left := adminPanelStyle.Width(leftWidth).Render(leftBody)
	right := adminInfoPanelStyle.Width(rightWidth).Render(rightBody)
	footer := "\n\n" + renderKeyHints("r", "refresh", "d", "PID details", "t", "terminate PID", "q", "back")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right) + footer
}

func renderProcessDetails(value string) string {
	if strings.TrimSpace(value) == "" {
		return adminMutedStyle.Render("Loading process details…")
	}
	return adminValueStyle.Render(value)
}

func keyMessage(input byte) tea.KeyPressMsg {
	key := tea.Key{Code: rune(input)}
	switch input {
	case '\r', '\n':
		key.Code = tea.KeyEnter
	case 0x08, 0x7f:
		key.Code = tea.KeyBackspace
	case 0x1b:
		key.Code = tea.KeyEscape
	default:
		if input >= 0x20 && input <= 0x7e {
			key.Text = string(rune(input))
		}
	}
	return tea.KeyPressMsg(key)
}

func listProcesses() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,ppid=,user=,stat=,etimes=,%cpu=,%mem=,comm=", "--sort=-%cpu").Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("process listing timed out")
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(sanitizeTerminalText(string(output))), "\n")
	if len(lines) > 25 {
		lines = lines[:25]
	}
	return "  PID  PPID USER       STAT ELAPSED %CPU %MEM COMMAND\r\n" + strings.Join(lines, "\r\n") + "\r\n", nil
}

func processDetails(pid int) (string, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,user=,group=,lstart=,etime=,stat=,%cpu=,%mem=,args=").Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", 0, errors.New("process lookup timed out")
	}
	if err != nil {
		return "", 0, err
	}
	details := strings.TrimSpace(sanitizeTerminalText(string(output)))
	if details == "" {
		return "", 0, fmt.Errorf("PID %d no longer exists", pid)
	}
	startTime, err := processStartTime(pid)
	if err != nil {
		return "", 0, err
	}
	return details, startTime, nil
}

func validateProcessPID(value string) (int, error) {
	pid, err := strconv.Atoi(value)
	if err != nil || pid < 2 {
		return 0, errors.New("enter a PID greater than 1")
	}
	if pid == os.Getpid() {
		return 0, errors.New("the monitor service cannot terminate itself")
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("PID %d does not exist", pid)
		}
		return 0, err
	}
	return pid, nil
}

func terminateProcess(pid int, expectedStartTime uint64) error {
	if _, err := validateProcessPID(strconv.Itoa(pid)); err != nil {
		return err
	}
	currentStartTime, err := processStartTime(pid)
	if err != nil {
		return err
	}
	if expectedStartTime == 0 || currentStartTime != expectedStartTime {
		return errors.New("PID was reused; refresh its details before terminating it")
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func processStartTime(pid int) (uint64, error) {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	closingParenthesis := strings.LastIndex(string(contents), ")")
	if closingParenthesis == -1 {
		return 0, errors.New("could not read process start time")
	}
	fields := strings.Fields(string(contents[closingParenthesis+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("could not read process start time")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, err
	}
	return startTime, nil
}

func sanitizeTerminalText(value string) string {
	var result strings.Builder
	for _, char := range value {
		if char == '\n' || char == '\r' || char == '\t' || char >= 0x20 && char != 0x7f {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func envString(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
