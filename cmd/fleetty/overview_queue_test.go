package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestOverviewQueueUsesRenderedPanelWidth(t *testing.T) {
	for _, withGPU := range []bool{false, true} {
		for _, width := range []int{60, 96, 160, 241, 242} {
			t.Run(fmt.Sprintf("gpu=%v/width=%d", withGPU, width), func(t *testing.T) {
				job := slurmDisplayJob{Job: slurmJob{
					ID: "134323_19", User: "lhc", State: "RUNNING", Priority: 2675,
					QOS: "normal", Elapsed: "1:08", TimeLimit: "40:00", Partition: "cpu",
					Name: strings.Repeat("long-job-name-", 30),
				}}
				m := &monitorModel{height: 48, snapshot: monitorSnapshot{MemoryTotal: 1, DiskTotal: 1},
					slurmQueue: &nodeSlurmQueue{Cluster: "test", Node: "node", CollectedAt: time.Now(),
						Jobs: []slurmDisplayJob{job}}}
				if withGPU {
					m.snapshot.GPUs = []gpuInfo{{Index: 0, Name: "GPU"}}
				}
				page, _ := m.renderOverviewPage(width)
				lines := strings.Split(ansi.Strip(page), "\n")
				panelWidth, start := width, 0
				if withGPU && width >= 96 {
					panelWidth = (width - 1) / 2
					start = width - panelWidth
				}
				foundRule, foundJob := false, false
				for i, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatalf("line exceeds terminal width: %q", line)
					}
					if strings.Contains(line, "visible jobs") {
						foundRule = true
						rule := ansi.Cut(lines[i-1], start+2, start+panelWidth-2)
						if rule != strings.Repeat("─", panelWidth-4) {
							t.Fatalf("queue rule does not span its %d-column panel: %q", panelWidth, rule)
						}
					}
					if strings.Contains(line, job.Job.ID) {
						foundJob = true
						row := ansi.Cut(line, start+2, start+panelWidth-2)
						want := ansi.Truncate(ansi.Strip(renderSlurmNodeJobRow(job, panelWidth-4)), panelWidth-4, "")
						if strings.TrimSpace(row) != strings.TrimSpace(want) {
							t.Fatalf("queue row uses wrong width:\n got: %q\nwant: %q", row, want)
						}
					}
				}
				if !foundRule || !foundJob {
					t.Fatal("queue job or refresh footer missing")
				}
			})
		}
	}
}
