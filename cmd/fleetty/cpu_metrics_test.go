package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shirou/gopsutil/v4/cpu"
)

func TestCPUCoreDeltasAndAggregate(t *testing.T) {
	c := &metricsCollector{}
	first := []cpu.TimesStat{
		{CPU: "cpu0", User: 10, Idle: 90, Guest: 5},
		{CPU: "cpu2", User: 20, Idle: 80},
	}
	var warmup monitorSnapshot
	c.updateCPU(&warmup, first)
	if !c.haveCPU || warmup.CPUCoreUsage[0].Available {
		t.Fatalf("invalid warmup: %+v", warmup.CPUCoreUsage)
	}
	// Reorder and use a sparse CPU ID. Guest is already part of User.
	var snapshot monitorSnapshot
	c.updateCPU(&snapshot, []cpu.TimesStat{
		{CPU: "cpu2", User: 20, Idle: 130, Iowait: 50},
		{CPU: "cpu0", User: 60, Idle: 140, Guest: 45},
	})
	if snapshot.CPUPercent != 25 || snapshot.CPUCores != 2 {
		t.Fatalf("aggregate = %g, count = %d", snapshot.CPUPercent, snapshot.CPUCores)
	}
	for i, want := range []float64{0, 50} {
		if got := snapshot.CPUCoreUsage[i]; !got.Available || got.Percent != want {
			t.Fatalf("core %d = %+v, want %g", i, got, want)
		}
	}
	if warmup.CPUCoreUsage[0].Available {
		t.Fatal("previous published snapshot was mutated")
	}
}

func TestCPUCoreHotplugAndReset(t *testing.T) {
	c := &metricsCollector{}
	var s monitorSnapshot
	c.updateCPU(&s, []cpu.TimesStat{{CPU: "cpu0", User: 50, Idle: 50}, {CPU: "cpu1", Idle: 100}})
	c.updateCPU(&s, []cpu.TimesStat{{CPU: "cpu0", User: 1, Idle: 1}, {CPU: "cpu3", Idle: 200}})
	if len(c.previousCPU) != 2 || s.CPUCoreUsage[0].Available || s.CPUCoreUsage[1].Available {
		t.Fatalf("reset/new CPUs must await sample: %+v", s.CPUCoreUsage)
	}
	if _, exists := c.previousCPU["cpu1"]; exists {
		t.Fatal("offline CPU baseline retained")
	}
	c.updateCPU(&s, []cpu.TimesStat{{CPU: "cpu3", User: 10, Idle: 200}, {CPU: "cpu0", User: 1, Idle: 11}})
	if s.CPUCoreUsage[0].Percent != 100 || s.CPUCoreUsage[1].Percent != 0 || s.CPUPercent != 50 {
		t.Fatalf("recovered sample = %+v, total %g", s.CPUCoreUsage, s.CPUPercent)
	}
	c.updateCPU(&s, []cpu.TimesStat{{CPU: "cpu1", Idle: 101}})
	if s.CPUCoreUsage[0].Available {
		t.Fatal("returning CPU reused an obsolete baseline")
	}
}

func TestCPUInvalidDeltas(t *testing.T) {
	for _, sample := range []cpu.TimesStat{
		{User: 10, Idle: 10}, // no elapsed ticks
		{User: 5, Idle: 10},  // counter reset
		{User: 20, Idle: 5},  // idle regression
		{User: 5, Idle: 30},  // busy regression
		{User: math.Inf(1), Idle: 10},
		{User: math.NaN(), Idle: 10},
	} {
		c := &metricsCollector{previousCPU: map[string]cpuCounters{"cpu0": {total: 20, idle: 10}}}
		var s monitorSnapshot
		sample.CPU = "cpu0"
		c.updateCPU(&s, []cpu.TimesStat{sample})
		if s.CPUCoreUsage[0].Available || s.CPUPercent != 0 {
			t.Fatalf("invalid sample accepted: %+v", sample)
		}
	}
}

func TestCPUCoreSnapshotTransportAndExports(t *testing.T) {
	snapshot := monitorSnapshot{NodeName: "test", CPUCoreUsage: []cpuCoreUsage{
		{CPU: "cpu0", Percent: 87.5, Available: true},
		{CPU: "cpu1"},
		{CPU: "cpu2", Available: true},
	}}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded monitorSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.CPUCoreUsage) != 3 || decoded.CPUCoreUsage[0] != snapshot.CPUCoreUsage[0] {
		t.Fatalf("RPC round trip lost per-core data: %+v", decoded.CPUCoreUsage)
	}
	exported, err := json.Marshal(exportSnapshot(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), `"cpu_core_usage":[{"cpu":"cpu0","percent":87.5,"available":true}`) {
		t.Fatalf("JSON export missing core data: %s", exported)
	}
	metrics := renderPrometheusMetrics(decoded)
	for _, want := range []string{
		`fleetty_cpu_core_percent{node="test",cpu="cpu0"} 87.5`,
		`fleetty_cpu_core_percent{node="test",cpu="cpu2"} 0`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing metric %q", want)
		}
	}
	if strings.Contains(metrics, `cpu="cpu1"`) || strings.Count(metrics, "# TYPE fleetty_cpu_core_percent ") != 1 {
		t.Fatal("invalid core exported or repeated metric header")
	}
}

func TestCPUCoreGridResponsiveAndComplete(t *testing.T) {
	for _, count := range []int{1, 16, 128, 256} {
		cores := make([]cpuCoreUsage, count)
		for i := range cores {
			cores[i] = cpuCoreUsage{CPU: fmt.Sprintf("cpu%d", i), Percent: float64(i % 101), Available: i != 0}
		}
		for _, width := range []int{16, 36, 56, 96, 156, 236} {
			lines := renderCPUCoreLines(cores, width)
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("%d CPUs width %d overflow: %q", count, width, line)
				}
			}
			fields := strings.Fields(ansi.Strip(strings.Join(lines, "\n")))
			seen := map[string]int{}
			for _, field := range fields {
				seen[field]++
			}
			for _, core := range cores {
				if seen[core.CPU] != 1 {
					t.Fatalf("%d CPUs width %d: %s displayed %d times", count, width, core.CPU, seen[core.CPU])
				}
			}
			if !strings.Contains(strings.Join(fields, " "), "--") {
				t.Fatal("unavailable CPU displayed as a measurement")
			}
		}
	}
}

func TestComputeCPUCoreScrollAndLargeWidget(t *testing.T) {
	m := &monitorModel{screen: screenMonitor, monitorPage: monitorPageCompute, width: 80, height: 24,
		snapshot: monitorSnapshot{CollectedAt: time.Now(), CPUCores: 256, MemoryTotal: 1, DiskTotal: 1}}
	for i := 0; i < 256; i++ {
		m.snapshot.CPUCoreUsage = append(m.snapshot.CPUCoreUsage, cpuCoreUsage{CPU: fmt.Sprintf("cpu%d", i), Percent: 50, Available: true})
	}
	view := m.monitorView()
	if !strings.Contains(ansi.Strip(view), "PER CORE") || m.dashboardContent <= m.dashboardViewport {
		t.Fatal("Compute page missing scrollable per-core CPU widget")
	}
	for i := 0; i < 100 && !strings.Contains(ansi.Strip(view), "cpu255"); i++ {
		m.handleKey(testKey("pgdown"))
		view = m.monitorView()
	}
	if !strings.Contains(ansi.Strip(view), "cpu255") {
		t.Fatal("last logical CPU not reachable by scrolling")
	}
	if lipgloss.Height(view) != 24 {
		t.Fatalf("viewport height = %d", lipgloss.Height(view))
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 80 {
			t.Fatal("viewport exceeds terminal width")
		}
	}
	card, _ := m.metricWidgetCard(dashboardPanelCPU)
	large := m.renderLargeSystemMetricWidget(dashboardPanelCPU, card, 80)
	if !strings.Contains(large, "cpu255") || !strings.Contains(large, "TOP CPU PROCESSES") {
		t.Fatal("large CPU widget must retain cores and process attribution")
	}
}

func TestCPUWidgetFormsShareOnePanel(t *testing.T) {
	m := &monitorModel{snapshot: monitorSnapshot{
		CPUPercent: 42, CPUCores: 2, LoadAverage: "load 1.0 · 0.5 · 0.2",
		CPUCoreUsage: []cpuCoreUsage{{CPU: "cpu0", Percent: 84, Available: true}, {CPU: "cpu1", Available: true}},
	}}
	for _, size := range []widgetSize{widgetSizeSmall, widgetSizeMedium, widgetSizeLarge} {
		widget, _ := m.renderDashboardWidget(dashboardPanelPreference{ID: dashboardPanelCPU, Size: size}, 80)
		plain := ansi.Strip(widget)
		if strings.Count(plain, "╭─ CPU ") != 1 || strings.Contains(plain, "CPU CORES") {
			t.Fatalf("%s must render a single CPU panel:\n%s", size, plain)
		}
		if !strings.Contains(plain, "42.0%") || !strings.Contains(plain, "load 1.0") {
			t.Fatalf("%s lost CPU summary", size)
		}
		if strings.Contains(plain, "cpu0") != (size != widgetSizeSmall) {
			t.Fatalf("%s has incorrect per-core visibility", size)
		}
		if strings.Contains(plain, "TOP CPU PROCESSES") != (size == widgetSizeLarge) {
			t.Fatalf("%s has incorrect process attribution visibility", size)
		}
	}
	for _, width := range []int{60, 100, 160} {
		page, _ := m.renderComputePage(width)
		plain := ansi.Strip(page)
		if strings.Count(plain, "╭─ CPU ") != 1 || strings.Contains(plain, "CPU CORES") || !strings.Contains(plain, "cpu1") {
			t.Fatalf("Compute width %d must integrate cores in one CPU panel:\n%s", width, plain)
		}
	}
}

func TestComputePanelOrder(t *testing.T) {
	for _, withGPU := range []bool{false, true} {
		for _, width := range []int{60, 100, 160} {
			m := &monitorModel{height: 40, snapshot: monitorSnapshot{MemoryTotal: 1}}
			if withGPU {
				m.snapshot.GPUs = []gpuInfo{{Index: 0, Name: "Apple GPU", Platform: "apple"}}
			}
			page, placements := m.renderComputePage(width)
			plain := ansi.Strip(page)
			titles := []string{"╭─ CPU ", "╭─ MEMORY ", "╭─ COMPUTE WORKLOADS "}
			if withGPU {
				titles = []string{"╭─ CPU ", "╭─ GPU ", "╭─ MEMORY ", "╭─ COMPUTE WORKLOADS "}
			}
			previous := -1
			for _, title := range titles {
				position := strings.Index(plain, title)
				if position <= previous {
					t.Fatalf("GPU=%v width=%d: missing or out-of-order %q:\n%s", withGPU, width, title, plain)
				}
				previous = position
			}
			if len(placements) != 1 || !strings.Contains(strings.Split(plain, "\n")[placements[0].Y], "COMPUTE WORKLOADS") {
				t.Fatal("process placement no longer matches the rendered panel")
			}
		}
	}
}

func TestCollectCPUOnHost(t *testing.T) {
	c := &metricsCollector{}
	var first, second monitorSnapshot
	if err := c.collectCPU(&first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := c.collectCPU(&second); err != nil {
		t.Fatal(err)
	}
	valid := 0
	for _, core := range second.CPUCoreUsage {
		if core.Available {
			valid++
			if core.Percent < 0 || core.Percent > 100 {
				t.Fatalf("invalid utilization: %+v", core)
			}
		}
	}
	if valid == 0 || second.CPUCores != len(second.CPUCoreUsage) {
		t.Fatalf("no valid logical CPU readings: %+v", second.CPUCoreUsage)
	}
	t.Logf("sampled %d logical CPUs, %d valid, overall %.1f%%", second.CPUCores, valid, second.CPUPercent)
}
