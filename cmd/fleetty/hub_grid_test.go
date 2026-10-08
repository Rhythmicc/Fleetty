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

func TestHubFixedWidthCardsAndClickTargets(t *testing.T) {
	for _, test := range []struct{ width, columns, cardWidth int }{
		{36, 1, 36}, {48, 1, 48}, {60, 1, 48},
		{96, 1, 48}, {97, 2, 48}, {145, 2, 48}, {146, 3, 48},
		{194, 3, 48}, {195, 4, 48}, {244, 5, 48},
		{300, 6, 48}, {512, 10, 48}, {1024, 20, 48},
	} {
		t.Run(fmt.Sprint(test.width), func(t *testing.T) {
			m := wideTestHub(test.width, 24)
			if m.columns() != test.columns {
				t.Fatalf("columns = %d, want %d", m.columns(), test.columns)
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
					if row.title != "" {
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
				card := m.renderNodeCard(0, hubCardWidth)
				plain := ansi.Strip(card)
				if !strings.Contains(plain, "IP 192.168.31.141") || strings.Contains(plain, "Enter or click") {
					t.Fatalf("wrong address footer: %s", plain)
				}
				if lipgloss.Width(card) != hubCardWidth || lipgloss.Height(card) != hubCardHeight {
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
