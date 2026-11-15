package main

import (
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type slurmProcessProgress struct {
	JobID uint32          `json:"job_id"`
	UID   int             `json:"uid"`
	PID   int             `json:"pid"`
	Value processProgress `json:"progress"`
}

func slurmProgressPercent(p processProgress) int {
	rate := int(percent(p.Done, p.Total))
	if p.Done < p.Total {
		return min(99, rate)
	}
	return rate
}

func renderSlurmJobName(job slurmDisplayJob, width int, base lipgloss.Style) string {
	if width < 8 {
		return base.Render(fixedCell(job.Job.Name, max(0, width), false))
	}
	style := accentStyle.Inherit(base)
	var indicator string
	if len(job.Reports) == 1 {
		rep := job.Reports[0].Value
		rate := slurmProgressPercent(rep)
		indicator = style.Render(fmt.Sprintf("%3d%%", rate))
		if barWidth := min(9, width-13); barWidth >= 3 {
			fill := (rate*barWidth + 50) / 100
			if rate < 100 {
				fill = min(fill, barWidth-1)
			}
			indicator = style.Render(strings.Repeat("━", fill)) +
				dimStyle.Inherit(base).Render(strings.Repeat("─", barWidth-fill)) + base.Render(" ") + indicator
		}
		if rep.Label != "" && width-lipgloss.Width(job.Job.Name)-lipgloss.Width(indicator) >= len(rep.Label)+6 {
			indicator += dimStyle.Inherit(base).Render(" · " + rep.Label)
		}
	} else if width >= 15 {
		indicator = style.Render(fmt.Sprintf("%d reports", len(job.Reports)))
	} else {
		indicator = style.Render(fmt.Sprintf("%d×", len(job.Reports)))
	}
	maxName := max(4, width-2-lipgloss.Width(indicator))
	nameText := ansi.Truncate(job.Job.Name, maxName, "…")
	combined := base.Render(nameText+"  ") + indicator
	pad := max(0, width-lipgloss.Width(combined))
	return combined + base.Render(strings.Repeat(" ", pad))
}

func (r *progressReceiver) slurmProgress() []slurmProcessProgress {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.prune(time.Now())
	entries := maps.Clone(r.entries)
	r.mu.Unlock()
	var reports []slurmProcessProgress
	for pid, entry := range entries {
		if id := processSlurmJobID(pid, entry.uid, entry.start); id != 0 {
			reports = append(reports, slurmProcessProgress{JobID: id, UID: entry.uid, PID: pid, Value: entry.value})
		}
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].PID < reports[j].PID })
	return reports
}

// Recognize Slurm's numeric job cgroup layouts, not job IDs provided by a
// reporter or a job_ directory nested in a delegated user subtree.
func slurmCgroupJobID(cgroups string) uint32 {
	var jobID uint32
	for line := range strings.SplitSeq(cgroups, "\n") {
		_, rest, _ := strings.Cut(line, ":")
		controller, path, ok := strings.Cut(rest, ":")
		if !ok || !strings.HasPrefix(path, "/") {
			continue
		}
		parts := strings.Split(path[1:], "/")
		if len(parts) < 3 {
			continue
		}
		switch {
		case controller == "" && parts[0] == "system.slice" &&
			(parts[1] == "slurmstepd.scope" || strings.HasSuffix(parts[1], "_slurmstepd.scope")):
			parts = parts[2:]
		case controller != "" && (parts[0] == "slurm" || strings.HasPrefix(parts[0], "slurm_")):
			parts = parts[1:]
			if strings.HasPrefix(parts[0], "uid_") {
				parts = parts[1:]
			}
		default:
			continue
		}
		if len(parts) < 2 || !strings.HasPrefix(parts[1], "step_") {
			continue
		}
		suffix, ok := strings.CutPrefix(parts[0], "job_")
		if !ok {
			continue
		}
		id, err := strconv.ParseUint(suffix, 10, 32)
		if err != nil || id == 0 {
			continue
		}
		if jobID != 0 && jobID != uint32(id) {
			return 0
		}
		jobID = uint32(id)
	}
	return jobID
}

type slurmProgressKey struct {
	cluster string
	id      uint32
	uid     int
}

type slurmNodeReport struct {
	node   string
	report slurmProcessProgress
}

type slurmProgressIndex map[slurmProgressKey][]slurmNodeReport

func (m *hubModel) hubSlurmProgress(now time.Time) slurmProgressIndex {
	var index slurmProgressIndex
	maxAge := 2 * m.config.refreshInterval()
	if maxAge < 6*time.Second {
		maxAge = 6 * time.Second
	}
	for i, node := range m.config.Nodes {
		if node.SlurmCluster == "" || node.SlurmNode == "" || i >= len(m.states) {
			continue
		}
		state := m.states[i]
		if state.Error != "" || state.Delay != "" || state.Warning != "" ||
			now.Sub(state.LastSeen) >= maxAge {
			continue
		}
		for _, report := range state.Snapshot.SlurmProgress {
			p := report.Value
			if report.JobID == 0 || p.Stale || p.Total == 0 || p.Done > p.Total {
				continue
			}
			if index == nil {
				index = make(slurmProgressIndex)
			}
			key := slurmProgressKey{node.SlurmCluster, report.JobID, report.UID}
			index[key] = append(index[key], slurmNodeReport{node.SlurmNode, report})
		}
	}
	return index
}

func (index slurmProgressIndex) forJob(cluster string, job slurmJob) []slurmProcessProgress {
	if job.RawID == 0 || !isSlurmRunning(job.State) {
		return nil
	}
	var reports []slurmProcessProgress
	for _, entry := range index[slurmProgressKey{cluster, job.RawID, job.UID}] {
		if slurmNodeListContains(job.NodeList, entry.node) {
			reports = append(reports, entry.report)
		}
	}
	return reports
}
