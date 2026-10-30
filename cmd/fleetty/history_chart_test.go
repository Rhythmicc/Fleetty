package main

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHistoryChartBoundsAndEmptyState(t *testing.T) {
	for _, history := range [][]float64{nil, {0}, {0, 50, 100}, {math.NaN(), math.Inf(1), math.Inf(-1)}, {-10, 200}} {
		for _, width := range []int{1, 2, 4, 8, 14, 18, 24, 40, 80, 160} {
			for _, height := range []int{1, 2, 3, 4} {
				lines := renderHistoryChart(history, width, height, "CPU %", 100, cpuTitleStyle)
				if len(lines) != height+2 {
					t.Fatalf("height=%d: got %d rows", height, len(lines))
				}
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatalf("width=%d overflow: %q", width, ansi.Strip(line))
					}
				}
				if width >= 18 && (!strings.Contains(ansi.Strip(lines[0]), "HIST · CPU %") || !strings.Contains(ansi.Strip(lines[len(lines)-1]), "→ now")) {
					t.Fatal("history lost metric/units/time direction")
				}
			}
		}
	}
	for _, ceiling := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		renderHistoryChart([]float64{0, 1, 2}, 20, 3, "RX B/s", ceiling, networkRXStyle)
	}
	view := ansi.Strip(strings.Join(renderHistoryChart(nil, 24, 3, "CPU %", 100, cpuTitleStyle), "\n"))
	if !strings.Contains(view, "awaiting sample") || strings.ContainsAny(view, "▁█") {
		t.Fatalf("empty history fabricated measurements: %s", view)
	}
}

func TestHistoryChartTimeWindowAndBaseline(t *testing.T) {
	history := make([]float64, 60)
	for i := 0; i < 30; i++ {
		history[i] = 100
	}
	before := append([]float64(nil), history...)
	lines := renderHistoryChart(history, 14, 3, "CPU %", 100, cpuTitleStyle)
	if got := ansi.Strip(lines[1]); got != "│ ██████      " {
		t.Fatalf("downsample did not retain the old half: %q", got)
	}
	if got := ansi.Strip(lines[3]); got != "│ ██████▁▁▁▁▁▁" {
		t.Fatalf("zero history not at baseline: %q", got)
	}
	if !reflect.DeepEqual(history, before) {
		t.Fatal("rendering modified recorded samples")
	}
	lines = renderHistoryChart([]float64{100}, 14, 3, "CPU %", 100, cpuTitleStyle)
	if got := ansi.Strip(lines[1]); got != "│            █" {
		t.Fatalf("newest sample is not on the right: %q", got)
	}
	// Missing warm-up columns stay blank, not zeros or floating dots.
	if strings.Contains(ansi.Strip(strings.Join(lines[1:4], "\n")), "·") {
		t.Fatal("missing history rendered as dots")
	}
}

func TestHistoryChartAllSurfaces(t *testing.T) {
	for _, width := range []int{28, 34, 40, 76, 120, 176} {
		lines := renderNetworkHistories([]float64{100}, []float64{50}, width)
		wantRows := 6
		if width < 34 {
			wantRows = 11
		}
		if len(lines) != wantRows {
			t.Fatalf("network width=%d: %d rows", width, len(lines))
		}
		plain := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"HIST · RX B/s", "HIST · TX B/s", "old", "→ now", "PEAK"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("network width=%d lost %s", width, want)
			}
		}
		for _, line := range lines {
			if lipgloss.Width(line) > width {
				t.Fatalf("network chart overflow width=%d", width)
			}
		}
	}
	if historyMax([]float64{math.Inf(1), math.NaN(), -1, 0}) != 0 {
		t.Fatal("invalid history set the network scale")
	}
	if view := ansi.Strip(strings.Join(renderNetworkHistories(nil, nil, 60), "\n")); !strings.Contains(view, "PEAK ↓ 0 B/s ↑ 0 B/s") {
		t.Fatal("empty history reported nonzero peak")
	}
	for _, count := range []int{18, 128} {
		m := cpuChartTestModel(count, 180, 50)
		m.cpuHistory = []float64{10, 20, 30}
		m.hourCPUHistory = []float64{20, 30, 40}
		m.networkRXHistory, m.networkTXHistory = []float64{10, 20}, []float64{30, 40}
		card, _ := m.metricWidgetCard(dashboardPanelCPU)
		for _, view := range []string{
			m.renderCPUWidget(card, 180, widgetSizeMedium),
			m.renderCPUWidget(card, 180, widgetSizeSmall),
			renderPagePanel(90, m.hostHealthPanelSpec(90, true)),
			renderPagePanel(180, m.networkTrafficSummaryPanelSpec(180)),
			m.nasNetworkPanel(180),
		} {
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "HIST ·") || !strings.Contains(plain, "→ now") {
				t.Fatalf("surface lacks unified history: %s", plain)
			}
		}
	}
}
