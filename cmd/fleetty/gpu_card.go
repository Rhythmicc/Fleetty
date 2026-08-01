package main

import (
	"fmt"
	"strings"
)

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
