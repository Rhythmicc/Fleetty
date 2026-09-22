package main

import (
	"hash/fnv"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const slurmCompactWidth = 112

type compactSlurmFormat struct {
	width, id, user, cluster int
}

func newCompactSlurmFormat(width int, cluster bool) compactSlurmFormat {
	f := compactSlurmFormat{
		width: width, id: min(13, max(6, width-31)),
		user: min(8, max(4, width-34)),
	}
	if cluster && width >= 92 {
		f.cluster = 10
	}
	return f
}

func (f compactSlurmFormat) render(display slurmDisplayJob, header bool, base lipgloss.Style) string {
	job := display.Job
	state, stateStyle := slurmDisplayState(display)
	switch state {
	case "RUNNING":
		state = "R"
	case "NEXT":
		state = "N"
	case "QUEUED":
		state = "Q"
	default:
		state = ansi.Truncate(state, 1, "")
	}
	marker := slurmQOSStyle(job.QOS).Inherit(base).Render("●")
	if header {
		job.ID, job.Elapsed, job.User, job.Name = "JOB ID", "ELAPSED", "USER", "NAME"
		display.Cluster, state, marker = "CLUSTER", "S", " "
		stateStyle = dimStyle
	}
	parts := []string{}
	if f.cluster > 0 {
		parts = append(parts, base.Render(fixedCell(display.Cluster, f.cluster, false)))
	}
	parts = append(parts,
		marker+base.Render(" ")+valueStyle.Inherit(base).Render(fixedCell(job.ID, f.id, false)),
		base.Render(fixedCell(job.User, f.user, false)),
		stateStyle.Inherit(base).Render(fixedCell(state, 1, false)),
		base.Render(fixedCell(job.Elapsed, 11, true)),
	)
	prefix := strings.Join(parts, base.Render(" ")) + base.Render(" ")
	row := prefix + base.Render(fixedCell(job.Name, max(0, f.width-lipgloss.Width(prefix)), false))
	row = ansi.Truncate(row, f.width, "")
	if header {
		return dimStyle.Copy().Bold(true).Render(ansi.Strip(row))
	}
	return row
}

func slurmQOSStyle(qos string) lipgloss.Style {
	name := strings.ToLower(slurmQOSLabel(qos))
	switch name {
	case "-", "low":
		return dimStyle
	case "normal":
		return networkRXStyle
	case "high":
		return warningStyle
	case "urgent":
		return dangerStyle
	case "long":
		return gpuTitleStyle
	default:
		// Arbitrary site-defined QOS names get stable colors, independent of
		// queue order, filtering, and which jobs are currently visible.
		palette := []lipgloss.Style{cpuTitleStyle, memoryTitleStyle, diskTitleStyle, networkTXStyle, networkRXStyle, warningStyle}
		h := fnv.New32a()
		_, _ = h.Write([]byte(name))
		return palette[int(h.Sum32())%len(palette)]
	}
}

func slurmQOSDot(qos string) string {
	return slurmQOSStyle(qos).Render("●")
}

func slurmQOSLegend(jobs []slurmDisplayJob, width int) string {
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[slurmQOSLabel(job.Job.QOS)] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := []string{dimStyle.Render("QOS")}
	for _, name := range names {
		parts = append(parts, slurmQOSDot(name)+" "+dimStyle.Render(sanitizeTerminalText(name)))
	}
	return ansi.Truncate(strings.Join(parts, "  "), max(1, width), "…")
}

func slurmTableRows(width, contentLines int) int {
	rows := max(0, contentLines-1) // column header
	if width < slurmCompactWidth && contentLines >= 3 {
		rows-- // QOS legend
	}
	return rows
}

func appendSlurmQOSLegend(lines []string, jobs []slurmDisplayJob, width int) []string {
	if width < slurmCompactWidth {
		return append(lines, slurmQOSLegend(jobs, width))
	}
	return lines
}
