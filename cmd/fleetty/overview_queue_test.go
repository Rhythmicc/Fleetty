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
				plain := ansi.Strip(page)
				if strings.Contains(plain, "visible jobs") {
					t.Fatalf("overview queue retained redundant bottom footer:\n%s", plain)
				}
				lines := strings.Split(plain, "\n")
				start, panelWidth := 0, width
				if width >= 96 {
					leftW, rightW := overviewSplitWidths(width)
					start, panelWidth = leftW+1, rightW
				}
				foundHeader, foundJob := false, false
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatalf("line exceeds terminal width: %q", line)
					}
					if strings.Contains(line, "NODE QUEUE / SLURM") {
						foundHeader = true
					}
					if strings.Contains(line, "134323 ") {
						foundJob = true
						row := ansi.Cut(line, start+2, start+panelWidth-2)
						if strings.Contains(row, job.Job.ID) {
							t.Fatalf("queue row includes the array suffix: %q", row)
						}
						want := ansi.Truncate(ansi.Strip(renderSlurmNodeJobRow(job, panelWidth-4)), panelWidth-4, "")
						if strings.TrimSpace(row) != strings.TrimSpace(want) {
							t.Fatalf("queue row uses wrong width (start=%d panelWidth=%d):\n got: %q\nwant: %q", start, panelWidth, row, want)
						}
					}
				}
				if !foundHeader || !foundJob {
					t.Fatal("queue header or job row missing")
				}
			})
		}
	}
}
