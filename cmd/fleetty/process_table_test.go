package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func processTableFixtures() []processInfo {
	return []processInfo{
		{PID: 30, CPU: 10, Memory: 1, RSS: 100, Elapsed: 100, User: "Zed", Command: "z-worker", State: "R"},
		{PID: 10, CPU: 70, Memory: 8, RSS: 800, Elapsed: 50, User: "alice", Command: "a-worker", State: "S"},
		{PID: 20, CPU: 70, Memory: 3, RSS: 300, Elapsed: 200, User: "Bob", Command: "b-helper", State: "I"},
	}
}

func processIDs(processes []processInfo) []int {
	ids := make([]int, len(processes))
	for i, p := range processes {
		ids[i] = p.PID
	}
	return ids
}

func TestProcessSortMetricsAndSessionIsolation(t *testing.T) {
	shared := processTableFixtures()
	original := slices.Clone(shared)
	for _, tc := range []struct {
		key  processSortKey
		asc  bool
		want []int
	}{
		{processSortCPU, false, []int{10, 20, 30}},
		{processSortCPU, true, []int{30, 10, 20}}, // Ties always use PID, not snapshot order.
		{processSortMemory, false, []int{10, 20, 30}},
		{processSortMemory, true, []int{30, 20, 10}},
		{processSortRSS, false, []int{10, 20, 30}},
		{processSortRSS, true, []int{30, 20, 10}},
		{processSortElapsed, false, []int{20, 30, 10}},
		{processSortElapsed, true, []int{10, 30, 20}},
		{processSortPID, false, []int{30, 20, 10}},
		{processSortPID, true, []int{10, 20, 30}},
		{processSortUser, true, []int{10, 20, 30}},
		{processSortUser, false, []int{30, 20, 10}},
		{processSortCommand, true, []int{10, 20, 30}},
		{processSortCommand, false, []int{30, 20, 10}},
	} {
		model := &monitorModel{snapshot: monitorSnapshot{Processes: shared}, processSort: tc.key, processSortAsc: tc.asc}
		if got := processIDs(model.filteredProcesses()); !slices.Equal(got, tc.want) {
			t.Fatalf("%s asc=%t got %v want %v", tc.key.label(), tc.asc, got, tc.want)
		}
		if !slices.Equal(shared, original) {
			t.Fatal("sorting mutated the shared snapshot")
		}
	}
	filtered := &monitorModel{snapshot: monitorSnapshot{Processes: shared}, filter: "WoRkEr", processSort: processSortElapsed}
	if got := processIDs(filtered.filteredProcesses()); !slices.Equal(got, []int{30, 10}) {
		t.Fatalf("filtered ordering = %v", got)
	}
}

func TestProcessSortAndRefreshKeepSelectedPID(t *testing.T) {
	m := &monitorModel{
		screen: screenMonitor, monitorRows: 2, height: 30,
		snapshot:      monitorSnapshot{Processes: processTableFixtures()},
		monitorCursor: 0, cursor: 1,
	}
	m.setProcessSort(processSortElapsed)
	monitor, admin := m.selectedProcessIDs()
	if monitor != 10 || admin != 20 || m.monitorOffset != 1 {
		t.Fatalf("sort moved selection: monitor=%d admin=%d offset=%d", monitor, admin, m.monitorOffset)
	}
	updated := processTableFixtures()
	updated[1].Elapsed = 300
	m.Update(snapshotMsg{snapshot: monitorSnapshot{Processes: updated}})
	monitor, admin = m.selectedProcessIDs()
	if monitor != 10 || admin != 20 || m.monitorCursor != 0 || m.monitorOffset != 0 {
		t.Fatalf("refresh moved selection: monitor=%d admin=%d index=%d offset=%d", monitor, admin, m.monitorCursor, m.monitorOffset)
	}
	m.handleKey(testKey("o"))
	if m.processSort != processSortPID || !m.processSortAsc {
		t.Fatal("PID sorting should start ascending")
	}
	m.handleKey(testKey("O"))
	if m.processSortAsc {
		t.Fatal("O must reverse the ordering")
	}
	m.filtering = true
	m.handleKey(testKey("o"))
	if m.filter != "o" || m.processSort != processSortPID {
		t.Fatal("sort shortcut consumed filter text")
	}
}

func TestProcessHeaderClickSortsEverySurface(t *testing.T) {
	for _, page := range []monitorPage{monitorPageOverview, monitorPageCompute, monitorPageCustom} {
		m := &monitorModel{
			screen: screenMonitor, monitorPage: page, width: 140, height: 50,
			admin: &adminController{}, profile: machineProfileGeneral,
			snapshot: monitorSnapshot{CollectedAt: time.Now(), Processes: processTableFixtures()},
		}
		m.monitorView()
		placement, y, ok := m.visibleWidgetPlacement(dashboardPanelProcesses)
		if !ok {
			t.Fatalf("process panel missing from page %v", page)
		}
		rowStart := placement.ProcessRowStart
		if rowStart == 0 {
			rowStart = 3
		}
		header := newProcessFormat(placement.Width).header()
		x := placement.X + 2 + strings.Index(header, "MEM")
		m.handleClick(x, y+rowStart-1)
		if m.screen != screenMonitor || m.processSort != processSortMemory || m.processSortAsc {
			t.Fatalf("page %v header click didn't select memory sort", page)
		}
		m.handleClick(x, y+rowStart-1)
		if !m.processSortAsc {
			t.Fatal("second header click must reverse ordering")
		}
		m.monitorView()
		m.handleClick(placement.X+2, y+rowStart)
		if m.screen != screenProcessDetail || m.selectedProcess.PID != m.filteredProcesses()[m.monitorOffset].PID || !m.processReadOnly {
			t.Fatal("row click no longer opens the displayed process")
		}
	}
	m := &monitorModel{screen: screenAdmin, width: 140, height: 40, admin: &adminController{}, snapshot: monitorSnapshot{Processes: processTableFixtures()}}
	m.handleClick(2+strings.Index(newProcessFormat(140).header(), "RSS"), adminProcessRowStart-1)
	if m.processSort != processSortRSS || m.screen != screenAdmin {
		t.Fatal("admin header sort did not work")
	}
}

func TestProcessTableCellsAlignAcrossWidthsThemesAndSelection(t *testing.T) {
	for _, width := range []int{36, 60, 78, 90, 112, 160, 250} {
		f := newProcessFormat(width)
		for _, mode := range []colorMode{colorModeDark, colorModeLight} {
			for _, selected := range []bool{false, true} {
				p := processInfo{PID: 1234567, User: "用户名-very-long", State: "R+", CPU: 1200, Memory: 42, RSS: 21 << 30, Elapsed: 900001, Command: "训练进程 --workers 32"}
				row := f.renderRow(p, 125, selected, mode, 50<<30)
				if mode == colorModeLight {
					row = applyLightTheme(row)
				}
				if got := lipgloss.Width(row); got != width-4 {
					t.Fatalf("width=%d selected=%t theme=%v row width=%d\n%s", width, selected, mode, got, row)
				}
				if lipgloss.Width(f.renderHeader(processSortMemory)) != width-4 {
					t.Fatal("header and row widths differ")
				}
				left := 0
				for _, column := range f.columns {
					if key, ok := f.sortKeyAt(left + column.width - 1); key != column.key || ok != (column.key != processRank) {
						t.Fatalf("incorrect hit box at width=%d column=%v", width, column.key)
					}
					left += column.width + 1
				}
				red := "38;2;255;83;112"
				if mode == colorModeLight {
					red = "38;2;168;15;45"
				}
				if !strings.Contains(row, red) {
					t.Fatal("load color was lost, including on a selected row")
				}
				if selected {
					background := "48;5;236"
					if mode == colorModeLight {
						background = "48;5;189"
					}
					if !strings.Contains(row, background) {
						t.Fatal("selection background missing")
					}
				}
			}
		}
	}
}

func TestProcessLoadColorsAreIndependentOfState(t *testing.T) {
	f := newProcessFormat(140)
	p := processInfo{PID: 42, State: "S", CPU: 95, Memory: 0.5, RSS: 1 << 30, Command: "worker"}
	row := f.renderRow(p, 1, false, colorModeDark, 4<<30)
	for _, expected := range []string{
		processLoadStyle(95, false).Render(fixedCell("95.0%", 8, true)),
		processLoadStyle(0.5, true).Render(fixedCell("0.5%", 6, true)),
		processLoadStyle(25, true).Render(fixedCell("1.0 GiB", 10, false)),
	} {
		if !strings.Contains(row, expected) {
			t.Fatalf("missing metric color %q in %q", expected, row)
		}
	}
	seen := map[string]bool{}
	for _, value := range []float64{0, 1, 20, 60, 100} {
		color := processLoadStyle(value, false).Render("load")
		if seen[color] {
			t.Fatalf("load level %.1f has no distinct color", value)
		}
		seen[color] = true
	}
}

func TestProcessPreviewCountsRanksAndHeight(t *testing.T) {
	processes := make([]processInfo, 100)
	for i := range processes {
		processes[i] = processInfo{PID: i + 1, CPU: float64(100 - i), Command: fmt.Sprintf("worker-%03d", i+1)}
	}
	m := &monitorModel{snapshot: monitorSnapshot{Processes: processes}, monitorCursor: 50, monitorOffset: 50}
	for _, width := range []int{36, 80, 140, 240} {
		panel := m.renderOverviewProcesses(width, 8, 8)
		plain := ansi.Strip(panel)
		if !strings.Contains(plain, "51–58") || !strings.Contains(plain, "100") || !strings.Contains(plain, "51") {
			t.Fatalf("missing counts at width %d: %s", width, plain)
		}
		if lipgloss.Height(panel) != 12 {
			t.Fatal("table must reserve exactly eight process rows and four overhead rows")
		}
		for _, line := range strings.Split(panel, "\n") {
			if lipgloss.Width(line) != width {
				t.Fatalf("table overflow at width %d: %q", width, line)
			}
		}
	}
	m.filter = "worker-00"
	m.monitorCursor, m.monitorOffset = 0, 0
	if meta := m.processTableMeta(0, 8, 140, "TOP PROCESSES"); !strings.Contains(meta, "1–8 / 9 MATCHED · 100 TOTAL") {
		t.Fatalf("ambiguous filtered counts: %s", meta)
	}
	m.filter = "does not exist"
	if meta := m.processTableMeta(0, 8, 140, "TOP PROCESSES"); !strings.Contains(meta, "0–0 / 0 MATCHED · 100 TOTAL") {
		t.Fatalf("empty counts: %s", meta)
	}
}
