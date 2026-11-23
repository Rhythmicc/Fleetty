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
			if width >= 94 && (!strings.Contains(plain, "NODE · A100") ||
				!strings.Contains(plain, "PARTITION") || strings.Contains(plain, "BLOCKER") || strings.Contains(plain, "DEPENDENCY")) {
				t.Errorf("queue must follow the inspector sidebar: %s", plain)
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
	if !strings.Contains(plain, "SLURM QUEUE") || !strings.Contains(plain, "NODE · A100") {
		t.Fatalf("compact terminal must keep nodes, inspector, and a queue summary: %s", plain)
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
				strings.Contains(preview[0], "PARTITION") != wide ||
				strings.Contains(preview[2], "gpu ×1") != wide ||
				strings.Contains(preview[0], "LIMIT") ||
				strings.Contains(preview[0], "TIME") != wide ||
				strings.Contains(preview[1], "4:20 / 30:00") != wide ||
				strings.Contains(preview[2], "0:00 / UNLIMITED") != wide {
				t.Errorf("width %d: wrong column tier or blocker: %q", width, preview)
			}
			if wide && !strings.Contains(strings.Join(strings.Fields(preview[0]), " "), "TIME PARTITION NAME") {
				t.Errorf("width %d: expected merged partition and time columns: %q", width, preview[0])
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
			if width >= 116 && (!strings.Contains(view, "TIME") || !strings.Contains(view, "PARTITION") || strings.Contains(view, "BLOCKER")) {
				t.Fatalf("%d: combined queue must show time and partition without blocker: %q", width, view)
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

func TestSlurmNextMarksOnlyEarliestEstimatePerTarget(t *testing.T) {
	m := compactTestHub(160, 40)
	m.slurmFilter = -1
	m.config.Nodes = []hubNodeConfig{{Name: "A100", SlurmCluster: "GPU", SlurmNode: "a100"}}
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: time.Now(),
		Nodes: []slurmNode{
			{Name: "a100", Partitions: []string{"a100"}},
			{Name: "gpu5090", Partitions: []string{"gpu5090"}},
			{Name: "gpu4090", Partitions: []string{"gpu4090", "gpu4090-long"}},
		},
		Jobs: []slurmJob{
			{ID: "355527", Name: "high-pri-na", User: "hemeng", State: "PENDING", Priority: 10625, Partition: "a100", Reason: "Resources"},
			{ID: "355274", Name: "a100-second", User: "junyuan", State: "PENDING", Priority: 6501, Partition: "a100", ExpectedNodes: "a100", ExpectedStart: "2026-11-20T10:24:00", Reason: "Priority"},
			{ID: "355155", Name: "a100-first", User: "junyuan", State: "PENDING", Priority: 6501, Partition: "a100", ExpectedNodes: "a100", ExpectedStart: "2026-11-20T10:22:00", Reason: "Priority"},
			{ID: "355509", Name: "a100-third", User: "nyy", State: "PENDING", Priority: 3791, Partition: "a100", ExpectedNodes: "a100", ExpectedStart: "2026-11-20T10:44:00", Reason: "Priority"},
			{ID: "355140", Name: "a100-running", User: "junyuan", State: "RUNNING", Priority: 6501, Partition: "a100", NodeList: "a100"},
			{ID: "355400", Name: "5090-tie-a", User: "nyy", State: "PENDING", Priority: 7489, Partition: "gpu5090", ExpectedNodes: "gpu5090", ExpectedStart: "2026-11-20T10:26:00", Reason: "Resources"},
			{ID: "355401", Name: "5090-tie-b", User: "bob", State: "PENDING", Priority: 7000, Partition: "gpu5090", ExpectedNodes: "gpu5090", ExpectedStart: "2026-11-20T10:26:00", Reason: "Priority"},
			{ID: "355600", Name: "4090-long", User: "carol", State: "PENDING", Priority: 8000, Partition: "gpu4090-long", ExpectedNodes: "gpu4090", ExpectedStart: "2026-11-20T11:00:00", Reason: "Priority"},
			{ID: "355601", Name: "4090-short", User: "dave", State: "PENDING", Priority: 5000, Partition: "gpu4090", ExpectedNodes: "gpu4090", ExpectedStart: "2026-11-20T10:30:00", Reason: "Resources"},
		},
	}}}

	jobs := m.selectedSlurmJobs()
	byID := make(map[string]slurmDisplayJob, len(jobs))
	var order []string
	for _, job := range jobs {
		byID[job.Job.ID] = job
		order = append(order, job.Job.ID)
	}
	if running, next, pending := slurmDisplayStateCounts(jobs); running != 1 || next != 4 || pending != 4 {
		t.Fatalf("counts = (run=%d, next=%d, pending=%d), want (1, 4, 4)", running, next, pending)
	}
	wantNext := map[string]bool{
		"355155": true,
		"355400": true,
		"355401": true,
		"355601": true,
	}
	for id, job := range byID {
		if job.Next != wantNext[id] {
			t.Fatalf("job %s Next = %v, want %v", id, job.Next, wantNext[id])
		}
		state, _ := slurmDisplayState(job)
		if wantNext[id] && state != "NEXT" {
			t.Fatalf("job %s state = %q, want NEXT", id, state)
		}
		if !wantNext[id] && !isSlurmRunning(job.Job.State) && state != "PENDING" {
			t.Fatalf("job %s state = %q, want PENDING", id, state)
		}
	}
	wantOrder := []string{"355140", "355155", "355400", "355401", "355601", "355527", "355274", "355509", "355600"}
	if fmt.Sprint(order) != fmt.Sprint(wantOrder) {
		t.Fatalf("cluster job order = %v, want %v", order, wantOrder)
	}
	detail := ansi.Strip(strings.Join(m.slurmExplanationLines(byID["355274"], 160, 14), "\n"))
	if !strings.Contains(detail, "EST. START  2026-11-20 10:24:00") {
		t.Fatalf("pending job with estimate lost EST. START in detail: %q", detail)
	}

	queue := m.nodeSlurmQueue(0)
	if queue == nil {
		t.Fatal("expected node Slurm queue for a100")
	}
	if running, next, pending := slurmDisplayStateCounts(queue.Jobs); running != 1 || next != 1 || pending != 3 {
		t.Fatalf("a100 node queue counts = (run=%d, next=%d, pending=%d), want (1, 1, 3)", running, next, pending)
	}
	if len(queue.Jobs) != 5 || queue.Jobs[1].Job.ID != "355155" || !queue.Jobs[1].Next {
		t.Fatalf("a100 node queue[1] = %+v, want 355155 as sole NEXT", queue.Jobs)
	}
	if queue.Jobs[2].Job.ID != "355527" || queue.Jobs[3].Job.ID != "355274" || queue.Jobs[4].Job.ID != "355509" {
		t.Fatalf("a100 node queue pending order = %+v", queue.Jobs[2:])
	}
}

func TestSlurmJobTaskCount(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"359602", 1},
		{"359022_43", 1},
		{"312889_[2-5%1]", 4},
		{"18421_[2-8]", 7},
		{"144886_[8-15%2]", 8},
		{"359022_[1,3,10-20:2%4]", 8},
		{"359022_[0-99:10]", 10},
		{"359022_[5]", 1},
		{"359022_[5-5]", 1},
		{"", 1},
		{"359022_[]", 1},
		{"359022_[%2]", 1},
		{"359022_[5-2]", 1},
		{"359022_[1-5:0]", 1},
		{"359022_[-1]", 1},
		{"359022_[1-3]+0", 1},
	} {
		if got := slurmJobTaskCount(tc.id); got != tc.want {
			t.Errorf("slurmJobTaskCount(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
}

func TestSlurmQueueGroupsArrayTasksByBaseIDAndState(t *testing.T) {
	now := time.Now()
	m := compactTestHub(160, 40)
	m.slurmFilter = -1
	m.config.Nodes = []hubNodeConfig{
		{Name: "gpu01", SlurmCluster: "GPU", SlurmNode: "gpu01"},
		{Name: "gpu02", SlurmCluster: "GPU", SlurmNode: "gpu02"},
	}
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.states = []hubNodeState{
		{LastSeen: now, Snapshot: monitorSnapshot{SlurmProgress: []slurmProcessProgress{
			{JobID: 359101, UID: 1000, PID: 101, Value: processProgress{Done: 40, Total: 100}},
			{JobID: 359102, UID: 1000, PID: 102, Value: processProgress{Done: 60, Total: 100}},
		}}},
		{LastSeen: now},
	}
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		Name: "GPU", CollectedAt: now,
		Nodes: []slurmNode{{Name: "gpu01", Partitions: []string{"gpu"}}},
		Jobs: []slurmJob{
			{ID: "359602", User: "alice", State: "RUNNING", Partition: "gpu", NodeList: "gpu01", Name: "solo-job"},
			{ID: "359022_43", User: "bob", State: "PENDING", Priority: 900, Partition: "gpu", Reason: "Priority", Name: "sweep"},
			{ID: "359022_42", User: "bob", State: "PENDING", Priority: 900, Partition: "gpu", Reason: "Priority", Name: "sweep"},
			{ID: "359100_1", RawID: 359101, UID: 1000, User: "alice", State: "RUNNING", Partition: "gpu", NodeList: "gpu01", Name: "train"},
			{ID: "359100_2", RawID: 359102, UID: 1000, User: "alice", State: "RUNNING", Partition: "gpu", NodeList: "gpu01", Name: "train"},
			{ID: "359100_[3-6%2]", User: "alice", State: "PENDING", Priority: 800, Partition: "gpu", Reason: "JobArrayTaskLimit", Name: "train"},
			{ID: "359200_1", User: "carol", State: "PENDING", Priority: 700, Partition: "gpu", ExpectedNodes: "gpu01", ExpectedStart: "2026-11-21T12:00:00", Reason: "Priority", Name: "eval"},
			{ID: "359200_[2-4]", User: "carol", State: "PENDING", Priority: 700, Partition: "gpu", ExpectedNodes: "gpu01", Reason: "Priority", Name: "eval"},
		},
	}}}

	jobs := m.selectedSlurmJobs()
	if len(jobs) != 6 {
		t.Fatalf("grouped jobs len = %d, want 6: %+v", len(jobs), jobs)
	}
	byListID := make(map[string]slurmDisplayJob, len(jobs))
	for _, job := range jobs {
		byListID[job.listID()] = job
	}
	if _, ok := byListID["359602"]; !ok || strings.Contains(strings.Join(func() []string {
		var ids []string
		for k := range byListID {
			ids = append(ids, k)
		}
		return ids
	}(), " "), "×1") {
		t.Fatalf("unexpected ×1 suffix or missing solo job: %#v", byListID)
	}
	if sweep, ok := byListID["359022×2"]; !ok || sweep.TaskCount != 2 || len(sweep.MemberIDs) != 2 ||
		sweep.MemberIDs[0] != "359022_43" || sweep.MemberIDs[1] != "359022_42" {
		t.Fatalf("expected 359022×2 grouped from _43 and _42: %+v", byListID)
	}
	if runTrain, ok := byListID["359100×2"]; !ok || len(runTrain.Reports) != 2 || runTrain.Job.State != "RUNNING" {
		t.Fatalf("expected RUNNING 359100×2 with 2 reports: %+v", byListID)
	}
	if pendTrain, ok := byListID["359100×4"]; !ok || pendTrain.Job.State != "PENDING" {
		t.Fatalf("expected PENDING 359100×4: %+v", byListID)
	}
	if nextEval, ok := byListID["359200"]; !ok || !nextEval.Next || nextEval.TaskCount != 1 {
		t.Fatalf("expected NEXT 359200 (Count=1): %+v", byListID)
	}
	if pendEval, ok := byListID["359200×3"]; !ok || pendEval.Next {
		t.Fatalf("expected PENDING 359200×3: %+v", byListID)
	}

	detail := ansi.Strip(strings.Join(m.slurmExplanationLines(byListID["359022×2"], 120, 20), "\n"))
	if !strings.Contains(detail, "GPU / 359022×2") || !strings.Contains(detail, "359022_43, 359022_42") {
		t.Fatalf("grouped details missing aggregated title or member IDs:\n%s", detail)
	}

	queue := m.nodeSlurmQueue(0)
	if queue == nil {
		t.Fatal("expected node queue")
	}
	nodeIDs := make(map[string]bool, len(queue.Jobs))
	for _, job := range queue.Jobs {
		nodeIDs[job.listID()] = true
	}
	for _, want := range []string{"359602", "359022×2", "359100×2", "359100×4", "359200", "359200×3"} {
		if !nodeIDs[want] {
			t.Fatalf("node queue missing %q: %#v", want, nodeIDs)
		}
	}

	// Retain cursor on 359022 when 359022_43 finishes/leaves and 359022_42 remains.
	for i, job := range jobs {
		if job.listID() == "359022×2" {
			m.slurmCursor = i
			break
		}
	}
	m.rememberSlurmSelection()
	m.slurmStates[0].Snapshot.Jobs = append(m.slurmStates[0].Snapshot.Jobs[:1], m.slurmStates[0].Snapshot.Jobs[2:]...)
	m.clampSlurmCursor()
	updated := m.selectedSlurmJobs()
	if updated[m.slurmCursor].listID() != "359022" {
		t.Fatalf("cursor moved to %q after _43 left, want 359022", updated[m.slurmCursor].listID())
	}
}
