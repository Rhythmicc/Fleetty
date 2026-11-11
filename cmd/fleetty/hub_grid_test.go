package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func wideTestHub(width, height int) *hubModel {
	nodes := make([]hubNodeConfig, 31)
	for i := range nodes {
		nodes[i] = hubNodeConfig{Name: fmt.Sprintf("node%02d", i), Group: "gpu"}
	}
	return &hubModel{
		width: width, height: height,
		config: hubConfig{
			Name:   "Grid test",
			Groups: []hubGroupConfig{{ID: "gpu", Title: "GPU COMPUTE", Style: hubGroupStyleGPU}},
			Nodes:  nodes,
		},
	}
}

func TestHubAdaptiveCardsAndClickTargets(t *testing.T) {
	for _, width := range []int{
		36, 48, 60, 72, 73, 80, 92, 93, 116, 117, 139, 140, 160, 186, 187, 220,
		233, 234, 238, 244, 300, 512, 1024,
	} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := wideTestHub(width, 24)
			columns, cardWidth := m.columns(), m.cardWidth()
			if width == 140 && columns != 3 {
				t.Fatalf("columns = %d, want 3", columns)
			}
			if width == 220 && columns != 4 {
				t.Fatalf("columns = %d, want 4", columns)
			}
			if gap := width - (columns*(cardWidth+1) - 1); gap < 0 || gap >= columns {
				t.Fatalf("unused grid width = %d, want less than column count", gap)
			}
			if got := lipgloss.Width(m.renderNodeCard(0, cardWidth)); got != cardWidth {
				t.Fatalf("rendered card width = %d, want %d", got, cardWidth)
			}
			view := m.hubView()
			lines := strings.Split(ansi.Strip(view), "\n")
			if got := strings.Count(lines[2], "╭"); got != columns {
				t.Fatalf("visible first-row cards = %d, want %d", got, columns)
			}
			if lipgloss.Height(view) != 24 {
				t.Fatal("view changed terminal height")
			}
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("view overflows: %q", line)
				}
			}
			// Check every card on every page, including the partial last row.
			seen := make(map[int]bool)
			for pageIndex, page := range m.pages() {
				m.offset = pageIndex
				y := 1
				for _, row := range page.rows {
					if len(row.headings) > 0 {
						if _, ok := m.nodeAt(0, y); ok {
							t.Fatal("heading is clickable")
						}
						y++
						continue
					}
					for col, index := range row.nodes {
						x := col * (cardWidth + 1)
						for _, dx := range []int{0, cardWidth - 1} {
							if got, ok := m.nodeAt(x+dx, y); !ok || got != index {
								t.Fatalf("click (%d,%d) = %d,%v, want %d", x+dx, y, got, ok, index)
							}
						}
						if _, ok := m.nodeAt(x+cardWidth, y); ok {
							t.Fatal("gap is clickable")
						}
						seen[index] = true
					}
					if _, ok := m.nodeAt(len(row.nodes)*(cardWidth+1), y); ok {
						t.Fatal("unused row space is clickable")
					}
					if _, ok := m.nodeAt(-1, y); ok {
						t.Fatal("negative coordinate is clickable")
					}
					y += hubCardHeight
				}
			}
			if len(seen) != 31 {
				t.Fatalf("only %d nodes reachable", len(seen))
			}
		})
	}
}

func TestHubWideGridNavigationAndResize(t *testing.T) {
	m := wideTestHub(244, 24)
	m.cursor = 4
	m.moveCursorVertical(1)
	if m.cursor != 9 {
		t.Fatalf("down from column 5 selected %d, want 9", m.cursor)
	}
	m.moveCursorVertical(-1)
	m.moveCursorHorizontal(1)
	if m.cursor != 5 {
		t.Fatalf("right across row selected %d, want 5", m.cursor)
	}
	m.moveCursorHorizontal(-1)
	if m.cursor != 4 {
		t.Fatalf("left across row selected %d, want 4", m.cursor)
	}
	m.cursor = 27
	for _, width := range []int{97, 512, 36, 300} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		if m.cursor != 27 || !strings.Contains(m.hubView(), "node27") {
			t.Fatalf("resize to %d lost selected node (cursor=%d, page=%d)", width, m.cursor, m.offset)
		}
	}
}

func TestHubOfflineReasonStaysShort(t *testing.T) {
	state := hubNodeState{
		Error:     "connect n3: dial tcp 192.168.31.180:23234: i/o timeout",
		LastSeen:  time.Now().Add(-time.Minute),
		NextRetry: time.Now().Add(26 * time.Second),
	}
	card := ansi.Strip(strings.Join(renderOfflineHubCard(state, 40), "\n"))
	if !strings.Contains(card, "i/o timeout") || strings.Contains(card, "dial tcp") || strings.Contains(card, "…") {
		t.Fatalf("offline reason was not shortened cleanly: %s", card)
	}
	if got := hubShortError("connection refused", 40); got != "connection refused" {
		t.Fatalf("short reason = %q", got)
	}
}

func TestHubCardsShowAddressWithoutOpenHint(t *testing.T) {
	for _, profile := range []string{machineProfileGPU, machineProfileCPU, machineProfileNAS} {
		for _, status := range []string{"waiting", "live", "offline", "warning"} {
			t.Run(profile+"/"+status, func(t *testing.T) {
				state := hubNodeState{}
				if status == "live" || status == "warning" {
					state.Snapshot.CollectedAt = time.Now()
				}
				if status == "offline" {
					state.Error = "connection refused"
				}
				if status == "warning" {
					state.Warning = "partial snapshot"
				}
				m := &hubModel{
					config: hubConfig{Nodes: []hubNodeConfig{{Name: "node", Address: "192.168.31.141:23234", Profile: profile}}},
					states: []hubNodeState{state},
				}
				card := m.renderNodeCard(0, hubPreferredCardWidth)
				plain := ansi.Strip(card)
				if !strings.Contains(plain, "IP 192.168.31.141") || strings.Contains(plain, "Enter or click") {
					t.Fatalf("wrong address footer: %s", plain)
				}
				if lipgloss.Width(card) != hubPreferredCardWidth || lipgloss.Height(card) != hubCardHeight {
					t.Fatalf("card dimensions changed: %s", plain)
				}
				if status == "warning" && !strings.Contains(plain, "WARN") {
					t.Fatal("warning indicator lost")
				}
			})
		}
	}
	for address, want := range map[string]string{
		"": "IP --", "10.18.19.162": "IP 10.18.19.162",
		"[2001:db8::1]:23234": "IP 2001:db8::1", "node.local:23234": "HOST node.local",
	} {
		if got := hubNodeAddressLabel(address); got != want {
			t.Errorf("address %q = %q, want %q", address, got, want)
		}
	}
}

func compactTestHub(width, height int) *hubModel {
	names := []string{"A100", "4090", "5090", "B580", "n1", "n2", "n3", "n4", "s80", "Z100", "9070XT", "7G100", "LG200", "intel9462", "NAS", "ssslab-login-1"}
	m := &hubModel{width: width, height: height, config: hubConfig{Name: "SSSLab Machine Hub", Groups: productionHubGroups()}}
	for i, name := range names {
		profile, group := machineProfileGPU, "gpu"
		if i == 13 {
			profile, group = machineProfileCPU, "cpu"
		} else if i >= 14 {
			group = "services"
			if i == 14 {
				profile = machineProfileNAS
			}
		}
		m.config.Nodes = append(m.config.Nodes, hubNodeConfig{Name: name, Profile: profile, Group: group, Address: fmt.Sprintf("192.168.31.%d:23234", 141+i)})
		m.states = append(m.states, hubNodeState{Latency: time.Duration(20+i*5) * time.Millisecond, Snapshot: monitorSnapshot{
			CollectedAt: time.Now(), CPUPercent: 2.6, CPUCores: 64, LoadAverage: "load 5.09 · 7.48 · 5.74",
			MemoryUsed: 4 << 30, MemoryTotal: 64 << 30, DiskUsed: 50 << 30, DiskTotal: 500 << 30,
			NetworkRX: 18 << 20, NetworkTX: 1200, NetworkRXTotal: 4 << 40, NetworkTXTotal: 5 << 40,
			GPUs: []gpuInfo{{Utilization: float64(i%3) * 50, MemoryUsed: 489 << 20, MemoryTotal: 64 << 30, Temperature: 48}},
		}})
	}
	m.states[12] = hubNodeState{Error: "connection refused"}
	return m
}

func TestHubPackedGroupsAndPagination(t *testing.T) {
	for _, width := range []int{36, 93, 160, 187, 238, 512, 1024} {
		for _, height := range []int{10, 18, 26, 48} {
			m := compactTestHub(width, height)
			seen := make(map[int]bool)
			for pageIndex, page := range m.pages() {
				if len(page.rows) == 0 || len(page.rows[0].headings) == 0 {
					t.Fatal("page must start with group context")
				}
				m.offset = pageIndex
				y := 1
				for _, row := range page.rows {
					if len(row.headings) > 0 {
						y++
						continue
					}
					for column, index := range row.nodes {
						if seen[index] {
							t.Fatalf("node %d occurs twice", index)
						}
						seen[index] = true
						if got, ok := m.nodeAt(column*(m.cardWidth()+1), y); !ok || got != index {
							t.Fatalf("packed card hit test: %d %v, want %d", got, ok, index)
						}
					}
					y += hubCardHeight
				}
				view := m.hubView()
				if lipgloss.Height(view) != height {
					t.Fatalf("%dx%d has wrong height", width, height)
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("%dx%d overflow: %q", width, height, line)
					}
				}
			}
			if len(seen) != 16 {
				t.Fatalf("%dx%d: only %d nodes reachable", width, height, len(seen))
			}
		}
	}
	// Cards pack left to right across groups. A label stays inside its card
	// column, so two group names can share a row without running together.
	for _, width := range []int{140, 187, 220, 238} {
		m := compactTestHub(width, 48)
		var rows [][]int
		var plain string
		for pageIndex, page := range m.pages() {
			m.offset = pageIndex
			plain += ansi.Strip(m.hubView()) + "\n"
			rows = append(rows, m.pageNodeRows(page)...)
		}
		if err := hubRowsArePacked(rows, m.nodeGroups(), m.columns()); err != nil {
			t.Fatalf("width %d: %v", width, err)
		}
		if strings.Count(plain, "GPU COMPUTE") != 1 || strings.Count(plain, "CPU COMPUTE") != 1 || strings.Count(plain, "CLUSTER SERVICES") != 1 {
			t.Fatalf("width %d lost or repeated a group label:\n%s", width, plain)
		}
		for _, line := range strings.Split(plain, "\n") {
			cpu := strings.Index(line, "CPU COMPUTE")
			services := strings.Index(line, "CLUSTER SERVICES")
			if cpu >= 0 && services >= 0 && services-cpu < len("CPU COMPUTE")+2 {
				t.Fatalf("width %d jammed two group labels: %s", width, line)
			}
		}
		if width == 140 && (m.columns() != 3 || len(m.pages()) != 1) {
			t.Fatalf("140x48 columns=%d pages=%d", m.columns(), len(m.pages()))
		}
	}
}

func hubRowsArePacked(rows [][]int, groups []hubNodeGroup, columns int) error {
	var got, want []int
	for index, row := range rows {
		if len(row) == 0 {
			return fmt.Errorf("empty card row")
		}
		if index < len(rows)-1 && len(row) != columns {
			return fmt.Errorf("row %d left a hole: %v", index, row)
		}
		got = append(got, row...)
	}
	for _, group := range groups {
		want = append(want, group.nodes...)
	}
	if len(got) != len(want) {
		return fmt.Errorf("packed %d nodes, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("packed order %v, want %v", got, want)
		}
	}
	return nil
}

func TestHubLiveCardShowsFullWidthBlockBarWithoutDuplicateLoad(t *testing.T) {
	m := compactTestHub(238, 48)
	card := ansi.Strip(m.renderNodeCard(2, hubPreferredCardWidth))
	for _, want := range []string{"CPU", "MEM", "GPU×1", "100%", "IP"} {
		if !strings.Contains(card, want) {
			t.Fatalf("missing %s: %s", want, card)
		}
	}
	if strings.Contains(card, "░") || strings.Contains(card, "▕") || strings.Contains(card, "cpu ") {
		t.Fatalf("hub card kept the old meter: %s", card)
	}
	if hubCapacityStyle(96, diskTitleStyle).Render("96%") != dangerStyle.Render("96%") {
		t.Fatal("real capacity risk lost its warning color")
	}
}

func TestHubAdaptiveGridAvoidsWidthCliffs(t *testing.T) {
	for width := 36; width <= 1200; width++ {
		m := wideTestHub(width, 24)
		columns, cardWidth := m.columns(), m.cardWidth()
		if cardWidth < hubMinimumCardWidth {
			t.Fatalf("%d: card too narrow: %d", width, cardWidth)
		}
		gap := width - (columns*cardWidth + columns - 1)
		if gap < 0 || gap >= columns {
			t.Fatalf("%d: excessive unused width: %d", width, gap)
		}
	}
	m := compactTestHub(73, 24)
	// Narrow cards must keep the temperature visible, even with many GPUs.
	m.states[0].Snapshot.GPUs = make([]gpuInfo, 16)
	m.states[0].Snapshot.GPUs[0] = gpuInfo{Utilization: 100, Temperature: 100}
	card := ansi.Strip(m.renderNodeCard(0, m.cardWidth()))
	if !strings.Contains(card, "GPU×16") || !strings.Contains(card, "100%") || !strings.Contains(card, "100°C") {
		t.Fatalf("important GPU fields clipped at minimum width: %s", card)
	}
}

func TestHubBlockBarReflectsBusyCPUAndGPUAcrossWidths(t *testing.T) {
	for _, profile := range []string{machineProfileGPU, machineProfileCPU} {
		for _, width := range []int{36, 39, 46, 52, 72} {
			for _, cpu := range []float64{0, 50, 100} {
				for _, gpu := range []float64{0, 50, 100} {
					m := compactTestHub(238, 48)
					m.config.Nodes[0].Profile = profile
					m.states[0].Snapshot.CPUPercent = cpu
					m.states[0].Snapshot.GPUs[0].Utilization = gpu
					m.states[0].Snapshot.NetworkRX = 120 << 20
					m.states[0].Snapshot.NetworkTX = 120 << 20
					card := m.renderNodeCard(0, width)
					if lipgloss.Width(card) != width || lipgloss.Height(card) != hubCardHeight {
						t.Fatalf("%s width %d changed card geometry", profile, width)
					}
					plain := ansi.Strip(card)
					if !strings.Contains(plain, "CPU") || !strings.Contains(plain, "MEM") {
						t.Fatalf("%s cpu %.0f gpu %.0f: missing meters: %s", profile, cpu, gpu, plain)
					}
					if profile == machineProfileGPU && !strings.Contains(plain, "GPU") {
						t.Fatalf("gpu profile lost the GPU meter: %s", plain)
					}
					if profile == machineProfileCPU && !strings.Contains(plain, "DSK") {
						t.Fatalf("cpu profile lost the disk meter: %s", plain)
					}
					cpuLine := hubMeterLine(plain, "CPU")
					if !meterMatches(cpuLine, cpu) {
						t.Fatalf("%s cpu %.0f: meter %q", profile, cpu, cpuLine)
					}
					if profile == machineProfileGPU {
						if gpuLine := hubMeterLine(plain, "GPU"); !meterMatches(gpuLine, gpu) {
							t.Fatalf("gpu %.0f: meter %q", gpu, gpuLine)
						}
					}
				}
			}
		}
	}
}

func hubMeterLine(card, label string) string {
	for _, line := range strings.Split(card, "\n") {
		if strings.Contains(line, label) && strings.Contains(line, "%") {
			return line
		}
	}
	return ""
}

func meterMatches(line string, value float64) bool {
	return strings.Contains(line, fmt.Sprintf("%3.0f%%", value))
}

func TestHubSparklineIsFixedScaleAndOneColumnPerSample(t *testing.T) {
	idle := ansi.Strip(hubFixedSparkline([]float64{0, 1, 2, 3}, 4))
	if idle != "▁▁▁▁" {
		t.Fatalf("idle samples lost the baseline or were scaled up: %q", idle)
	}
	idleColored := hubFixedSparkline([]float64{0, 1, 2, 3}, 4)
	if strings.Contains(idleColored, "38;2;158;228;147") || !strings.Contains(idleColored, "38;2;52;60;73") {
		t.Fatalf("idle baseline should be the faint track, not green: %q", idleColored)
	}
	healthy := hubFixedSparkline([]float64{50}, 1)
	if strings.Contains(healthy, "38;2;158;228;147") || !strings.Contains(healthy, "38;2;156;163;188") {
		t.Fatalf("healthy column should be muted, not green: %q", healthy)
	}
	if !strings.Contains(hubMeterStyle(stateOK).Render("62%"), "38;2;245;247;255") {
		t.Fatal("healthy percent should use the normal foreground")
	}
	hot := hubFixedSparkline([]float64{4, 100}, 4)
	plain := ansi.Strip(hot)
	if !strings.HasPrefix(plain, "··") || !strings.Contains(plain, "█") || !strings.Contains(plain, "▁") {
		t.Fatalf("short history should pad on the left and keep 100%% tall: %q", plain)
	}
	if !strings.Contains(hot, "38;2;255;138;128") {
		t.Fatalf("crit sample stayed gray: %q", hot)
	}
	busy := appendSpark(nil, hubSparkSample{CPU: 10})
	for i := 0; i < hubSparkCap+5; i++ {
		busy = appendSpark(busy, hubSparkSample{CPU: float64(i)})
	}
	if len(busy) != hubSparkCap || busy[len(busy)-1].CPU != float64(hubSparkCap+4) {
		t.Fatalf("spark ring = %d last %.0f", len(busy), busy[len(busy)-1].CPU)
	}
	if hubThroughputLevel(400<<20, 0) != stateOK || hubThroughputLevel(500<<20, 0) != stateWarn || hubThroughputLevel(1<<30, 0) != stateCrit {
		t.Fatal("absolute throughput fallback thresholds drifted")
	}
	link := uint64(10000) * 1000 * 1000 / 8
	if hubThroughputLevel(link*8/10, link) != stateWarn || hubThroughputLevel(link*95/100, link) != stateCrit {
		t.Fatal("link-speed throughput scale drifted")
	}
	if hubVRAMLevel(59, 96) != stateOK || hubVRAMLevel(80, 100) != stateWarn || hubVRAMLevel(95, 100) != stateCrit {
		t.Fatal("VRAM ratio scale drifted")
	}
	if temperatureLevel(74) != stateOK || temperatureLevel(75) != stateWarn || temperatureLevel(85) != stateCrit {
		t.Fatal("temperature scale drifted")
	}
}

func TestHubFactsFollowTheirMetricRow(t *testing.T) {
	rate := func(mib float64) uint64 { return uint64(mib * 1024 * 1024) }
	spark := make([]hubSparkSample, 30)
	for index := range spark {
		spark[index] = hubSparkSample{CPU: 1, Mem: 40, Disk: 12, GPU: 2}
	}
	gpu := &hubModel{
		width: 140, height: 48,
		config: hubConfig{Nodes: []hubNodeConfig{{Name: "gpu-a01", Profile: machineProfileGPU}}},
		states: []hubNodeState{{
			Spark: spark,
			Snapshot: monitorSnapshot{
				CollectedAt: time.Now(), Profile: machineProfileGPU,
				CPUPercent: 62, MemoryUsed: 71, MemoryTotal: 100,
				NetworkRX: rate(12.3), NetworkTX: rate(1.2),
				GPUs: []gpuInfo{
					{Utilization: 91, MemoryUsed: 30 << 30, MemoryTotal: 48 << 30},
					{Utilization: 47, MemoryUsed: 29 << 30, MemoryTotal: 48 << 30},
				},
				Processes: []processInfo{{User: "alice", Command: "protein-fold", CPU: 40}},
			},
		}},
	}
	if gpu.columns() != 3 {
		t.Fatalf("140-col hub uses %d columns, want 3", gpu.columns())
	}
	plain := ansi.Strip(gpu.renderNodeCard(0, gpu.cardWidth()))
	cpuLine, memLine, gpuLine := hubMeterLine(plain, "CPU"), hubMeterLine(plain, "MEM"), hubMeterLine(plain, "GPU")
	if !strings.Contains(cpuLine, "alice") || strings.Contains(cpuLine, "protein") || strings.Contains(cpuLine, "vram") {
		t.Fatalf("CPU row: %s", cpuLine)
	}
	if glyphs := sparkGlyphs(cpuLine); glyphs < 15 || glyphs > 18 {
		t.Fatalf("CPU sparkline is %d columns:\n%s", glyphs, cpuLine)
	}
	if !strings.Contains(memLine, "↓12M ↑1M") {
		t.Fatalf("MEM row: %s", memLine)
	}
	if !strings.Contains(gpuLine, "vram 59/96G") {
		t.Fatalf("GPU row: %s", gpuLine)
	}
	gpu.states[0].Snapshot.GPUs = nil
	gpu.states[0].Snapshot.Processes = []processInfo{
		{User: "alice", Command: "a", CPU: 40},
		{User: "bob", Command: "b", CPU: 30},
		{User: "carol", Command: "c", CPU: 20},
		{User: "alice", Command: "d", CPU: 10},
	}
	if got := ansi.Strip(gpu.hubOccupant(0, gpu.states[0].Snapshot, 20)); got != "alice +2" {
		t.Fatalf("users = %q", got)
	}
	if strings.Contains(plain, "up ") || strings.Contains(plain, "load ") {
		t.Fatalf("hub card still shows uptime or load:\n%s", plain)
	}

	cpu := &hubModel{
		width: 140, height: 48,
		config: hubConfig{Nodes: []hubNodeConfig{{Name: "cpu-01", Profile: machineProfileCPU}}},
		states: []hubNodeState{{
			Spark: spark,
			Snapshot: monitorSnapshot{
				CollectedAt: time.Now(), Profile: machineProfileCPU,
				CPUPercent: 18, MemoryUsed: 30, MemoryTotal: 100, DiskUsed: 20, DiskTotal: 100,
				DiskRead: rate(40), DiskWrite: rate(8),
				NetworkRX: rate(12.3), NetworkTX: rate(1.2),
			},
		}},
	}
	cpuPlain := ansi.Strip(cpu.renderNodeCard(0, cpu.cardWidth()))
	dsk := hubMeterLine(cpuPlain, "DSK")
	if !strings.Contains(dsk, "r40M w8M") {
		t.Fatalf("DSK row: %s", dsk)
	}
	idle := ansi.Strip(hubFixedSparkline([]float64{0, 0, 0}, 8))
	if idle != "·····▁▁▁" {
		t.Fatalf("missing samples should stay faint and zeros should baseline: %q", idle)
	}
}

func sparkGlyphs(line string) int {
	count := 0
	for _, glyph := range line {
		switch glyph {
		case '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█', '·':
			count++
		}
	}
	return count
}
