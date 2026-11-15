package main

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *hubModel) hubQueueShowsCluster(filter int) bool {
	return filter < 0 && len(m.config.SlurmClusters) > 1
}

func (m *hubModel) hubQueueMeta(filter int, jobs []slurmDisplayJob, visible string) (string, bool) {
	clusters := len(m.config.SlurmClusters)
	online, offline, stale := 0, 0, 0
	if filter >= 0 {
		clusters = 1
	}
	now := time.Now()
	for i, cluster := range m.config.SlurmClusters {
		if i >= len(m.slurmStates) {
			break
		}
		if filter >= 0 && i != filter {
			continue
		}
		state := m.slurmStates[i]
		switch {
		case state.Error != "":
			offline++
		case !state.Snapshot.CollectedAt.IsZero():
			online++
			if slurmSnapshotStale(cluster, state, now) {
				stale++
			}
		}
	}
	if online == 0 {
		if offline == clusters {
			return warningStyle.Render("UNAVAILABLE"), false
		}
		return dimStyle.Render("WAITING FOR SLURM…"), false
	}
	running, next, pending := slurmDisplayStateCounts(jobs)
	parts := []string{processRunningStyle.Render(fmt.Sprintf("● RUN %d", running))}
	if next > 0 {
		parts = append(parts, accentStyle.Render(fmt.Sprintf("◆ NEXT %d", next)))
	}
	parts = append(parts, processWaitingStyle.Render(fmt.Sprintf("○ PENDING %d", pending)))
	if visible != "" {
		parts = append(parts, dimStyle.Render(visible))
	}
	if stale > 0 {
		parts = append(parts, warningStyle.Render(fmt.Sprintf("%d STALE", stale)))
	}
	return strings.Join(parts, dimStyle.Render(" · ")), true
}

func (m *hubModel) renderHubQueuePreviewPanel(width, outerHeight int) string {
	innerWidth := max(20, width-4)
	contentRows := max(1, outerHeight-2)
	jobs := m.slurmJobs(-1)
	meta, available := m.hubQueueMeta(-1, jobs, "")

	lines := make([]string, 0, contentRows)
	if len(jobs) == 0 {
		if available {
			lines = append(lines, dimStyle.Render("No jobs in the online queues."))
		} else {
			lines = append(lines, dimStyle.Render("Waiting for Slurm queue data…"))
		}
	} else {
		showCluster := m.hubQueueShowsCluster(-1)
		lines = append(lines, slurmJobTableHeader(innerWidth, showCluster))
		contentLines := max(1, contentRows)
		tableRows := slurmTableRows(innerWidth, contentLines)
		limit := min(len(jobs), tableRows)
		if limit < len(jobs) && limit > 1 {
			limit--
		}
		for _, display := range jobs[:limit] {
			lines = append(lines, renderSlurmJobRow(display, innerWidth, showCluster, lipgloss.NewStyle()))
		}
		if limit < len(jobs) && limit < tableRows && len(lines) < contentRows {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("+%d more jobs in queue (press s to expand & inspect)", len(jobs)-limit)))
		}
		if innerWidth < slurmCompactWidth && contentLines >= 3 && len(lines) < contentRows {
			lines = append(lines, slurmQOSLegend(jobs, innerWidth))
		}
	}
	for len(lines) < contentRows {
		lines = append(lines, "")
	}
	return btopPanel(width, "SLURM QUEUE", meta, strings.Join(lines[:contentRows], "\n"), accentStyle, colorBorder)
}

func (m *hubModel) renderHubQueueExpandedPanel(width, outerHeight int) string {
	innerWidth := max(20, width-4)
	contentRows := max(1, outerHeight-2)
	jobs := m.selectedSlurmJobs()
	start, end := m.hubQueueWindow(len(jobs))
	visible := ""
	if end-start < len(jobs) && !m.slurmExplain {
		visible = fmt.Sprintf("%d–%d / %d", start+1, end, len(jobs))
	}
	meta, available := m.hubQueueMeta(m.slurmFilter, jobs, visible)
	title := "SLURM QUEUE"
	if m.slurmFilter >= 0 && m.slurmFilter < len(m.config.SlurmClusters) {
		title += " · " + m.config.SlurmClusters[m.slurmFilter].Name
	}

	var lines []string
	selectedRow := -1
	if len(jobs) == 0 {
		if available {
			lines = append(lines, dimStyle.Render("No jobs in the selected queue."))
		}
	} else if m.slurmExplain {
		lines = m.slurmExplanationLines(jobs[m.slurmCursor], innerWidth, contentRows)
	} else {
		showCluster := m.hubQueueShowsCluster(m.slurmFilter)
		m.slurmOffset = start
		lines = append(lines, slurmJobTableHeader(innerWidth, showCluster))
		for index := start; index < end && len(lines) < contentRows; index++ {
			if index == m.slurmCursor {
				selectedRow = len(lines)
			}
			lines = append(lines, renderSlurmJobRow(jobs[index], innerWidth, showCluster, lipgloss.NewStyle()))
		}
		if innerWidth < slurmCompactWidth && contentRows >= 3 && len(lines) < contentRows {
			lines = append(lines, slurmQOSLegend(jobs, innerWidth))
		}
	}
	for len(lines) < contentRows {
		lines = append(lines, "")
	}
	borderColor := color.Color(colorBorderFocus)
	panel := btopPanel(width, title, meta, strings.Join(lines[:contentRows], "\n"), accentStyle, borderColor)
	if selectedRow > 0 {
		panelLines := strings.Split(panel, "\n")
		if rowIdx := 1 + selectedRow; rowIdx < len(panelLines)-1 {
			panelLines[rowIdx] = hubHighlightRow(accentStyle.Render("› ")+lines[selectedRow]+"  ", width, m.colorMode)
			panel = strings.Join(panelLines, "\n")
		}
	}
	return panel
}

func (m *hubModel) hubQueueHeading(width, filter int, jobs []slurmDisplayJob, visible string) (string, bool) {
	clusters := len(m.config.SlurmClusters)
	title := accentStyle.Render("SLURM QUEUE")
	if width < 32 {
		title = accentStyle.Render("SLURM")
	}
	if filter >= 0 && filter < len(m.config.SlurmClusters) {
		title += dimStyle.Render(" / ") + valueStyle.Render(m.config.SlurmClusters[filter].Name)
	}
	online, offline, stale := 0, 0, 0
	if filter >= 0 {
		clusters = 1
	}
	now := time.Now()
	for i, cluster := range m.config.SlurmClusters {
		if i >= len(m.slurmStates) {
			break
		}
		if filter >= 0 && i != filter {
			continue
		}
		state := m.slurmStates[i]
		switch {
		case state.Error != "":
			offline++
		case !state.Snapshot.CollectedAt.IsZero():
			online++
			if slurmSnapshotStale(cluster, state, now) {
				stale++
			}
		}
	}
	if online == 0 {
		message := dimStyle.Render("Waiting for Slurm…")
		if offline == clusters {
			message = warningStyle.Render("Slurm queues unavailable.")
		}
		if lipgloss.Width(title)+lipgloss.Width(message)+1 > width {
			message = warningStyle.Render("WAITING")
			if offline == clusters {
				message = warningStyle.Render("UNAVAILABLE")
			}
		}
		return hubRackAlign(title, message, width), false
	}
	running, next, pending := slurmDisplayStateCounts(jobs)
	parts := []string{processRunningStyle.Render(fmt.Sprintf("RUN %d", running))}
	compact := fmt.Sprintf("R %d", running)
	if next > 0 {
		parts = append(parts, accentStyle.Render(fmt.Sprintf("NEXT %d", next)))
		compact += fmt.Sprintf("  N %d", next)
	}
	parts = append(parts, processWaitingStyle.Render(fmt.Sprintf("PENDING %d", pending)))
	compact += fmt.Sprintf("  P %d", pending)
	counts := strings.Join(parts, dimStyle.Render("  ·  "))
	if lipgloss.Width(title)+lipgloss.Width(counts)+1 > width {
		counts = compact
	}
	if stale > 0 {
		label := fmt.Sprintf("%d STALE", stale)
		if lipgloss.Width(title)+lipgloss.Width(counts)+len(label)+3 > width {
			counts = compact
			label = "STALE"
		}
		counts += "  " + warningStyle.Render(label)
	}
	if online < clusters && width >= 46 {
		coverage := fmt.Sprintf("%d/%d SOURCES", online, clusters)
		if lipgloss.Width(title)+lipgloss.Width(counts)+len(coverage)+3 <= width {
			counts += "  " + warningStyle.Render(coverage)
		}
	}
	if visible != "" && lipgloss.Width(title)+lipgloss.Width(counts)+len(visible)+4 <= width {
		title += "  " + dimStyle.Render(visible)
	}
	return hubRackAlign(title, counts, width), true
}

func (m *hubModel) renderHubQueuePreview(width, height int) []string {
	if len(m.config.SlurmClusters) == 0 || height < 1 {
		return nil
	}
	jobs := m.slurmJobs(-1)
	heading, available := m.hubQueueHeading(width, -1, jobs, "")
	if height < 4 || width < 48 {
		return []string{heading}
	}
	lines := []string{"", heading}
	if len(jobs) == 0 {
		if available {
			lines = append(lines, dimStyle.Render("No jobs in the online queues."))
		}
		return lines
	}

	tableWidth, contentLines := width-4, height-len(lines)
	tableRows := slurmTableRows(tableWidth, contentLines)
	showCluster := m.hubQueueShowsCluster(-1)
	lines = append(lines, "  "+slurmJobTableHeader(tableWidth, showCluster))
	limit := min(len(jobs), tableRows)
	if limit < len(jobs) && limit > 1 {
		limit--
	}
	for _, display := range jobs[:limit] {
		lines = append(lines, "  "+renderSlurmJobRow(display, tableWidth, showCluster, lipgloss.NewStyle()))
	}
	if limit < len(jobs) && limit < tableRows {
		lines = append(lines, "  "+dimStyle.Render(fmt.Sprintf("+%d more jobs", len(jobs)-limit)))
	}
	if tableWidth < slurmCompactWidth && contentLines >= 3 {
		lines = append(lines, "  "+slurmQOSLegend(jobs, tableWidth))
	}
	return lines
}

func (m *hubModel) hubQueueHeight() int {
	return max(0, m.height-hubHeaderRows-1-m.hubRackBudget())
}

func (m *hubModel) hubQueuePageSize() int {
	if m.hubFramedMode() {
		return max(1, m.hubQueueHeight()-3)
	}
	return max(1, slurmTableRows(max(1, m.width-4), m.hubQueueHeight()-1))
}

func (m *hubModel) hubQueueWindow(count int) (start, end int) {
	page := m.hubQueuePageSize()
	start = min(max(0, m.slurmOffset), max(0, count-page))
	if m.slurmCursor < start {
		start = m.slurmCursor
	} else if m.slurmCursor >= start+page {
		start = m.slurmCursor - page + 1
	}
	start = min(max(0, start), max(0, count-page))
	return start, min(count, start+page)
}

func (m *hubModel) slurmJobAt(x, y int) (int, bool) {
	if !m.queueExpanded || m.slurmExplain || x < 0 || x >= m.width || y >= m.height-1 {
		return 0, false
	}
	firstRow := hubHeaderRows + m.hubRackBudget() + 2
	start, end := m.hubQueueWindow(len(m.selectedSlurmJobs()))
	index := start + y - firstRow
	return index, index >= start && index < end
}

func (m *hubModel) renderHubQueueExpanded(width int) []string {
	height := m.hubQueueHeight()
	if height < 1 {
		return nil
	}
	jobs := m.selectedSlurmJobs()
	start, end := m.hubQueueWindow(len(jobs))
	visible := ""
	if end-start < len(jobs) && height >= 3 && !m.slurmExplain {
		visible = fmt.Sprintf("%d–%d / %d", start+1, end, len(jobs))
	}
	heading, available := m.hubQueueHeading(width, m.slurmFilter, jobs, visible)
	if height < 2 || len(jobs) == 0 {
		lines := []string{heading}
		if height > 1 && available {
			lines = append(lines, dimStyle.Render("No jobs in the selected queue."))
		}
		return lines
	}
	if m.slurmExplain {
		return append([]string{heading}, m.slurmExplanationLines(jobs[m.slurmCursor], width, height-1)...)
	}
	tableWidth := max(1, width-4)
	showCluster := m.hubQueueShowsCluster(m.slurmFilter)
	m.slurmOffset = start
	lines := []string{heading, "  " + slurmJobTableHeader(tableWidth, showCluster)}
	for index := start; index < end && len(lines) < height; index++ {
		prefix := "  "
		if index == m.slurmCursor {
			prefix = accentStyle.Render("› ")
		}
		line := prefix + renderSlurmJobRow(jobs[index], tableWidth, showCluster, lipgloss.NewStyle())
		if index == m.slurmCursor {
			line = hubHighlightRow(line, width, m.colorMode)
		}
		lines = append(lines, line)
	}
	if tableWidth < slurmCompactWidth && height-1 >= 3 && len(lines) < height {
		lines = append(lines, "  "+slurmQOSLegend(jobs, tableWidth))
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return lines
}
