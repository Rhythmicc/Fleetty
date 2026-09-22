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
	for _, core := range cores {
		labelWidth = max(labelWidth, lipgloss.Width(sanitizeTerminalText(core.CPU)))
	}
	columns := min(len(cores), max(1, (width+2)/(labelWidth+18)))
	cellWidth := (width - 2*(columns-1)) / columns
	var lines []string
	for start := 0; start < len(cores); start += columns {
		var cells []string
		for _, core := range cores[start:min(start+columns, len(cores))] {
			value := "    --"
			if core.Available {
				value = fmt.Sprintf("%5.1f%%", core.Percent)
			}
			cell := dimStyle.Render(fixedCell(sanitizeTerminalText(core.CPU), labelWidth, false)) +
				" " + valueStyle.Render(value)
			barWidth := cellWidth - labelWidth - 8
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
		return btopPanel(width, card.title, "PER CORE", strings.Join(m.cpuMetricLines(card, width), "\n"),
			card.titleStyle, card.borderColor)
	default:
		return renderMetricWidget(card, width, widgetSizeSmall)
	}
}

func (m *monitorModel) cpuMetricLines(card metricCard, width int) []string {
	contentWidth := max(4, width-4)
	lines := []string{
		valueStyle.Render(truncate(card.value, contentWidth)),
		dimStyle.Render(truncate(card.detail, contentWidth)),
		renderMetricVisual(card, contentWidth),
		"",
		dimStyle.Render(truncate(fmt.Sprintf("%d logical CPUs · each 0–100%% · -- awaiting sample", m.snapshot.CPUCores), contentWidth)),
	}
	lines = append(lines, renderCPUCoreLines(m.snapshot.CPUCoreUsage, contentWidth)...)
	return lines
}
