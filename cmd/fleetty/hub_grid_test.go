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
	for _, test := range []struct{ width, columns, cardWidth int }{
		{36, 1, 36}, {48, 1, 48}, {60, 1, 60}, {72, 1, 72}, {73, 2, 36}, {80, 2, 39},
		{92, 2, 45}, {93, 2, 46}, {116, 2, 57}, {117, 3, 38}, {139, 3, 45}, {140, 3, 46},
		{160, 3, 52}, {186, 4, 45}, {187, 4, 46}, {233, 5, 45}, {234, 5, 46}, {238, 5, 46}, {244, 5, 48},
		{300, 6, 49}, {512, 11, 45}, {1024, 22, 45},
	} {
		t.Run(fmt.Sprint(test.width), func(t *testing.T) {
			m := wideTestHub(test.width, 24)
			if m.columns() != test.columns {
				t.Fatalf("columns = %d, want %d", m.columns(), test.columns)
			}
			if gap := test.width - (m.columns()*(m.cardWidth()+1) - 1); gap < 0 || gap >= m.columns() {
				t.Fatalf("unused grid width = %d, want less than column count", gap)
			}
			if got := lipgloss.Width(m.renderNodeCard(0, m.cardWidth())); got != test.cardWidth {
				t.Fatalf("rendered card width = %d, want %d", got, test.cardWidth)
			}
			view := m.hubView()
			lines := strings.Split(ansi.Strip(view), "\n")
			if got := strings.Count(lines[2], "╭"); got != test.columns {
				t.Fatalf("visible first-row cards = %d, want %d", got, test.columns)
			}
			if lipgloss.Height(view) != 24 {
				t.Fatal("view changed terminal height")
			}
			for _, line := range lines {
				if lipgloss.Width(line) > test.width {
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
						x := col * (test.cardWidth + 1)
						for _, dx := range []int{0, test.cardWidth - 1} {
							if got, ok := m.nodeAt(x+dx, y); !ok || got != index {
								t.Fatalf("click (%d,%d) = %d,%v, want %d", x+dx, y, got, ok, index)
							}
						}
						if _, ok := m.nodeAt(x+test.cardWidth, y); ok {
							t.Fatal("gap is clickable")
						}
						seen[index] = true
					}
					if _, ok := m.nodeAt(len(row.nodes)*(test.cardWidth+1), y); ok {
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
	// A wrapped GPU tail stays on its own row. CPU and services share the next
	// row, and each group header is printed once.
	for _, width := range []int{140, 187, 238} {
		m := compactTestHub(width, 48)
		plain := ansi.Strip(m.hubView())
		if strings.Count(plain, "GPU COMPUTE") != 1 {
			t.Fatalf("width %d repeated the GPU header:\n%s", width, plain)
		}
		if strings.Count(plain, "CPU COMPUTE") != 1 || strings.Count(plain, "CLUSTER SERVICES") != 1 {
			t.Fatalf("width %d lost a group header:\n%s", width, plain)
		}
		for _, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "CPU COMPUTE") && strings.Contains(line, "CLUSTER SERVICES") {
				t.Fatalf("width %d put two group headers on one row: %s", width, line)
			}
		}
		rows := m.pageNodeRows(m.pages()[0])
		if err := hubRowsAreContiguous(rows, m.nodeGroups()); err != nil {
			t.Fatalf("width %d: %v", width, err)
		}
	}
}

func hubRowsAreContiguous(rows [][]int, groups []hubNodeGroup) error {
	groupOf := map[int]int{}
	for index, group := range groups {
		for _, node := range group.nodes {
			groupOf[node] = index
		}
	}
	appeared := map[int]bool{}
	last := -1
	for _, row := range rows {
		if len(row) == 0 {
			return fmt.Errorf("empty card row")
		}
		present := map[int]bool{}
		for _, node := range row {
			group := groupOf[node]
			if group < last && !present[group] {
				return fmt.Errorf("node %d returns to an earlier group", node)
			}
			present[group] = true
			last = group
		}
		if len(present) > 1 {
			for group := range present {
				if appeared[group] {
					return fmt.Errorf("group %d continues into a mixed row", group)
				}
			}
		}
		for group := range present {
			appeared[group] = true
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
	if strings.Contains(card, "█") || strings.Contains(card, "░") || strings.Contains(card, "▕") || strings.Contains(card, "VRAM") {
		t.Fatalf("hub card kept a chunky meter: %s", card)
	}
	if !strings.Contains(card, "━") {
		t.Fatalf("hub card lost the thin meter: %s", card)
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
		if strings.Contains(line, label) && strings.Contains(line, "%") &&
			(strings.Contains(line, "━") || strings.Contains(line, "─")) {
			return line
		}
	}
	return ""
}

func meterMatches(line string, value float64) bool {
	filled := strings.Count(line, "━")
	empty := strings.Count(line, "─")
	width := filled + empty
	if width == 0 {
		return false
	}
	want := int(value / 100 * float64(width))
	return filled == max(0, min(width, want))
}
