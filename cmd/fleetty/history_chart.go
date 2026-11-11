package main

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const historyPlotHeight = 3

func verticalGraphGlyph(value float64, available bool, row, height int) string {
	if !available || math.IsNaN(value) || math.IsInf(value, 0) {
		if row == height-1 {
			return "?"
		}
		return " "
	}
	value = math.Max(0, math.Min(100, value))
	if value == 0 && row == height-1 {
		return "▁"
	}
	units := int(math.Ceil(value / 100 * float64(height*8)))
	level := max(0, min(8, units-(height-1-row)*8))
	return string([]rune(" ▁▂▃▄▅▆▇█")[level])
}

// Every history uses the same filled, bottom-aligned plot. Downsampling covers
// the entire retained window, not just the newest columns. Warm-up stays blank.
func renderHistoryChart(history []float64, width, height int, label string, ceiling float64, style lipgloss.Style) []string {
	width, height = max(1, width), max(1, height)
	plotWidth := max(1, width-2)
	if ceiling <= 0 || math.IsNaN(ceiling) || math.IsInf(ceiling, 0) {
		ceiling = 1
	}
	values := make([]float64, plotWidth)
	available := make([]bool, plotWidth)
	for column := range values {
		start, end := column, column+1
		if len(history) > plotWidth {
			start = column * len(history) / plotWidth
			end = (column + 1) * len(history) / plotWidth
		} else {
			start -= plotWidth - len(history)
			end = start + 1
		}
		count := 0
		for index := max(0, start); index < end; index++ {
			value := history[index]
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				values[column] += math.Max(0, math.Min(100, value/ceiling*100))
				count++
			}
		}
		if count > 0 {
			available[column] = true
			values[column] /= float64(count)
		}
	}
	frame := func(content string) string {
		return ansi.Truncate(dimStyle.Render("│ ")+content, width, "")
	}
	lines := []string{frame(dimStyle.Render(fixedCell("HIST · "+label, plotWidth, false)))}
	anySample := false
	for _, valid := range available {
		anySample = anySample || valid
	}
	for row := 0; row < height; row++ {
		var line strings.Builder
		for column, value := range values {
			glyph := " "
			if available[column] {
				glyph = verticalGraphGlyph(value, true, row, height)
			}
			line.WriteString(style.Render(glyph))
		}
		content := line.String()
		if !anySample && row == height-1 {
			content = dimStyle.Render(fixedCell("awaiting sample", plotWidth, false))
		}
		lines = append(lines, frame(content))
	}
	timeAxis := "old" + strings.Repeat(" ", max(1, plotWidth-8)) + "→ now"
	if plotWidth < 8 {
		timeAxis = "→ now"
	}
	return append(lines, frame(dimStyle.Render(fixedCell(timeAxis, plotWidth, false))))
}

func renderNetworkHistories(rx, tx []float64, width int) []string {
	ceiling := historyMax(rx, tx)
	var lines []string
	if width >= 34 {
		leftWidth := (width - 2) / 2
		left := renderHistoryChart(rx, leftWidth, historyPlotHeight, "RX B/s", ceiling, chartStyle)
		right := renderHistoryChart(tx, width-leftWidth-2, historyPlotHeight, "TX B/s", ceiling, chartStyle)
		lines = historyColumns(left, right, leftWidth)
	} else {
		lines = renderHistoryChart(rx, width, historyPlotHeight, "RX B/s", ceiling, chartStyle)
		lines = append(lines, renderHistoryChart(tx, width, historyPlotHeight, "TX B/s", ceiling, chartStyle)...)
	}
	peak := fmt.Sprintf("PEAK ↓ %s/s ↑ %s/s", bytes(uint64(historyMax(rx))), bytes(uint64(historyMax(tx))))
	return append(lines, dimStyle.Render(truncate(peak, width)))
}

func historyColumns(left, right []string, leftWidth int) []string {
	lines := make([]string, max(len(left), len(right)))
	for row := range lines {
		leftLine, rightLine := "", ""
		if row < len(left) {
			leftLine = left[row]
		}
		if row < len(right) {
			rightLine = right[row]
		}
		lines[row] = leftLine + strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(leftLine))+2) + rightLine
	}
	return lines
}
