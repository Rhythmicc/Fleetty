package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestAdminAuthenticationAndSelection(t *testing.T) {
	admin := &adminController{
		password: "correct horse battery staple",
		actions: []adminAction{
			{label: "Restart monitor service", command: "true"},
			{label: "Reboot machine", command: "true"},
		},
	}
	m := &monitorModel{admin: admin, screen: screenMonitor}
	m.handleKey(testKey("m"))
	if m.screen != screenPassword {
		t.Fatalf("screen after m = %v, want password", m.screen)
	}
	for _, r := range "correct horse battery staple" {
		m.handleKey(testKey(string(r)))
	}
	m.handleKey(testKey("enter"))
	if m.screen != screenAdmin {
		t.Fatalf("screen after password = %v, want admin", m.screen)
	}
	m.handleKey(testKey("2"))
	if m.screen != screenConfirm || m.selectedAction == nil || m.selectedAction.label != "Reboot machine" {
		t.Fatalf("selection = screen %v action %#v", m.screen, m.selectedAction)
	}
}

func TestPasswordPasteAndLegacyKeyInput(t *testing.T) {
	m := &monitorModel{admin: &adminController{password: "p@ss word"}, screen: screenPassword}
	_, _ = m.Update(tea.PasteMsg{Content: "p@ss word\n"})
	if m.password != "p@ss word" {
		t.Fatalf("pasted password = %q", m.password)
	}
	m.password = ""
	m.handleKey(tea.KeyPressMsg{Code: 'x'}) // Legacy terminals can omit Key.Text.
	if m.password != "x" {
		t.Fatalf("legacy key input = %q", m.password)
	}
}

func TestThemeSelectionIsSessionLocal(t *testing.T) {
	first := &monitorModel{screen: screenMonitor, colorMode: colorModeDark}
	second := &monitorModel{screen: screenMonitor, colorMode: colorModeDark}

	first.handleKey(testKey("t"))
	if first.colorMode != colorModeLight {
		t.Fatalf("first session theme = %s, want light", first.colorMode)
	}
	if second.colorMode != colorModeDark {
		t.Fatalf("second session theme changed to %s", second.colorMode)
	}

	lightView := first.View()
	darkView := second.View()
	lightR, lightG, lightB, _ := lightView.BackgroundColor.RGBA()
	darkR, darkG, darkB, _ := darkView.BackgroundColor.RGBA()
	if lightR == darkR && lightG == darkG && lightB == darkB {
		t.Fatal("light and dark sessions should use different terminal backgrounds")
	}
	if lightView.Content == darkView.Content {
		t.Fatal("light theme should remap the session's rendered palette")
	}

	filtering := &monitorModel{screen: screenAdmin, filtering: true, colorMode: colorModeDark}
	filtering.handleKey(testKey("T"))
	if filtering.colorMode != colorModeDark || filtering.filter != "T" {
		t.Fatalf("filter input should receive T without changing theme: mode=%s filter=%q", filtering.colorMode, filtering.filter)
	}
}

func TestProcessHeaderHasHighContrastInBothThemes(t *testing.T) {
	dark := processTableHeader("PID  USER  CPU  COMMAND", 50)
	if !strings.Contains(dark, "38;2;16;19;26") || !strings.Contains(dark, "48;2;159;195;255") {
		t.Fatalf("dark process header should use dark text on a bright background: %q", dark)
	}
	light := applyLightTheme(dark)
	if !strings.Contains(light, "38;2;248;250;252") || !strings.Contains(light, "48;2;49;93;145") {
		t.Fatalf("light process header should use light text on a dark background: %q", light)
	}
}

func TestDisabledManagementMode(t *testing.T) {
	m := &monitorModel{admin: &adminController{}, screen: screenMonitor}
	m.handleKey(testKey("m"))
	if m.screen != screenMonitor || m.status == "" {
		t.Fatalf("disabled management should remain on monitor, got screen=%v status=%q", m.screen, m.status)
	}
}

func TestManagementCardsCanBeClicked(t *testing.T) {
	m := &monitorModel{
		admin: &adminController{actions: []adminAction{
			{label: "Restart monitor service", command: "true"},
			{label: "Reboot machine", command: "true"},
		}},
		screen: screenAdmin,
	}
	firstWidth := compactButtonWidth("1", "Restart monitor service")
	m.handleClick(firstWidth+2, 2)
	if m.screen != screenConfirm || m.selectedAction == nil || m.selectedAction.label != "Reboot machine" {
		t.Fatalf("click should select reboot, got screen=%v action=%#v", m.screen, m.selectedAction)
	}
}

func TestProcessFilterAndMouseSelection(t *testing.T) {
	m := &monitorModel{
		admin:  &adminController{},
		screen: screenAdmin,
		snapshot: monitorSnapshot{Processes: []processInfo{
			{PID: 100, User: "root", Command: "python trainer.py"},
			{PID: 200, User: "alice", Command: "sshd"},
		}},
	}
	m.handleKey(testKey("/"))
	_, _ = m.Update(tea.PasteMsg{Content: "train"})
	m.handleKey(testKey("enter"))
	filtered := m.filteredProcesses()
	if len(filtered) != 1 || filtered[0].PID != 100 {
		t.Fatalf("filtered processes = %#v", filtered)
	}
	m.handleClick(4, adminProcessRowStart)
	if m.screen != screenProcessDetail || m.selectedProcess == nil || m.selectedProcess.PID != 100 {
		t.Fatalf("process selection = screen %v process %#v", m.screen, m.selectedProcess)
	}
}

func TestProcessSelectionScrollsWithTerminalHeight(t *testing.T) {
	processes := make([]processInfo, 12)
	for i := range processes {
		processes[i] = processInfo{PID: 100 + i, Command: "worker"}
	}
	m := &monitorModel{height: 12, screen: screenAdmin, snapshot: monitorSnapshot{Processes: processes}}
	for range 6 {
		m.handleKey(testKey("down"))
	}
	if m.cursor != 6 || m.processOffset == 0 {
		t.Fatalf("cursor=%d offset=%d, want a scrolled selection", m.cursor, m.processOffset)
	}
}

func TestProtectedProcessesCannotBeTerminated(t *testing.T) {
	if canTerminatePID(1) {
		t.Fatal("PID 1 must be protected")
	}
	if canTerminatePID(os.Getpid()) {
		t.Fatal("monitor process must be protected")
	}
	if got := sanitizeTerminalText("safe\x1b[2J\ntext"); got != "safe[2Jtext" {
		t.Fatalf("sanitized text = %q", got)
	}
}

func TestProcessStartTicksParserHandlesSpacesInName(t *testing.T) {
	stat := []byte("123 (worker process) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242")
	got, err := parseProcessStartTicks(stat)
	if err != nil {
		t.Fatal(err)
	}
	if got != 424242 {
		t.Fatalf("start ticks = %d", got)
	}
}

func TestFormattingHelpers(t *testing.T) {
	if got := bytes(1024 * 1024 * 1536); got != "1.5 GiB" {
		t.Fatalf("bytes = %q", got)
	}
	if got := elapsed(3661); got != "1h01m" {
		t.Fatalf("elapsed = %q", got)
	}
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := trimLastRune("a界"); got != "a" {
		t.Fatalf("trimLastRune = %q", got)
	}
	if got := counterDelta(150, 100); got != 50 {
		t.Fatalf("counter delta = %d", got)
	}
	if got := counterDelta(10, 100); got != 0 {
		t.Fatalf("reset counter delta = %d, want 0", got)
	}
}

func TestDashboardLayoutUsesTerminalSize(t *testing.T) {
	wide := newDashboardLayout(161, 50, 2, false)
	if wide.width != 161 || wide.metricCols != 4 {
		t.Fatalf("wide layout = %#v, want full width and four metric columns", wide)
	}
	if wide.processRows < 25 {
		t.Fatalf("wide terminal should use its height for process rows, got %d", wide.processRows)
	}
	narrow := newDashboardLayout(70, 24, 1, false)
	if narrow.metricCols != 2 || !narrow.compactGPU {
		t.Fatalf("narrow layout = %#v, want two compact metric columns and compact GPU", narrow)
	}
}

func TestMonitorViewFitsCommonTerminalSizes(t *testing.T) {
	processes := make([]processInfo, 40)
	for i := range processes {
		processes[i] = processInfo{PID: 1000 + i, User: "worker", State: "S", CPU: float64(i), Memory: 0.1, RSS: 12 * 1024 * 1024, Elapsed: 120, Command: "python training-job.py"}
	}
	model := &monitorModel{
		status: "Live data refreshes every second.",
		snapshot: monitorSnapshot{
			CollectedAt: time.Date(2026, 7, 25, 12, 34, 56, 0, time.Local),
			CPUPercent:  42, LoadAverage: "load 0.42 · 0.38 · 0.31",
			MemoryUsed: 24 * 1024 * 1024 * 1024, MemoryTotal: 128 * 1024 * 1024 * 1024,
			DiskUsed: 200 * 1024 * 1024 * 1024, DiskTotal: 1024 * 1024 * 1024 * 1024,
			NetworkRX: 128 * 1024, NetworkTX: 64 * 1024, NetworkRXTotal: 8 * 1024 * 1024 * 1024, NetworkTXTotal: 4 * 1024 * 1024 * 1024,
			GPUs: []gpuInfo{
				{Index: 0, Name: "NVIDIA A100-PCIE-40GB", Utilization: 80, MemoryUsed: 16 * 1024 * 1024 * 1024, MemoryTotal: 40 * 1024 * 1024 * 1024, ClockMHz: 1410, Power: 180, PowerLimit: 250, Temperature: 70},
				{Index: 1, Name: "NVIDIA TITAN RTX", Utilization: 20, MemoryUsed: 2 * 1024 * 1024 * 1024, MemoryTotal: 24 * 1024 * 1024 * 1024, ClockMHz: 900, Power: 80, PowerLimit: 280, Temperature: 45},
			},
			Processes: processes,
		},
		cpuHistory:       []float64{10, 25, 35, 42},
		networkRXHistory: []float64{1024, 4096, 2048, 8192},
		networkTXHistory: []float64{512, 1024, 4096, 2048},
	}
	for _, size := range []struct{ width, height int }{{161, 50}, {100, 40}, {80, 30}, {60, 24}} {
		model.width, model.height = size.width, size.height
		rendered := model.monitorView()
		if got := lipgloss.Height(rendered); got > size.height {
			t.Fatalf("%dx%d view height = %d\n%s", size.width, size.height, got, rendered)
		}
		for index, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d line %d width = %d\n%q", size.width, size.height, index, got, line)
			}
		}
	}
}

func TestGPUClockAndPowerParsing(t *testing.T) {
	gpus := parseGPUs([]byte("0, NVIDIA A100-PCIE-40GB, 82, 1024, 40960, 55, 1410, 70.64, 250.00\n"))
	if len(gpus) != 1 {
		t.Fatalf("parsed GPUs = %#v", gpus)
	}
	gpu := gpus[0]
	if gpu.ClockMHz != 1410 || gpu.Power != 70.64 || gpu.PowerLimit != 250 {
		t.Fatalf("GPU telemetry = %#v", gpu)
	}
	if got := gpuTelemetry(gpu, true); got != "CLK 1410 MHz · PWR  71/250 W ·  55°C" {
		t.Fatalf("GPU telemetry text = %q", got)
	}
}

func TestGPULoadStatusAndWideLayout(t *testing.T) {
	cases := []struct {
		utilization float64
		status      string
	}{
		{0, "IDLE"},
		{20, "LIGHT"},
		{50, "ACTIVE"},
		{75, "BUSY"},
		{90, "HIGH"},
		{99, "MAX"},
	}
	for _, tc := range cases {
		status, _ := gpuLoadStatus(tc.utilization)
		if status != tc.status {
			t.Fatalf("utilization %.0f status = %q, want %q", tc.utilization, status, tc.status)
		}
	}
	if bar(20, 10) == bar(90, 10) {
		t.Fatal("low and high utilization bars should use different colors")
	}

	model := &monitorModel{snapshot: monitorSnapshot{GPUs: []gpuInfo{
		{Index: 0, Name: "NVIDIA A100", MemoryTotal: 40 * 1024 * 1024 * 1024},
		{Index: 1, Name: "NVIDIA TITAN RTX", MemoryTotal: 24 * 1024 * 1024 * 1024},
	}}}
	layout := newDashboardLayout(161, 50, 2, false)
	if height := lipgloss.Height(model.gpuPanel(layout)); height != 6 {
		t.Fatalf("wide GPU panel height = %d, want two lines per GPU plus border", height)
	}
}

func TestGPUMemoryColumnsAreAligned(t *testing.T) {
	gpus := []gpuInfo{
		{MemoryUsed: 425 * 1024 * 1024, MemoryTotal: 40 * 1024 * 1024 * 1024},
		{MemoryUsed: 1 * 1024 * 1024, MemoryTotal: 24 * 1024 * 1024 * 1024},
	}
	usedWidth, totalWidth := gpuMemoryWidths(gpus)
	first := gpuMemoryText(gpus[0], usedWidth, totalWidth)
	second := gpuMemoryText(gpus[1], usedWidth, totalWidth)
	if lipgloss.Width(first) != lipgloss.Width(second) || strings.Index(first, "/") != strings.Index(second, "/") {
		t.Fatalf("GPU memory columns are not aligned: %q / %q", first, second)
	}
}

func TestMetricCardsJoinHorizontally(t *testing.T) {
	cards := []metricCard{
		{title: "CPU", value: "1%", detail: "load 0.1", visual: metricVisualCPU, primaryHistory: []float64{1}, titleStyle: cpuTitleStyle, borderColor: colorCPUBorder},
		{title: "MEMORY", value: "2 GiB", detail: "10% used", visual: metricVisualMeter, usage: 10, titleStyle: memoryTitleStyle, borderColor: colorMemoryBorder},
		{title: "DISK", value: "3 GiB", detail: "20% used", visual: metricVisualMeter, usage: 20, titleStyle: diskTitleStyle, borderColor: colorDiskBorder},
		{title: "NETWORK", value: "1 KiB/s", detail: "2 KiB/s", visual: metricVisualNetwork, primaryHistory: []float64{1}, secondaryHistory: []float64{2}, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder},
	}
	rendered := renderMetricRows(cards, dashboardLayout{width: 161, metricCols: 4})
	if height := lipgloss.Height(rendered); height != 5 {
		t.Fatalf("metric cards height = %d, want one five-line row", height)
	}
}

func TestBtopPanelsAndHistoryVisuals(t *testing.T) {
	panel := btopPanel(80, "CPU", "LIVE", "line one\nline two", cpuTitleStyle, colorCPUBorder)
	for index, line := range strings.Split(panel, "\n") {
		if width := lipgloss.Width(line); width != 80 {
			t.Fatalf("panel line %d width = %d, want 80: %q", index, width, line)
		}
	}
	if !strings.Contains(panel, "CPU") || !strings.Contains(panel, "LIVE") {
		t.Fatalf("panel title metadata missing: %q", panel)
	}

	history := []float64{}
	for i := 0; i < 70; i++ {
		history = appendHistory(history, float64(i), 60)
	}
	if len(history) != 60 || history[0] != 10 || history[len(history)-1] != 69 {
		t.Fatalf("bounded history = %#v", history)
	}
	graph := sparkline([]float64{0, 50, 100}, 8, 100, cpuTitleStyle)
	if lipgloss.Width(graph) != 8 || !strings.Contains(graph, "█") {
		t.Fatalf("sparkline = %q, width %d", graph, lipgloss.Width(graph))
	}
}

func TestProcessFormatRespondsToWidth(t *testing.T) {
	if got := newProcessFormat(140).mode; got != processFull {
		t.Fatalf("wide mode = %d, want full", got)
	}
	if got := newProcessFormat(90).mode; got != processMedium {
		t.Fatalf("medium mode = %d, want medium", got)
	}
	if got := newProcessFormat(60).mode; got != processCompact {
		t.Fatalf("compact mode = %d, want compact", got)
	}
}

func TestProcessColumnsAreAligned(t *testing.T) {
	format := newProcessFormat(140)
	header := format.header()
	row := format.row(processInfo{
		PID: 42, User: "alice", State: "R+", CPU: 12.3, Memory: 4.5,
		RSS: 10 * 1024 * 1024, Elapsed: 65, Command: "trainer",
	})
	if strings.Contains(header, "STAT") || strings.Contains(row, "R+") {
		t.Fatalf("process state should not occupy a table column\nheader: %q\nrow:    %q", header, row)
	}
	for _, field := range []string{"RSS", "ELAPSED", "COMMAND"} {
		if strings.Index(header, field) != strings.Index(row, map[string]string{
			"RSS": "10.0 MiB", "ELAPSED": "1m05s", "COMMAND": "trainer",
		}[field]) {
			t.Fatalf("%s is not aligned\nheader: %q\nrow:    %q", field, header, row)
		}
	}
	if strings.Index(header, "CPU")+len("CPU") != strings.Index(row, "12.3%")+len("12.3%") {
		t.Fatalf("CPU is not right-aligned\nheader: %q\nrow:    %q", header, row)
	}
	if strings.Index(header, "MEM")+len("MEM") != strings.Index(row, "4.5%")+len("4.5%") {
		t.Fatalf("MEM is not right-aligned\nheader: %q\nrow:    %q", header, row)
	}
}

func TestProcessLegendAdaptsToWidth(t *testing.T) {
	wide := processLegend(120)
	for _, label := range []string{"RUNNING", "SLEEPING", "WAITING", "STOPPED", "ZOMBIE", "IDLE"} {
		if !strings.Contains(wide, label) {
			t.Fatalf("wide legend does not contain %q: %q", label, wide)
		}
	}
	compact := processLegend(50)
	if strings.Contains(compact, "RUNNING") {
		t.Fatalf("compact legend should use state letters only: %q", compact)
	}
	for _, state := range []string{"R", "S", "D", "T", "Z", "I"} {
		if !strings.Contains(compact, state) {
			t.Fatalf("compact legend does not contain state %q: %q", state, compact)
		}
	}
}

func TestProcessStateColorsAndRefreshInterval(t *testing.T) {
	if refreshInterval != time.Second {
		t.Fatalf("refresh interval = %s", refreshInterval)
	}
	if processStateStyle("R").Render("row") == processStateStyle("S").Render("row") {
		t.Fatal("running and sleeping processes should use different colors")
	}
	if processStateStyle("D").Render("row") == processStateStyle("Z").Render("row") {
		t.Fatal("waiting and zombie processes should use different colors")
	}
}

func testKey(s string) tea.KeyMsg { return tea.KeyPressMsg{Code: keyCode(s), Text: keyText(s)} }

func keyCode(s string) rune {
	switch s {
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEscape
	case "backspace":
		return tea.KeyBackspace
	}
	return []rune(s)[0]
}

func keyText(s string) string {
	if s == "enter" || s == "esc" || s == "backspace" {
		return ""
	}
	return s
}
