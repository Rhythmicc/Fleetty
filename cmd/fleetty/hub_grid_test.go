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
	// At four columns, the GPU tail, CPU and services share one complete row.
	m := compactTestHub(187, 48)
	rows := m.pageNodeRows(m.pages()[0])
	if len(rows) != 4 || len(rows[3]) != 4 {
		t.Fatalf("small groups still leave separate sparse rows: %v", rows)
	}
	// At five columns, keep the two service cards together, not split over rows.
	m = compactTestHub(238, 48)
	rows = m.pageNodeRows(m.pages()[0])
	if len(rows) != 4 || len(rows[3]) != 2 || rows[3][0] != 14 || rows[3][1] != 15 {
		t.Fatalf("service group split: %v", rows)
	}
}

func TestHubLiveCardRemovesDuplicateLoadAndThickBar(t *testing.T) {
	m := compactTestHub(238, 48)
	card := ansi.Strip(m.renderNodeCard(2, hubPreferredCardWidth))
	for _, want := range []string{"GPU ×1", "PEAK 100%", "━", "VRAM", "↓", "↑", "IP"} {
		if !strings.Contains(card, want) {
			t.Fatalf("missing %s: %s", want, card)
		}
	}
	if strings.ContainsAny(card, "█░") || strings.Contains(card, "MAX") || strings.Contains(card, "LOAD") {
		t.Fatalf("duplicate/heavy GPU load display: %s", card)
	}
	if hubLoadMeter(100, 8) != gpuTitleStyle.Render(strings.Repeat("━", 8))+dimStyle.Render("") {
		t.Fatal("normal full utilization styled as an alarm")
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
	if !strings.Contains(card, "GPU ×16") || !strings.Contains(card, "PEAK 100%") || !strings.Contains(card, "100°C") {
		t.Fatalf("important GPU fields clipped at minimum width: %s", card)
	}
}
