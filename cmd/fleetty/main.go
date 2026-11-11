package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"image/color"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
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
	handled, err := runOperations(os.Args[1:], os.Stdout, os.Stderr)
	if handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleetty:", err)
			os.Exit(1)
		}
		return
	}
	runServer()
}

func runServer() {
	host := envString("SSH_HOST", "0.0.0.0")
	port := envString("SSH_PORT", "23234")
	hostKeyPath := envString("SSH_HOST_KEY_PATH", ".ssh/fleetty_ed25519")
	machine, err := loadMachineConfig(os.Getenv("MACHINE_CONFIG_FILE"))
	if err != nil {
		log.Fatal("Could not load machine configuration", "error", err)
	}
	admin := newAdminController()
	cache := newSnapshotCache(newMetricsCollector(machine))
	history := newHistoryStore(defaultHistoryPath())
	cache.record = history.Record
	rpc := newNodeRPCService(admin, cache, history)
	hub, err := loadHubConfig(os.Getenv("HUB_NODES_FILE"))
	if err != nil {
		log.Fatal("Could not load hub configuration", "error", err)
	}
	var hubRuntime *hubService
	stopHub := func() {}
	if hub != nil {
		hubRuntime = newHubService(*hub)
		hubContext, cancelHub := context.WithCancel(context.Background())
		stopHub = cancelHub
		hubRuntime.start(hubContext)
	}
	access, err := loadSSHAccessConfig()
	if err != nil {
		log.Fatal("Could not configure SSH client authentication", "error", err)
	}

	serverOptions := []ssh.Option{
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(hostKeyPath),
	}
	serverOptions = append(serverOptions, access.options()...)
	serverOptions = append(serverOptions,
		wish.WithMiddleware(
			bubblewish.Middleware(func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
				pty, _, ok := sess.Pty()
				if !ok {
					return nil, nil
				}
				if hubRuntime != nil {
					return newHubModel(hubRuntime, sess, pty.Window.Width, pty.Window.Height), nil
				}
				return newMonitorModel(admin, cache, history, machine, sess, pty.Window.Width, pty.Window.Height), nil
			}),
			terminalThemeCleanup,
			activeterm.Middleware(),
			nodeRPCMiddleware(rpc),
			accesscontrol.Middleware(nodeRPCCommand),
			logging.Middleware(),
		),
	)
	server, err := wish.NewServer(serverOptions...)
	if err != nil {
		log.Fatal("Could not create SSH server", "error", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("Starting Fleetty", "host", host, "port", port)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("Could not start SSH server", "error", err)
			done <- syscall.SIGTERM
		}
	}()

	<-done
	log.Info("Stopping Fleetty")
	stopHub()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("Could not stop SSH server", "error", err)
	}
}

// Remote monitor sessions are deliberately read-only: they never start a shell
// or proxy terminal programs. Local `fleetty top` sessions may additionally
// expose storage actions, constrained by the current Unix user's permissions
// and the local storage scan root.
type monitorModel struct {
	backend           monitorBackend
	admin             *adminController
	user              string
	remote            string
	nodeName          string
	profile           string
	width             int
	height            int
	snapshot          monitorSnapshot
	loadErr           error
	screen            screen
	password          string
	filter            string
	filtering         bool
	processSort       processSortKey
	processSortAsc    bool
	cursor            int
	processOffset     int
	monitorCursor     int
	monitorOffset     int
	queueOffset       int
	selectedAction    *adminAction
	selectedProcess   *processInfo
	processDetail     *processDetail
	processReadOnly   bool
	detailErr         error
	status            string
	busy              bool
	adminCredential   string
	authFailures      int
	authLockedUntil   time.Time
	collecting        bool
	cpuHistory        []float64
	networkRXHistory  []float64
	networkTXHistory  []float64
	hourCPUHistory    []float64
	hourHistoryAt     time.Time
	colorMode         colorMode
	theme             terminalTheme
	slurmQueue        *nodeSlurmQueue
	monitorPage       monitorPage
	monitorFocus      monitorPanelFocus
	monitorRows       int
	dashboardScroll   int
	dashboardContent  int
	dashboardViewport int
	widgetPlacements  []widgetPlacement
	cpuCoreSelected   string
	cpuCoreFocused    bool
	cpuCoreDetailed   bool
	panelLayout       panelLayoutConfig
	layoutPath        string
	layoutCursor      int
	layoutButtonY     int
	layoutFirstRowY   int
	pageTabY          int
	pageTabs          []monitorPageTab
	storage           *storageMapState
	storageAction     *storageActionRequest
	storageConfirm    string
	storageActionStop context.CancelFunc
}

type monitorPage int

const (
	monitorPageOverview monitorPage = iota
	monitorPageCompute
	monitorPageNetwork
	monitorPageStorage
	monitorPageCustom
)

type monitorPageTab struct {
	Page     monitorPage
	X, Width int
}

type monitorPanelFocus int

const (
	monitorFocusQueue monitorPanelFocus = iota
	monitorFocusProcesses
)

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
	screenStorageConfirm
	screenLayout
)

func newMonitorModel(
	admin *adminController,
	cache *snapshotCache,
	history *historyStore,
	machine machineConfig,
	sess ssh.Session,
	width, height int,
) *monitorModel {
	remote := "local"
	if addr := sess.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	return &monitorModel{
		backend:   newLocalMonitorBackend(admin, cache, history, sess.User(), remote),
		admin:     admin,
		user:      sess.User(),
		remote:    remote,
		nodeName:  machine.Name,
		profile:   machine.Profile,
		width:     width,
		height:    height,
		status:    "Live data refreshes every second.",
		colorMode: parseColorMode(os.Getenv("DEFAULT_THEME")),
		theme:     newTerminalTheme(os.Getenv("DEFAULT_THEME")),
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
	return tea.Batch(m.theme.init(), m.startCollect(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}

type historyMsg struct {
	history []historySample
	err     error
}
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
type storageScanResultMsg struct {
	generation uint64
	result     storageScanResult
	err        error
}
type storageScanProgressMsg struct {
	generation uint64
	result     storageScanResult
	updates    <-chan storageScanUpdate
}
type storageActionResultMsg struct {
	request     storageActionRequest
	archivePath string
	err         error
}
type storageDuplicateResultMsg struct {
	generation uint64
	result     storageDuplicateResult
	err        error
}
type storageDuplicateProgressMsg struct {
	generation uint64
	result     storageDuplicateResult
	updates    <-chan storageDuplicateUpdate
}
type storageDedupeScriptResultMsg struct {
	path   string
	groups int
	err    error
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

// hourHistoryCmd refreshes the persisted one-hour trend at most once per
// minute. The shared node history is cheap locally; remote sessions perform
// one small RPC per minute instead of per second.
func (m *monitorModel) hourHistoryCmd() tea.Cmd {
	if m.backend == nil || time.Since(m.hourHistoryAt) < time.Minute {
		return nil
	}
	return func() tea.Msg {
		history, err := m.backend.History(60)
		return historyMsg{history: history, err: err}
	}
}

func hourCPUValues(samples []historySample) []float64 {
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.CPUPercent)
	}
	return values
}

func (m *monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.theme.update(msg, &m.colorMode) {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.collecting = false
		monitorPID, adminPID := m.selectedProcessIDs()
		m.snapshot = msg.snapshot
		if msg.snapshot.Profile != "" {
			m.profile = msg.snapshot.Profile
		}
		m.loadErr = msg.err
		m.cpuHistory = appendHistory(m.cpuHistory, msg.snapshot.CPUPercent, 60)
		m.networkRXHistory = appendHistory(m.networkRXHistory, float64(msg.snapshot.NetworkRX), 60)
		m.networkTXHistory = appendHistory(m.networkTXHistory, float64(msg.snapshot.NetworkTX), 60)
		if len(msg.snapshot.ManagementActions) > 0 && m.admin != nil {
			m.admin.actions = actionsFromInfo(msg.snapshot.ManagementActions)
		}
		m.restoreProcessSelection(monitorPID, adminPID)
	case historyMsg:
		if msg.err == nil && len(msg.history) > 0 {
			m.hourCPUHistory = hourCPUValues(msg.history)
		}
		m.hourHistoryAt = time.Now()
	case authenticationResultMsg:
		m.busy = false
		if msg.err != nil {
			m.password, m.status = "", "Authentication failed: "+msg.err.Error()
		} else if msg.ok {
			m.screen, m.password, m.adminCredential = screenAdmin, "", msg.password
			m.authFailures, m.authLockedUntil = 0, time.Time{}
			m.status = "Management mode enabled. Filter or select a process."
		} else {
			m.password = ""
			m.authFailures++
			if m.authFailures >= maxManagementAuthFailures {
				m.authLockedUntil = time.Now().Add(managementAuthLockout)
				m.status = "Too many incorrect passwords. Management authentication is locked for 30 seconds."
			} else {
				remaining := maxManagementAuthFailures - m.authFailures
				m.status = fmt.Sprintf("Incorrect password. %d attempts remain.", remaining)
			}
		}
	case tickMsg:
		return m, tea.Batch(m.startCollect(), tick(), m.hourHistoryCmd())
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
	case storageScanResultMsg:
		m.applyStorageScanResult(msg)
	case storageScanProgressMsg:
		if m.applyStorageScanProgress(msg) {
			return m, waitForStorageScanUpdate(msg.generation, msg.updates)
		}
	case storageActionResultMsg:
		return m, m.applyStorageActionResult(msg)
	case storageDuplicateResultMsg:
		m.applyStorageDuplicateResult(msg)
	case storageDuplicateProgressMsg:
		if m.applyStorageDuplicateProgress(msg) {
			return m, waitForStorageDuplicateUpdate(msg.generation, msg.updates)
		}
	case storageDedupeScriptResultMsg:
		if msg.err != nil {
			m.status = "Could not generate dedupe script: " + msg.err.Error()
		} else {
			if state := m.storageDuplicateState(); state != nil {
				state.ScriptPath = msg.path
			}
			m.status = fmt.Sprintf(
				"Generated a %d-group dedupe script at %s · review it before running.",
				msg.groups, msg.path,
			)
		}
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m, m.handleClick(msg.Mouse().X, msg.Mouse().Y)
		}
	case tea.MouseWheelMsg:
		if m.screen == screenMonitor {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.adjustDashboardScroll(-3)
			case tea.MouseWheelDown:
				m.adjustDashboardScroll(3)
			}
		}
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.PasteMsg:
		if m.screen == screenPassword {
			m.appendPassword(msg.Content)
		} else if m.screen == screenStorageConfirm {
			m.appendStorageConfirmation(msg.Content)
		} else if (m.screen == screenAdmin || m.screen == screenMonitor) && m.filtering {
			m.appendFilter(msg.Content)
		}
	}
	return m, nil
}

func (m *monitorModel) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		m.cancelStorageScan()
		m.cancelStorageDuplicateScan()
		m.cancelStorageActionProcess()
		return tea.Quit
	}
	if m.screen == screenMonitor && m.filtering {
		switch key {
		case "esc":
			m.filtering = false
			m.status = "Filter editing cancelled."
		case "enter":
			m.filtering = false
			m.monitorCursor, m.monitorOffset = 0, 0
			m.status = fmt.Sprintf("Filter applied: %q", m.filter)
		case "backspace", "delete":
			m.filter = trimLastRune(m.filter)
			m.monitorCursor, m.monitorOffset = 0, 0
		default:
			text := msg.Key().Text
			if text == "" && len([]rune(key)) == 1 {
				text = key
			}
			m.appendFilter(text)
			m.monitorCursor, m.monitorOffset = 0, 0
		}
		return nil
	}
	themeKey := key == "T" || key == "t" &&
		(m.screen == screenMonitor || m.screen == screenAdmin || m.screen == screenLayout)
	if themeKey && m.screen != screenPassword && !(m.screen == screenAdmin && m.filtering) {
		m.toggleColorMode()
		return nil
	}

	switch m.screen {
	case screenMonitor:
		if m.handleCPUChartKey(key) {
			return nil
		}
		switch key {
		case "o", "O":
			if m.monitorPage == monitorPageOverview || m.monitorPage == monitorPageCompute || m.monitorPage == monitorPageCustom {
				if key == "O" {
					m.setProcessSort(m.processSort)
				} else {
					m.setProcessSort((m.processSort + 1) % processSortCount)
				}
			}
		case "1":
			return m.switchMonitorPage(monitorPageOverview)
		case "2":
			return m.switchMonitorPage(monitorPageCompute)
		case "3":
			return m.switchMonitorPage(monitorPageNetwork)
		case "4":
			return m.switchMonitorPage(monitorPageStorage)
		case "m":
			if !m.admin.enabled() {
				m.status = "Management mode is disabled: configure ADMIN_PASSWORD_HASH."
				return nil
			}
			m.screen, m.password, m.status = screenPassword, "", "Enter the management password."
		case "r":
			if m.monitorPage == monitorPageStorage {
				if m.storage != nil && m.storage.DuplicateMode {
					return tea.Sequence(
						forceFullScreenRedraw(),
						m.beginStorageDuplicateScan(m.storage.Duplicates.Path),
					)
				}
				return tea.Sequence(
					forceFullScreenRedraw(),
					m.refreshStorageScan(m.storage.Path),
				)
			}
			m.status = "Refreshing now…"
			return m.startCollect()
		case "d":
			if m.monitorPage == monitorPageStorage &&
				m.storage != nil && !m.storage.DuplicateMode {
				m.prepareStorageAction(storageActionDelete)
			}
		case "z":
			if m.monitorPage == monitorPageStorage &&
				m.storage != nil && !m.storage.DuplicateMode {
				m.prepareStorageAction(storageActionArchiveDelete)
			}
		case "u":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					return m.leaveStorageDuplicates()
				}
				return m.beginStorageDuplicateScan(m.storage.Path)
			}
		case "g":
			if m.monitorPage == monitorPageStorage &&
				m.storage != nil && m.storage.DuplicateMode {
				return m.generateStorageDedupeScript(false)
			}
		case "G":
			if m.monitorPage == monitorPageStorage &&
				m.storage != nil && m.storage.DuplicateMode {
				return m.generateStorageDedupeScript(true)
			}
		case "c":
			if m.monitorPage == monitorPageStorage &&
				m.storage != nil && m.storage.DuplicateMode {
				m.cycleStorageDuplicateKeeper()
				return nil
			}
			m.filter, m.monitorCursor, m.monitorOffset = "", 0, 0
			m.status = "Process filter cleared."
		case "l":
			if m.profile == machineProfileNAS || m.snapshot.Profile == machineProfileNAS {
				m.status = "Custom NAS layouts are not available yet."
				return nil
			}
			m.ensurePanelLayout()
			m.screen = screenLayout
			m.status = "Layout changes apply immediately to this session."
		case "tab":
			if m.monitorPage == monitorPageCustom {
				queueVisible := m.slurmQueue != nil &&
					!m.dashboardPanelCollapsed(dashboardPanelNodeQueue)
				processesVisible := !m.dashboardPanelCollapsed(dashboardPanelProcesses)
				switch {
				case !queueVisible:
					m.monitorFocus = monitorFocusProcesses
				case !processesVisible:
					m.monitorFocus = monitorFocusQueue
				case m.monitorFocus == monitorFocusQueue:
					m.monitorFocus = monitorFocusProcesses
				default:
					m.monitorFocus = monitorFocusQueue
				}
				m.status = "Focused " + m.monitorFocus.label() + "."
			} else {
				return m.cycleMonitorPage(1)
			}
		case "shift+tab", "backtab":
			return m.cycleMonitorPage(-1)
		case "left", "h":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					return m.leaveStorageDuplicates()
				}
				return m.storageNavigateParent()
			}
			return m.cycleMonitorPage(-1)
		case "right":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					return nil
				}
				return m.storageOpenSelected()
			}
			return m.cycleMonitorPage(1)
		case "+", "=":
			if m.monitorPage == monitorPageCustom {
				m.adjustFocusedWidgetSize(1)
			}
		case "-", "_":
			if m.monitorPage == monitorPageCustom {
				m.adjustFocusedWidgetSize(-1)
			}
		case "up", "k":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					m.moveStorageDuplicateSelection(-1)
				} else {
					m.moveStorageSelection(-1)
				}
			} else if m.monitorPage == monitorPageCustom ||
				m.monitorPage == monitorPageOverview ||
				m.monitorPage == monitorPageCompute {
				m.moveMonitorSelection(-1)
			} else {
				m.adjustDashboardScroll(-1)
			}
		case "down", "j":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					m.moveStorageDuplicateSelection(1)
				} else {
					m.moveStorageSelection(1)
				}
			} else if m.monitorPage == monitorPageCustom ||
				m.monitorPage == monitorPageOverview ||
				m.monitorPage == monitorPageCompute {
				m.moveMonitorSelection(1)
			} else {
				m.adjustDashboardScroll(1)
			}
		case "pgup":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					m.moveStorageDuplicateSelection(-max(1, m.height/4))
				} else {
					m.moveStoragePage(-1)
				}
			} else {
				m.adjustDashboardScroll(-max(3, m.dashboardViewport/2))
			}
		case "pgdown":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					m.moveStorageDuplicateSelection(max(1, m.height/4))
				} else {
					m.moveStoragePage(1)
				}
			} else {
				m.adjustDashboardScroll(max(3, m.dashboardViewport/2))
			}
		case "home":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					if state := m.storageDuplicateState(); state != nil {
						state.Cursor = 0
					}
					return nil
				}
				return m.storageNavigateRoot()
			}
			m.dashboardScroll = 0
			m.status = "Dashboard scrolled to the first widget."
		case "end":
			m.dashboardScroll = max(0, m.dashboardContent-m.dashboardViewport)
			m.status = "Dashboard scrolled to the last widget."
		case "enter":
			if m.monitorPage == monitorPageStorage {
				if m.storage.DuplicateMode {
					return nil
				}
				return m.storageOpenSelected()
			}
			if m.monitorPage == monitorPageOverview ||
				m.monitorPage == monitorPageCompute ||
				m.monitorPage == monitorPageCustom &&
					m.effectiveMonitorFocus() == monitorFocusProcesses &&
					!m.dashboardPanelCollapsed(dashboardPanelProcesses) {
				return m.openReadOnlyProcess(m.monitorCursor)
			}
		case "/":
			if m.monitorPage == monitorPageCustom &&
				m.dashboardPanelCollapsed(dashboardPanelProcesses) {
				m.setDashboardPanelCollapsed(dashboardPanelProcesses, false)
			}
			if m.monitorPage != monitorPageOverview &&
				m.monitorPage != monitorPageCompute &&
				m.monitorPage != monitorPageCustom {
				m.switchMonitorPage(monitorPageCompute)
			}
			m.monitorFocus = monitorFocusProcesses
			m.filtering = true
			m.status = "Type a process name, user, or PID."
		case "esc", "q":
			if m.monitorPage == monitorPageStorage {
				if m.storage != nil && m.storage.DuplicateMode {
					return m.leaveStorageDuplicates()
				}
				if m.storage != nil && m.storage.Path != m.storage.Root {
					return m.storageNavigateParent()
				}
			}
			m.cancelStorageScan()
			m.cancelStorageDuplicateScan()
			return tea.Quit
		case "backspace", "delete":
			if m.monitorPage == monitorPageStorage && m.storage != nil {
				if m.storage.DuplicateMode {
					return m.leaveStorageDuplicates()
				}
				return m.storageNavigateParent()
			}
		}
	case screenPassword:
		switch key {
		case "esc":
			m.screen, m.password, m.status = screenMonitor, "", "Management mode cancelled."
		case "enter":
			if time.Now().Before(m.authLockedUntil) {
				remaining := time.Until(m.authLockedUntil).Round(time.Second)
				m.status = fmt.Sprintf("Management authentication is locked for %s.", remaining)
			} else if !m.busy && m.password != "" {
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
		case "o":
			m.setProcessSort((m.processSort + 1) % processSortCount)
		case "O":
			m.setProcessSort(m.processSort)
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
		default:
			if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				m.selectAction(int(key[0] - '1'))
			}
		}
	case screenConfirm:
		switch key {
		case "esc", "q", "n":
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
			if m.processReadOnly {
				m.screen, m.status = screenMonitor, "Returned to read-only monitor."
			} else {
				m.screen, m.status = screenAdmin, "Returned to process manager."
			}
		case "r":
			if m.selectedProcess != nil {
				return m.loadProcessDetail(m.selectedProcess.PID)
			}
		case "t":
			if !m.processReadOnly && m.selectedProcessCanTerminate() {
				m.screen = screenProcessTerminateConfirm
				m.status = "Confirm before sending SIGTERM."
			}
		}
	case screenProcessTerminateConfirm:
		switch key {
		case "esc", "q", "n":
			m.screen, m.status = screenProcessDetail, "Process termination cancelled."
		case "y", "enter":
			if m.selectedProcess != nil && !m.busy {
				m.busy = true
				m.status = fmt.Sprintf("Sending SIGTERM to PID %d…", m.selectedProcess.PID)
				return m.terminateProcess(m.selectedProcess.PID, m.processDetail.StartTimeTicks)
			}
		}
	case screenStorageConfirm:
		switch key {
		case "esc":
			if m.busy {
				if m.storageAction != nil &&
					m.storageAction.Kind == storageActionArchiveDelete {
					m.cancelStorageActionProcess()
					m.status = "Cancellation requested; waiting for a safe stop…"
				} else {
					m.status = "Deletion is already in progress and cannot be interrupted."
				}
			} else {
				m.cancelStorageAction("Storage action cancelled.")
			}
		case "backspace", "delete":
			m.storageConfirm = trimLastRune(m.storageConfirm)
		case "enter":
			if m.storageAction == nil {
				m.cancelStorageAction("Storage action is no longer available.")
			} else if m.storageConfirm != m.storageAction.confirmation() {
				m.status = "Confirmation phrase does not match."
			} else if !m.busy {
				m.busy = true
				m.status = "Running " + m.storageAction.label() + "…"
				return m.runStorageAction(*m.storageAction)
			}
		default:
			text := msg.Key().Text
			if text == "" && len([]rune(key)) == 1 {
				text = key
			}
			m.appendStorageConfirmation(text)
		}
	case screenLayout:
		switch key {
		case "esc", "q", "l":
			m.screen = screenMonitor
			m.monitorPage = monitorPageCustom
			m.dashboardScroll = 0
			m.status = "Custom layout enabled for this session. Press 1–3 for designed pages."
		case "up", "k":
			m.ensurePanelLayout()
			m.layoutCursor = max(0, m.layoutCursor-1)
		case "down", "j":
			m.ensurePanelLayout()
			m.layoutCursor = min(len(m.panelLayout.Panels)-1, m.layoutCursor+1)
		case "[", "K", "shift+up", "ctrl+up":
			m.moveSelectedLayoutPanel(-1)
		case "]", "J", "shift+down", "ctrl+down":
			m.moveSelectedLayoutPanel(1)
		case "-", "_", "left":
			m.resizeSelectedLayoutPanel(-1)
		case "+", "=", "right":
			m.resizeSelectedLayoutPanel(1)
		case " ", "space", "enter":
			m.toggleSelectedLayoutPanel()
		case "s":
			if err := savePanelLayout(m.layoutPath, m.panelLayout); err != nil {
				m.status = "Could not save layout: " + err.Error()
			} else {
				m.status = "Layout saved as the local user's default."
			}
		case "r":
			m.resetPanelLayout()
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
	if m.screen == screenMonitor {
		m.monitorCursor, m.monitorOffset = 0, 0
	} else {
		m.cursor, m.processOffset = 0, 0
	}
}

func (m *monitorModel) handleClick(x, y int) tea.Cmd {
	if m.screen == screenMonitor {
		if m.monitorPage != monitorPageCustom && y == m.pageTabY {
			for _, tab := range m.pageTabs {
				if x >= tab.X && x < tab.X+tab.Width {
					return m.switchMonitorPage(tab.Page)
				}
			}
		}
		if m.monitorPage == monitorPageStorage {
			if m.storage != nil && m.storage.DuplicateMode {
				return m.handleStorageDuplicateClick(x, y)
			}
			return m.handleStorageClick(x, y)
		}
		if m.handleCPUChartClick(x, y) {
			return nil
		}
		if m.monitorPage == monitorPageCustom {
			if placement, screenY, ok := m.visibleWidgetPlacement(dashboardPanelNodeQueue); ok &&
				x >= placement.X && x < placement.X+placement.Width &&
				y >= screenY && y < screenY+placement.Height {
				m.monitorFocus = monitorFocusQueue
				m.status = "Focused node queue. Use +/- to resize and ↑/↓ to scroll."
				return nil
			}
		}
		if placement, screenY, ok := m.visibleWidgetPlacement(dashboardPanelProcesses); ok &&
			x >= placement.X && x < placement.X+placement.Width &&
			y >= screenY && y < screenY+placement.Height {
			m.cpuCoreFocused = false
			m.monitorFocus = monitorFocusProcesses
			processRowStart := placement.ProcessRowStart
			if processRowStart <= 0 {
				processRowStart = 3
			}
			processRowY := screenY + processRowStart
			if y == processRowY-1 {
				m.sortProcessHeaderAt(x-placement.X-2, placement.Width)
				return nil
			}
			if y >= processRowY && y < processRowY+placement.ProcessRows {
				return m.openReadOnlyProcess(m.monitorOffset + y - processRowY)
			}
			m.status = "Focused processes. Use ↑/↓ to select or Enter for details."
			return nil
		}
	}
	if m.screen == screenAdmin {
		if y == 2 {
			left := 0
			for index := range m.admin.actions {
				width := compactButtonWidth(strconv.Itoa(index+1), m.adminActionLabel(index))
				if x >= left && x < left+width {
					m.selectAction(index)
					break
				}
				left += width + 1
			}
		} else if y == 3 {
			m.filtering = true
			m.status = "Type a process name, user, or PID."
		} else if y == adminProcessRowStart-1 && !m.filtering {
			m.sortProcessHeaderAt(x-2, usableWidth(m.width))
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
	if m.screen == screenProcessDetail && !m.processReadOnly &&
		y == 2 && x < compactButtonWidth("T", "Terminate process") {
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
	if m.screen == screenLayout {
		if y == m.layoutButtonY {
			m.handleLayoutButtonClick(x)
			return nil
		}
		if y >= m.layoutFirstRowY && y < m.layoutFirstRowY+len(m.panelLayout.Panels) {
			m.layoutCursor = y - m.layoutFirstRowY
			m.status = "Widget selected. Use +/- to resize or Space to hide."
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

func (focus monitorPanelFocus) label() string {
	if focus == monitorFocusProcesses {
		return "processes"
	}
	return "node queue"
}

func (m *monitorModel) effectiveMonitorFocus() monitorPanelFocus {
	if m.monitorPage != monitorPageCustom {
		return monitorFocusProcesses
	}
	queueAvailable := m.slurmQueue != nil && !m.dashboardPanelCollapsed(dashboardPanelNodeQueue)
	processesAvailable := !m.dashboardPanelCollapsed(dashboardPanelProcesses)
	if !queueAvailable {
		return monitorFocusProcesses
	}
	if !processesAvailable {
		return monitorFocusQueue
	}
	return m.monitorFocus
}

func (m *monitorModel) adjustFocusedWidgetSize(delta int) {
	id := dashboardPanelProcesses
	if m.effectiveMonitorFocus() == monitorFocusQueue {
		id = dashboardPanelNodeQueue
	}
	m.resizeDashboardWidget(id, delta)
}

func (m *monitorModel) moveMonitorSelection(delta int) {
	if m.effectiveMonitorFocus() == monitorFocusQueue {
		if m.slurmQueue == nil || len(m.slurmQueue.Jobs) == 0 {
			return
		}
		rows := widgetTableRows(m.dashboardPanelSize(dashboardPanelNodeQueue), m.height)
		m.queueOffset = min(max(0, m.queueOffset+delta), max(0, len(m.slurmQueue.Jobs)-rows))
		m.status = fmt.Sprintf("Node queue rows %d–%d of %d.",
			m.queueOffset+1, min(len(m.slurmQueue.Jobs), m.queueOffset+rows), len(m.slurmQueue.Jobs))
		return
	}
	if m.dashboardPanelCollapsed(dashboardPanelProcesses) {
		return
	}
	processes := m.filteredProcesses()
	if len(processes) == 0 {
		return
	}
	m.monitorCursor = min(max(0, m.monitorCursor+delta), len(processes)-1)
	m.clampMonitorProcessCursor(m.monitorRows)
	m.status = fmt.Sprintf("Selected PID %d. Press Enter for read-only details.", processes[m.monitorCursor].PID)
}

func (m *monitorModel) clampMonitorProcessCursor(rows int) {
	processes := m.filteredProcesses()
	if len(processes) == 0 {
		m.monitorCursor, m.monitorOffset = 0, 0
		return
	}
	m.monitorCursor = min(max(0, m.monitorCursor), len(processes)-1)
	if rows <= 0 {
		m.monitorOffset = 0
		return
	}
	if m.monitorCursor < m.monitorOffset {
		m.monitorOffset = m.monitorCursor
	}
	if m.monitorCursor >= m.monitorOffset+rows {
		m.monitorOffset = m.monitorCursor - rows + 1
	}
	m.monitorOffset = min(max(0, m.monitorOffset), max(0, len(processes)-rows))
}

func (m *monitorModel) adminVisibleProcessRows() int {
	return max(3, m.height-9)
}

func (m *monitorModel) visibleAdminProcessCount() int {
	return min(m.adminVisibleProcessRows(), max(0, len(m.filteredProcesses())-m.processOffset))
}

func (m *monitorModel) adminActionLabel(index int) string {
	if index < 0 || index >= len(m.admin.actions) {
		return ""
	}
	if usableWidth(m.width) < 54 {
		switch m.admin.actions[index].ID {
		case 0:
			return "Restart"
		case 1:
			return "Reboot"
		}
	}
	return m.admin.actions[index].label
}

func (m *monitorModel) openProcess(index int) tea.Cmd {
	return m.openProcessMode(index, false)
}

func (m *monitorModel) openReadOnlyProcess(index int) tea.Cmd {
	return m.openProcessMode(index, true)
}

func (m *monitorModel) openProcessMode(index int, readOnly bool) tea.Cmd {
	processes := m.filteredProcesses()
	if index < 0 || index >= len(processes) {
		return nil
	}
	process := processes[index]
	if readOnly {
		m.monitorCursor = index
	} else {
		m.cursor = index
	}
	m.selectedProcess = &process
	m.processDetail, m.detailErr = nil, nil
	m.processReadOnly = readOnly
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
		m.processDetail.CanTerminate &&
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

func forceFullScreenRedraw() tea.Cmd {
	return func() tea.Msg {
		return tea.ClearScreen()
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
	case screenStorageConfirm:
		body = m.storageConfirmView()
	case screenLayout:
		body = m.layoutView()
	default:
		body = m.monitorView()
	}
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "Fleetty"
	if m.profile == machineProfileNAS {
		v.WindowTitle = "Fleetty · NAS"
	}
	v.BackgroundColor, v.ForegroundColor = m.theme.colors(m.colorMode)
	return v
}

func (m *monitorModel) monitorView() string {
	if m.profile == machineProfileNAS || m.snapshot.Profile == machineProfileNAS {
		return m.nasView()
	}
	if m.monitorPage == monitorPageCustom {
		return m.widgetDashboardView()
	}
	return m.monitorPagesView()
}

func (m *monitorModel) systemMetricCards() []metricCard {
	memoryPercent := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
	diskPercent := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
	return []metricCard{
		{
			title: "CPU", value: cpuSummaryValue(m.snapshot), detail: m.snapshot.LoadAverage,
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
}

func dashboardHeader(width int, collectedAt time.Time, mode colorMode, nodeName string) string {
	return dashboardHeaderNamed("FLEETTY", width, collectedAt, mode, nodeName)
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
	return newGPUCardComponent(m.snapshot).render(gpuCardDetailed, layout.width, 0)
}

type gpuPanelColumns struct {
	contentWidth int
	compact      bool
	index        int
	name         int
	util         int
	memory       int
	power        int
	clock        int
	temperature  int
	barWidth     int
}

func newGPUPanelColumns(width int) gpuPanelColumns {
	contentWidth := max(20, width-4)
	compact := contentWidth < 72
	columns := gpuPanelColumns{
		contentWidth: contentWidth,
		compact:      compact,
		index:        6,
	}
	if compact {
		columns.barWidth = max(2, min(5, contentWidth/32))
		columns.util = columns.barWidth + 5
		columns.memory = columns.barWidth + 12
		columns.power = columns.barWidth + 12
		columns.clock = 8
		columns.temperature = 6
	} else {
		columns.barWidth = max(4, min(12, contentWidth/24))
		columns.util = columns.barWidth + 5
		columns.memory = columns.barWidth + 16
		columns.power = columns.barWidth + 16
		columns.clock = 11
		columns.temperature = 6
	}
	// There is one separator between each cell. Keep the model column fixed
	// too, so a short model cannot pull all following metrics to the left.
	separators := 6
	fixed := columns.index + columns.util + columns.memory + columns.power + columns.clock + columns.temperature + separators
	// Keep enough room for a recognizable model even on a narrow SSH
	// terminal; the trailing temperature cell may be truncated instead.
	columns.name = max(12, contentWidth-fixed)
	return columns
}

func gpuStyledCell(style lipgloss.Style, value string, width int, right bool) string {
	return style.Render(fixedCell(value, width, right))
}

func gpuPanelMetricsLine(gpu gpuInfo, width int) string {
	columns := newGPUPanelColumns(width)
	displayName := gpu.Name
	if columns.compact {
		displayName = shortGPUName(displayName)
	}
	name := gpuStyledCell(valueStyle, displayName, columns.name, false)
	utilStyle := gpuLoadStyle(gpu.Utilization)
	memoryUsage := percent(gpu.MemoryUsed, gpu.MemoryTotal)
	memoryStyle := gpuLoadStyle(memoryUsage)
	powerUsage := 0.0
	if gpu.PowerLimit > 0 {
		powerUsage = gpu.Power * 100 / gpu.PowerLimit
	}
	powerStyle := gpuLoadStyle(powerUsage)
	memory := compactByteCount(gpu.MemoryUsed) + "/" + compactByteCount(gpu.MemoryTotal)
	power := fmt.Sprintf("%.0fW", gpu.Power)
	if gpu.PowerLimit > 0 {
		power = fmt.Sprintf("%.0f/%.0fW", gpu.Power, gpu.PowerLimit)
	}
	barWidth := columns.barWidth
	clockText := fmt.Sprintf("CLK %dMHz", gpu.ClockMHz)
	if columns.compact {
		memory = shortGPUCount(gpu.MemoryUsed) + "/" + shortGPUCount(gpu.MemoryTotal)
		if gpu.PowerLimit > 0 {
			power = fmt.Sprintf("%.0f/%.0fW", gpu.Power, gpu.PowerLimit)
		}
		clockText = fmt.Sprintf("%dMHz", gpu.ClockMHz)
	}
	utilPart := gpuStyledCell(utilStyle, fmt.Sprintf("%s %3.0f%%", shortGPUBar(gpu.Utilization, barWidth), gpu.Utilization), columns.util, false)
	memoryLabel := "MEM"
	powerLabel := "PWR"
	if columns.compact {
		memoryLabel = "M"
		powerLabel = "P"
	}
	memoryPart := gpuStyledCell(memoryStyle, fmt.Sprintf("%s %s %s", shortGPUBar(memoryUsage, barWidth), memoryLabel, memory), columns.memory, false)
	powerPart := gpuStyledCell(powerStyle, fmt.Sprintf("%s %s %s", shortGPUBar(powerUsage, barWidth), powerLabel, power), columns.power, false)
	clockPart := gpuStyledCell(gpuClockStyle(gpu.ClockMHz), clockText, columns.clock, false)
	temperaturePart := gpuStyledCell(gpuTemperatureStyle(gpu.Temperature), fmt.Sprintf("%d°C", gpu.Temperature), columns.temperature, false)
	parts := []string{
		gpuStyledCell(valueStyle, fmt.Sprintf("GPU %-2d", gpu.Index), columns.index, false),
		name,
		utilPart,
		memoryPart,
		powerPart,
		clockPart,
		temperaturePart,
	}
	return ansi.Truncate(strings.Join(parts, " "), columns.contentWidth, "…")
}

func shortGPUBar(value float64, width int) string {
	filled := int(value / 100 * float64(width))
	filled = max(0, min(width, filled))
	return strings.Repeat("█", filled) + barTrackStyle.Render(strings.Repeat("░", width-filled))
}

func shortGPUCount(value uint64) string {
	return strings.ReplaceAll(compactByteCount(value), ".0", "")
}

func gpuPanelWorkloadLine(gpu gpuInfo, width int) string {
	columns := newGPUPanelColumns(width)
	indent := strings.Repeat(" ", columns.index+1)
	if len(gpu.Workloads) == 0 {
		if gpu.Platform == "apple" {
			return dimStyle.Render(fmt.Sprintf("%sNO ACTIVE JOB  ·  RENDER %.0f%%  ·  TILER %.0f%%", indent,
				gpu.RendererUtilization, gpu.TilerUtilization))
		}
		return dimStyle.Render(indent + "NO ACTIVE JOB")
	}
	parts := make([]string, 0, len(gpu.Workloads))
	for _, workload := range gpu.Workloads {
		name := strings.TrimSpace(workload.Name)
		if separator := strings.LastIndex(name, "/"); separator >= 0 {
			name = name[separator+1:]
		}
		if name == "" || strings.EqualFold(name, "[No data]") {
			name = fmt.Sprintf("PID %d", workload.PID)
		}
		owner := strings.TrimSpace(workload.User)
		if owner == "" {
			owner = "unknown"
		}
		parts = append(parts,
			gpuTitleStyle.Render("JOB ")+
				valueStyle.Render(name)+
				dimStyle.Render(" · USER "+owner+" · PID "+strconv.Itoa(workload.PID)),
		)
	}
	return ansi.Truncate(indent+strings.Join(parts, dimStyle.Render("  │  ")), columns.contentWidth, "…")
}

func gpuTelemetry(gpu gpuInfo, showPowerLimit bool) string {
	if gpu.Platform == "apple" {
		cores := "CORES ?"
		if gpu.CoreCount > 0 {
			cores = fmt.Sprintf("CORES %d", gpu.CoreCount)
		}
		return fmt.Sprintf("%s · RENDER %3.0f%% · TILER %3.0f%%",
			cores, gpu.RendererUtilization, gpu.TilerUtilization)
	}
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
	if gpu.Platform == "apple" {
		return fmt.Sprintf("UMA %*s USED · %*s ALLOC",
			usedWidth, bytes(gpu.MemoryUsed), totalWidth, bytes(gpu.MemoryTotal))
	}
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
	// The words stay so a row can still say IDLE or BUSY. The color is only
	// the shared ok / warn / crit scale.
	style := stateStyle(usageLevel(utilization))
	switch {
	case utilization < 5:
		return "IDLE", style
	case utilization < 35:
		return "LIGHT", style
	case utilization < 65:
		return "ACTIVE", style
	case utilization < 85:
		return "BUSY", style
	case utilization < 95:
		return "HIGH", style
	default:
		return "MAX", style
	}
}

// Clock speed is informational. Temperature is the health signal, colored
// with the shared state scale.
func gpuClockStyle(int) lipgloss.Style {
	// Clock speed is not a health state. Keep it neutral.
	return valueStyle
}

func gpuTemperatureStyle(temperature int) lipgloss.Style {
	return stateStyle(temperatureLevel(temperature))
}

func (m *monitorModel) processPanel(layout dashboardLayout) string {
	w := layout.width
	format := newProcessFormat(w)
	processes := m.filteredProcesses()
	lines := []string{
		processLegend(w - 4),
		format.renderHeader(m.processSort),
	}
	end := min(len(processes), m.monitorOffset+layout.processRows)
	for i := m.monitorOffset; i < end; i++ {
		row := format.renderRow(processes[i], i+1,
			m.effectiveMonitorFocus() == monitorFocusProcesses && i == m.monitorCursor,
			m.colorMode, m.snapshot.MemoryTotal)
		lines = append(lines, row)
	}
	if len(processes) == 0 {
		message := "No process data available."
		if m.filter != "" {
			message = "No processes match the current filter."
		}
		lines = append(lines, dimStyle.Render(message))
	}
	titleForPanel := processTitleStyle
	border := colorProcessBorder
	if m.monitorFocus == monitorFocusProcesses || m.slurmQueue == nil {
		titleForPanel = accentStyle
		border = colorBorderFocus
	}
	meta := m.processTableMeta(m.monitorOffset, layout.processRows, w, "PROCESSES")
	return btopPanel(w, "PROCESSES", meta, strings.Join(lines, "\n"), titleForPanel, border)
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
		warningStyle.Render(m.status),
	}, "\n")
	footer := helpStyle.Render("[enter] continue  [esc] return") + "  " +
		dimStyle.Render("Type or paste the management password.")
	return centeredTerminalFrame(centeredPanel(w, content), footer, w, m.height)
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
		processLegend(w - 4),
		format.renderHeader(m.processSort),
	}
	for i := m.processOffset; i < end; i++ {
		row := format.renderRow(processes[i], i+1, i == m.cursor && !m.filtering, m.colorMode, m.snapshot.MemoryTotal)
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
	actionButtons := make([]string, 0, len(m.admin.actions)*2)
	for index, action := range m.admin.actions {
		if index > 0 {
			actionButtons = append(actionButtons, " ")
		}
		actionButtons = append(actionButtons,
			compactButton(strconv.Itoa(index+1), m.adminActionLabel(index), action.dangerous))
	}
	actions := lipgloss.JoinHorizontal(lipgloss.Top, actionButtons...)
	body := strings.Join([]string{
		titleStyle.Render("MANAGEMENT MODE") + "  " + accentStyle.Render("AUTHORIZED"),
		dimStyle.Render(truncate("Available actions reflect the service account permissions and require confirmation.", w)),
		actions,
		filterLine,
		btopPanel(w, "PROCESS MANAGER", m.processTableMeta(m.processOffset, rows, w, "PROCESS MANAGER"), strings.Join(table, "\n"), processTitleStyle, colorProcessBorder),
	}, "\n")
	footer := helpStyle.Render("[o/O] sort/reverse  [/] filter  [↑/↓] select  [enter/click] details  [c] clear  [esc] monitor") + "  " + dimStyle.Render(m.status)
	return terminalFrame(body, footer, w, m.height)
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
	body := strings.Join([]string{
		titleStyle.Render("CONFIRM MANAGEMENT ACTION"),
		warningStyle.Render("This runs on the host immediately: " + m.selectedAction.label),
		"",
		panelStyle(w).Render(sectionStyle.Render(m.selectedAction.label) + "\n" + m.selectedAction.description),
		"",
		actionCard(w, "Y", confirmLabel, "Click to execute", true),
		"",
		actionCard(w, "N", "Cancel", "Return without making a change", false),
	}, "\n")
	footer := helpStyle.Render("[y/enter] confirm  [n/esc] cancel") + "  " + dimStyle.Render(m.status)
	return terminalFrame(body, footer, w, m.height)
}

func (m *monitorModel) processDetailView() string {
	w := usableWidth(m.width)
	if m.selectedProcess == nil {
		if m.processReadOnly {
			m.screen = screenMonitor
			return m.monitorView()
		}
		m.screen = screenAdmin
		return m.adminView()
	}
	pid := m.selectedProcess.PID
	action := compactButton("Esc", "Back to monitor", false)
	description := "Read-only process information. Sensitive arguments and the working directory are redacted."
	help := "[r] reload  [esc] back"
	if !m.processReadOnly {
		action = compactButton("T", "Terminate process", true)
		if !m.selectedProcessCanTerminate() {
			action = dimStyle.Render("Termination unavailable until details are loaded, or for protected processes")
		}
		action += "  " + compactButton("Esc", "Back to process manager", false)
		description = "Inspect the selected process before taking action."
		help = "[t/click] terminate  [r] reload  [esc] back"
	}
	body := "Loading process details…"
	if m.detailErr != nil {
		body = warningStyle.Render("Unable to read process details: " + m.detailErr.Error())
	} else if m.processDetail != nil {
		d := m.processDetail
		lines := []string{
			sectionStyle.Render(fmt.Sprintf("%s  PID %d", d.Name, d.PID)),
			fmt.Sprintf("%s %d    %s %d    %s %s    %s %d", dimStyle.Render("PPID"), d.PPID, dimStyle.Render("UID"), d.UID, dimStyle.Render("USER"), d.User, dimStyle.Render("THREADS"), d.Threads),
			fmt.Sprintf("%s %s    %s %.1f%%    %s %.1f%%    %s %s", dimStyle.Render("STATE"), d.State, dimStyle.Render("CPU"), d.CPU, dimStyle.Render("MEM"), d.Memory, dimStyle.Render("RSS"), bytes(d.RSS)),
			fmt.Sprintf("%s %s", dimStyle.Render("ELAPSED"), elapsed(d.Elapsed)),
			"",
			dimStyle.Render("COMMAND") + "  " + truncate(d.CommandLine, max(12, w-14)),
			dimStyle.Render("EXEC") + "     " + truncate(d.Executable, max(12, w-14)),
			dimStyle.Render("CWD") + "      " + truncate(d.CWD, max(12, w-14)),
		}
		if d.Redacted {
			lines = append([]string{warningStyle.Render("REDACTED  Authenticate for sensitive process details."), ""}, lines...)
		}
		body = strings.Join(lines, "\n")
	}
	pageBody := strings.Join([]string{
		titleStyle.Render("PROCESS DETAILS") + "  " + accentStyle.Render(fmt.Sprintf("PID %d", pid)),
		dimStyle.Render(description),
		action,
		panelStyle(w).Render(body),
	}, "\n")
	footer := helpStyle.Render(help) + "  " + dimStyle.Render(m.status)
	return terminalFrame(pageBody, footer, w, m.height)
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
	body := strings.Join([]string{
		titleStyle.Render("CONFIRM PROCESS TERMINATION"),
		warningStyle.Render(fmt.Sprintf("PID %d (%s) will receive SIGTERM.", p.PID, p.Command)),
		confirm + "  " + compactButton("N", "Cancel", false),
		panelStyle(w).Render(strings.Join([]string{
			sectionStyle.Render(fmt.Sprintf("PID %d", p.PID)),
			fmt.Sprintf("%s %s", dimStyle.Render("USER"), p.User),
			fmt.Sprintf("%s %.1f%%    %s %.1f%%    %s %s", dimStyle.Render("CPU"), p.CPU, dimStyle.Render("MEM"), p.Memory, dimStyle.Render("RSS"), bytes(p.RSS)),
			dimStyle.Render("COMMAND") + "  " + truncate(p.Command, max(12, w-14)),
		}, "\n")),
	}, "\n")
	footer := helpStyle.Render("[y/enter/click] confirm  [n/esc] cancel") + "  " + dimStyle.Render(m.status)
	return terminalFrame(body, footer, w, m.height)
}

type metricVisual int

const (
	metricVisualNone metricVisual = iota
	metricVisualCPU
	metricVisualMeter
	metricVisualNetwork
	metricVisualBattery
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
	return newDashboardLayoutForMetrics(width, height, gpuCount, gpuUnavailable, 4)
}

func newDashboardLayoutForMetrics(width, height, gpuCount int, gpuUnavailable bool, metricCount int) dashboardLayout {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	metricCount = max(1, metricCount)
	cols := 1
	switch {
	case metricCount >= 5 && width >= 140:
		cols = 5
	case metricCount >= 5 && width >= 72:
		cols = 3
	case width >= 132:
		cols = 4
	case width >= 52:
		cols = 2
	}
	compactGPU := width < 112
	metricLines := ((metricCount + cols - 1) / cols) * 10 // Readings, multi-row history and borders.
	gpuContentLines := -2
	if gpuCount < 0 {
		gpuContentLines = -2
	} else if gpuUnavailable {
		gpuContentLines = 1
	} else {
		gpuContentLines = gpuCount * 2
	}
	if !gpuUnavailable && gpuCount == 0 {
		gpuContentLines = 1
	}
	gpuLines := max(0, gpuContentLines+2) // rounded border
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
	render := func(c metricCard, minimumRows int) string {
		contentWidth := max(4, cardWidth-4)
		title := c.titleStyle
		if title.GetForeground() == nil {
			title = sectionStyle
		}
		border := c.borderColor
		if border == nil {
			border = colorPanelBorder
		}
		lines := []string{
			valueStyle.Render(truncate(c.value, cardWidth-4)),
			dimStyle.Render(truncate(c.detail, cardWidth-4)),
			renderMetricVisual(c, contentWidth),
		}
		if c.visual == metricVisualCPU && contentWidth >= 58 {
			historyWidth := 24
			leftWidth := contentWidth - historyWidth - 3
			left := []string{valueStyle.Render(truncate(c.value, leftWidth)), dimStyle.Render(truncate(c.detail, leftWidth))}
			lines = historyColumns(left, renderHistoryChart(c.primaryHistory, historyWidth, historyPlotHeight, "CPU %", 100, chartStyle), leftWidth)
		}
		lines = strings.Split(strings.Join(lines, "\n"), "\n")
		for len(lines) < minimumRows {
			lines = append(lines, "")
		}
		return btopPanel(cardWidth, c.title, "", strings.Join(lines, "\n"), title, border)
	}
	rows := make([]string, 0, (len(cards)+layout.metricCols-1)/layout.metricCols)
	for start := 0; start < len(cards); start += layout.metricCols {
		end := min(start+layout.metricCols, len(cards))
		minimumRows := 0
		for _, card := range cards[start:end] {
			minimumRows = max(minimumRows, lipgloss.Height(render(card, 0))-2)
		}
		items := make([]string, 0, end-start)
		for _, card := range cards[start:end] {
			if len(items) > 0 {
				items = append(items, " ")
			}
			items = append(items, render(card, minimumRows))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, items...))
	}
	return strings.Join(rows, "\n")
}

func renderMetricVisual(card metricCard, width int) string {
	switch card.visual {
	case metricVisualCPU:
		return strings.Join(renderHistoryChart(card.primaryHistory, width, historyPlotHeight, "CPU %", 100, chartStyle), "\n")
	case metricVisualMeter:
		return bar(card.usage, width)
	case metricVisualNetwork:
		return strings.Join(renderNetworkHistories(card.primaryHistory, card.secondaryHistory, width), "\n")
	case metricVisualBattery:
		return batteryBar(card.usage, width)
	default:
		return dimStyle.Render(strings.Repeat("─", max(1, width)))
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
		label := processStateMarker(item.state)
		switch {
		case width >= 108:
			label += " " + item.full
		case width >= 78:
			label += " " + item.short
		default:
			label += item.state
		}
		parts = append(parts, processStateStyle(item.state).Render(label))
	}
	legend := strings.Join(parts, "  ")
	load := dimStyle.Render("   LOAD ") + okStyle.Render("OK") + " " +
		warningStyle.Render("WARN") + " " + dangerStyle.Render("CRIT")
	if lipgloss.Width(legend)+lipgloss.Width(load) <= width {
		legend += load
	}
	return ansi.Truncate(legend, width, "")
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
	operation   managementOperation
	target      string
	privileged  bool
	dangerous   bool
}

type adminController struct {
	password     string
	passwordHash string
	actions      []adminAction
}

func newAdminController() *adminController {
	scope := strings.ToLower(strings.TrimSpace(os.Getenv("FLEETTY_INSTALL_SCOPE")))
	return &adminController{
		password:     os.Getenv("ADMIN_PASSWORD"),
		passwordHash: os.Getenv("ADMIN_PASSWORD_HASH"),
		actions:      defaultAdminActions(runtime.GOOS, scope, os.Geteuid(), privilegedSocketPath()),
	}
}

func defaultAdminActions(goos, scope string, effectiveUID int, privilegedSocket string) []adminAction {
	if goos != "linux" {
		return nil
	}
	userScope := scope == "user" || scope == "" && effectiveUID != 0
	actions := []adminAction{
		{
			ID: 0, label: "Restart Fleetty", description: "Restart the Fleetty node service.",
			operation: managementRestartService, target: "fleetty.service",
			privileged: !userScope && effectiveUID != 0,
		},
	}
	if (!userScope && effectiveUID == 0) || strings.TrimSpace(privilegedSocket) != "" {
		actions = append(actions, adminAction{
			ID: 1, label: "Reboot machine",
			description: "Restart the entire host. Active workloads will be interrupted.",
			operation:   managementRebootHost,
			privileged:  effectiveUID != 0,
			dangerous:   true,
		})
	}
	return actions
}

func newRemoteAdminController() *adminController {
	admin := newAdminController()
	admin.password = "remote"
	admin.passwordHash = ""
	return admin
}

func (a *adminController) enabled() bool { return a.password != "" || a.passwordHash != "" }

func (a *adminController) actionInfo() []managementActionInfo {
	result := make([]managementActionInfo, 0, len(a.actions))
	for _, action := range a.actions {
		result = append(result, managementActionInfo{
			ID: action.ID, Label: action.label,
			Description: action.description, Dangerous: action.dangerous,
		})
	}
	return result
}

func actionsFromInfo(infos []managementActionInfo) []adminAction {
	result := make([]adminAction, 0, len(infos))
	for _, info := range infos {
		result = append(result, adminAction{
			ID: info.ID, label: info.Label,
			description: info.Description, dangerous: info.Dangerous,
		})
	}
	return result
}

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
	OSName                  string
	CPUModel                string
	CPUCores                int
	Uptime                  uint64
	CPUPercent              float64
	CPUCoreUsage            []cpuCoreUsage
	CPUFrequency            *cpuFrequencyInfo
	LoadAverage             string
	MemoryUsed, MemoryTotal uint64
	DiskUsed, DiskTotal     uint64
	NetworkRX, NetworkTX    uint64
	NetworkRXTotal          uint64
	NetworkTXTotal          uint64
	NetworkProcesses        []processNetworkInfo
	NetworkConnections      []networkConnectionInfo
	NetworkConnectionMode   bool
	NetworkProcessError     string
	Battery                 *batteryInfo
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
	ManagementActions       []managementActionInfo
}

type batteryInfo struct {
	Percent       float64
	Status        string
	TimeRemaining string
	PowerSource   string
}

type managementActionInfo struct {
	ID          int
	Label       string
	Description string
	Dangerous   bool
}

type gpuInfo struct {
	Index                   int
	Name                    string
	UUID                    string
	DriverVersion           string
	Platform                string
	Utilization             float64
	RendererUtilization     float64
	TilerUtilization        float64
	MemoryUsed, MemoryTotal uint64
	CoreCount               int
	Temperature             int
	ClockMHz                int
	Power, PowerLimit       float64
	Workloads               []gpuWorkloadInfo
}

type gpuWorkloadInfo struct {
	PID        int
	User       string
	Name       string
	MemoryUsed uint64
}

type processInfo struct {
	PID                  int
	User, State, Command string
	CPU, Memory          float64
	RSS, Elapsed         uint64
}

type processNetworkInfo struct {
	PID              int
	Name             string
	RX, TX           uint64
	RXTotal, TXTotal uint64
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
	Redacted                bool
	CanTerminate            bool
}

type metricsCollector struct {
	config                     machineConfig
	identity                   systemIdentity
	previousCPU                map[string]cpuCounters
	cpuFrequencyReader         cpuFrequencyReader
	cpuFrequencyInitialized    bool
	previousNet                netCounters
	previousInterfaces         map[string]networkDeviceCounters
	previousProcessNet         map[int]processNetworkCounters
	previousConnectionNet      map[string]connectionNetworkCounters
	haveCPU                    bool
	haveNet                    bool
	lastNetAt                  time.Time
	lastProcessNetAt           time.Time
	lastServiceAt              time.Time
	cachedServices             []serviceHealth
	cachedContainers           []containerInfo
	cachedDockerError          string
	cachedPM2Processes         []pm2ProcessInfo
	cachedPM2Error             string
	processRefreshInterval     time.Duration
	gpuWorkloadRefreshInterval time.Duration
	processNetRefreshInterval  time.Duration
	lastProcessesAt            time.Time
	cachedProcesses            []processInfo
	lastGPUWorkloadsAt         time.Time
	cachedGPUWorkloads         map[string][]gpuWorkloadInfo
	cachedNetworkProcesses     []processNetworkInfo
	cachedNetworkConnections   []networkConnectionInfo
	cachedConnectionWarning    string
}

type netCounters struct{ rx, tx uint64 }

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func newMetricsCollector(config machineConfig) *metricsCollector {
	processNetInterval := 5 * time.Second
	if runtime.GOOS == "linux" {
		processNetInterval = time.Second
	}
	return &metricsCollector{
		config:                     config,
		identity:                   readSystemIdentity(),
		previousInterfaces:         make(map[string]networkDeviceCounters),
		previousProcessNet:         make(map[int]processNetworkCounters),
		processRefreshInterval:     3 * time.Second,
		gpuWorkloadRefreshInterval: 5 * time.Second,
		processNetRefreshInterval:  processNetInterval,
	}
}

func (c *metricsCollector) needsWarmup() bool {
	return !c.haveCPU || !c.haveNet
}

func (c *metricsCollector) collect() (monitorSnapshot, error) {
	return c.collectWithProcesses(true)
}

func (c *metricsCollector) collectSummary() (monitorSnapshot, error) {
	return c.collectWithProcesses(false)
}

func (c *metricsCollector) collectWithProcesses(includeProcesses bool) (monitorSnapshot, error) {
	s := monitorSnapshot{CollectedAt: time.Now()}
	s.OSName = c.identity.OSName
	s.CPUModel = c.identity.CPUModel
	s.CPUCores = c.identity.CPUCores
	s.Uptime = uptimeSeconds(c.identity, s.CollectedAt)
	var errs []string
	if err := c.collectCPU(&s); err != nil {
		errs = append(errs, "cpu: "+err.Error())
	}
	c.collectCPUFrequency(&s)
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
	if includeProcesses {
		c.collectProcessNetwork(&s)
	}
	s.LoadAverage = readLoadAverage()
	if runtime.GOOS == "darwin" {
		s.Battery, _ = readDarwinBattery()
		s.GPUs, _ = readDarwinAppleGPU()
	} else if c.config.Profile == machineProfileGPU {
		s.GPUs, s.GPUError = c.collectGPUs()
	}
	c.collectMachineDetails(&s)
	if includeProcesses {
		c.collectProcesses(&s, &errs)
		attachGPUWorkloadUsers(s.GPUs, s.Processes)
	} else {
		// The hub card names who is on the machine without shipping the
		// full process table. Older hubs ignore a short Processes list.
		var ignored []string
		c.collectProcesses(&s, &ignored)
		attachGPUWorkloadUsers(s.GPUs, s.Processes)
		s.Processes = topProcesses(s.Processes, 3)
	}
	if len(errs) > 0 {
		return s, errors.New(strings.Join(errs, "; "))
	}
	return s, nil
}

func readMemory() (used, total uint64, err error) {
	if runtime.GOOS == "darwin" {
		return readDarwinMemory()
	}
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
	if runtime.GOOS == "darwin" {
		return readDarwinLoadAverage()
	}
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

// collectGPUs refreshes the per-GPU summary on every cycle and reuses the
// heavier compute-apps attribution until gpuWorkloadRefreshInterval elapses.
func (c *metricsCollector) collectGPUs() ([]gpuInfo, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,uuid,name,utilization.gpu,memory.used,memory.total,temperature.gpu,clocks.current.graphics,power.draw,power.limit,driver_version", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, gpuMetricsError(err, "/sys/bus/pci/devices")
	}
	gpus := parseGPUs(output)
	c.attachGPUWorkloads(gpus)
	return gpus, ""
}

func (c *metricsCollector) attachGPUWorkloads(gpus []gpuInfo) {
	due := c.lastGPUWorkloadsAt.IsZero() ||
		time.Since(c.lastGPUWorkloadsAt) >= c.gpuWorkloadRefreshInterval
	if !due {
		for index := range gpus {
			gpus[index].Workloads = append(
				[]gpuWorkloadInfo(nil), c.cachedGPUWorkloads[gpus[index].UUID]...,
			)
		}
		return
	}
	workloadCtx, workloadCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer workloadCancel()
	workloadOutput, workloadErr := exec.CommandContext(
		workloadCtx, "nvidia-smi",
		"--query-compute-apps=gpu_uuid,pid,process_name,used_gpu_memory",
		"--format=csv,noheader,nounits",
	).Output()
	if workloadErr == nil {
		workloads := parseGPUWorkloads(workloadOutput)
		c.cachedGPUWorkloads = workloads
	}
	c.lastGPUWorkloadsAt = time.Now()
	for index := range gpus {
		gpus[index].Workloads = append(
			[]gpuWorkloadInfo(nil), c.cachedGPUWorkloads[gpus[index].UUID]...,
		)
	}
}

// collectProcesses refreshes the full process table at a lower cadence than
// the host metrics and reuses the last successful table in between.
func (c *metricsCollector) collectProcesses(snapshot *monitorSnapshot, errs *[]string) {
	if c.lastProcessesAt.IsZero() ||
		time.Since(c.lastProcessesAt) >= c.processRefreshInterval {
		processes, err := readProcesses()
		if err != nil {
			*errs = append(*errs, "processes: "+err.Error())
		} else {
			c.cachedProcesses = processes
			c.lastProcessesAt = time.Now()
		}
	}
	snapshot.Processes = append([]processInfo(nil), c.cachedProcesses...)
}

func parseGPUs(output []byte) []gpuInfo {
	var gpus []gpuInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 9 && len(parts) != 11 {
			continue
		}
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		index, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		nameIndex, utilIndex := 1, 2
		uuid, driver := "", ""
		if len(parts) == 11 {
			uuid, nameIndex, utilIndex, driver = parts[1], 2, 3, parts[10]
		}
		memUsed, _ := strconv.ParseUint(parts[utilIndex+1], 10, 64)
		memTotal, _ := strconv.ParseUint(parts[utilIndex+2], 10, 64)
		util, _ := strconv.ParseFloat(parts[utilIndex], 64)
		temp, _ := strconv.Atoi(parts[utilIndex+3])
		clockMHz, _ := strconv.Atoi(parts[utilIndex+4])
		power, _ := strconv.ParseFloat(parts[utilIndex+5], 64)
		powerLimit, _ := strconv.ParseFloat(parts[utilIndex+6], 64)
		gpus = append(gpus, gpuInfo{
			Index: index, UUID: uuid, Name: parts[nameIndex], DriverVersion: driver,
			Utilization: util,
			MemoryUsed:  memUsed * 1024 * 1024, MemoryTotal: memTotal * 1024 * 1024,
			Temperature: temp, ClockMHz: clockMHz, Power: power, PowerLimit: powerLimit,
		})
	}
	return gpus
}

func parseGPUWorkloads(output []byte) map[string][]gpuWorkloadInfo {
	workloads := make(map[string][]gpuWorkloadInfo)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 4 {
			continue
		}
		uuid := strings.TrimSpace(parts[0])
		pid, pidErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		memoryMiB, memoryErr := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
		if uuid == "" || pidErr != nil || pid <= 0 || memoryErr != nil {
			continue
		}
		workloads[uuid] = append(workloads[uuid], gpuWorkloadInfo{
			PID: pid, Name: sanitizeTerminalText(parts[2]), MemoryUsed: memoryMiB << 20,
		})
	}
	return workloads
}

func topProcesses(processes []processInfo, limit int) []processInfo {
	if limit <= 0 || len(processes) == 0 {
		return nil
	}
	remaining := append([]processInfo(nil), processes...)
	picked := make([]processInfo, 0, min(limit, len(remaining)))
	for len(picked) < limit && len(remaining) > 0 {
		best := 0
		for index := 1; index < len(remaining); index++ {
			if remaining[index].CPU > remaining[best].CPU {
				best = index
			}
		}
		if remaining[best].CPU <= 0 {
			break
		}
		picked = append(picked, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return picked
}

func attachGPUWorkloadUsers(gpus []gpuInfo, processes []processInfo) {
	users := make(map[int]string, len(processes))
	for _, process := range processes {
		users[process.PID] = process.User
	}
	for gpuIndex := range gpus {
		for workloadIndex := range gpus[gpuIndex].Workloads {
			gpus[gpuIndex].Workloads[workloadIndex].User =
				users[gpus[gpuIndex].Workloads[workloadIndex].PID]
		}
	}
}

func readProcesses() ([]processInfo, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcesses()
	}
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

func readProcessDetailWithSensitive(pid int, includeSensitive bool) (processDetail, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcessDetail(pid, includeSensitive)
	}
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
	applyProcessDetailPolicy(&detail, includeSensitive)
	detail.StartTimeTicks, _ = readProcessStartTicks(pid)
	return detail, nil
}

func applyProcessDetailPolicy(detail *processDetail, includeSensitive bool) {
	if detail == nil || includeSensitive {
		return
	}
	detail.CommandLine = redactProcessCommandLine(detail.CommandLine)
	detail.CWD = redactedProcessDetailMessage
	detail.Redacted = true
}

func readProcessStartTicks(pid int) (uint64, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcessStart(pid)
	}
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

func panelStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(0, 1).Width(max(20, width-2))
}
func clickableStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorderFocus).Padding(0, 1).Width(max(20, width-2))
}

func btopPanel(width int, title, meta, content string, titleStyle lipgloss.Style, borderColor color.Color) string {
	width = max(20, width)
	innerWidth := width - 2
	contentWidth := width - 4
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)

	titleText := " " + truncate(title, max(4, innerWidth-5)) + " "
	metaText := ""
	if meta != "" {
		// Callers may pass a precolored meta (hub address, latency, WARN).
		if strings.Contains(meta, "\x1b") {
			metaText = " " + meta + " "
		} else {
			metaText = dimStyle.Render(" " + meta + " ")
		}
	}
	fillWidth := innerWidth - 1 - lipgloss.Width(titleText) - lipgloss.Width(metaText)
	if fillWidth < 1 {
		metaText = ""
		fillWidth = max(1, innerWidth-1-lipgloss.Width(titleText))
	}
	top := borderStyle.Render("╭─") +
		titleStyle.Render(titleText) +
		borderStyle.Render(strings.Repeat("─", fillWidth)) +
		metaText +
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

// terminalFrame reserves the final terminal row for the page's operation bar.
// Page renderers own their body layout, while this function owns the one
// invariant shared by every interactive Fleetty screen: controls never move
// when content grows, shrinks, or refreshes.
func terminalFrame(body, footer string, width, height int) string {
	width = max(1, width)
	footer = ansi.Truncate(footer, width, "")
	if height <= 0 {
		if body == "" {
			return footer
		}
		return body + "\n" + footer
	}

	bodyRows := max(0, height-lipgloss.Height(footer))
	lines := []string(nil)
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	if len(lines) > bodyRows {
		lines = lines[:bodyRows]
	}
	for len(lines) < bodyRows {
		lines = append(lines, "")
	}
	lines = append(lines, footer)
	return strings.Join(lines, "\n")
}

func centeredTerminalFrame(content, footer string, width, height int) string {
	footerHeight := lipgloss.Height(footer)
	bodyHeight := max(1, height-footerHeight)
	body := lipgloss.Place(max(1, width), bodyHeight,
		lipgloss.Center, lipgloss.Center, content)
	return terminalFrame(body, footer, width, height)
}

func (m *monitorModel) renderMonitorFooter(width int) string {
	if m.filtering {
		value := m.filter
		if value == "" {
			value = "process name, user, or PID"
		}
		return ansi.Truncate(accentStyle.Render("FILTER /")+" "+
			inputStyle.Render(value+"█")+"  "+dimStyle.Render("[enter] apply  [esc] cancel"), width, "")
	}
	hints := []string{keyHint("m", "management")}
	if m.slurmQueue != nil {
		hints = append(hints,
			keyHint("tab", "focus"),
			keyHint("+/-", "size"),
		)
	}
	hints = append(hints,
		keyHint("l", "layout"),
		keyHint("pg↑↓", "widgets"),
		keyHint("↑↓", "select"),
		keyHint("enter", "details"),
		keyHint("/", "filter"),
		keyHint("o/O", "sort/reverse"),
		keyHint("t", "theme"),
		keyHint("r", "refresh"),
		keyHint("q", "quit"),
	)
	joined := strings.Join(hints, "  ")
	if lipgloss.Width(joined) > width {
		return ansi.Truncate(joined, width, "")
	}
	remaining := width - lipgloss.Width(joined) - 2
	if remaining < 12 || strings.TrimSpace(m.status) == "" {
		return joined
	}
	return joined + "  " + dimStyle.Render(truncate(m.status, remaining))
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
	m.theme.manual()
	if m.colorMode == colorModeLight {
		m.colorMode = colorModeDark
	} else {
		m.colorMode = colorModeLight
	}
	m.status = fmt.Sprintf("%s theme enabled for this SSH session.", strings.ToUpper(m.colorMode.String()))
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
			if !math.IsNaN(value) && !math.IsInf(value, 0) && value > maxValue {
				maxValue = value
			}
		}
	}
	return maxValue
}

func bar(value float64, width int) string {
	filled := int(value / 100 * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return stateStyle(usageLevel(value)).Render(strings.Repeat("█", filled)) + barTrackStyle.Render(strings.Repeat("░", width-filled))
}

func batteryBar(value float64, width int) string {
	filled := int(value / 100 * float64(width))
	filled = max(0, min(width, filled))
	return stateStyle(batteryLevel(value)).Render(strings.Repeat("█", filled)) + barTrackStyle.Render(strings.Repeat("░", width-filled))
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
