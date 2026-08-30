package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Rhythmicc/fleetty/internal/buildinfo"
)

type launcherAction int

const (
	launcherActionQuit launcherAction = iota
	launcherActionTop
	launcherActionServe
)

type launcherPage int

const (
	launcherPageHome launcherPage = iota
	launcherPageSSH
	launcherPageConfig
	launcherPageHelp
)

type launcherResult struct {
	Action             launcherAction
	AuthorizedKeysPath string
}

type launcherSSHState struct {
	Ready         bool
	Mode          string
	Error         string
	Endpoint      string
	ConfiguredKey string
	SuggestedKey  string
}

type launcherModel struct {
	width      int
	height     int
	cursor     int
	page       launcherPage
	colorMode  colorMode
	result     launcherResult
	ssh        launcherSSHState
	hostname   string
	optionRows [4][2]int
}

type launcherOption struct {
	Title       string
	Description string
	Badge       string
	BadgeStyle  lipgloss.Style
}

func runLauncher(stdin io.Reader, stdout io.Writer) (launcherResult, error) {
	if !interactiveTerminal(stdin) || !interactiveTerminal(stdout) {
		return launcherResult{}, errors.New(
			"an interactive terminal is required; use `fleetty top`, `fleetty serve`, or `fleetty help` explicitly",
		)
	}
	model := newLauncherModel()
	final, err := tea.NewProgram(model, tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
	if err != nil {
		return launcherResult{}, err
	}
	launcher, ok := final.(*launcherModel)
	if !ok {
		return launcherResult{}, errors.New("launcher returned an unexpected model")
	}
	return launcher.result, nil
}

func interactiveTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func newLauncherModel() *launcherModel {
	hostname, _ := os.Hostname()
	return &launcherModel{
		width: 88, height: 28,
		colorMode: parseColorMode(os.Getenv("DEFAULT_THEME")),
		ssh:       inspectLauncherSSH(),
		hostname:  sanitizeTerminalText(hostname),
	}
}

func inspectLauncherSSH() launcherSSHState {
	host := envString("SSH_HOST", "0.0.0.0")
	port := envString("SSH_PORT", "23234")
	state := launcherSSHState{Endpoint: net.JoinHostPort(host, port)}
	configured := strings.TrimSpace(os.Getenv("SSH_AUTHORIZED_KEYS_FILE"))
	if configured != "" {
		state.ConfiguredKey = configured
	}
	if _, err := loadSSHAccessConfig(); err == nil {
		state.Ready = true
		if envBool("SSH_ALLOW_ANONYMOUS", false) {
			state.Mode = "anonymous migration mode"
		} else {
			state.Mode = configured
		}
		return state
	} else {
		state.Error = sanitizeTerminalText(err.Error())
	}
	for _, candidate := range launcherAuthorizedKeyCandidates() {
		if _, err := loadAuthorizedKeySet(candidate); err == nil {
			state.SuggestedKey = candidate
			break
		}
	}
	return state
}

func launcherAuthorizedKeyCandidates() []string {
	seen := make(map[string]struct{})
	var candidates []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}
	add(os.Getenv("SSH_AUTHORIZED_KEYS_FILE"))
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config", "fleetty", "authorized_keys"))
		add(filepath.Join(home, ".ssh", "authorized_keys"))
	}
	return candidates
}

func (m *launcherModel) Init() tea.Cmd { return nil }

func (m *launcherModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case tea.MouseClickMsg:
		if message.Button == tea.MouseLeft && m.page == launcherPageHome {
			mouse := message.Mouse()
			for index, rows := range m.optionRows {
				if mouse.Y >= rows[0] && mouse.Y <= rows[1] {
					m.cursor = index
					return m.activate()
				}
			}
		}
	case tea.KeyPressMsg:
		key := message.String()
		if key == "ctrl+c" {
			m.result.Action = launcherActionQuit
			return m, tea.Quit
		}
		if key == "t" || key == "T" {
			m.toggleTheme()
			return m, nil
		}
		if m.page != launcherPageHome {
			return m.updateDetail(key)
		}
		switch key {
		case "q", "Q", "esc":
			m.result.Action = launcherActionQuit
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor + 3) % 4
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % 4
		case "1", "2", "3", "4":
			m.cursor = int(key[0] - '1')
			return m.activate()
		case "enter", " ":
			return m.activate()
		}
	}
	return m, nil
}

func (m *launcherModel) activate() (tea.Model, tea.Cmd) {
	switch m.cursor {
	case 0:
		m.result.Action = launcherActionTop
		return m, tea.Quit
	case 1:
		m.page = launcherPageSSH
	case 2:
		m.page = launcherPageConfig
	case 3:
		m.page = launcherPageHelp
	}
	return m, nil
}

func (m *launcherModel) updateDetail(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "Q", "esc", "backspace", "left":
		m.page = launcherPageHome
		return m, nil
	case "1":
		m.result.Action = launcherActionTop
		return m, tea.Quit
	case "r", "R":
		m.ssh = inspectLauncherSSH()
		return m, nil
	case "s", "S", "enter":
		if m.page != launcherPageSSH {
			return m, nil
		}
		if m.ssh.Ready {
			m.result.Action = launcherActionServe
			return m, tea.Quit
		}
		if m.ssh.SuggestedKey != "" {
			m.result = launcherResult{
				Action: launcherActionServe, AuthorizedKeysPath: m.ssh.SuggestedKey,
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *launcherModel) toggleTheme() {
	if m.colorMode == colorModeLight {
		m.colorMode = colorModeDark
	} else {
		m.colorMode = colorModeLight
	}
}

func (m *launcherModel) View() tea.View {
	body := m.homeView()
	switch m.page {
	case launcherPageSSH:
		body = m.sshView()
	case launcherPageConfig:
		body = m.configView()
	case launcherPageHelp:
		body = m.helpView()
	}
	if m.colorMode == colorModeLight {
		body = applyLightTheme(body)
	}
	view := tea.NewView(body)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "Fleetty · Start"
	view.BackgroundColor, view.ForegroundColor = viewColors(m.colorMode)
	return view
}

func (m *launcherModel) homeView() string {
	width := max(20, min(92, m.width-2))
	sshBadge := "SETUP"
	sshBadgeStyle := warningStyle
	sshDescription := "Configure authorized keys before opening the read-only SSH dashboard."
	if m.ssh.Ready {
		sshBadge = "READY"
		sshBadgeStyle = processRunningStyle
		sshDescription = "Authentication is configured; inspect the endpoint or start the server."
	} else if m.ssh.SuggestedKey != "" {
		sshBadge = "KEY FOUND"
		sshDescription = "A local authorized_keys file can be used safely for this session."
	}
	options := []launcherOption{
		{Title: "LOCAL MONITOR", Description: "Open CPU, memory, disk, network, GPU and process dashboards.", Badge: "READY", BadgeStyle: processRunningStyle},
		{Title: "SSH SERVER", Description: sshDescription, Badge: sshBadge, BadgeStyle: sshBadgeStyle},
		{Title: "CONFIGURATION", Description: "View paths, runtime capabilities and current SSH access state.", Badge: "VIEW", BadgeStyle: networkRXStyle},
		{Title: "COMMAND HELP", Description: "See explicit commands for monitoring, serving, exports and diagnostics.", Badge: "HELP", BadgeStyle: accentStyle},
	}
	contentWidth := width - 4
	var lines []string
	for index, option := range options {
		selected := index == m.cursor
		prefix := "  "
		if selected {
			prefix = "› "
		}
		first := prefix + accentStyle.Render(option.Title)
		badge := option.BadgeStyle.Render(option.Badge)
		gap := max(1, contentWidth-lipgloss.Width(first)-lipgloss.Width(badge))
		first += strings.Repeat(" ", gap) + badge
		plainDescription := truncate(option.Description, max(4, contentWidth-2))
		second := "  " + dimStyle.Render(plainDescription)
		if selected {
			style := launcherSelectedStyle(m.colorMode).Width(contentWidth)
			plainFirst := prefix + option.Title
			gap = max(1, contentWidth-lipgloss.Width(plainFirst)-lipgloss.Width(option.Badge))
			first = style.Render(plainFirst + strings.Repeat(" ", gap) + option.Badge)
			second = style.Render("  " + plainDescription)
		}
		lines = append(lines, first, second)
		if index != len(options)-1 {
			lines = append(lines, "")
		}
	}
	meta := buildinfo.Current().Version + " · " + runtime.GOOS + "/" + runtime.GOARCH
	panel := btopPanel(width, "START", meta, strings.Join(lines, "\n"), titleStyle, colorPanelBorder)
	footer := keyHint("↑↓", "select") + "  " + keyHint("enter", "open") + "  " +
		keyHint("t", "theme") + "  " + keyHint("q", "quit")
	bodyHeight := max(1, m.height-lipgloss.Height(footer))
	top := max(0, (bodyHeight-lipgloss.Height(panel))/2)
	for index := range m.optionRows {
		row := top + 1 + index*3
		m.optionRows[index] = [2]int{row, row + 1}
	}
	return centeredTerminalFrame(panel, footer, max(1, m.width), m.height)
}

func (m *launcherModel) sshView() string {
	width := max(20, min(96, m.width-2))
	status := dangerStyle.Render("SETUP REQUIRED")
	access := dimStyle.Render("No authorized-key source is active.")
	action := dimStyle.Render("Create an authorized_keys file, then press r to refresh.")
	if m.ssh.Ready {
		status = processRunningStyle.Render("READY")
		access = valueStyle.Render(m.ssh.Mode)
		action = keyHint("enter", "start SSH server")
	} else if m.ssh.SuggestedKey != "" {
		status = warningStyle.Render("KEY FOUND")
		access = valueStyle.Render(m.ssh.SuggestedKey)
		action = keyHint("enter", "use this key file and start once")
	}
	lines := []string{
		sectionStyle.Render("READ-ONLY SSH DASHBOARD") + "  " + status,
		"",
		dimStyle.Render("ENDPOINT") + "  " + valueStyle.Render(m.ssh.Endpoint),
		dimStyle.Render("ACCESS") + "    " + access,
		"",
		"Fleetty never opens a system shell. Public-key authentication is required",
		"before the remote dashboard can listen for clients.",
		"",
		action,
	}
	if !m.ssh.Ready && m.ssh.SuggestedKey == "" {
		reason := truncate(m.ssh.Error, max(8, width-8))
		if reason != "" {
			lines = append(lines, "", dangerStyle.Render("WHY")+"  "+dimStyle.Render(reason))
		}
		lines = append(lines, "", warningStyle.Render("Example:"),
			dimStyle.Render(`SSH_AUTHORIZED_KEYS_FILE="$HOME/.ssh/authorized_keys" fleetty serve`))
	}
	footer := keyHint("enter", "start") + "  " + keyHint("r", "recheck") + "  " +
		keyHint("esc", "back") + "  " + keyHint("t", "theme") + "  " + keyHint("q", "quit")
	return m.detailPanel(width, "SSH SETUP", strings.Join(lines, "\n"), footer)
}

func (m *launcherModel) configView() string {
	width := max(20, min(96, m.width-2))
	layoutPath, _ := resolvePanelLayoutPath("")
	sshStatus := dangerStyle.Render("NOT CONFIGURED")
	if m.ssh.Ready {
		sshStatus = processRunningStyle.Render("READY")
	} else if m.ssh.SuggestedKey != "" {
		sshStatus = warningStyle.Render("LOCAL KEY AVAILABLE")
	}
	info := buildinfo.Current()
	lines := []string{
		sectionStyle.Render("RUNTIME"),
		dimStyle.Render("HOST") + "      " + valueStyle.Render(m.hostname),
		dimStyle.Render("BUILD") + "     " + valueStyle.Render(info.Version) + "  " + dimStyle.Render(info.Commit),
		dimStyle.Render("PLATFORM") + "  " + valueStyle.Render(runtime.GOOS+"/"+runtime.GOARCH),
		"",
		sectionStyle.Render("LOCAL DATA"),
		dimStyle.Render("LAYOUT") + "    " + dimStyle.Render(layoutPath),
		dimStyle.Render("HISTORY") + "   " + dimStyle.Render(defaultHistoryPath()),
		"",
		sectionStyle.Render("SSH ACCESS") + "  " + sshStatus,
		dimStyle.Render("ENDPOINT") + "  " + valueStyle.Render(m.ssh.Endpoint),
	}
	footer := keyHint("1", "local monitor") + "  " + keyHint("esc", "back") + "  " +
		keyHint("t", "theme") + "  " + keyHint("q", "quit")
	return m.detailPanel(width, "CONFIGURATION", strings.Join(lines, "\n"), footer)
}

func (m *launcherModel) helpView() string {
	width := max(20, min(96, m.width-2))
	lines := []string{
		sectionStyle.Render("COMMON COMMANDS"),
		valueStyle.Render("fleetty") + "          " + dimStyle.Render("interactive start window"),
		valueStyle.Render("fleetty top") + "      " + dimStyle.Render("local system monitor"),
		valueStyle.Render("fleetty serve") + "    " + dimStyle.Render("read-only SSH dashboard server"),
		valueStyle.Render("fleetty snapshot") + " " + dimStyle.Render("machine-readable JSON snapshot"),
		valueStyle.Render("fleetty metrics") + "  " + dimStyle.Render("Prometheus text output"),
		valueStyle.Render("fleetty doctor") + "   " + dimStyle.Render("installed service diagnostics"),
		valueStyle.Render("fleetty version") + "  " + dimStyle.Render("build and platform information"),
		"",
		"SSH serving is always explicit. Running Fleetty without arguments never",
		"opens a network port or silently enables anonymous access.",
	}
	footer := keyHint("1", "local monitor") + "  " + keyHint("esc", "back") + "  " +
		keyHint("t", "theme") + "  " + keyHint("q", "quit")
	return m.detailPanel(width, "COMMAND HELP", strings.Join(lines, "\n"), footer)
}

func (m *launcherModel) detailPanel(width int, title, content, footer string) string {
	panel := btopPanel(width, title, strings.ToUpper(m.colorMode.String()), content, titleStyle, colorPanelBorder)
	return centeredTerminalFrame(panel, footer, max(1, m.width), m.height)
}

func launcherSelectedStyle(mode colorMode) lipgloss.Style {
	if mode == colorModeLight {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#172033")).Background(lipgloss.Color("#BFE8D0")).Bold(true)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FF")).Background(lipgloss.Color("#303A4E")).Bold(true)
}
