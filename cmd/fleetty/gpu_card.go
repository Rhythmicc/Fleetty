package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func singleGPUOverviewLines(gpu gpuInfo, width int) []string {
	name := sanitizeTerminalText(gpu.Name)
	if gpu.Platform == "apple" && gpu.CoreCount > 0 {
		name += fmt.Sprintf(" · %d cores", gpu.CoreCount)
	}
	var telemetry []string
	if gpu.Platform != "apple" {
		if gpu.Temperature > 0 {
			telemetry = append(telemetry, fmt.Sprintf("%d°C", gpu.Temperature))
		}
		if gpu.Power > 0 || gpu.PowerLimit > 0 {
			telemetry = append(telemetry, fmt.Sprintf("%.0fW", gpu.Power))
		}
		if gpu.ClockMHz > 0 {
			telemetry = append(telemetry, fmt.Sprintf("%dMHz", gpu.ClockMHz))
		}
	}
	lines := []string{alignedPageLine(accentStyle.Render(name), dimStyle.Render(strings.Join(telemetry, " · ")), width)}
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
			_, style := gpuLoadStatus(item.usage)
			label := dimStyle.Render(item.label+" ") + style.Render(item.value)
			label = ansi.Truncate(label, cellWidth, "…")
			labels = append(labels, label+strings.Repeat(" ", max(0, cellWidth-ansi.StringWidth(label))))
			bars = append(bars, bar(item.usage, cellWidth))
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
			bar(gpu.Utilization, max(1, width-4)),
		},
		titleStyle: style, borderColor: colorGPUBorder,
	}
}

func gpuDeviceCountMeta(count int) string {
	meta := fmt.Sprintf("%d DEVICE", count)
	if count != 1 {
		meta += "S"
	}
	return meta
}
