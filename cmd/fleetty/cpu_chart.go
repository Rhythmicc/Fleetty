package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

const cpuChartThreshold = 32

func (m *monitorModel) hasCPUChart() bool {
	return m.monitorPage == monitorPageCompute && len(m.snapshot.CPUCoreUsage) > cpuChartThreshold
}

func (m *monitorModel) useCPUChart() bool { return m.hasCPUChart() && !m.cpuCoreDetailed }

func (m *monitorModel) cpuChartCores() []cpuCoreUsage {
	cores := append([]cpuCoreUsage(nil), m.snapshot.CPUCoreUsage...)
	sort.SliceStable(cores, func(i, j int) bool {
		left, le := strconv.Atoi(strings.TrimPrefix(cores[i].CPU, "cpu"))
		right, re := strconv.Atoi(strings.TrimPrefix(cores[j].CPU, "cpu"))
		if le == nil && re == nil {
			return left < right
		}
		return cores[i].CPU < cores[j].CPU
	})
	return cores
}

func (m *monitorModel) cpuChartSelection(cores []cpuCoreUsage) int {
	for i, core := range cores {
		if core.CPU == m.cpuCoreSelected {
			return i
		}
	}
	return 0
}

type cpuChartLayout struct {
	columns, barHeight, start, end int
	plotWidth, historyWidth        int
}

func newCPUChartLayout(count, width, terminalHeight, selected int) cpuChartLayout {
	plotWidth := max(1, width-4) // Left-hand axis.
	columns := plotWidth
	for columns+(columns-1)/16 > plotWidth {
		columns--
	}
	if columns >= 16 {
		columns -= columns % 16
	}
	columns = max(1, min(count, columns))
	selected = min(max(0, selected), max(0, count-1))
	pageSize := columns * 2
	start := selected / pageSize * pageSize
	end := min(count, start+pageSize)
	height := 4
	if end-start > columns {
		height = 3
	}
	if terminalHeight > 0 && terminalHeight < 32 {
		height = 2
	}
	minimumPlot := columns + (columns-1)/16
	historyWidth := 0
	if spare := plotWidth - minimumPlot - 3; spare >= 14 {
		historyWidth = min(spare, min(40, max(14, width/4)))
		plotWidth -= historyWidth + 3
	}
	return cpuChartLayout{columns: columns, barHeight: height, start: start, end: end,
		plotWidth: plotWidth, historyWidth: historyWidth}
}

// Distribute whole terminal cells across equal logical columns. Rendering,
// labels and hit testing share the same rounded boundaries, including gaps.
func (layout cpuChartLayout) barSpan(index int) (start, end int) {
	usable := layout.plotWidth - (layout.columns-1)/16
	start = index*usable/layout.columns + index/16
	end = (index+1)*usable/layout.columns + index/16
	return start, end
}

// A column is a real 0–100% bar: each terminal cell has eight vertical levels.
// Zero rests on the baseline (▁); unknown samples retain a question mark (?).
func cpuVerticalGlyph(core cpuCoreUsage, row, height int) string {
	return verticalGraphGlyph(core.Percent, core.Available, row, height)
}

// Anchor the last label to the final bar, dropping nearby ticks rather than
// overlapping labels. The detail line always retains the full selected CPU ID.
func (layout cpuChartLayout) labels(cores []cpuCoreUsage) string {
	count := len(cores)
	if count == 0 {
		return ""
	}
	_, width := layout.barSpan(count - 1)
	labels := []rune(strings.Repeat(" ", width))
	label := func(index int) []rune {
		text := sanitizeTerminalText(cores[index].CPU)
		if lipgloss.Width(text) > width {
			text = strings.TrimPrefix(text, "cpu")
		}
		return []rune(truncate(text, width))
	}
	last := label(count - 1)
	lastColumn := width - len(last)
	copy(labels[lastColumn:], last)
	occupied := -1
	for index := 0; index < count-1; index += 16 {
		column, _ := layout.barSpan(index)
		tick := label(index)
		if column <= occupied || column+len(tick) >= lastColumn {
			continue
		}
		copy(labels[column:], tick)
		occupied = column + len(tick)
	}
	return string(labels)
}

// Resample the whole retained time window into buckets; left-padding
// distinguishes warm-up from idle CPU. The layout bounds its sidebar width.
func (m *monitorModel) cpuChartHistory(width, height int) []string {
	return renderHistoryChart(m.cpuHistory, width, height-1, "CPU %", 100, chartStyle)
}

func (m *monitorModel) renderCPUChart(width int) []string {
	cores := m.cpuChartCores()
	if len(cores) == 0 {
		return nil
	}
	selected := m.cpuChartSelection(cores)
	layout := newCPUChartLayout(len(cores), width, m.height, selected)
	var lines []string
	for start := layout.start; start < layout.end; start += layout.columns {
		end := min(layout.end, start+layout.columns)
		bandStart := len(lines)
		for row := 0; row < layout.barHeight; row++ {
			axis := "   │"
			if row == 0 {
				axis = "100│"
			}
			var line strings.Builder
			line.WriteString(dimStyle.Render(axis))
			for index := start; index < end; index++ {
				if index > start && (index-start)%16 == 0 {
					line.WriteByte(' ')
				}
				core := cores[index]
				_, style := gpuLoadStatus(core.Percent)
				glyph := cpuVerticalGlyph(core, row, layout.barHeight)
				if glyph == "?" || core.Percent <= 0 {
					style = dimStyle
				}
				if index == selected {
					style = style.Background(selectedProcessStyle(m.colorMode).GetBackground())
					if m.cpuCoreFocused {
						style = style.Underline(true)
					}
				}
				left, right := layout.barSpan(index - start)
				line.WriteString(style.Render(strings.Repeat(glyph, right-left)))
			}
			lines = append(lines, line.String())
		}
		lines = append(lines, dimStyle.Render("CPU│"+layout.labels(cores[start:end])))
		if start == layout.start && layout.historyWidth > 0 {
			historyX := width - layout.historyWidth
			for row, history := range m.cpuChartHistory(layout.historyWidth, layout.barHeight) {
				index := bandStart + row
				lines[index] += strings.Repeat(" ", historyX-lipgloss.Width(lines[index])) + history
			}
		}
	}
	core := cores[selected]
	load, clock := "--", "--"
	if core.Available && !math.IsNaN(core.Percent) && !math.IsInf(core.Percent, 0) {
		load = fmt.Sprintf("%.1f%%", core.Percent)
	}
	if validCPUFrequency(core.FrequencyMHz) {
		clock = fmt.Sprintf("%.2fGHz", core.FrequencyMHz/1000)
	}
	prefix := ""
	if m.cpuCoreFocused {
		prefix = "▶ "
	}
	detail := fmt.Sprintf("%s%s · %s · %s", prefix, sanitizeTerminalText(core.CPU), load, clock)
	if layout.end-layout.start < len(cores) {
		detail += fmt.Sprintf(" · %d–%d/%d", layout.start+1, layout.end, len(cores))
	}
	if m.cpuCoreFocused {
		if hint := "  ←→↑↓ core · esc back"; lipgloss.Width(detail+hint) <= width {
			detail += hint
		}
	}
	detailStyle := valueStyle
	if m.cpuCoreFocused {
		detailStyle = accentStyle
	}
	lines = append(lines, detailStyle.Render(truncate(detail, width)))
	return lines
}

func (m *monitorModel) handleCPUChartKey(key string) bool {
	if !m.hasCPUChart() {
		m.cpuCoreFocused = false
		return false
	}
	if key == "v" {
		m.cpuCoreDetailed = !m.cpuCoreDetailed
		m.cpuCoreFocused = false
		m.dashboardScroll = 0
		return true
	}
	if !m.useCPUChart() {
		return false
	}
	if key == "f" {
		m.cpuCoreFocused = !m.cpuCoreFocused
		if m.cpuCoreFocused {
			m.dashboardScroll = 0
		}
		return true
	}
	if !m.cpuCoreFocused {
		return false
	}
	cores := m.cpuChartCores()
	selected := m.cpuChartSelection(cores)
	layout := newCPUChartLayout(len(cores), max(4, usableWidth(m.width)-4), m.height, selected)
	switch key {
	case "left", "h":
		selected--
	case "right":
		selected++
	case "up", "k":
		selected -= layout.columns
	case "down", "j":
		selected += layout.columns
	case "home":
		selected = 0
	case "end":
		selected = len(cores) - 1
	case "enter":
		m.cpuCoreDetailed, m.cpuCoreFocused, m.dashboardScroll = true, false, 0
		return true
	case "esc":
		m.cpuCoreFocused = false
		return true
	default:
		// Leave existing page, filter and process actions available.
		if key == "tab" || key == "shift+tab" || key == "backtab" || key == "/" || key == "l" || key == "m" {
			m.cpuCoreFocused = false
		}
		return false
	}
	selected = max(0, min(len(cores)-1, selected))
	m.cpuCoreSelected = cores[selected].CPU
	return true
}

func (m *monitorModel) handleCPUChartClick(x, y int) bool {
	if !m.useCPUChart() {
		return false
	}
	placement, screenY, ok := m.visibleWidgetPlacement(dashboardPanelCPU)
	if !ok || x < placement.X+2 || x >= placement.X+placement.Width-2 ||
		y < screenY+1 || y >= screenY+placement.Height-1 || y < 2 {
		return false
	}
	card, _ := m.metricWidgetCard(dashboardPanelCPU)
	chartY := screenY + 1 + len(m.cpuMetricHeaderLines(card, placement.Width))
	cores := m.cpuChartCores()
	layout := newCPUChartLayout(len(cores), max(4, placement.Width-4), m.height, m.cpuChartSelection(cores))
	row, column := y-chartY, x-placement.X-2-4
	if row < 0 || column < 0 {
		return false
	}
	band := row / (layout.barHeight + 1)
	if band >= 2 || column >= layout.plotWidth {
		return false
	}
	for within := 0; within < layout.columns; within++ {
		left, right := layout.barSpan(within)
		if column < left || column >= right {
			continue
		}
		index := layout.start + band*layout.columns + within
		if index >= layout.end {
			return false
		}
		m.cpuCoreSelected, m.cpuCoreFocused = cores[index].CPU, true
		return true
	}
	return false
}
