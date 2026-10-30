package main

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func cpuChartTestModel(count, width, height int) *monitorModel {
	m := &monitorModel{screen: screenMonitor, monitorPage: monitorPageCompute, width: width, height: height,
		snapshot: monitorSnapshot{CollectedAt: time.Now(), CPUCores: count, MemoryTotal: 1, DiskTotal: 1, LoadAverage: "load 1.0 · 2.0 · 3.0"}}
	for i := 0; i < count; i++ {
		m.snapshot.CPUCoreUsage = append(m.snapshot.CPUCoreUsage, cpuCoreUsage{
			CPU: fmt.Sprintf("cpu%d", i), Percent: float64(i % 101), Available: true, FrequencyMHz: 2000 + float64(i%2)*1340,
		})
	}
	return m
}

func TestCPUVerticalGlyphScale(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  string
	}{
		{0, "   ▁"}, {25, "   █"}, {50, "  ██"}, {75, " ███"}, {100, "████"}, {200, "████"}, {-1, "   ▁"}, {0.1, "   ▁"}, {math.NaN(), "   ?"}, {math.Inf(1), "   ?"},
	} {
		var got string
		for row := 0; row < 4; row++ {
			got += cpuVerticalGlyph(cpuCoreUsage{Available: true, Percent: tc.value}, row, 4)
		}
		if got != tc.want {
			t.Fatalf("%v%% glyphs %q, want %q", tc.value, got, tc.want)
		}
	}
	if got := cpuVerticalGlyph(cpuCoreUsage{}, 3, 4); got != "?" {
		t.Fatalf("missing sample shown as zero: %q", got)
	}
}

func TestCPUIdleBaseline(t *testing.T) {
	for _, height := range []int{2, 3, 4} {
		for row := 0; row < height; row++ {
			want := " "
			if row == height-1 {
				want = "▁"
			}
			if got := cpuVerticalGlyph(cpuCoreUsage{Available: true}, row, height); got != want {
				t.Fatalf("idle marker not at bottom height=%d row=%d: %q", height, row, got)
			}
		}
	}
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		m := cpuChartTestModel(128, 150, 45)
		m.colorMode = mode
		for i := range m.snapshot.CPUCoreUsage {
			m.snapshot.CPUCoreUsage[i].Percent = 0
		}
		// Keep unknown samples distinct from an idle core.
		m.snapshot.CPUCoreUsage[127].Available = false
		layout := newCPUChartLayout(128, 146, m.height, 0)
		lines := m.renderCPUChart(146)
		for row := 0; row < layout.barHeight; row++ {
			line := ansi.Strip(lines[row])
			if strings.Contains(line, "·") || (row < layout.barHeight-1 && strings.ContainsAny(line, "▁?")) {
				t.Fatalf("idle marker floats above baseline: %q", line)
			}
		}
		bottom := ansi.Strip(lines[layout.barHeight-1])
		left, right := layout.barSpan(127)
		wantIdle := layout.plotWidth - (layout.columns-1)/16 - (right - left)
		if strings.Count(bottom, "▁") != wantIdle || !strings.HasSuffix(bottom, strings.Repeat("?", right-left)) {
			t.Fatalf("idle/missing markers wrong: %q", bottom)
		}
	}
}

func TestCPUChartDefaultsAndHeight(t *testing.T) {
	for _, count := range []int{1, 18, 32, 33, 64, 128, 256, 1024} {
		for _, width := range []int{32, 60, 80, 100, 150, 240} {
			m := cpuChartTestModel(count, width, 45)
			if got := m.useCPUChart(); got != (count > 32) {
				t.Fatalf("%d CPU default = %v", count, got)
			}
			card, _ := m.metricWidgetCard(dashboardPanelCPU)
			view := m.renderCPUWidget(card, width, widgetSizeMedium)
			if count <= 32 {
				if !strings.Contains(ansi.Strip(view), "cpu0 ") || strings.Contains(view, "VERTICAL") {
					t.Fatal("small CPU layout changed")
				}
				continue
			}
			for _, mode := range []colorMode{colorModeDark, colorModeLight} {
				m.colorMode = mode
				lines := m.renderCPUChart(width - 4)
				if len(lines) > 9 {
					t.Fatalf("%d CPUs width=%d chart uses %d rows", count, width, len(lines))
				}
				for _, line := range lines {
					if lipgloss.Width(line) > width-4 {
						t.Fatalf("chart overflow %d: %q", width, line)
					}
				}
			}
			if count == 128 && width == 150 && lipgloss.Height(view) != 10 {
				t.Fatalf("128-core panel height=%d, want 10", lipgloss.Height(view))
			}
			m.monitorPage = monitorPageCustom
			if m.useCPUChart() {
				t.Fatal("custom layout unexpectedly changed")
			}
		}
	}
	m := cpuChartTestModel(128, 150, 45)
	_, placements := m.renderComputePage(usableWidth(m.width))
	if placements[1].ProcessRows < 20 {
		t.Fatalf("workload table still squeezed: %d rows", placements[1].ProcessRows)
	}
}

func TestCPUChartPaginationAndStableIDs(t *testing.T) {
	for _, width := range []int{16, 28, 56, 76, 146, 236} {
		count := 1024
		previousEnd := 0
		for selected := 0; selected < count; {
			layout := newCPUChartLayout(count, width, 45, selected)
			if layout.start != previousEnd || layout.end <= layout.start || layout.end-layout.start > layout.columns*2 {
				t.Fatalf("page lost cores: %+v", layout)
			}
			previousEnd = layout.end
			selected = layout.end
		}
		if previousEnd != count {
			t.Fatal("last CPU missing")
		}
	}
	m := cpuChartTestModel(128, 80, 24)
	// Snapshot ordering and load changes must never reorder bars or selection.
	for left, right := 0, len(m.snapshot.CPUCoreUsage)-1; left < right; left, right = left+1, right-1 {
		m.snapshot.CPUCoreUsage[left], m.snapshot.CPUCoreUsage[right] = m.snapshot.CPUCoreUsage[right], m.snapshot.CPUCoreUsage[left]
	}
	before := append([]cpuCoreUsage(nil), m.snapshot.CPUCoreUsage...)
	cores := m.cpuChartCores()
	if cores[0].CPU != "cpu0" || cores[127].CPU != "cpu127" || !reflect.DeepEqual(before, m.snapshot.CPUCoreUsage) {
		t.Fatal("numeric order wrong or published snapshot mutated")
	}
	m.cpuCoreSelected = "cpu127"
	if !strings.Contains(ansi.Strip(strings.Join(m.renderCPUChart(30), "\n")), "cpu127 · 26.0% · 3.34GHz") {
		t.Fatal("last CPU details unavailable")
	}
	m.cpuCoreSelected = "cpu99999"
	if m.cpuChartSelection(cores) != 0 {
		t.Fatal("removed CPU left an invalid selection")
	}
}

func TestCPUChartKeyboardAndMouse(t *testing.T) {
	m := cpuChartTestModel(128, 150, 45)
	m.monitorView()
	m.handleKey(testKey("f"))
	if !m.cpuCoreFocused {
		t.Fatal("keyboard cannot focus CPU")
	}
	m.handleKey(testKey("right"))
	if m.cpuCoreSelected != "cpu1" || m.monitorPage != monitorPageCompute {
		t.Fatal("CPU arrow navigated away")
	}
	m.handleKey(testKey("end"))
	if m.cpuCoreSelected != "cpu127" || !strings.Contains(ansi.Strip(m.monitorView()), "cpu127 · 26.0% · 3.34GHz") {
		t.Fatal("last CPU inaccessible")
	}
	m.handleKey(testKey("esc"))
	if m.cpuCoreFocused || m.screen != screenMonitor {
		t.Fatal("escape should return focus, not quit")
	}
	m.handleKey(testKey("v"))
	if m.useCPUChart() || !strings.Contains(ansi.Strip(m.monitorView()), "cpu0 ") {
		t.Fatal("full detail toggle failed")
	}
	m.handleKey(testKey("v"))
	m.cpuCoreSelected = "cpu0"
	m.monitorView()
	placement, screenY, ok := m.visibleWidgetPlacement(dashboardPanelCPU)
	if !ok {
		t.Fatal("CPU has no mouse region")
	}
	card, _ := m.metricWidgetCard(dashboardPanelCPU)
	y := screenY + 1 + len(m.cpuMetricHeaderLines(card, placement.Width))
	layout := newCPUChartLayout(128, placement.Width-4, m.height, 0)
	_, gap := layout.barSpan(15)
	if m.handleCPUChartClick(placement.X+2+4+gap, y) {
		t.Fatal("group separator selected a core")
	}
	start, _ := layout.barSpan(16)
	m.handleClick(placement.X+2+4+start, y)
	if m.cpuCoreSelected != "cpu16" || !m.cpuCoreFocused {
		t.Fatal("click failed to select CPU16")
	}
	m.handleKey(testKey("f"))
	m.filtering = true
	m.handleKey(testKey("v"))
	if m.filter != "v" || m.cpuCoreDetailed {
		t.Fatal("CPU hotkey intercepted filter typing")
	}
	// A core in the second band uses the same geometry after dashboard scroll.
	n := cpuChartTestModel(128, 60, 45)
	n.monitorView()
	_, sy, _ := n.visibleWidgetPlacement(dashboardPanelCPU)
	card, _ = n.metricWidgetCard(dashboardPanelCPU)
	layout = newCPUChartLayout(128, usableWidth(n.width)-4, n.height, 0)
	y = sy + 1 + len(n.cpuMetricHeaderLines(card, usableWidth(n.width))) + layout.barHeight + 1
	if !n.handleCPUChartClick(6, y) || n.cpuCoreSelected != fmt.Sprintf("cpu%d", layout.columns) {
		t.Fatal("second-band hit test failed")
	}
}

func TestCPUChartEndpointLabels(t *testing.T) {
	for _, count := range []int{1, 2, 16, 17, 18, 32, 33, 65, 128, 256} {
		m := cpuChartTestModel(count, 180, 45)
		cores := m.cpuChartCores()
		width := count + (count-1)/16
		layout := cpuChartLayout{columns: count, plotWidth: width}
		labels := layout.labels(cores)
		if lipgloss.Width(labels) != width {
			t.Fatalf("%d CPUs label width %d, want %d", count, lipgloss.Width(labels), width)
		}
		if width >= 12 && (!strings.HasPrefix(labels, "cpu0") || !strings.HasSuffix(labels, cores[count-1].CPU)) {
			t.Fatalf("%d CPUs missing endpoint labels: %q", count, labels)
		}
	}
	for _, count := range []int{33, 65, 128, 1024} {
		for _, width := range []int{16, 28, 56, 146, 176, 236} {
			m := cpuChartTestModel(count, width+4, 45)
			m.cpuCoreSelected = fmt.Sprintf("cpu%d", count-1)
			layout := newCPUChartLayout(count, width, m.height, count-1)
			lines := m.renderCPUChart(width)
			band := 0
			for start := layout.start; start < layout.end; start += layout.columns {
				end := min(layout.end, start+layout.columns)
				label := ansi.Strip(lines[band*(layout.barHeight+1)+layout.barHeight])
				if end-start >= 12 && !strings.Contains(label, fmt.Sprintf("cpu%d", end-1)) {
					t.Fatalf("last label missing count=%d width=%d: %q", count, width, label)
				}
				band++
			}
		}
	}
	// Labels refer to actual CPU IDs, not positions in the sample array.
	cores := cpuChartTestModel(33, 80, 45).cpuChartCores()
	cores[32].CPU = "cpu999"
	layout := cpuChartLayout{columns: len(cores), plotWidth: 35}
	if !strings.HasSuffix(layout.labels(cores), "cpu999") {
		t.Fatal("sparse final CPU ID was replaced by its array index")
	}
}

func TestCPUChartSideHistory(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		m := cpuChartTestModel(128, 180, 45)
		m.colorMode = mode
		for i := 0; i < 60; i++ {
			m.cpuHistory = append(m.cpuHistory, float64(i)*100/59)
		}
		card, _ := m.metricWidgetCard(dashboardPanelCPU)
		if strings.Contains(strings.Join(m.cpuMetricHeaderLines(card, 180), "\n"), "HIST") {
			t.Fatal("vertical chart still has history above the bars")
		}
		lines := m.renderCPUChart(176)
		layout := newCPUChartLayout(128, 176, 45, 0)
		if len(lines) != layout.barHeight+2 {
			t.Fatal("side history added panel height")
		}
		occupied := 4 + layout.plotWidth
		if strings.Index(ansi.Strip(lines[0]), "HIST") <= occupied || !strings.Contains(ansi.Strip(lines[layout.barHeight]), "cpu127") {
			t.Fatal("history not separated from cores or endpoint lost")
		}
		for _, line := range lines {
			if lipgloss.Width(line) > 176 {
				t.Fatal("side history overflow")
			}
		}
		if strings.Contains(strings.Join(m.renderCPUChart(146), "\n"), "HIST") {
			t.Fatal("history squeezed into insufficient spare width")
		}
		m.monitorView()
		placement, y, _ := m.visibleWidgetPlacement(dashboardPanelCPU)
		y += 1 + len(m.cpuMetricHeaderLines(card, placement.Width))
		if m.handleCPUChartClick(placement.X+2+occupied+3, y) {
			t.Fatal("history click selected a core")
		}
		if !m.handleCPUChartClick(placement.X+2+occupied-1, y) || m.cpuCoreSelected != "cpu127" {
			t.Fatal("endpoint bar hit testing changed")
		}
		m.cpuCoreDetailed = true
		if !strings.Contains(strings.Join(m.cpuMetricLines(card, 180), "\n"), "HIST · CPU %") {
			t.Fatal("full detail lost side history")
		}
	}
	// Oldest samples are on the left; newest on the right, including bucketed
	// history. Missing/non-finite samples must not masquerade as a busy CPU.
	m := cpuChartTestModel(128, 180, 45)
	m.cpuHistory = append(make([]float64, 30), make([]float64, 30)...)
	for i := 30; i < 60; i++ {
		m.cpuHistory[i] = 100
	}
	history := m.cpuChartHistory(14, 4)
	if got := ansi.Strip(history[1]); got != "│       ██████" {
		t.Fatalf("history time order or scale wrong: %q", got)
	}
	m.cpuHistory = []float64{math.NaN(), math.Inf(1)}
	for _, line := range m.cpuChartHistory(14, 4)[1:4] {
		if strings.Contains(ansi.Strip(line), "█") {
			t.Fatal("invalid history rendered as load")
		}
	}
	m.cpuHistory = nil
	if got := ansi.Strip(m.cpuChartHistory(14, 4)[3]); !strings.Contains(got, "awaiting") {
		t.Fatalf("warm-up history is not explicit: %q", got)
	}
}

func TestCPUChartAdaptivePlotWidth(t *testing.T) {
	for _, count := range []int{33, 40, 64, 128, 256} {
		for _, width := range []int{36, 56, 96, 146, 196, 316} {
			m := cpuChartTestModel(count, width+4, 45)
			layout := newCPUChartLayout(count, width, 45, 0)
			lastEnd := 0
			for index := 0; index < layout.columns; index++ {
				left, right := layout.barSpan(index)
				gap := 0
				if index > 0 && index%16 == 0 {
					gap = 1
				}
				if left != lastEnd+gap || right <= left {
					t.Fatalf("count=%d width=%d: invalid bar %d span %d–%d", count, width, index, left, right)
				}
				lastEnd = right
			}
			if lastEnd != layout.plotWidth || layout.historyWidth > 40 {
				t.Fatalf("count=%d width=%d: unbalanced layout %+v", count, width, layout)
			}
			occupied := 4 + layout.plotWidth
			if layout.historyWidth > 0 {
				occupied += 3 + layout.historyWidth
			}
			if occupied != width {
				t.Fatalf("plot left %d columns unused", width-occupied)
			}
			for _, mode := range []colorMode{colorModeDark, colorModeLight} {
				m.colorMode = mode
				lines := m.renderCPUChart(width)
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatalf("%s count=%d width=%d: chart overflow", mode, count, width)
					}
				}
				if layout.historyWidth > 0 {
					plain := ansi.Strip(lines[0])
					index := strings.Index(plain, "HIST")
					if index < 0 || lipgloss.Width(plain[:index]) != width-layout.historyWidth+2 {
						t.Fatalf("history sidebar is not anchored to the right: %s", plain)
					}
				}
			}
		}
	}
	m := cpuChartTestModel(40, 150, 45)
	layout := newCPUChartLayout(40, 146, 45, 0)
	if layout.columns != 40 || layout.start != 0 || layout.end != 40 || layout.plotWidth < 90 {
		t.Fatalf("40-core plot remains compressed: %+v", layout)
	}
	for index := 0; index < 40; index++ {
		left, right := layout.barSpan(index)
		if right-left < 2 {
			t.Fatalf("CPU%d was not widened", index)
		}
	}
	lines := m.renderCPUChart(146)
	if len(lines) != 6 || !strings.Contains(ansi.Strip(lines[4]), "cpu39") || strings.Contains(lines[5], "f focus") {
		t.Fatal("40-core panel grew, lost its endpoint, or repeated its shortcuts")
	}
}

func TestCPUChartExpandedBarHitTesting(t *testing.T) {
	for _, width := range []int{80, 150, 240} {
		m := cpuChartTestModel(40, width, 45)
		m.monitorView()
		placement, screenY, ok := m.visibleWidgetPlacement(dashboardPanelCPU)
		if !ok {
			t.Fatal("missing CPU placement")
		}
		card, _ := m.metricWidgetCard(dashboardPanelCPU)
		y := screenY + 1 + len(m.cpuMetricHeaderLines(card, placement.Width))
		layout := newCPUChartLayout(40, placement.Width-4, m.height, 0)
		origin := placement.X + 2 + 4
		for index := 0; index < 40; index++ {
			left, right := layout.barSpan(index)
			for x := left; x < right; x++ {
				if !m.handleCPUChartClick(origin+x, y) || m.cpuCoreSelected != fmt.Sprintf("cpu%d", index) {
					t.Fatalf("width=%d: CPU%d column %d selected %s", width, index, x, m.cpuCoreSelected)
				}
			}
			if index > 0 && index%16 == 0 && m.handleCPUChartClick(origin+left-1, y) {
				t.Fatal("expanded group separator selected a CPU")
			}
		}
		for x := layout.plotWidth; x < placement.Width-8; x++ {
			if m.handleCPUChartClick(origin+x, y) {
				t.Fatal("history sidebar or gap selected a CPU")
			}
		}
	}
}
