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
	width, id, user, cluster, tail int
	hub                            bool
}

func newCompactSlurmFormat(width int, cluster, hub bool) compactSlurmFormat {
	f := compactSlurmFormat{
		width: width, id: min(13, max(6, width-31)),
		user: min(8, max(4, width-34)), hub: hub,
	}
	if cluster && width >= 92 {
		f.cluster = 10
	}
	if !hub && width >= 72 {
		f.tail = min(20, width-f.id-f.user-f.cluster-39)
	}
	return f
}

func (f compactSlurmFormat) render(display slurmDisplayJob, header bool, base lipgloss.Style) string {
	job := display.Job
	id := display.listID()
	state, stateStyle := slurmDisplayState(display)
	switch state {
	case "RUNNING":
		state = "R"
	case "NEXT":
		state = "N"
	case "PENDING":
		state = "P"
	default:
		state = ansi.Truncate(state, 1, "")
	}
	marker := slurmQOSStyle(job.QOS).Inherit(base).Render("●")
	reason, reasonStyle := "", dimStyle
	if !header && f.tail > 0 {
		reason = slurmDisplayReason(display)
		if isSlurmPending(job.State) && reason != "—" {
			reasonStyle = warningStyle
		}
	}
	if header {
		id, job.Elapsed, job.User, job.Name = "JOB ID", "ELAPSED", "USER", "NAME"
		display.Cluster, state, marker = "CLUSTER", "S", " "
		stateStyle = dimStyle
		reason = "NODE / BLOCKER"
	}
	parts := []string{}
	if f.cluster > 0 {
		parts = append(parts, base.Render(fixedCell(display.Cluster, f.cluster, false)))
	}
	parts = append(parts,
		marker+base.Render(" ")+valueStyle.Inherit(base).Render(fixedCell(id, f.id, false)),
		base.Render(fixedCell(job.User, f.user, false)),
		stateStyle.Inherit(base).Render(fixedCell(state, 1, false)),
		base.Render(fixedCell(job.Elapsed, 11, true)),
	)
	prefix := strings.Join(parts, base.Render(" ")) + base.Render(" ")
	nameWidth := f.width - lipgloss.Width(prefix)
	if f.tail > 0 {
		nameWidth -= f.tail + 1
	}
	name := base.Render(fixedCell(job.Name, max(0, nameWidth), false))
	if f.hub && !header && len(display.Reports) > 0 {
		name = renderSlurmJobName(display, nameWidth, base)
	}
	row := prefix + name
	if f.tail > 0 {
		row += base.Render(" ") + reasonStyle.Inherit(base).Render(fixedCell(reason, f.tail, false))
	}
	row = ansi.Truncate(row, f.width, "")
	if header {
		return dimStyle.Copy().Bold(true).Render(ansi.Strip(row))
	}
	return row
}

func slurmQOSStyle(qos string) lipgloss.Style {
	name := strings.ToLower(slurmQOSLabel(qos))
	switch name {
	case "-":
		return dimStyle
	case "low":
		return slurmQOSLowStyle
	case "normal":
		return slurmQOSNormalStyle
	case "high":
		return slurmQOSHighStyle
	case "urgent":
		return dangerStyle
	case "long":
		return accentStyle
	default:
		// Keep site-defined colors stable across refreshes and filters.
		palette := [...]lipgloss.Style{accentStyle, networkRXStyle, networkTXStyle, slurmQOSLowStyle, slurmQOSNormalStyle, slurmQOSHighStyle}
		h := fnv.New32a()
		_, _ = h.Write([]byte(name))
		return palette[h.Sum32()%uint32(len(palette))]
	}
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
		parts = append(parts, slurmQOSStyle(name).Render("● "+sanitizeTerminalText(name)))
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
