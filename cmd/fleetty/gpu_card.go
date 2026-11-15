package main

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func singleGPUOverviewLines(gpu gpuInfo, width int) []string {
	name := sanitizeTerminalText(gpu.Name)
	if gpu.Platform == "apple" && gpu.CoreCount > 0 {
		name += fmt.Sprintf(" · %d cores", gpu.CoreCount)
	}
	var telemetry []string
	temp := ""
	if gpu.Platform != "apple" {
		if gpu.Temperature > 0 {
			temp = temperatureReading(gpu.Temperature, fmt.Sprintf("%d°C", gpu.Temperature))
		}
		if gpu.Power > 0 || gpu.PowerLimit > 0 {
			telemetry = append(telemetry, fmt.Sprintf("%.0fW", gpu.Power))
		}
		if gpu.ClockMHz > 0 {
			telemetry = append(telemetry, fmt.Sprintf("%dMHz", gpu.ClockMHz))
		}
	}
	aside := dimStyle.Render(strings.Join(telemetry, " · "))
	if temp != "" && aside != "" {
		aside = temp + dimStyle.Render(" · ") + aside
	} else if temp != "" {
		aside = temp
	}
	lines := []string{alignedPageLine(valueStyle.Render(name), aside, width)}
	type reading struct {
		label, value string
		usage        float64
	}
	memoryLabel := "VRAM"
	if gpu.Platform == "apple" {
		memoryLabel = "UMA"
	}
	readings := []reading{
		{"LOAD", fmt.Sprintf("%.0f%%", gpu.Utilization), gpu.Utilization},
		{memoryLabel, compactByteCount(gpu.MemoryUsed) + " / " + compactByteCount(gpu.MemoryTotal), percent(gpu.MemoryUsed, gpu.MemoryTotal)},
	}
	if gpu.Platform == "apple" {
		readings = append(readings, reading{"RENDER", fmt.Sprintf("%.0f%%", gpu.RendererUtilization), gpu.RendererUtilization},
			reading{"TILER", fmt.Sprintf("%.0f%%", gpu.TilerUtilization), gpu.TilerUtilization})
	}
	columns := 1
	if width >= 50 {
		columns = 2
	}
	if width >= 100 {
		columns = len(readings)
	}
	cellWidth := max(1, (width-2*(columns-1))/columns)
	for start := 0; start < len(readings); start += columns {
		var labels, bars []string
		for _, item := range readings[start:min(start+columns, len(readings))] {
			style := gpuLoadStyle(item.usage)
			label := dimStyle.Render(item.label+" ") + style.Render(item.value)
			label = ansi.Truncate(label, cellWidth, "…")
			labels = append(labels, label+strings.Repeat(" ", max(0, cellWidth-ansi.StringWidth(label))))
			bars = append(bars, loadBar(item.usage, cellWidth, style))
		}
		lines = append(lines, strings.Join(labels, "  "), strings.Join(bars, "  "))
	}
	if len(gpu.Workloads) > 0 {
		lines = append(lines, ansi.Truncate(gpuWorkloadSummary(gpu), width, "…"))
	}
	return lines
}

// gpuCardVariant describes presentation density, not where the metrics came
// from. Local `fleetty top`, Hub node details, and custom dashboards all feed
// the same snapshot into this component so their GPU design cannot drift.
type gpuCardVariant uint8

const (
	gpuCardOverview gpuCardVariant = iota
	gpuCardDetailed
	gpuCardCompact
)

type gpuCardComponent struct {
	gpus     []gpuInfo
	gpuError string
}

func newGPUCardComponent(snapshot monitorSnapshot) gpuCardComponent {
	return gpuCardComponent{
		gpus:     snapshot.GPUs,
		gpuError: snapshot.GPUError,
	}
}

func (card gpuCardComponent) panelSpec(variant gpuCardVariant, width, deviceLimit int) pagePanelSpec {
	switch variant {
	case gpuCardOverview:
		return buildGPUOverviewPanelSpec(card.gpus, card.gpuError, width)
	case gpuCardCompact:
		return card.compactPanelSpec(width)
	default:
		return card.detailedPanelSpec(width, deviceLimit)
	}
}

func (card gpuCardComponent) render(variant gpuCardVariant, width, deviceLimit int) string {
	if len(card.gpus) == 0 && card.gpuError == "" {
		return ""
	}
	return renderPagePanel(width, card.panelSpec(variant, width, deviceLimit))
}

func (card gpuCardComponent) detailedPanelSpec(width, deviceLimit int) pagePanelSpec {
	gpus := card.gpus
	if deviceLimit > 0 && len(gpus) > deviceLimit {
		gpus = gpus[:deviceLimit]
	}
	lines := make([]string, 0, max(1, len(gpus)*2))
	if card.gpuError != "" && len(gpus) == 0 {
		lines = append(lines, warningStyle.Render("GPU metrics unavailable: "+card.gpuError))
	} else {
		for _, gpu := range gpus {
			lines = append(lines,
				gpuPanelMetricsLine(gpu, width),
				gpuPanelWorkloadLine(gpu, width),
			)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, dimStyle.Render("No GPU was reported."))
	}
	meta := gpuDeviceCountMeta(len(gpus))
	if len(card.gpus) > len(gpus) {
		meta += fmt.Sprintf(" · +%d", len(card.gpus)-len(gpus))
	}
	return pagePanelSpec{
		title: "GPU", meta: meta, lines: lines,
		titleStyle: gpuTitleStyle, borderColor: colorGPUBorder,
	}
}

func (card gpuCardComponent) compactPanelSpec(width int) pagePanelSpec {
	if card.gpuError != "" && len(card.gpus) == 0 {
		return pagePanelSpec{
			title: "GPU", meta: "SMALL",
			lines: []string{
				dimStyle.Render(truncate(card.gpuError, max(4, width-4))),
				"",
				dimStyle.Render(strings.Repeat("─", max(1, width-4))),
			},
			titleStyle: gpuTitleStyle, borderColor: colorGPUBorder,
		}
	}
	if len(card.gpus) == 0 {
		return pagePanelSpec{}
	}
	gpu := card.gpus[0]
	status, style := gpuLoadStatus(gpu.Utilization)
	return pagePanelSpec{
		title: "GPU", meta: "SMALL",
		lines: []string{
			style.Render(fmt.Sprintf("%.0f%%  %s", gpu.Utilization, status)),
			dimStyle.Render(truncate(gpu.Name, max(4, width-4))),
			loadBar(gpu.Utilization, max(1, width-4), style),
		},
		titleStyle: sectionStyle, borderColor: colorBorder,
	}
}

func gpuDeviceCountMeta(count int) string {
	meta := fmt.Sprintf("%d DEVICE", count)
	if count != 1 {
		meta += "S"
	}
	return meta
}

func (m *monitorModel) gpuPanel(layout dashboardLayout) string {
	return newGPUCardComponent(m.snapshot).render(gpuCardDetailed, layout.width, 0)
}

type gpuPanelColumns struct {
	contentWidth int
	compact      bool
	index        int
	name         int
	util         int
	memory       int
	power        int
	clock        int
	temperature  int
	barWidth     int
}

func newGPUPanelColumns(width int) gpuPanelColumns {
	contentWidth := max(20, width-4)
	compact := contentWidth < 72
	columns := gpuPanelColumns{
		contentWidth: contentWidth,
		compact:      compact,
		index:        6,
	}
	if compact {
		columns.barWidth = max(2, min(5, contentWidth/32))
		columns.util = columns.barWidth + 5
		columns.memory = columns.barWidth + 12
		columns.power = columns.barWidth + 12
		columns.clock = 8
		columns.temperature = 6
	} else {
		columns.barWidth = max(6, min(14, contentWidth/14))
		columns.util = columns.barWidth + 5
		columns.memory = columns.barWidth + 16
		columns.power = columns.barWidth + 16
		columns.clock = 11
		columns.temperature = 6
	}
	// There is one separator between each cell. Keep the model column fixed
	// too, so a short model cannot pull all following metrics to the left.
	separators := 6
	fixed := columns.index + columns.util + columns.memory + columns.power + columns.clock + columns.temperature + separators
	// Keep enough room for a recognizable model without creating a huge blank gap.
	columns.name = min(24, max(12, contentWidth-fixed))
	return columns
}

func gpuStyledCell(style lipgloss.Style, value string, width int, right bool) string {
	return style.Render(fixedCell(value, width, right))
}

func gpuPanelMetricsLine(gpu gpuInfo, width int) string {
	columns := newGPUPanelColumns(width)
	displayName := gpu.Name
	if columns.compact {
		displayName = shortGPUName(displayName)
	}
	name := gpuStyledCell(valueStyle, displayName, columns.name, false)
	utilStyle := gpuLoadStyle(gpu.Utilization)
	memoryUsage := percent(gpu.MemoryUsed, gpu.MemoryTotal)
	memoryStyle := gpuLoadStyle(memoryUsage)
	powerUsage := 0.0
	if gpu.PowerLimit > 0 {
		powerUsage = gpu.Power * 100 / gpu.PowerLimit
	}
	powerStyle := gpuLoadStyle(powerUsage)
	memory := compactByteCount(gpu.MemoryUsed) + "/" + compactByteCount(gpu.MemoryTotal)
	power := fmt.Sprintf("%.0fW", gpu.Power)
	if gpu.PowerLimit > 0 {
		power = fmt.Sprintf("%.0f/%.0fW", gpu.Power, gpu.PowerLimit)
	}
	barWidth := columns.barWidth
	clockText := fmt.Sprintf("CLK %dMHz", gpu.ClockMHz)
	if columns.compact {
		memory = shortGPUCount(gpu.MemoryUsed) + "/" + shortGPUCount(gpu.MemoryTotal)
		if gpu.PowerLimit > 0 {
			power = fmt.Sprintf("%.0f/%.0fW", gpu.Power, gpu.PowerLimit)
		}
		clockText = fmt.Sprintf("%dMHz", gpu.ClockMHz)
	}
	utilPart := gpuStyledCell(utilStyle, fmt.Sprintf("%s %3.0f%%", shortGPUBar(gpu.Utilization, barWidth), gpu.Utilization), columns.util, false)
	memoryLabel := "MEM"
	powerLabel := "PWR"
	if columns.compact {
		memoryLabel = "M"
		powerLabel = "P"
	}
	memoryPart := gpuStyledCell(memoryStyle, fmt.Sprintf("%s %s %s", shortGPUBar(memoryUsage, barWidth), memoryLabel, memory), columns.memory, false)
	powerPart := gpuStyledCell(powerStyle, fmt.Sprintf("%s %s %s", shortGPUBar(powerUsage, barWidth), powerLabel, power), columns.power, false)
	clockPart := gpuStyledCell(gpuClockStyle(gpu.ClockMHz), clockText, columns.clock, false)
	temperaturePart := gpuStyledCell(gpuTemperatureStyle(gpu.Temperature), fmt.Sprintf("%d°C", gpu.Temperature), columns.temperature, false)
	parts := []string{
		gpuStyledCell(valueStyle, fmt.Sprintf("GPU %-2d", gpu.Index), columns.index, false),
		name,
		utilPart,
		memoryPart,
		powerPart,
		clockPart,
		temperaturePart,
	}
	return ansi.Truncate(strings.Join(parts, " "), columns.contentWidth, "…")
}

func shortGPUBar(value float64, width int) string {
	return loadBar(value, width, lipgloss.NewStyle())
}

func shortGPUCount(value uint64) string {
	return strings.ReplaceAll(compactByteCount(value), ".0", "")
}

func gpuPanelWorkloadLine(gpu gpuInfo, width int) string {
	columns := newGPUPanelColumns(width)
	indent := strings.Repeat(" ", columns.index+1)
	if len(gpu.Workloads) == 0 {
		if gpu.Platform == "apple" {
			return dimStyle.Render(fmt.Sprintf("%sNO ACTIVE JOB  ·  RENDER %.0f%%  ·  TILER %.0f%%", indent,
				gpu.RendererUtilization, gpu.TilerUtilization))
		}
		return dimStyle.Render(indent + "NO ACTIVE JOB")
	}
	parts := make([]string, 0, len(gpu.Workloads))
	for _, workload := range gpu.Workloads {
		name := strings.TrimSpace(workload.Name)
		if separator := strings.LastIndex(name, "/"); separator >= 0 {
			name = name[separator+1:]
		}
		if name == "" || strings.EqualFold(name, "[No data]") {
			name = fmt.Sprintf("PID %d", workload.PID)
		}
		owner := strings.TrimSpace(workload.User)
		if owner == "" {
			owner = "unknown"
		}
		vramSuffix := ""
		if workload.MemoryUsed > 0 {
			vramSuffix = " · VRAM " + bytes(workload.MemoryUsed)
		}
		parts = append(parts,
			gpuTitleStyle.Render("JOB ")+
				valueStyle.Render(name)+
				dimStyle.Render(" · USER "+owner+" · PID "+strconv.Itoa(workload.PID)+vramSuffix),
		)
	}
	return ansi.Truncate(indent+strings.Join(parts, dimStyle.Render("  │  ")), columns.contentWidth, "…")
}

func gpuLoadStatus(utilization float64) (string, lipgloss.Style) {
	switch {
	case usageLevel(utilization) == stateUnknown:
		return "UNKNOWN", dimStyle
	case utilization < 5:
		return "IDLE", gpuIdleStyle
	case utilization < 35:
		return "LIGHT", gpuLightStyle
	case utilization < 65:
		return "ACTIVE", gpuActiveStyle
	case utilization < 85:
		return "BUSY", gpuBusyStyle
	case utilization < 95:
		return "HIGH", gpuHighStyle
	default:
		return "MAX", gpuMaxStyle
	}
}

// Clock speed is informational. Temperature is the health signal, colored
// with the shared state scale.
func gpuClockStyle(int) lipgloss.Style {
	// Clock speed is not a health state. Keep it neutral.
	return valueStyle
}

func gpuTemperatureStyle(temperature int) lipgloss.Style {
	return stateStyle(temperatureLevel(temperature))
}
