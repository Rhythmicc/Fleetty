package main

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func themeForeground(t *testing.T, style lipgloss.Style, mode colorMode) color.Color {
	t.Helper()
	rendered := style.Render("sample")
	if mode == colorModeLight {
		rendered = applyLightTheme(rendered)
	}
	sequence := regexp.MustCompile(`38;2;[0-9]+;[0-9]+;[0-9]+`).FindString(rendered)
	var r, g, b uint8
	if _, err := fmt.Sscanf(sequence, "38;2;%d;%d;%d", &r, &g, &b); err != nil {
		t.Fatalf("missing RGB foreground in %q: %v", rendered, err)
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func themeContrast(a, b color.Color) float64 {
	luminance := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		linear := func(v uint32) float64 {
			x := float64(v) / 65535
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
	}
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

func TestThemeVisualHierarchy(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		for _, indexed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/256=%t", mode, indexed), func(t *testing.T) {
				convert := func(c color.Color) color.Color {
					if indexed {
						return ansi.Convert256(c)
					}
					return c
				}
				background, _ := viewColors(mode)
				background = convert(background)
				contrast := func(style lipgloss.Style) float64 {
					return themeContrast(convert(themeForeground(t, style, mode)), background)
				}
				border := contrast(lipgloss.NewStyle().Foreground(colorPanelBorder))
				track := contrast(barTrackStyle)
				selected := contrast(accentStyle)
				if track >= border || track >= 2 || border >= 3 {
					t.Fatalf("decorative elements dominate: track %.2f, border %.2f", track, border)
				}
				if selected < 3 || selected/border < 3 {
					t.Fatalf("selection lost emphasis: selected %.2f, border %.2f", selected, border)
				}
				for name, style := range map[string]lipgloss.Style{
					"values": hubMetricStyle, "labels": dimStyle,
					"danger": dangerStyle, "warning": warningStyle,
				} {
					if got := contrast(style); got < 4.5 {
						t.Errorf("%s text contrast %.2f is below 4.5", name, got)
					}
				}
				for _, load := range []float64{0, 10, 30, 60, 80, 100} {
					_, style := gpuLoadStatus(load)
					if got := contrast(style); got < 3 || got < 2*track {
						t.Errorf("load %.0f fill %.2f lost emphasis over track %.2f", load, got, track)
					}
				}
			})
		}
	}
}

func TestLoadBarsUseIndependentTrackStyle(t *testing.T) {
	for name, render := range map[string]func(float64, int) string{
		"load": bar, "battery": batteryBar, "GPU": shortGPUBar,
	} {
		for _, mode := range []colorMode{colorModeDark, colorModeLight} {
			for _, load := range []float64{0, 50, 100} {
				rendered := render(load, 20)
				track := barTrackStyle.Render(strings.Repeat("░", 20-int(load/5)))
				if mode == colorModeLight {
					rendered, track = applyLightTheme(rendered), applyLightTheme(track)
				}
				if load < 100 && !strings.Contains(rendered, track) {
					t.Errorf("%s/%s: empty track lost its independent color", name, mode)
				}
				plain := ansi.Strip(rendered)
				if lipgloss.Width(plain) != 20 || strings.Count(plain, "█") != int(load/5) {
					t.Errorf("%s/%s: changed load geometry: %s", name, mode, plain)
				}
			}
		}
	}
}

func TestHubSelectionHasNonColorCueInBothThemes(t *testing.T) {
	for _, width := range []int{36, 52, 80} {
		for _, state := range []string{"live", "offline", "waiting"} {
			m := compactTestHub(160, 40)
			switch state {
			case "offline":
				m.states[0] = hubNodeState{Error: "connection refused"}
			case "waiting":
				m.states[0] = hubNodeState{}
			}
			for _, mode := range []colorMode{colorModeDark, colorModeLight} {
				for _, selected := range []bool{true, false} {
					m.cursor = 1
					if selected {
						m.cursor = 0
					}
					card := m.renderNodeCard(0, width)
					if mode == colorModeLight {
						card = applyLightTheme(card)
					}
					if strings.Contains(ansi.Strip(card), "▸ A100") != selected {
						t.Errorf("%s/%s: incorrect selection marker", mode, state)
					}
					if lipgloss.Width(card) != width || lipgloss.Height(card) != hubCardHeight {
						t.Errorf("%s/%s: selection changed card size at width %d", mode, state, width)
					}
				}
			}
		}
	}
}

func TestGhosttyPromptPaletteContrast(t *testing.T) {
	for _, mode := range []string{"Dark", "Light"} {
		t.Run(mode, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "themes", "ghostty", "Fleetty "+mode))
			if err != nil {
				t.Fatal(err)
			}
			palette := map[int]color.Color{}
			for _, line := range strings.Split(string(data), "\n") {
				var index int
				var hex string
				if _, err := fmt.Sscanf(line, "palette = %d=%s", &index, &hex); err == nil {
					palette[index] = lipgloss.Color(hex)
				}
			}
			for _, index := range []int{0, 4, 7, 12} {
				if palette[index] == nil {
					t.Fatalf("missing prompt palette index %d", index)
				}
			}
			if got := themeContrast(palette[0], palette[7]); got < 4.5 {
				t.Errorf("OS icon contrast %.2f is below 4.5", got)
			}
			textColors := []ansi.IndexedColor{254, 255}
			if mode == "Dark" {
				textColors = append(textColors, 250) // Shortened path segments.
			}
			for _, foreground := range textColors {
				if got := themeContrast(palette[4], foreground); got < 4.5 {
					t.Errorf("directory text %d contrast %.2f is below 4.5", foreground, got)
				}
			}
			if mode == "Dark" && palette[4] == palette[12] {
				t.Error("prompt background must not reuse the bright-blue foreground")
			}
		})
	}
}
