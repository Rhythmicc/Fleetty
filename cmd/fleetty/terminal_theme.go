package main

import (
	"image/color"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ssh"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Each terminal session owns its preference and detection state. Explicit
// choices never change in response to terminal reports.
type terminalTheme struct {
	automatic      bool
	pending        bool
	backgroundRead bool
	schemeRead     bool
}

type terminalThemeTimeoutMsg struct{}

func newTerminalTheme(preference string) terminalTheme {
	preference = strings.ToLower(strings.TrimSpace(preference))
	auto := preference == "" || preference == "auto"
	return terminalTheme{automatic: auto, pending: auto}
}

func (t terminalTheme) init() tea.Cmd {
	if !t.automatic {
		return nil
	}
	return tea.Batch(
		tea.Raw(ansi.SetModeLightDark+ansi.RequestLightDarkReport+ansi.RequestBackgroundColor),
		tea.Tick(750*time.Millisecond, func(time.Time) tea.Msg { return terminalThemeTimeoutMsg{} }),
	)
}

func (t *terminalTheme) update(msg tea.Msg, mode *colorMode) bool {
	var detected colorMode
	switch msg := msg.(type) {
	case uv.DarkColorSchemeEvent:
		detected = colorModeDark
		t.schemeRead = true
	case uv.LightColorSchemeEvent:
		detected = colorModeLight
		t.schemeRead = true
	case tea.BackgroundColorMsg:
		// OSC 11 describes the terminal background, which Fleetty can change
		// after startup. Only the first reply is usable for initial detection;
		// explicit color-scheme reports remain authoritative thereafter.
		if t.backgroundRead || t.schemeRead || msg.Color == nil {
			return true
		}
		t.backgroundRead = true
		if !msg.IsDark() {
			detected = colorModeLight
		}
	case terminalThemeTimeoutMsg:
		t.pending = false
		return true
	default:
		return false
	}
	if t.automatic {
		*mode = detected
		t.pending = false
	}
	return true
}

func (t *terminalTheme) manual() {
	t.automatic = false
	t.pending = false
}

func (t terminalTheme) colors(mode colorMode) (color.Color, color.Color) {
	if t.pending {
		return nil, nil
	}
	return viewColors(mode)
}

func resetTerminalTheme(output io.Writer) {
	_, _ = io.WriteString(output, ansi.ResetModeLightDark)
}

// Run after the SSH TUI closes, including disconnects and program errors.
func terminalThemeCleanup(next ssh.Handler) ssh.Handler {
	return func(session ssh.Session) {
		if _, _, ok := session.Pty(); ok {
			defer resetTerminalTheme(session)
		}
		next(session)
	}
}
