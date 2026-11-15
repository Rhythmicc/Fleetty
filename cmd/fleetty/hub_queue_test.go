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

func TestHubQueueReportsStaleSourceOnce(t *testing.T) {
	m := compactTestHub(160, 40)
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: time.Now().Add(-time.Hour),
		Jobs: []slurmJob{{ID: "88001", State: "RUNNING"}},
	}}}
	for _, width := range []int{24, 36, 62, 160} {
		text := ansi.Strip(strings.Join(m.renderHubQueuePreview(width, 7), "\n"))
		if strings.Count(text, "STALE") != 1 || (width >= 48 && !strings.Contains(text, "88001")) {
			t.Errorf("width %d should show a single stale heading without losing visible jobs: %q", width, text)
		}
	}
}

func TestHubQueueShowsNextOnlyWithSlurmEstimate(t *testing.T) {
	m := compactTestHub(160, 40)
	m.slurmFilter = -1
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	const estimate = "2026-11-13T18:30:00"
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: time.Now(),
		Jobs: []slurmJob{
			{ID: "50001", Name: "waiting", User: "bob", State: "PENDING", Priority: 500, Reason: "Priority"},
			{ID: "50002", Name: "scheduled", User: "alice", State: "PENDING", ExpectedStart: estimate, Reason: "Resources"},
			{ID: "50003", Name: "running", User: "carol", State: "RUNNING"},
		},
	}}}
	jobs := m.selectedSlurmJobs()
	if len(jobs) != 3 || jobs[0].Job.ID != "50003" || jobs[1].Job.ID != "50002" {
		t.Fatalf("estimated job should follow running jobs: %+v", jobs)
	}
	for _, width := range []int{24, 62, 160} {
		m.width = width
		heading, _ := m.hubQueueHeading(width, -1, jobs, "")
		if width == 24 {
			if !strings.Contains(ansi.Strip(heading), "R 1  N 1  P 1") {
				t.Fatalf("narrow queue summary: %q", heading)
			}
			continue
		}
		m.queueExpanded = true
		for _, lines := range [][]string{m.renderHubQueuePreview(width, 12), m.renderHubQueueExpanded(width)} {
			view := ansi.Strip(strings.Join(lines, "\n"))
			want := "N"
			if width == 160 {
				want = "NEXT"
			}
			if !strings.Contains(view, "NEXT 1") || !strings.Contains(view, "PENDING 1") {
				t.Fatalf("width %d, NEXT must not be double counted: %q", width, view)
			}
			if !strings.Contains(strings.Join(strings.Fields(view), " "), "50002 alice "+want+" ") {
				t.Fatalf("width %d, estimated job is missing its NEXT status: %q", width, view)
			}
		}
	}
	detail := ansi.Strip(strings.Join(m.slurmExplanationLines(jobs[1], 160, 12), "\n"))
	if !strings.Contains(detail, "EST. START  2026-11-13 18:30:00") {
		t.Fatalf("job details should show controller estimate: %q", detail)
	}
}

func TestHubQueueKeyboardKeepsRackSelectionAndPagesAllJobs(t *testing.T) {
	m := compactTestHub(62, 15)
	m.cursor = 5
	m.slurmFilter = -1
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	snapshot := slurmSnapshot{Name: "GPU", CollectedAt: time.Now()}
	for i := range 42 {
		snapshot.Jobs = append(snapshot.Jobs, slurmJob{
			ID: fmt.Sprintf("JOB%03d", i), User: "alice", State: "PENDING", Reason: "Priority",
		})
	}
	m.slurmStates = []slurmClusterState{{Snapshot: snapshot}}
	m.Update(testKey("tab"))
	if m.queueExpanded {
		t.Fatal("Tab still opens a separate queue")
	}
	plain := func() string { return ansi.Strip(m.View().Content) }
	if view := plain(); !strings.Contains(view, "SLURM QUEUE") ||
		!strings.Contains(view, "PENDING 42") || !strings.Contains(view, "expand queue") {
		t.Fatalf("missing queue summary or shortcut in narrow Hub:\n%s", view)
	}
	m.Update(testKey("s"))
	if view := plain(); !m.queueExpanded || !strings.Contains(view, "GPU COMPUTE") ||
		!strings.Contains(view, "JOB000") || strings.Contains(view, "SLURM QUEUES") {
		t.Fatalf("queue did not expand in place:\n%s", view)
	}
	for range 42 {
		m.Update(testKey("pgdown"))
	}
	if view := plain(); m.slurmCursor != 41 || !strings.Contains(view, "JOB041") {
		t.Fatalf("last job was not reachable, cursor=%d:\n%s", m.slurmCursor, view)
	}
	m.Update(testKey("enter"))
	if view := plain(); !m.slurmExplain || !strings.Contains(view, "WHY IT IS NOT RUNNING") ||
		!strings.Contains(view, "CONTROLLER") {
		t.Fatalf("missing inline explanation:\n%s", view)
	}
	m.Update(testKey("esc"))
	if m.slurmExplain || !m.queueExpanded || m.slurmCursor != 41 {
		t.Fatal("Esc should return to the same job")
	}
	m.Update(testKey("esc"))
	if m.queueExpanded || m.cursor != 5 {
		t.Fatal("Esc should collapse the queue and keep the node")
	}
	m.Update(testKey("s"))
	if !m.queueExpanded || m.slurmCursor != 41 {
		t.Fatal("reopening the queue should restore the job")
	}
}

func TestHubQueuePreviewUsesSpareRowsAndAllClusters(t *testing.T) {
	m := compactTestHub(160, 40)
	now := time.Now()
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}, {Name: "CPU"}, {Name: "Unreachable"}}
	m.slurmStates = []slurmClusterState{
		{Snapshot: slurmSnapshot{Name: "GPU", CollectedAt: now, Jobs: []slurmJob{
			{ID: "12345_3", Name: "train-model", User: "alice", State: "RUNNING", NodeList: "gpu01", Elapsed: "3:30"},
		}}},
		{Snapshot: slurmSnapshot{Name: "CPU", CollectedAt: now, Jobs: []slurmJob{
			{ID: "12346", Name: "next-build", User: "bob", State: "PENDING", Partition: "cpu", Reason: "Resources"},
			{ID: "12347", Name: "blocked-build", User: "bob", State: "PENDING", Partition: "cpu", Reason: "Dependency"},
		}}},
		{Error: "timeout", Snapshot: slurmSnapshot{Name: "Unreachable", CollectedAt: now, Jobs: []slurmJob{
			{ID: "12348", Name: "stale-job", State: "RUNNING"},
		}}},
	}
	m.slurmFilter = 1
	m.linksView = true
	m.links = hubLinkGraph{sampledAt: now, eligible: 1, sampled: 1, edges: []hubLinkEdge{{a: 0, b: 14, known: true, ab: 1}}}
	for _, width := range []int{160, 62} {
		for _, mode := range []colorMode{colorModeDark, colorModeLight} {
			m.width, m.colorMode = width, mode
			view := m.View().Content
			plain := ansi.Strip(view)
			for _, want := range []string{"SLURM QUEUE", "RUN 1", "PENDING 2", "JOB ID", "12345", "alice", "train-model", "next-build", "blocked-build"} {
				if !strings.Contains(plain, want) {
					t.Errorf("width %d, theme %v: missing %q in %s", width, mode, want, plain)
				}
			}
			if width >= 94 && (strings.Index(plain, "A100 ↔ NAS") < 0 ||
				strings.Index(plain, "A100 ↔ NAS") > strings.Index(plain, "SLURM QUEUE") ||
				!strings.Contains(plain, "NODE") || strings.Contains(plain, "BLOCKER") || strings.Contains(plain, "DEPENDENCY")) {
				t.Errorf("queue must follow the complete links sidebar: %s", plain)
			}
			if strings.Contains(plain, "stale-job") || strings.Contains(plain, "12345_3") || m.slurmFilter != 1 {
				t.Errorf("preview changed filtering or used stale/unformatted jobs: %s", plain)
			}
			if lipgloss.Width(view) > width || lipgloss.Height(view) != m.height {
				t.Errorf("preview overflow at %d×%d: %d×%d", width, m.height, lipgloss.Width(view), lipgloss.Height(view))
			}
			start := strings.Index(plain, "SLURM QUEUE")
			if start < 0 {
				t.Fatal("queue preview did not render")
			}
			queueY := strings.Count(plain[:start], "\n")
			if _, ok := m.nodeAt(0, queueY+2); ok {
				t.Fatal("queue preview must not overlap node click targets")
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 25})
	plain := ansi.Strip(m.hubView())
	if !strings.Contains(plain, "SLURM QUEUE") || !strings.Contains(plain, "192.168.31.156") || !strings.Contains(plain, "CLUSTER LINKS") {
		t.Fatalf("compact terminal must keep nodes, links, and a queue summary: %s", plain)
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	for i := 0; i < 48; i++ {
		m.links.edges = append(m.links.edges, hubLinkEdge{a: 0, b: 14, known: true, ab: 1})
	}
	plain = ansi.Strip(m.hubView())
	if !strings.Contains(plain, "SLURM QUEUE") || !strings.Contains(plain, "1–30 / 49") {
		t.Fatalf("a full links page should leave one row for the queue summary: %s", plain)
	}
}

func TestHubQueuePreviewMatchesFullQueueAtSameWidth(t *testing.T) {
	const jobName = "train-model-with-context1"
	m := compactTestHub(160, 48)
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.slurmFilter = -1
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: time.Now(),
		Nodes: []slurmNode{{
			Name: "gpu01", Partitions: []string{"gpu"}, State: "mix",
			CPUsIdle: 8, MemoryTotalBytes: 64 << 30,
		}},
		Jobs: []slurmJob{
			{ID: "22001", Name: jobName, User: "alice", State: "RUNNING", NodeList: "gpu01", QOS: "normal",
				Elapsed: "4:20", TimeLimit: "30:00"},
			{ID: "22002", Name: "blocked-build", User: "bob", State: "PENDING", Partition: "gpu", QOS: "high",
				Priority: 200, Elapsed: "0:00", TimeLimit: "UNLIMITED", Nodes: 1, Reason: "Resources", MemoryBytes: 128 << 30},
		},
	}}}
	table := func(view string) []string {
		t.Helper()
		lines := strings.Split(ansi.Strip(view), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "JOB ID") {
				continue
			}
			if len(lines)-i < 3 {
				t.Fatalf("missing job rows after table header: %q", lines[i:])
			}
			rows := lines[i : i+3]
			for j, row := range rows {
				// Treat the full panel's border as an inset, not a column.
				if inner, ok := strings.CutPrefix(row, "│"); ok {
					row = " " + strings.TrimSuffix(inner, "│")
				}
				rows[j] = strings.TrimRight(row, " ")
			}
			return rows
		}
		t.Fatal("missing job table")
		return nil
	}
	for _, width := range []int{62, 115, 116, 117, 155, 156, 160, 200} {
		m.width = width
		for _, theme := range []colorMode{colorModeDark, colorModeLight} {
			m.colorMode = theme
			m.queueExpanded = false
			preview := table(m.View().Content)
			m.queueExpanded = true
			full := table(m.View().Content)
			for i, row := range preview {
				if row != strings.Replace(full[i], "› ", "  ", 1) {
					t.Errorf("width %d, theme %v, row %d differs\npreview: %q\nfull:    %q", width, theme, i, row, full[i])
				}
				if lipgloss.Width(row) > width {
					t.Errorf("width %d: preview row overflows: %q", width, row)
				}
			}
			wide := width-4 >= slurmCompactWidth
			firstJob := strings.TrimPrefix(strings.TrimSpace(preview[1]), "● ")
			if strings.Contains(preview[0], "CLUSTER") || !strings.HasPrefix(firstJob, "22001") ||
				strings.Contains(preview[0], "WEIGHT") != wide ||
				strings.HasSuffix(preview[2], "—") != (width-4 >= 72) ||
				strings.Contains(preview[0], "LIMIT") ||
				strings.Contains(preview[0], "TIME") != wide ||
				strings.Contains(preview[1], "4:20 / 30:00") != wide ||
				strings.Contains(preview[2], "0:00 / UNLIMITED") != wide {
				t.Errorf("width %d: wrong column tier or blocker: %q", width, preview)
			}
			if wide && !strings.Contains(strings.Join(strings.Fields(preview[0]), " "), "TIME NODES") {
				t.Errorf("width %d: expected a single time column: %q", width, preview[0])
			}
			if width >= 117 && !strings.Contains(preview[1], jobName) {
				t.Errorf("width %d: the combined time column should leave space for the full job name: %q", width, preview[1])
			}
		}
	}
	detail := ansi.Strip(strings.Join(m.slurmExplanationLines(m.selectedSlurmJobs()[0], 62, 18), "\n"))
	if !strings.Contains(detail, "TIME        4:20 / 30:00") {
		t.Fatalf("narrow job details lost the elapsed/limit pair: %q", detail)
	}
}

func TestHubQueueClusterColumnOnlyForCombinedView(t *testing.T) {
	m := compactTestHub(160, 40)
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "Alpha"}, {Name: "Beta"}}
	const jobName = "job-name-with-context-01"
	for i, cluster := range m.config.SlurmClusters {
		m.slurmStates = append(m.slurmStates, slurmClusterState{Snapshot: slurmSnapshot{
			Name: cluster.Name, CollectedAt: time.Now(),
			Jobs: []slurmJob{{ID: fmt.Sprintf("2200%d", i), Name: jobName, State: "RUNNING"}},
		}})
	}
	for _, width := range []int{100, 116, 124, 164} {
		m.width = width
		m.slurmFilter = -1
		preview := ansi.Strip(strings.Join(m.renderHubQueuePreview(width, 12), "\n"))
		m.queueExpanded = true
		expanded := ansi.Strip(strings.Join(m.renderHubQueueExpanded(width), "\n"))
		for _, view := range []string{preview, expanded} {
			if !strings.Contains(view, "CLUSTER") || !strings.Contains(view, "Alpha") || !strings.Contains(view, "Beta") {
				t.Fatalf("%d: combined queue needs cluster names: %q", width, view)
			}
			if width >= 116 && (!strings.Contains(view, "TIME") || !strings.Contains(view, "NODE") || strings.Contains(view, "BLOCKER")) {
				t.Fatalf("%d: combined queue must show time and a node-only column: %q", width, view)
			}
		}
		m.slurmFilter = 0
		filtered := ansi.Strip(strings.Join(m.renderHubQueueExpanded(width), "\n"))
		if strings.Contains(filtered, "CLUSTER") || strings.Contains(filtered, "Beta") || !strings.Contains(filtered, "22000") {
			t.Fatalf("%d: filtered queue shouldn't repeat the cluster column: %q", width, filtered)
		}
		if width >= 124 && !strings.Contains(filtered, jobName) {
			t.Fatalf("%d: reclaimed columns should make room for the job name: %q", width, filtered)
		}
	}
}

func TestHubQueuePreviewJobBudgetAndUnavailableStates(t *testing.T) {
	m := &hubModel{config: hubConfig{SlurmClusters: []slurmClusterConfig{{Name: "GPU"}}}}
	preview := func(height int) string {
		return ansi.Strip(strings.Join(m.renderHubQueuePreview(160, height), "\n"))
	}
	if got := preview(5); !strings.Contains(got, "Waiting for Slurm") || strings.Contains(got, "RUN 0") {
		t.Fatalf("waiting state shown as empty queue: %q", got)
	}
	m.slurmStates = []slurmClusterState{{Error: "timeout"}}
	if got := preview(5); !strings.Contains(got, "unavailable") || strings.Contains(got, "RUN 0") {
		t.Fatalf("unavailable state shown as empty queue: %q", got)
	}
	m.slurmStates[0] = slurmClusterState{Snapshot: slurmSnapshot{CollectedAt: time.Now()}}
	if got := preview(5); !strings.Contains(got, "No jobs") || !strings.Contains(got, "RUN 0") {
		t.Fatalf("missing empty live queue state: %q", got)
	}
	m.slurmStates[0].Snapshot.Jobs = []slurmJob{
		{ID: "12345", Name: "first-job", State: "RUNNING"},
		{ID: "12346", Name: "second-job", State: "RUNNING"},
		{ID: "12347", Name: "third-job", State: "RUNNING"},
	}
	if got := preview(5); !strings.Contains(got, "RUN 3") || !strings.Contains(got, "first-job") ||
		!strings.Contains(got, "+2 more jobs") || strings.Contains(got, "second-job") || strings.Count(got, "\n") != 4 {
		t.Fatalf("wrong preview row budget: %q", got)
	}
}
