package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHubLinkSidebarShowsBothPeersWithoutInventingZero(t *testing.T) {
	m := compactTestHub(145, 34)
	m.linksView = true
	now := time.Now()
	m.links = mergeHubLinks([]hubLinkObservation{
		{node: 14, available: true, sampledAt: now, peers: []hubPeerLink{
			{node: 0, sockets: 1, sampled: 1, tx: 6 << 20, rx: 82 << 20},
			{node: 1, sockets: 1, sampled: 1},
			{node: 2, sockets: 2},
		}},
		{node: 15, available: true, sampledAt: now, peers: []hubPeerLink{
			{node: 0, sockets: 1, sampled: 1, tx: 2 << 20, rx: 11 << 20},
			{node: 2, sockets: 1, sampled: 1, rx: 1},
		}},
	})
	m.links.sampledAt, m.links.eligible = now, 14
	left, _ := hubRackWidths(m.width)
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		m.colorMode = mode
		rendered := m.View().Content
		if lipgloss.Width(rendered) > m.width || lipgloss.Height(rendered) != m.height {
			t.Fatalf("link sidebar overflow: %dx%d", lipgloss.Width(rendered), lipgloss.Height(rendered))
		}
		plain := ansi.Strip(rendered)
		for _, want := range []string{"CLUSTER LINKS", "2/14 observed", "A100 ↔ NAS", "82M      6M", "A100 ↔ ssslab-login-1", "11M      2M", "5090 ↔ ssslab-login-1", "1       0", "1 unmeasured"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("missing %q in links sidebar:\n%s", want, plain)
			}
		}
		if strings.Contains(plain, "4090 ↔") || strings.Contains(plain, "5090 ↔ NAS") {
			t.Fatalf("zero and unknown links should not add placeholder rows:\n%s", plain)
		}
		if _, ok := m.nodeAt(left+5, hubHeaderRows+1); ok {
			t.Fatal("clicking a link should not select an unrelated node")
		}
	}
	m.Update(testKey("n"))
	if m.linksView || !strings.Contains(ansi.Strip(m.View().Content), "NODE ·") {
		t.Fatal("node metrics must remain available")
	}
	m.Update(testKey("n"))
	if !strings.Contains(ansi.Strip(m.View().Content), "A100 ↔ NAS") {
		t.Fatal("n should return to the active links list")
	}
}

func TestHubLinksLayoutFitsAndLeavesQueueReachable(t *testing.T) {
	for _, width := range []int{24, 44, 60, 93, 94, 113, 114, 125, 147, 190, 220} {
		for _, height := range []int{8, 12, 17, 24, 34} {
			m := compactTestHub(width, height)
			m.linksView, m.linksExplicit = true, true
			m.slurmFilter = -1
			m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
			m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
				CollectedAt: time.Now(), Name: "GPU",
				Jobs: []slurmJob{{ID: "86701", Name: "test-job", State: "PENDING", Reason: "Priority"}},
			}}}
			for _, expanded := range []bool{false, true} {
				m.queueExpanded = expanded
				frame := m.View().Content
				if w, h := lipgloss.Width(frame), lipgloss.Height(frame); w > width || h != height {
					t.Fatalf("%dx%d expanded=%v got %dx%d", width, height, expanded, w, h)
				}
				plain := ansi.Strip(frame)
				title := "SLURM QUEUE"
				if width < 32 {
					title = "SLURM"
				}
				if !strings.Contains(plain, title) {
					t.Fatalf("%dx%d lost queue heading", width, height)
				}
				if expanded {
					for y, row := range strings.Split(plain, "\n") {
						if strings.Contains(row, "86701") {
							if index, ok := m.slurmJobAt(3, y); !ok || index != 0 {
								t.Fatalf("%dx%d queue hit test: %d, %v", width, height, index, ok)
							}
						}
					}
				}
			}
		}
	}
}

func TestHubRackUsesNeutralSelectionWithoutTintingSectionHeadings(t *testing.T) {
	m := compactTestHub(180, 35)
	m.linksView = true
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.config.Nodes[0].SlurmCluster = "GPU"
	m.config.Nodes[0].SlurmNode = "A100"
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: time.Now(),
		Nodes: []slurmNode{{Name: "A100", State: "down"}},
		Jobs:  []slurmJob{{ID: "86701", Name: "test-job", State: "RUNNING"}},
	}}}
	for _, tc := range []struct {
		mode colorMode
		bg   string
	}{
		{colorModeDark, "\x1b[48;5;237m"},
		{colorModeLight, "\x1b[48;5;253m"},
	} {
		m.colorMode = tc.mode
		for _, expanded := range []bool{false, true} {
			m.queueExpanded = expanded
			selected := false
			for _, line := range strings.Split(m.View().Content, "\n") {
				plain := ansi.Strip(line)
				if strings.HasPrefix(plain, "SLURM QUEUE") && strings.Contains(line, "\x1b[48;") {
					t.Fatalf("queue title should not have a solid background: %q", line)
				}
				if strings.Contains(plain, "▸") || strings.Contains(plain, "›") {
					selected = true
					if !strings.Contains(line, tc.bg) {
						t.Fatalf("selected row should have a neutral background: %q", line)
					}
				}
				if strings.HasPrefix(plain, "NODE") && strings.Contains(plain, "SLURM") {
					t.Fatalf("rack still has a scheduler column: %q", plain)
				}
			}
			if !selected {
				t.Fatalf("no focus marker: mode=%v, queue expanded=%v", tc.mode, expanded)
			}
		}
	}
}

func TestHubResizesLinksAndPreservesNodeSelection(t *testing.T) {
	m := compactTestHub(147, 32)
	m.linksView, m.linksExplicit, m.cursor = true, true, 1
	m.links = hubLinkGraph{sampledAt: time.Now(), sampled: 2, eligible: 2, edges: []hubLinkEdge{
		{a: 0, b: 14, ab: 1024, known: true},
		{a: 1, b: 15, ab: 512, known: true},
	}}
	view := func() string { return ansi.Strip(m.View().Content) }
	resize := func(width, height int) {
		m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if m.cursor != 1 || lipgloss.Width(m.View().Content) > width {
			t.Fatalf("resize to %dx%d lost selection or overflowed", width, height)
		}
	}
	if !strings.Contains(view(), "A100 ↔ NAS") {
		t.Fatal("wide sidebar is missing")
	}
	resize(100, 24)
	if !strings.Contains(view(), "A100 ↔ NAS") {
		t.Fatal("middle width must use the link list")
	}
	resize(44, 8)
	if !strings.Contains(view(), "A100 ↔ NAS") {
		t.Fatal("narrowing should keep the selected Links view open")
	}
	m.Update(testKey("n"))
	if !strings.Contains(view(), "4090") || !strings.Contains(view(), "n  links") {
		t.Fatal("phone rack is missing or links are unreachable")
	}
	m.Update(testKey("n"))
	if !strings.Contains(view(), "CLUSTER LINKS") || !strings.Contains(view(), "A100 ↔ NAS") {
		t.Fatal("phone links view did not open")
	}
	if _, ok := m.nodeAt(0, 3); ok {
		t.Fatal("a link row is not a node click target")
	}
	m.Update(testKey("pgdown"))
	if !strings.Contains(view(), "2–2 / 2") {
		// A 5-row rack fits both pairs; shrink further to test one pair per page.
		resize(44, 6)
		m.Update(testKey("pgdown"))
		if !strings.Contains(view(), "2/2 B/s") {
			t.Fatal("phone links cannot page to the last pair")
		}
	}
	m.Update(testKey("esc"))
	if m.hubCompactLinks() || m.cursor != 1 {
		t.Fatal("leaving phone links did not restore the node")
	}
	resize(147, 32)
	if !strings.Contains(view(), "NODE ·") || strings.Contains(view(), "CLUSTER LINKS") {
		t.Fatal("widening did not preserve the metrics view")
	}
}
