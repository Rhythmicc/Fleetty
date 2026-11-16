package main

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSlurmCgroupJobID(t *testing.T) {
	for _, tc := range []struct {
		path string
		want uint32
	}{
		{"0::/system.slice/slurmstepd.scope/job_338751/step_batch/user/task_0", 338751},
		{"0::/system.slice/a100_slurmstepd.scope/job_338751/step_1/user", 338751},
		{"7:freezer:/slurm/uid_1004/job_338751/step_batch\n4:memory:/slurm/uid_1004/job_338751/step_batch", 338751},
		{"0::/user.slice/session.scope/slurmstepd.scope/job_338751/step_batch", 0},
		{"0::/system.slice/slurmstepd.scope/job_10/step_batch/user/job_20/step_fake", 10},
		{"0::/system.slice/slurmstepd.scope/job_4294967296/step_batch", 0},
		{"7:freezer:/slurm/uid_1004/job_10/step_batch\n4:memory:/slurm/uid_1004/job_15/step_batch", 0},
	} {
		if got := slurmCgroupJobID(tc.path); got != tc.want {
			t.Errorf("%q: job ID %d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestHubSlurmReportsMatchAllocationAndRender(t *testing.T) {
	now := time.Now()
	m := compactTestHub(124, 45)
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}, {Name: "Other"}}
	m.config.Nodes = []hubNodeConfig{
		{SlurmCluster: "GPU", SlurmNode: "gpu01"},
		{SlurmCluster: "GPU", SlurmNode: "gpu03"},
		{SlurmCluster: "Other", SlurmNode: "gpu01"},
	}
	report := func(id uint32, uid, pid int, done uint64) slurmProcessProgress {
		return slurmProcessProgress{JobID: id, UID: uid, PID: pid,
			Value: processProgress{Done: done, Total: 100, Label: "epoch 4/6"}}
	}
	expired := report(110, 1000, 122, 99)
	expired.Value.Stale = true
	m.states = []hubNodeState{
		{LastSeen: now, Snapshot: monitorSnapshot{SlurmProgress: []slurmProcessProgress{
			report(110, 1000, 101, 62), report(110, 1001, 103, 90), report(99, 1000, 104, 91), expired,
		}}},
		{LastSeen: now, Snapshot: monitorSnapshot{SlurmProgress: []slurmProcessProgress{report(110, 1000, 105, 92)}}},
		{LastSeen: now, Snapshot: monitorSnapshot{SlurmProgress: []slurmProcessProgress{report(110, 1000, 106, 93)}}},
	}
	job := slurmJob{ID: "99_3", RawID: 110, UID: 1000, State: "RUNNING", NodeList: "gpu[01-02]", Name: "training"}
	pending := slurmJob{ID: "99_[4-5]", RawID: 110, UID: 1000, State: "PENDING",
		NodeList: "gpu01", ExpectedNodes: "gpu01", Reason: "(Dependency)", Name: "pending"}
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{Name: "GPU", CollectedAt: now, Jobs: []slurmJob{job, pending}}}}
	jobs := m.slurmJobs(-1)
	if len(jobs) != 2 || len(jobs[0].Reports) != 1 || jobs[0].Reports[0].PID != 101 || len(jobs[1].Reports) != 0 {
		t.Fatalf("incorrect reports for the running or pending array task: %+v", jobs)
	}
	for _, width := range []int{52, 92, 112, 152} {
		row := ansi.Strip(renderSlurmJobRow(jobs[0], width, false, lipgloss.NewStyle()))
		waiting := strings.TrimSpace(ansi.Strip(renderSlurmJobRow(jobs[1], width, false, lipgloss.NewStyle())))
		header := ansi.Strip(slurmJobTableHeader(width, false))
		if lipgloss.Width(row) != width || !strings.Contains(row, "62%") ||
			strings.Contains(row, "━") != (width >= 92) ||
			strings.Contains(header, "BLOCKER") || strings.Contains(waiting, "DEPENDENCY") ||
			strings.Contains(waiting, "gpu01") || strings.Contains(header, " NODE") {
			t.Fatalf("width %d: unexpected queue:\n%s\n%s\n%s", width, header, row, waiting)
		}
	}
	detail := ansi.Strip(strings.Join(m.slurmExplanationLines(jobs[0], 90, 16), "\n"))
	if !strings.Contains(detail, "PID 101 · 62 / 100 · 62% · epoch 4/6") {
		t.Fatalf("detail lost progress counters: %s", detail)
	}
	m.states[0].Snapshot.SlurmProgress = append(m.states[0].Snapshot.SlurmProgress, report(110, 1000, 125, 83))
	jobs = m.slurmJobs(-1)
	row := ansi.Strip(renderSlurmJobRow(jobs[0], 124, false, lipgloss.NewStyle()))
	if !strings.Contains(row, "2 reports") || strings.Contains(row, "%") {
		t.Fatalf("multiple reports should not produce a synthesized percent: %s", row)
	}
	m.states[0].LastSeen = now.Add(-time.Minute)
	if jobs = m.slurmJobs(-1); len(jobs[0].Reports) != 0 {
		t.Fatal("offline data should not leave a frozen progress bar")
	}
	m.states[0].LastSeen = now
	m.slurmStates[0].Warning = "cached"
	if jobs = m.slurmJobs(-1); len(jobs[0].Reports) != 0 {
		t.Fatal("stale Slurm snapshot should not be matched against fresh reporters")
	}
}
