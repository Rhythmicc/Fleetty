package main

import (
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestTerminalThemePreferencesAndQueries(t *testing.T) {
	for _, preference := range []string{"", "auto", " AUTO "} {
		theme := newTerminalTheme(preference)
		if !theme.automatic || !theme.pending {
			t.Fatalf("%q did not select automatic detection", preference)
		}
		if bg, fg := theme.colors(colorModeDark); bg != nil || fg != nil {
			t.Fatal("initial view overwrites the terminal colors before detection")
		}
		commands := theme.init()().(tea.BatchMsg)
		query := commands[0]().(tea.RawMsg).Msg.(string)
		for _, sequence := range []string{ansi.SetModeLightDark, ansi.RequestLightDarkReport, ansi.RequestBackgroundColor} {
			if !strings.Contains(query, sequence) {
				t.Fatalf("missing theme query/subscription %q", sequence)
			}
		}
	}
	for _, preference := range []string{"dark", "light"} {
		theme := newTerminalTheme(preference)
		mode := parseColorMode(preference)
		if theme.automatic || theme.pending || theme.init() != nil {
			t.Fatal("explicit theme starts automatic detection")
		}
		theme.update(uv.LightColorSchemeEvent{}, &mode)
		theme.update(uv.DarkColorSchemeEvent{}, &mode)
		if mode != parseColorMode(preference) {
			t.Fatal("terminal report overrides explicit theme")
		}
	}
}

func TestTerminalThemeDetectionAndReportPrecedence(t *testing.T) {
	theme := newTerminalTheme("auto")
	mode := colorModeDark
	theme.update(tea.BackgroundColorMsg{Color: color.White}, &mode)
	if mode != colorModeLight || theme.pending {
		t.Fatal("light background was not detected")
	}
	theme.update(tea.BackgroundColorMsg{Color: color.Black}, &mode)
	if mode != colorModeLight {
		t.Fatal("a later background report caused a feedback loop")
	}
	theme.update(uv.DarkColorSchemeEvent{}, &mode)
	if mode != colorModeDark {
		t.Fatal("system change did not override initial background detection")
	}
	theme.update(uv.LightColorSchemeEvent{}, &mode)
	if mode != colorModeLight {
		t.Fatal("system change back to light was ignored")
	}
	firstReport := newTerminalTheme("auto")
	firstReport.update(uv.DarkColorSchemeEvent{}, &mode)
	firstReport.update(tea.BackgroundColorMsg{Color: color.White}, &mode)
	if mode != colorModeDark {
		t.Fatal("delayed background reply overrides the color-scheme report")
	}
	if bg, fg := firstReport.colors(mode); bg == nil || fg == nil {
		t.Fatal("detected theme never activates its view colors")
	}
}

func TestTerminalThemeTimeoutAndLateReports(t *testing.T) {
	theme := newTerminalTheme("auto")
	mode := colorModeDark
	theme.update(terminalThemeTimeoutMsg{}, &mode)
	if theme.pending || mode != colorModeDark {
		t.Fatal("unresponsive terminal blocks rendering or changes initial mode")
	}
	theme.update(uv.LightColorSchemeEvent{}, &mode)
	if mode != colorModeLight {
		t.Fatal("slow SSH response was discarded")
	}
	theme.update(terminalThemeTimeoutMsg{}, &mode)
	if mode != colorModeLight {
		t.Fatal("late timeout overrides detected theme")
	}
}

func TestTerminalThemeWireReports(t *testing.T) {
	var decoder uv.EventDecoder
	theme := newTerminalTheme("auto")
	mode := colorModeDark
	for _, test := range []struct {
		report string
		want   colorMode
	}{{"\x1b[?997;2n", colorModeLight}, {"\x1b[?997;1n", colorModeDark}} {
		n, event := decoder.Decode([]byte(test.report))
		if n != len(test.report) || !theme.update(event, &mode) || mode != test.want {
			t.Fatalf("report %q decoded as %#v, mode=%s", test.report, event, mode)
		}
	}
	var output strings.Builder
	resetTerminalTheme(&output)
	if output.String() != ansi.ResetModeLightDark {
		t.Fatal("theme subscription is not disabled on exit")
	}
}

func TestAutomaticMonitorThemeIsSessionLocalAndManualWins(t *testing.T) {
	first := &monitorModel{theme: newTerminalTheme("auto")}
	second := &monitorModel{theme: newTerminalTheme("auto")}
	first.Update(uv.LightColorSchemeEvent{})
	if first.colorMode != colorModeLight || second.colorMode != colorModeDark {
		t.Fatal("automatic mode leaked between SSH sessions")
	}
	first.handleKey(testKey("t"))
	first.Update(uv.LightColorSchemeEvent{})
	first.Update(tea.BackgroundColorMsg{Color: color.White})
	if first.colorMode != colorModeDark || first.theme.automatic {
		t.Fatal("manual monitor choice was overridden")
	}
}

func TestAutomaticHubThemeFollowsIntoDetails(t *testing.T) {
	hub := &hubModel{
		theme:  newTerminalTheme("auto"),
		config: hubConfig{Nodes: []hubNodeConfig{{Name: "test-node"}}},
	}
	hub.Update(uv.LightColorSchemeEvent{})
	hub.openSelected()
	if hub.detail.colorMode != colorModeLight || !hub.detail.theme.automatic {
		t.Fatal("node detail did not inherit automatic Hub theme")
	}
	hub.Update(uv.DarkColorSchemeEvent{})
	if hub.colorMode != colorModeDark || hub.detail.colorMode != colorModeDark {
		t.Fatal("theme update did not reach open node detail")
	}
	hub.Update(testKey("t"))
	hub.Update(uv.DarkColorSchemeEvent{})
	if hub.colorMode != colorModeLight || hub.detail.colorMode != colorModeLight || hub.theme.automatic {
		t.Fatal("node's manual choice did not disable automatic Hub changes")
	}
	hub.Update(testKey("esc"))
	hub.Update(uv.DarkColorSchemeEvent{})
	if hub.detail != nil || hub.colorMode != colorModeLight {
		t.Fatal("returning to Hub lost the manual theme choice")
	}
}

func TestAutomaticLauncherTheme(t *testing.T) {
	t.Setenv("DEFAULT_THEME", "")
	m := newLauncherModel()
	if !m.theme.automatic {
		t.Fatal("launcher does not default to auto")
	}
	m.Update(uv.LightColorSchemeEvent{})
	if m.colorMode != colorModeLight {
		t.Fatal("launcher ignored terminal theme")
	}
	m.Update(testKey("t"))
	m.Update(uv.LightColorSchemeEvent{})
	if m.colorMode != colorModeDark || m.theme.automatic {
		t.Fatal("launcher ignored manual theme choice")
	}
}

func TestTopAcceptsAutomaticTheme(t *testing.T) {
	var output, diagnostics strings.Builder
	err := runTopCommand([]string{"--theme", "auto", "--config", filepath.Join(t.TempDir(), "missing")}, strings.NewReader(""), &output, &diagnostics)
	if err == nil || strings.Contains(err.Error(), "theme must") {
		t.Fatalf("auto did not pass theme validation: %v", err)
	}
}
