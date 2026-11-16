package main

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHubLayoutFitsAndLeavesQueueReachable(t *testing.T) {
	for _, width := range []int{24, 44, 60, 93, 94, 113, 114, 125, 147, 190, 220} {
		for _, height := range []int{8, 12, 17, 24, 34} {
			m := compactTestHub(width, height)
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
