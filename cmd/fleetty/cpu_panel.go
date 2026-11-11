package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func renderCPUCoreLines(cores []cpuCoreUsage, width int) []string {
	width = max(1, width)
	if len(cores) == 0 {
		return []string{dimStyle.Render(truncate("Per-core data unavailable from this node.", width))}
	}
	labelWidth := 4
	haveFrequency := false
	for _, core := range cores {
		labelWidth = max(labelWidth, lipgloss.Width(sanitizeTerminalText(core.CPU)))
		haveFrequency = haveFrequency || validCPUFrequency(core.FrequencyMHz)
	}
	showFrequency := haveFrequency && width >= labelWidth+18
	minimumCell := labelWidth + 18
	if showFrequency {
		minimumCell += 9
	}
	columns := min(len(cores), max(1, (width+2)/minimumCell))
	cellWidth := (width - 2*(columns-1)) / columns
	var lines []string
	for start := 0; start < len(cores); start += columns {
		var cells []string
		for _, core := range cores[start:min(start+columns, len(cores))] {
			value := "    --"
			if core.Available {
				value = fmt.Sprintf("%5.1f%%", core.Percent)
			}
			reading := value
			if core.Available {
				reading = usageReading(core.Percent, value)
			} else {
				reading = dimStyle.Render(value)
			}
			cell := dimStyle.Render(fixedCell(sanitizeTerminalText(core.CPU), labelWidth, false)) +
				" " + reading
			barWidth := cellWidth - labelWidth - 8
			if showFrequency {
				clock := "--"
				if validCPUFrequency(core.FrequencyMHz) {
					clock = fmt.Sprintf("%.2fGHz", core.FrequencyMHz/1000)
				}
				cell += " " + valueStyle.Render(fixedCell(clock, 8, true))
				barWidth -= 9
			}
			if barWidth > 0 {
				meter := dimStyle.Render(strings.Repeat("·", barWidth))
				if core.Available {
					meter = bar(core.Percent, barWidth)
				}
				cell += " " + meter
			}
			cells = append(cells, ansi.Truncate(cell, cellWidth, ""))
		}
		lines = append(lines, strings.Join(cells, "  "))
	}
	return lines
}

// The CPU widget has three forms: summary, per-core, and per-core with
// process attribution. Compute uses the same per-core form as CUSTOM.
func (m *monitorModel) renderCPUWidget(card metricCard, width int, size widgetSize) string {
	switch size {
	case widgetSizeLarge:
		return m.renderLargeProcessMetricWidget(card, width, false)
	case widgetSizeMedium:
		meta := "PER CORE"
		if m.hasCPUChart() {
			meta += " · v BARS"
			if m.useCPUChart() {
				meta = "VERTICAL · f FOCUS · v DETAIL"
			}
		}
		return btopPanel(width, card.title, meta, strings.Join(m.cpuMetricLines(card, width), "\n"),
			card.titleStyle, card.borderColor)
	default:
		return renderMetricWidget(card, width, widgetSizeSmall)
	}
}

func (m *monitorModel) cpuMetricLines(card metricCard, width int) []string {
	lines := m.cpuMetricHeaderLines(card, width)
	if m.useCPUChart() {
		return append(lines, m.renderCPUChart(max(4, width-4))...)
	}
	contentWidth := max(4, width-4)
	if contentWidth >= 76 {
		historyWidth := min(40, max(24, contentWidth/4))
		coreWidth := contentWidth - historyWidth - 3
		cores := renderCPUCoreLines(m.snapshot.CPUCoreUsage, coreWidth)
		history := renderHistoryChart(card.primaryHistory, historyWidth, historyPlotHeight, "CPU %", 100, chartStyle)
		return append(lines, historyColumns(cores, history, coreWidth)...)
	}
	return append(lines, renderCPUCoreLines(m.snapshot.CPUCoreUsage, contentWidth)...)
}

func (m *monitorModel) cpuMetricHeaderLines(card metricCard, width int) []string {
	contentWidth := max(4, width-4)
	value := valueStyle.Render(truncate(card.value, contentWidth))
	detail := dimStyle.Render(truncate(card.detail, contentWidth))
	var lines []string
	if lipgloss.Width(value)+2+lipgloss.Width(detail) <= contentWidth {
		gap := contentWidth - lipgloss.Width(value) - lipgloss.Width(detail)
		lines = append(lines, value+strings.Repeat(" ", gap)+detail)
	} else {
		lines = append(lines, value, detail)
	}
	metadata := dimStyle.Render(cpuCoreMetadata(m.snapshot, contentWidth))
	lines = append(lines, metadata)
	return lines
}

func cpuCoreMetadata(snapshot monitorSnapshot, width int) string {
	count := fmt.Sprintf("%d CPUs", snapshot.CPUCores)
	frequency := snapshot.CPUFrequency
	if frequency == nil || !validCPUFrequency(frequency.AverageMHz) {
		return truncate(count+" · -- unavailable", width)
	}
	if frequency.SampledCores != snapshot.CPUCores {
		count = fmt.Sprintf("%d/%d CPUs", frequency.SampledCores, snapshot.CPUCores)
	}
	clock := fmt.Sprintf("%.2f–%.2fGHz", frequency.MinMHz/1000, frequency.MaxMHz/1000)
	metadata := count + " · CLK " + clock
	if lipgloss.Width(metadata) > width {
		metadata = count + " · " + clock
	}
	return truncate(metadata, width)
}

func cpuClockSummary(frequency *cpuFrequencyInfo) string {
	if frequency == nil || !validCPUFrequency(frequency.AverageMHz) {
		return "CLK --"
	}
	return fmt.Sprintf("AVG %.2fGHz", frequency.AverageMHz/1000)
}

func cpuSummaryValue(snapshot monitorSnapshot) string {
	return fmt.Sprintf("%.1f%% · %s", snapshot.CPUPercent, cpuClockSummary(snapshot.CPUFrequency))
}
