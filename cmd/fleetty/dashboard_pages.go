package main

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type monitorCapabilities struct {
	Battery  bool
	GPU      bool
	Slurm    bool
	Network  bool
	Services bool
}

type pagePanelSpec struct {
	title       string
	meta        string
	lines       []string
	titleStyle  lipgloss.Style
	borderColor color.Color
}

func (m *monitorModel) capabilities() monitorCapabilities {
	return monitorCapabilities{
		Battery: m.snapshot.Battery != nil,
		GPU: len(m.snapshot.GPUs) > 0 ||
			m.profile == machineProfileGPU ||
			m.snapshot.Profile == machineProfileGPU,
		Slurm:   m.slurmQueue != nil,
		Network: true,
		Services: len(m.snapshot.Services) > 0 || len(m.snapshot.Containers) > 0 ||
			len(m.snapshot.PM2Processes) > 0,
	}
}

func (page monitorPage) label() string {
	switch page {
	case monitorPageCompute:
		return "COMPUTE"
	case monitorPageNetwork:
		return "NETWORK"
	case monitorPageStorage:
		return "STORAGE"
	case monitorPageCustom:
		return "CUSTOM"
	default:
		return "OVERVIEW"
	}
}

func (m *monitorModel) monitorPagesView() string {
	width := usableWidth(m.width)
	header := m.renderMonitorPageHeader(width)
	footer := m.renderMonitorPageFooter(width)
	if m.snapshot.CollectedAt.IsZero() {
		return strings.Join([]string{
			header,
			panelStyle(width).Render("Collecting system metrics…"),
			footer,
		}, "\n")
	}

	body, placements := m.renderMonitorPage(width)
	bodyLines := strings.Split(body, "\n")
	if body == "" {
		bodyLines = nil
	}
	viewportHeight := max(1, m.height-lipgloss.Height(header)-lipgloss.Height(footer))
	maxScroll := max(0, len(bodyLines)-viewportHeight)
	m.dashboardScroll = min(max(0, m.dashboardScroll), maxScroll)
	end := min(len(bodyLines), m.dashboardScroll+viewportHeight)
	visible := bodyLines[m.dashboardScroll:end]

	m.dashboardContent = len(bodyLines)
	m.dashboardViewport = viewportHeight
	m.widgetPlacements = placements
	m.monitorRows = 0
	for _, placement := range placements {
		if placement.ID == dashboardPanelProcesses {
			m.monitorRows = placement.ProcessRows
		}
	}
	if maxScroll > 0 {
		scroll := fmt.Sprintf("  ↕ %d–%d/%d", m.dashboardScroll+1, end, len(bodyLines))
		footer = ansi.Truncate(footer, max(1, width-lipgloss.Width(scroll)), "") +
			dimStyle.Render(scroll)
	}

	sections := []string{header}
	if len(visible) > 0 {
		sections = append(sections, strings.Join(visible, "\n"))
	}
	sections = append(sections, footer)
	return strings.Join(sections, "\n")
}

func (m *monitorModel) renderMonitorPageHeader(width int) string {
	m.pageTabs = nil
	title := titleStyle.Render("FLEETTY")
	if m.nodeName != "" {
		title += dimStyle.Render(" │ ") + accentStyle.Render(truncate(m.nodeName, max(8, width/5)))
	}
	leftParts := []string{title, liveBadgeStyle.Render("● LIVE")}
	if width >= 92 && m.snapshot.OSName != "" {
		leftParts = append(leftParts, dimStyle.Render(truncate(m.snapshot.OSName, 24)))
	}
	if width >= 112 && m.snapshot.Uptime > 0 {
		leftParts = append(leftParts, dimStyle.Render(elapsed(m.snapshot.Uptime)+" up"))
	}
	if width >= 132 && m.slurmQueue != nil {
		leftParts = append(leftParts, processTitleStyle.Render("Slurm: "+m.slurmOverviewState()))
	}
	left := strings.Join(leftParts, dimStyle.Render(" │ "))

	var capabilityLabels []string
	capabilities := m.capabilities()
	if capabilities.Battery {
		battery := m.snapshot.Battery
		batteryText := fmt.Sprintf("BAT %.0f%%", battery.Percent)
		if battery.PowerSource != "" {
			batteryText += " " + compactPowerSource(battery.PowerSource)
		}
		if battery.TimeRemaining != "" {
			batteryText += " " + battery.TimeRemaining
		}
		capabilityLabels = append(capabilityLabels, batteryTitleStyle.Render(batteryText))
	}
	if capabilities.GPU {
		names := []string{"GPU"}
		for _, gpu := range m.snapshot.GPUs {
			if gpu.Platform == "apple" {
				names = append(names, "Metal", "UMA")
				break
			}
		}
		if len(names) == 1 {
			names = append(names, "CUDA")
		}
		capabilityLabels = append(capabilityLabels,
			dimStyle.Render("Caps: ")+gpuTitleStyle.Render(strings.Join(names, ",")))
	}
	if capabilities.Services {
		capabilityLabels = append(capabilityLabels, networkTitleStyle.Render("SERVICES"))
	}
	capabilityText := strings.Join(capabilityLabels, dimStyle.Render(" │ "))
	clock := "--:--:--"
	if !m.snapshot.CollectedAt.IsZero() {
		clock = m.snapshot.CollectedAt.Format("15:04:05")
	}
	right := dimStyle.Render("Refresh 1s │ "+strings.ToUpper(m.colorMode.String())+" │ ") +
		clockStyle.Render(clock)
	if capabilityText != "" && width >= 78 {
		right = capabilityText + dimStyle.Render(" │ ") + right
	}
	leftRoom := max(12, width-lipgloss.Width(right)-2)
	left = ansi.Truncate(left, leftRoom, "…")
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	firstLine := ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "")

	pages := m.availableMonitorPages()
	var tabParts []string
	x := 0
	for index, page := range pages {
		label := fmt.Sprintf("%d %s", index+1, page.label())
		rendered := dimStyle.Render("[" + label + "]")
		if m.monitorPage == page {
			rendered = liveBadgeStyle.Render("[" + label + "]")
		}
		if len(tabParts) > 0 {
			tabParts = append(tabParts, "  ")
			x += 2
		}
		m.pageTabs = append(m.pageTabs, monitorPageTab{
			Page: page, X: x, Width: lipgloss.Width(rendered),
		})
		tabParts = append(tabParts, rendered)
		x += lipgloss.Width(rendered)
	}
	tabs := strings.Join(tabParts, "")
	advanced := accentStyle.Render("[l CUSTOMIZE]") + dimStyle.Render(" ADVANCED")
	if width-lipgloss.Width(tabs)-lipgloss.Width(advanced) >= 3 {
		tabs += strings.Repeat(" ", width-lipgloss.Width(tabs)-lipgloss.Width(advanced)) + advanced
	}
	m.pageTabY = 1
	return firstLine + "\n" + ansi.Truncate(tabs, width, "")
}

func (m *monitorModel) slurmOverviewState() string {
	if m.slurmQueue == nil {
		return "off"
	}
	for _, display := range m.slurmQueue.Jobs {
		if isSlurmRunning(display.Job.State) {
			return "active"
		}
	}
	return "idle"
}

func compactPowerSource(source string) string {
	source = strings.TrimSpace(source)
	switch {
	case strings.Contains(strings.ToLower(source), "battery"):
		return "BATTERY"
	case strings.Contains(strings.ToLower(source), "ac"):
		return "AC"
	default:
		return strings.ToUpper(source)
	}
}

func (m *monitorModel) renderMonitorPageFooter(width int) string {
	if m.filtering {
		value := m.filter
		if value == "" {
			value = "process name, user, or PID"
		}
		return ansi.Truncate(accentStyle.Render("FILTER /")+" "+
			inputStyle.Render(value+"█")+"  "+dimStyle.Render("[enter] apply  [esc] cancel"), width, "")
	}
	pageRange := "1–3"
	if m.storage != nil {
		pageRange = "1–4"
	}
	hints := []string{
		keyHint(pageRange, "pages"),
		keyHint("tab", "next"),
		keyHint("m", "management"),
	}
	if m.monitorPage == monitorPageStorage {
		hints = append(hints,
			keyHint("↑↓", "select"),
			keyHint("enter/click", "open"),
			keyHint("pg↑↓", "pages"),
			keyHint("d", "delete"),
			keyHint("z", "7z + delete"),
			keyHint("←/⌫", "up"),
			keyHint("home", "root"),
		)
	} else if m.monitorPage == monitorPageOverview ||
		m.monitorPage == monitorPageCompute {
		hints = append(hints,
			keyHint("↑↓", "select"),
			keyHint("enter", "details"),
			keyHint("/", "filter"),
		)
	}
	hints = append(hints,
		keyHint("l", "customize"),
		keyHint("t", "theme"),
		keyHint("r", "refresh"),
		keyHint("q", "quit"),
	)
	joined := strings.Join(hints, "  ")
	if lipgloss.Width(joined) > width {
		return ansi.Truncate(joined, width, "")
	}
	remaining := width - lipgloss.Width(joined) - 2
	if remaining < 12 || strings.TrimSpace(m.status) == "" {
		return joined
	}
	return joined + "  " + dimStyle.Render(truncate(m.status, remaining))
}

func (m *monitorModel) renderMonitorPage(width int) (string, []widgetPlacement) {
	switch m.monitorPage {
	case monitorPageCompute:
		return m.renderComputePage(width)
	case monitorPageNetwork:
		return m.renderNetworkPage(width)
	case monitorPageStorage:
		return m.renderStoragePage(width)
	default:
		return m.renderOverviewPage(width)
	}
}

func (m *monitorModel) renderOverviewPage(width int) (string, []widgetPlacement) {
	capabilities := m.capabilities()
	columns := width >= 96
	rich := width >= 96 && m.height >= 34
	var sections []string
	y := 0
	appendSection := func(section string) {
		if section == "" {
			return
		}
		sections = append(sections, section)
		y += lipgloss.Height(section)
	}

	summaryWidth := width
	if columns {
		summaryWidth = (width - 1) / 2
	}
	host := m.hostHealthPanelSpec(summaryWidth, rich)
	compute := m.computeActivityPanelSpec(summaryWidth)
	tallGrowth := 0
	if columns && m.height > 40 {
		tallGrowth = m.height - 40
	}
	detailGrowth := tallGrowth / 2
	detailWidth := width
	if columns && (capabilities.Slurm || !capabilities.GPU) {
		detailWidth = (width - 1) / 2
	}
	networkGrowth := detailGrowth
	if columns && !capabilities.GPU {
		networkGrowth = 2
	}
	network := m.networkActivityPanelSpec(detailWidth, rich, 5+networkGrowth)
	queueRows := 4
	if rich {
		queueRows = 7 + detailGrowth
	}
	queue := m.nodeQueuePanelSpec(queueRows, detailWidth)
	networkRendered := false
	summaryTarget := max(pagePanelLineHeight(host), pagePanelLineHeight(compute))
	detailTarget := max(pagePanelLineHeight(network), pagePanelLineHeight(queue))

	switch {
	case columns && capabilities.GPU:
		appendSection(renderPagePanelRowAtLeast(width, summaryTarget, host, compute))
	case columns:
		appendSection(renderPagePanelRowAtLeast(width, summaryTarget, host, network))
		networkRendered = true
	default:
		appendSection(renderPagePanel(width, host))
		if capabilities.GPU {
			appendSection(renderPagePanel(width, compute))
		}
	}

	if !networkRendered {
		if columns && capabilities.Slurm {
			appendSection(renderPagePanelRowAtLeast(width, detailTarget, network, queue))
		} else {
			appendSection(renderPagePanelAtLeast(width, detailTarget, network))
			if capabilities.Slurm {
				appendSection(renderPagePanel(width, queue))
			}
		}
	} else if capabilities.Slurm {
		appendSection(renderPagePanel(width, queue))
	}

	headerHeight := 2
	footerHeight := 1
	processOverhead := 3
	availableRows := m.height - headerHeight - footerHeight - y - processOverhead
	// Taller terminals grow the summary/detail regions first. Every row left in
	// the process panel then becomes a real process row, keeping the component's
	// information density coupled to its rendered height.
	processPanelRows := max(3, availableRows)
	processRows := processPanelRows
	processPanel := m.renderOverviewProcesses(width, processPanelRows, processRows)
	processY := y
	appendSection(processPanel)
	placement := widgetPlacement{
		ID: dashboardPanelProcesses, X: 0, Y: processY,
		Width: width, Height: lipgloss.Height(processPanel),
		ProcessRows: processRows, ProcessRowStart: 2,
	}
	return strings.Join(sections, "\n"), []widgetPlacement{placement}
}

func (m *monitorModel) hostHealthPanelSpec(width int, rich bool) pagePanelSpec {
	memoryUsage := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
	diskUsage := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
	memoryAvailable := counterDelta(m.snapshot.MemoryTotal, m.snapshot.MemoryUsed)
	diskFree := counterDelta(m.snapshot.DiskTotal, m.snapshot.DiskUsed)
	cpuState, cpuStateStyle := healthStatus(m.snapshot.CPUPercent)
	memoryState, memoryStateStyle := healthStatus(memoryUsage)
	diskState, diskStateStyle := capacityStatus(diskUsage)
	lines := []string{
		fmt.Sprintf("%s  %s  %s  %s",
			cpuTitleStyle.Render("CPU"),
			valueStyle.Render(fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent)),
			cpuStateStyle.Render(cpuState),
			dimStyle.Render(truncate(m.snapshot.LoadAverage, 30))),
		sparkline(m.cpuHistory, 28, 100, cpuTitleStyle),
		fmt.Sprintf("%s  %s  %s  %s",
			memoryTitleStyle.Render("MEMORY"),
			valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal))),
			memoryStateStyle.Render(memoryState),
			dimStyle.Render("AVAILABLE "+bytes(memoryAvailable))),
		bar(memoryUsage, 28),
		fmt.Sprintf("%s  %s  %s  %s",
			diskTitleStyle.Render("DISK /"),
			valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.DiskUsed), bytes(m.snapshot.DiskTotal))),
			diskStateStyle.Render(diskState),
			dimStyle.Render("FREE "+bytes(diskFree))),
		bar(diskUsage, 28),
	}
	if rich {
		contentWidth := max(24, width-4)
		load := strings.TrimSpace(strings.TrimPrefix(m.snapshot.LoadAverage, "load"))
		cpuModel := m.hostCPUModelLabel()
		if cpuModel == "" {
			cpuModel = "CPU model unavailable"
		}
		cpuCount := ""
		if m.snapshot.CPUCores > 0 {
			cpuCount = fmt.Sprintf("%d vCPU", m.snapshot.CPUCores)
		}
		loadText := dimStyle.Render("LOAD ") + valueStyle.Render(load) +
			dimStyle.Render("  (1m 5m 15m)")
		loadGraphWidth := min(24, max(6,
			contentWidth-lipgloss.Width(loadText)-lipgloss.Width(cpuModel)-4))
		lines = []string{
			alignedPageLine(
				cpuTitleStyle.Render("CPU")+"  "+
					valueStyle.Render(fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent))+"  "+
					cpuStateStyle.Render(cpuState),
				dimStyle.Render(cpuCount), contentWidth),
			alignedPageLine(
				loadText+"  "+sparkline(m.cpuHistory, loadGraphWidth, 100, cpuTitleStyle),
				dimStyle.Render(cpuModel), contentWidth),
			pageRule(contentWidth),
			alignedPageLine(memoryTitleStyle.Render("MEMORY"),
				dimStyle.Render(bytes(m.snapshot.MemoryUsed)+" used"), contentWidth),
			fmt.Sprintf("%s  %s  %s",
				valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal))),
				dimStyle.Render(fmt.Sprintf("(%.1f%%)", memoryUsage)),
				memoryStateStyle.Render(memoryState)),
			alignedPageLine(bar(memoryUsage, min(28, max(12, contentWidth/3))),
				dimStyle.Render(bytes(memoryAvailable)+" available"), contentWidth),
			pageRule(contentWidth),
			alignedPageLine(diskTitleStyle.Render("ROOT DISK  (/)"),
				dimStyle.Render(bytes(m.snapshot.DiskUsed)+" used"), contentWidth),
			fmt.Sprintf("%s  %s  %s",
				valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.DiskUsed), bytes(m.snapshot.DiskTotal))),
				dimStyle.Render(fmt.Sprintf("(%.1f%%)", diskUsage)),
				diskStateStyle.Render(diskState)),
			alignedPageLine(bar(diskUsage, min(28, max(12, contentWidth/3))),
				dimStyle.Render(bytes(diskFree)+" free"), contentWidth),
		}
	}
	return pagePanelSpec{
		title: "HOST HEALTH", meta: "CURRENT",
		lines: lines, titleStyle: processRunningStyle, borderColor: colorPanelBorder,
	}
}

func (m *monitorModel) hostCPUModelLabel() string {
	model := strings.TrimSpace(m.snapshot.CPUModel)
	for _, gpu := range m.snapshot.GPUs {
		if model != "" && strings.EqualFold(model, strings.TrimSpace(gpu.Name)) {
			if gpu.Platform == "apple" {
				return "Apple Silicon CPU"
			}
			return ""
		}
	}
	return model
}

func alignedPageLine(left, right string, width int) string {
	width = max(1, width)
	if right == "" {
		return ansi.Truncate(left, width, "")
	}
	right = ansi.Truncate(right, max(1, width/2), "…")
	leftRoom := max(1, width-lipgloss.Width(right)-1)
	left = ansi.Truncate(left, leftRoom, "…")
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func pageRule(width int) string {
	return dimStyle.Render(strings.Repeat("─", max(1, width)))
}

func healthStatus(usage float64) (string, lipgloss.Style) {
	switch {
	case usage >= 90:
		return "CRITICAL", dangerStyle
	case usage >= 80:
		return "HIGH", processWaitingStyle
	case usage >= 70:
		return "WATCH", warningStyle
	default:
		return "HEALTHY", processRunningStyle
	}
}

func (m *monitorModel) computeActivityPanelSpec(width int) pagePanelSpec {
	var lines []string
	if m.snapshot.GPUError != "" && len(m.snapshot.GPUs) == 0 {
		lines = append(lines, warningStyle.Render("GPU metrics unavailable: "+m.snapshot.GPUError))
	}
	gpus := m.snapshot.GPUs
	if len(gpus) > 1 {
		lines = multiGPUOverviewLines(gpus, max(24, width-4))
		meta := fmt.Sprintf("%d DEVICES", len(gpus))
		return pagePanelSpec{
			title: "COMPUTE ACTIVITY", meta: meta,
			lines: lines, titleStyle: gpuTitleStyle, borderColor: colorGPUBorder,
		}
	}
	limit := min(3, len(gpus))
	for _, gpu := range gpus[:limit] {
		status, statusStyle := gpuLoadStatus(gpu.Utilization)
		lines = append(lines,
			fmt.Sprintf("%s  %-22s  %s",
				accentStyle.Render(fmt.Sprintf("GPU %d", gpu.Index)),
				truncate(gpu.Name, 22),
				statusStyle.Render(status)),
			gpuMetricLine(
				"UTIL",
				bar(gpu.Utilization, 18),
				statusStyle.Render(fmt.Sprintf("%.0f%%", gpu.Utilization)),
			),
		)
		memoryUsage := percent(gpu.MemoryUsed, gpu.MemoryTotal)
		_, memoryStyle := gpuLoadStatus(memoryUsage)
		lines = append(lines, gpuMetricLine(
			"MEMORY",
			bar(memoryUsage, 18),
			memoryStyle.Render(gpuMemoryText(gpu, 1, 1)),
		))
		if len(gpus) == 1 {
			if gpu.Platform == "apple" {
				_, rendererStyle := gpuLoadStatus(gpu.RendererUtilization)
				_, tilerStyle := gpuLoadStatus(gpu.TilerUtilization)
				lines = append(lines,
					gpuMetricLine(
						"RENDER",
						bar(gpu.RendererUtilization, 18),
						rendererStyle.Render(fmt.Sprintf("%.0f%%", gpu.RendererUtilization)),
					),
					gpuMetricLine(
						"TILER",
						bar(gpu.TilerUtilization, 18),
						tilerStyle.Render(fmt.Sprintf("%.0f%%", gpu.TilerUtilization)),
					),
					pageRule(36),
					gpuTitleStyle.Render(fmt.Sprintf("CORES %d", gpu.CoreCount))+
						dimStyle.Render("  ·  Unified memory architecture"),
					dimStyle.Render("API Metal  ·  Memory shared with the system"),
					dimStyle.Render("ARCH Integrated Apple Silicon GPU"),
					dimStyle.Render("SOURCE IOKit AGXAccelerator  ·  Refresh 1s"),
				)
			} else {
				lines = append(lines,
					gpuMetricLine("CLOCK", "", fmt.Sprintf("%d MHz", gpu.ClockMHz)),
					gpuMetricLine("POWER", "", fmt.Sprintf("%.0f / %.0f W", gpu.Power, gpu.PowerLimit)),
					gpuMetricLine("TEMP", "", fmt.Sprintf("%d°C", gpu.Temperature)),
					pageRule(36),
					dimStyle.Render("NVIDIA CUDA device  ·  Live telemetry"),
				)
			}
		} else if gpu.Platform == "apple" {
			lines = append(lines, statusStyle.Render(gpuTelemetry(gpu, false)))
		} else {
			lines = append(lines, statusStyle.Render(gpuTelemetry(gpu, true)))
		}
		if gpu.Platform != "apple" {
			lines = append(lines, gpuWorkloadSummary(gpu))
		}
	}
	if len(gpus) > 0 && gpus[0].Platform != "apple" && gpus[0].DriverVersion != "" {
		lines = append(lines, pageRule(36),
			dimStyle.Render("DRIVER ")+valueStyle.Render(gpus[0].DriverVersion)+
				dimStyle.Render("  ·  NVIDIA management telemetry"))
	}
	if len(gpus) > limit {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("+ %d more devices on the Compute page", len(gpus)-limit)))
	}
	if len(lines) == 0 {
		lines = append(lines, dimStyle.Render("No GPU capability was reported by this host."))
	}
	meta := fmt.Sprintf("%d DEVICE", len(gpus))
	if len(gpus) != 1 {
		meta += "S"
	}
	return pagePanelSpec{
		title: "COMPUTE ACTIVITY", meta: meta,
		lines: lines, titleStyle: gpuTitleStyle, borderColor: colorGPUBorder,
	}
}

func multiGPUOverviewLines(gpus []gpuInfo, width int) []string {
	width = max(24, width)
	limit := min(4, len(gpus))
	lines := []string{multiGPUOverviewHeader(width)}
	var (
		active        int
		workloads     int
		totalMemory   uint64
		usedMemory    uint64
		totalPower    float64
		totalLimit    float64
		totalUtil     float64
		busiestIndex  int
		busiestUtil   float64
		driverVersion string
	)
	for index, gpu := range gpus {
		if gpu.Utilization >= 5 {
			active++
		}
		workloads += len(gpu.Workloads)
		totalMemory += gpu.MemoryTotal
		usedMemory += gpu.MemoryUsed
		totalPower += gpu.Power
		totalLimit += gpu.PowerLimit
		totalUtil += gpu.Utilization
		if index == 0 || gpu.Utilization > busiestUtil {
			busiestIndex, busiestUtil = gpu.Index, gpu.Utilization
		}
		if driverVersion == "" {
			driverVersion = strings.TrimSpace(gpu.DriverVersion)
		}
		if index < limit {
			lines = append(lines, multiGPUOverviewRow(gpu, width))
		}
	}

	lines = append(lines, pageRule(width))
	average := totalUtil / float64(max(1, len(gpus)))
	lines = append(lines, ansi.Truncate(
		fmt.Sprintf("%s  %s  %s",
			gpuTitleStyle.Render(fmt.Sprintf("ACTIVE %d/%d", active, len(gpus))),
			dimStyle.Render(fmt.Sprintf("AVG %.0f%%", average)),
			gpuLoadStyle(busiestUtil).Render(fmt.Sprintf("PEAK GPU %d  %.0f%%", busiestIndex, busiestUtil))),
		width, "…",
	))
	power := fmt.Sprintf("%.0f W", totalPower)
	if totalLimit > 0 {
		power = fmt.Sprintf("%.0f/%.0f W", totalPower, totalLimit)
	}
	lines = append(lines, ansi.Truncate(
		fmt.Sprintf("%s  %s  %s",
			memoryTitleStyle.Render("VRAM "+compactByteCount(usedMemory)+"/"+compactByteCount(totalMemory)),
			dimStyle.Render("POWER "+power),
			dimStyle.Render(fmt.Sprintf("JOBS %d", workloads))),
		width, "…",
	))
	driver := "NVIDIA management telemetry"
	if driverVersion != "" {
		driver = "DRIVER " + driverVersion + "  ·  " + driver
	}
	lines = append(lines, ansi.Truncate(dimStyle.Render(driver), width, "…"))
	more := ""
	if len(gpus) > limit {
		more = fmt.Sprintf("  ·  +%d more devices", len(gpus)-limit)
	}
	lines = append(lines, ansi.Truncate(
		accentStyle.Render("[2 COMPUTE]")+" "+dimStyle.Render("full per-device telemetry"+more),
		width, "…",
	))
	return lines
}

func multiGPUOverviewHeader(width int) string {
	var header string
	switch {
	case width >= 68:
		header = fixedCell("GPU", 3, false) + " " +
			fixedCell("MODEL", 17, false) + " " +
			fixedCell("LOAD", 12, false) + " " +
			fixedCell("VRAM", 15, false) + " " +
			fixedCell("PWR", 6, true) + " " +
			fixedCell("TEMP", 5, true) + " JOB"
	case width >= 50:
		header = fixedCell("GPU", 3, false) + " " +
			fixedCell("MODEL", 14, false) + " " +
			fixedCell("LOAD", 11, false) + " " +
			fixedCell("VRAM", 13, false) + " JOB"
	default:
		header = fixedCell("GPU / MODEL", max(10, width-17), false) + " LOAD / VRAM"
	}
	return dimStyle.Copy().Bold(true).Render(ansi.Truncate(header, width, ""))
}

func multiGPUOverviewRow(gpu gpuInfo, width int) string {
	statusStyle := gpuLoadStyle(gpu.Utilization)
	load := bar(gpu.Utilization, 5) + " " + fmt.Sprintf("%3.0f%%", gpu.Utilization)
	memoryUsage := percent(gpu.MemoryUsed, gpu.MemoryTotal)
	memoryStyle := gpuLoadStyle(memoryUsage)
	memory := compactByteCount(gpu.MemoryUsed) + "/" + compactByteCount(gpu.MemoryTotal)
	job := gpuOverviewWorkload(gpu)
	id := accentStyle.Render(fmt.Sprintf("%d", gpu.Index))
	model := shortGPUName(gpu.Name)
	var row string
	switch {
	case width >= 68:
		row = fixedCell(id, 3, false) + " " +
			fixedCell(model, 17, false) + " " +
			fixedCell(load, 12, false) + " " +
			memoryStyle.Render(fixedCell(memory, 15, false)) + " " +
			statusStyle.Render(fixedCell(fmt.Sprintf("%.0fW", gpu.Power), 6, true)) + " " +
			statusStyle.Render(fixedCell(fmt.Sprintf("%d°C", gpu.Temperature), 5, true)) + " " +
			gpuTitleStyle.Render(job)
	case width >= 50:
		row = fixedCell(id, 3, false) + " " +
			fixedCell(model, 14, false) + " " +
			fixedCell(load, 11, false) + " " +
			memoryStyle.Render(fixedCell(memory, 13, false)) + " " +
			gpuTitleStyle.Render(job)
	default:
		left := fixedCell(id, 3, false) + " " + model
		right := statusStyle.Render(fmt.Sprintf("%.0f%%", gpu.Utilization)) + " " +
			memoryStyle.Render(memory)
		row = alignedPageLine(left, right, width)
	}
	return ansi.Truncate(row, width, "…")
}

func gpuLoadStyle(utilization float64) lipgloss.Style {
	_, style := gpuLoadStatus(utilization)
	return style
}

func shortGPUName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"NVIDIA GeForce ", "NVIDIA "} {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

func gpuOverviewWorkload(gpu gpuInfo) string {
	if len(gpu.Workloads) == 0 {
		return "—"
	}
	workload := gpu.Workloads[0]
	name := strings.TrimSpace(workload.Name)
	if separator := strings.LastIndex(name, "/"); separator >= 0 {
		name = name[separator+1:]
	}
	if name == "" {
		name = fmt.Sprintf("PID %d", workload.PID)
	}
	if workload.User != "" {
		name += " · " + workload.User
	}
	if len(gpu.Workloads) > 1 {
		name += fmt.Sprintf(" +%d", len(gpu.Workloads)-1)
	}
	return name
}

func compactByteCount(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	number := float64(value)
	index := 0
	for number >= unit && index < len(units)-1 {
		number /= unit
		index++
	}
	if number >= 10 {
		return fmt.Sprintf("%.0f%s", number, units[index])
	}
	return fmt.Sprintf("%.1f%s", number, units[index])
}

func gpuWorkloadSummary(gpu gpuInfo) string {
	uuid := compactGPUUUID(gpu.UUID)
	if len(gpu.Workloads) == 0 {
		if uuid == "" {
			return dimStyle.Render("WORKLOAD  none reported")
		}
		return dimStyle.Render("UUID " + uuid + "  ·  no compute workload")
	}
	workload := gpu.Workloads[0]
	name := strings.TrimSpace(workload.Name)
	if separator := strings.LastIndex(name, "/"); separator >= 0 {
		name = name[separator+1:]
	}
	if name == "" {
		name = fmt.Sprintf("PID %d", workload.PID)
	}
	owner := strings.TrimSpace(workload.User)
	if owner == "" {
		owner = fmt.Sprintf("PID %d", workload.PID)
	}
	extra := ""
	if len(gpu.Workloads) > 1 {
		extra = fmt.Sprintf("  +%d more", len(gpu.Workloads)-1)
	}
	prefix := ""
	if uuid != "" {
		prefix = dimStyle.Render("UUID " + uuid + "  ·  ")
	}
	return prefix + gpuTitleStyle.Render("JOB ") +
		valueStyle.Render(truncate(name, 22)) +
		dimStyle.Render(" ("+owner+")"+extra)
}

func compactGPUUUID(uuid string) string {
	uuid = strings.TrimSpace(uuid)
	if len(uuid) <= 18 {
		return uuid
	}
	return uuid[:12] + "…" + uuid[len(uuid)-4:]
}

func gpuMetricLine(label, visual, value string) string {
	parts := []string{dimStyle.Render(fixedCell(label, 8, false))}
	if visual != "" {
		parts = append(parts, visual)
	}
	if value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
}

func (m *monitorModel) networkActivityPanelSpec(width int, rich bool, detailLimit int) pagePanelSpec {
	detailWidth := max(24, width-4)
	summary := networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
		networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s") + "    " +
		dimStyle.Render(fmt.Sprintf("TOTAL ↓ %s  ↑ %s",
			bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal)))
	detailLimit = max(1, detailLimit)
	detailTitle, details := m.networkOverviewDetails(detailWidth, detailLimit)
	if !rich {
		lines := []string{
			summary,
			renderMetricVisual(metricCard{
				visual:         metricVisualNetwork,
				primaryHistory: m.networkRXHistory, secondaryHistory: m.networkTXHistory,
			}, max(12, min(72, width-4))),
			networkTitleStyle.Render(detailTitle),
		}
		lines = append(lines, details...)
		return pagePanelSpec{
			title: "NETWORK ACTIVITY", meta: "NOW + TOTAL",
			lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
		}
	}

	contentWidth := max(24, width-4)
	if width >= 96 {
		leftWidth := min(68, max(36, (contentWidth-3)/2))
		rightWidth := contentWidth - leftWidth - 3
		detailTitle, details = m.networkOverviewDetails(rightWidth, detailLimit)
		left := []string{
			dimStyle.Render("60 SECOND HISTORY"),
			networkHistoryLine("RX", m.networkRXHistory, leftWidth, networkRXStyle),
			networkHistoryLine("TX", m.networkTXHistory, leftWidth, networkTXStyle),
			alignedPageLine(dimStyle.Render("TOTAL RX"), valueStyle.Render(bytes(m.snapshot.NetworkRXTotal)), leftWidth),
			alignedPageLine(dimStyle.Render("TOTAL TX"), valueStyle.Render(bytes(m.snapshot.NetworkTXTotal)), leftWidth),
			alignedPageLine(dimStyle.Render("SOURCE"), valueStyle.Render(m.primaryNetworkSource()), leftWidth),
			dimStyle.Render("Rates refresh every second."),
		}
		right := []string{
			networkTitleStyle.Render(detailTitle),
			m.networkOverviewHeader(rightWidth),
		}
		right = append(right, details...)
		rowCount := max(len(left), len(right))
		lines := []string{summary, pageRule(contentWidth)}
		for index := 0; index < rowCount; index++ {
			leftLine, rightLine := "", ""
			if index < len(left) {
				leftLine = left[index]
			}
			if index < len(right) {
				rightLine = right[index]
			}
			lines = append(lines, joinPageColumns(leftLine, rightLine, leftWidth, contentWidth))
		}
		return pagePanelSpec{
			title: "NETWORK ACTIVITY", meta: "NOW + TOTAL",
			lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
		}
	}

	lines := []string{
		summary,
		pageRule(contentWidth),
		dimStyle.Render("60 SECOND HISTORY"),
		networkHistoryLine("RX", m.networkRXHistory, contentWidth, networkRXStyle),
		networkHistoryLine("TX", m.networkTXHistory, contentWidth, networkTXStyle),
		pageRule(contentWidth),
		networkTitleStyle.Render(detailTitle),
		m.networkOverviewHeader(contentWidth),
	}
	lines = append(lines, details...)
	return pagePanelSpec{
		title: "NETWORK ACTIVITY", meta: "NOW + TOTAL",
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func (m *monitorModel) networkOverviewDetails(width, limit int) (string, []string) {
	var lines []string
	switch {
	case len(m.snapshot.NetworkProcesses) > 0:
		limit = min(limit, len(m.snapshot.NetworkProcesses))
		for _, process := range m.snapshot.NetworkProcesses[:limit] {
			lines = append(lines, renderNetworkProcessRow(process, width))
		}
		return "TOP APPLICATIONS / PROCESSES", lines
	case len(m.snapshot.NetworkInterfaces) > 0:
		limit = min(limit, len(m.snapshot.NetworkInterfaces))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
			lines = append(lines, renderNetworkInterfaceRow(networkInterface, width))
		}
		return "NETWORK INTERFACES", lines
	default:
		return "TRAFFIC SOURCES", []string{
			dimStyle.Render(truncate(m.snapshot.NetworkProcessError, width)),
		}
	}
}

func (m *monitorModel) networkOverviewHeader(width int) string {
	if len(m.snapshot.NetworkProcesses) > 0 {
		return networkProcessHeader(width)
	}
	return networkInterfaceHeader(width)
}

func (m *monitorModel) primaryNetworkSource() string {
	if len(m.snapshot.NetworkInterfaces) > 0 {
		return m.snapshot.NetworkInterfaces[0].Name
	}
	return "aggregate"
}

func networkHistoryLine(label string, history []float64, width int, style lipgloss.Style) string {
	peak := historyMax(history)
	peakText := "PEAK " + bytes(uint64(math.Max(0, peak))) + "/s"
	prefix := style.Render(label + " ")
	graphWidth := max(8, width-lipgloss.Width(prefix)-lipgloss.Width(peakText)-2)
	return alignedPageLine(
		prefix+sparkline(history, graphWidth, math.Max(1, peak), style),
		dimStyle.Render(peakText), width,
	)
}

func joinPageColumns(left, right string, leftWidth, totalWidth int) string {
	left = ansi.Truncate(left, leftWidth, "")
	left += strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(left)))
	separator := dimStyle.Render(" │ ")
	rightWidth := max(1, totalWidth-leftWidth-lipgloss.Width(separator))
	return left + separator + ansi.Truncate(right, rightWidth, "…")
}

func (m *monitorModel) nodeQueuePanelSpec(rowLimit, width int) pagePanelSpec {
	queue := m.slurmQueue
	if queue == nil {
		return pagePanelSpec{}
	}
	meta := fmt.Sprintf("%s · %s", queue.Cluster, queue.Node)
	running, queued, next := 0, 0, 0
	for _, display := range queue.Jobs {
		switch {
		case isSlurmRunning(display.Job.State):
			running++
		case display.Next:
			next++
		default:
			queued++
		}
	}
	lines := []string{
		strings.Join([]string{
			processRunningStyle.Render(fmt.Sprintf("● RUNNING (%d)", running)),
			processWaitingStyle.Render(fmt.Sprintf("◆ NEXT (%d)", next)),
			gpuTitleStyle.Render(fmt.Sprintf("○ QUEUED (%d)", queued)),
		}, dimStyle.Render(" · ")),
		slurmNodeJobHeader(max(20, width-4)),
	}
	if queue.Warning != "" {
		lines = append(lines, warningStyle.Render(queue.Warning))
	}
	limit := min(rowLimit, len(queue.Jobs))
	for _, job := range queue.Jobs[:limit] {
		lines = append(lines, renderSlurmNodeJobRow(job, max(20, width-4)))
	}
	if len(queue.Jobs) == 0 {
		lines = append(lines, dimStyle.Render("No running or eligible queued jobs for this node."))
	}
	if !queue.CollectedAt.IsZero() {
		age := time.Since(queue.CollectedAt)
		if !m.snapshot.CollectedAt.IsZero() {
			age = m.snapshot.CollectedAt.Sub(queue.CollectedAt)
			if age < 0 {
				age = 0
			}
		}
		lines = append(lines,
			pageRule(max(20, width-4)),
			dimStyle.Render(fmt.Sprintf("%d visible jobs · refreshed %s ago",
				len(queue.Jobs), hubAge(age))),
		)
	}
	return pagePanelSpec{
		title: "NODE QUEUE / SLURM", meta: meta,
		lines: lines, titleStyle: processTitleStyle, borderColor: colorProcessBorder,
	}
}

func renderPagePanel(width int, spec pagePanelSpec) string {
	return btopPanel(width, spec.title, spec.meta, strings.Join(spec.lines, "\n"),
		spec.titleStyle, spec.borderColor)
}

func renderPagePanelAtLeast(width, lineCount int, spec pagePanelSpec) string {
	return renderPagePanel(width, padPagePanelSpec(spec, lineCount))
}

func renderPagePanelRow(width int, specs ...pagePanelSpec) string {
	return renderPagePanelRowAtLeast(width, 0, specs...)
}

func renderPagePanelRowAtLeast(width, lineCount int, specs ...pagePanelSpec) string {
	if len(specs) == 0 {
		return ""
	}
	gap := 1
	available := width - gap*(len(specs)-1)
	base := available / len(specs)
	extra := available % len(specs)
	maxLines := 1
	for _, spec := range specs {
		maxLines = max(maxLines, len(spec.lines))
	}
	maxLines = max(maxLines, lineCount)
	parts := make([]string, 0, len(specs)*2)
	for index, spec := range specs {
		panelWidth := base
		if index < extra {
			panelWidth++
		}
		lines := append([]string(nil), spec.lines...)
		for len(lines) < maxLines {
			lines = append(lines, "")
		}
		if index > 0 {
			parts = append(parts, strings.Repeat(" ", gap))
		}
		parts = append(parts, btopPanel(
			panelWidth, spec.title, spec.meta, strings.Join(lines, "\n"),
			spec.titleStyle, spec.borderColor,
		))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func pagePanelLineHeight(specs ...pagePanelSpec) int {
	height := 0
	for _, spec := range specs {
		height = max(height, len(spec.lines))
	}
	return height
}

func padPagePanelSpec(spec pagePanelSpec, lineCount int) pagePanelSpec {
	spec.lines = append([]string(nil), spec.lines...)
	for len(spec.lines) < lineCount {
		spec.lines = append(spec.lines, "")
	}
	return spec
}

func (m *monitorModel) renderOverviewProcesses(width, panelRows, visibleRows int) string {
	return m.renderProcessPreviewRows(
		width, panelRows, visibleRows, "TOP PROCESSES",
		fmt.Sprintf("TOP %d · FULL LIST [2 COMPUTE]", min(visibleRows, len(m.filteredProcesses()))),
	)
}

func (m *monitorModel) renderProcessPreview(width, rows int, title, meta string) string {
	return m.renderProcessPreviewRows(width, rows, rows, title, meta)
}

func (m *monitorModel) renderProcessPreviewRows(width, panelRows, visibleRows int, title, meta string) string {
	processes := m.filteredProcesses()
	panelRows = max(1, panelRows)
	visibleRows = min(max(1, visibleRows), panelRows)
	m.clampMonitorProcessCursor(visibleRows)
	format := newProcessFormat(width)
	lines := []string{processTableHeader(format.header(), width-4)}
	end := min(len(processes), m.monitorOffset+visibleRows)
	for index := m.monitorOffset; index < end; index++ {
		row := format.row(processes[index])
		if index == m.monitorCursor {
			row = selectedProcessStyle(m.colorMode).Render(row)
		} else {
			row = processStateStyle(processes[index].State).Render(row)
		}
		lines = append(lines, row)
	}
	if len(processes) == 0 {
		lines = append(lines, dimStyle.Render("No process data available."))
	}
	for len(lines) < panelRows+1 {
		lines = append(lines, "")
	}
	if m.filter != "" {
		meta += " · FILTER " + truncate(m.filter, 16)
	}
	return btopPanel(width, title, meta, strings.Join(lines, "\n"),
		processTitleStyle, colorProcessBorder)
}

func (m *monitorModel) renderComputePage(width int) (string, []widgetPlacement) {
	var (
		sections []string
		y        int
	)
	appendSection := func(section string) {
		if section == "" {
			return
		}
		sections = append(sections, section)
		y += lipgloss.Height(section)
	}
	if m.capabilities().GPU {
		layout := dashboardLayout{width: width, height: m.height, compactGPU: width < 112}
		appendSection(m.gpuPanel(layout))
	} else {
		appendSection(renderPagePanel(width, pagePanelSpec{
			title: "COMPUTE", meta: "CPU",
			lines: []string{
				valueStyle.Render(fmt.Sprintf("CPU %.1f%%", m.snapshot.CPUPercent)),
				dimStyle.Render(m.snapshot.LoadAverage),
				sparkline(m.cpuHistory, max(12, width-4), 100, cpuTitleStyle),
			},
			titleStyle: cpuTitleStyle, borderColor: colorCPUBorder,
		}))
	}
	cpuCard, _ := m.metricWidgetCard(dashboardPanelCPU)
	memoryCard, _ := m.metricWidgetCard(dashboardPanelMemory)
	if width >= 96 {
		appendSection(lipgloss.JoinHorizontal(lipgloss.Top,
			renderMetricWidget(cpuCard, (width-1)/2, widgetSizeSmall),
			" ",
			renderMetricWidget(memoryCard, width-(width-1)/2-1, widgetSizeSmall),
		))
	} else {
		appendSection(renderMetricWidget(cpuCard, width, widgetSizeSmall))
		appendSection(renderMetricWidget(memoryCard, width, widgetSizeSmall))
	}
	if m.slurmQueue != nil {
		appendSection(m.slurmNodePanel(width, min(6, max(3, m.height/5))))
	}
	const (
		headerHeight    = 2
		footerHeight    = 1
		processOverhead = 3
	)
	processRows := max(4, m.height-headerHeight-footerHeight-y-processOverhead)
	processY := y
	workloads := m.renderProcessPreview(
		width, processRows, "COMPUTE WORKLOADS",
		fmt.Sprintf("TOP %d BY CPU · ENTER DETAILS · / FILTER", min(processRows, len(m.filteredProcesses()))),
	)
	appendSection(workloads)
	return strings.Join(sections, "\n"), []widgetPlacement{{
		ID: dashboardPanelProcesses, X: 0, Y: processY, Width: width,
		Height: lipgloss.Height(workloads), ProcessRows: processRows, ProcessRowStart: 2,
	}}
}

func (m *monitorModel) renderNetworkPage(width int) (string, []widgetPlacement) {
	const (
		headerHeight  = 2
		footerHeight  = 1
		panelOverhead = 2
	)
	bodyHeight := max(15, m.height-headerHeight-footerHeight)
	summary := renderPagePanel(width, m.networkTrafficSummaryPanelSpec(width))
	remaining := max(10, bodyHeight-lipgloss.Height(summary))

	applicationHeight := min(
		len(m.snapshot.NetworkProcesses)+1+panelOverhead,
		max(7, remaining/2),
	)
	applicationHeight = max(5, applicationHeight)
	interfaceHeight := remaining - applicationHeight
	if interfaceHeight < 5 {
		interfaceHeight = 5
		applicationHeight = max(5, remaining-interfaceHeight)
	}

	applications := renderPagePanelAtLeast(
		width,
		max(3, applicationHeight-panelOverhead),
		m.networkApplicationsPanelSpec(width, max(1, applicationHeight-panelOverhead-1)),
	)
	interfaces := renderPagePanelAtLeast(
		width,
		max(3, interfaceHeight-panelOverhead),
		m.networkInterfacesPanelSpec(width, max(1, interfaceHeight-panelOverhead-1)),
	)
	return strings.Join([]string{summary, applications, interfaces}, "\n"), nil
}

func (m *monitorModel) networkTrafficSummaryPanelSpec(width int) pagePanelSpec {
	contentWidth := max(24, width-4)
	totalTraffic := m.snapshot.NetworkRX + m.snapshot.NetworkTX
	rxShare, txShare := 0.0, 0.0
	if totalTraffic > 0 {
		rxShare = float64(m.snapshot.NetworkRX) * 100 / float64(totalTraffic)
		txShare = 100 - rxShare
	}
	errorsAndDrops := uint64(0)
	for _, networkInterface := range m.snapshot.NetworkInterfaces {
		errorsAndDrops += networkInterface.RXErrors + networkInterface.TXErrors +
			networkInterface.RXDrops + networkInterface.TXDrops
	}
	processState := fmt.Sprintf("%d ATTRIBUTED", len(m.snapshot.NetworkProcesses))
	if len(m.snapshot.NetworkProcesses) == 0 && m.snapshot.NetworkProcessError != "" {
		processState = truncate(m.snapshot.NetworkProcessError, max(12, contentWidth/2))
	}
	lines := []string{
		alignedPageLine(
			networkRXStyle.Render("RX  ↓ "+bytes(m.snapshot.NetworkRX)+"/s"),
			networkTXStyle.Render("TX  ↑ "+bytes(m.snapshot.NetworkTX)+"/s"),
			contentWidth,
		),
		alignedPageLine(
			dimStyle.Render("RECEIVED  ")+valueStyle.Render(bytes(m.snapshot.NetworkRXTotal)),
			dimStyle.Render("SENT  ")+valueStyle.Render(bytes(m.snapshot.NetworkTXTotal)),
			contentWidth,
		),
		pageRule(contentWidth),
		networkHistoryLine("RX", m.networkRXHistory, contentWidth, networkRXStyle),
		networkHistoryLine("TX", m.networkTXHistory, contentWidth, networkTXStyle),
		pageRule(contentWidth),
		alignedPageLine(
			dimStyle.Render("PRIMARY  ")+valueStyle.Render(m.primaryNetworkSource()),
			dimStyle.Render(fmt.Sprintf("%d INTERFACES", len(m.snapshot.NetworkInterfaces))),
			contentWidth,
		),
		alignedPageLine(
			dimStyle.Render("TRAFFIC MIX  ")+networkRXStyle.Render(fmt.Sprintf("%.0f%% RX", rxShare))+
				dimStyle.Render(" / ")+networkTXStyle.Render(fmt.Sprintf("%.0f%% TX", txShare)),
			dimStyle.Render(processState+" · ERR/DROP ")+valueStyle.Render(fmt.Sprint(errorsAndDrops)),
			contentWidth,
		),
	}
	return pagePanelSpec{
		title: "NETWORK ACTIVITY", meta: "LIVE · 60 SECOND WINDOW",
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func (m *monitorModel) networkApplicationsPanelSpec(width, limit int) pagePanelSpec {
	contentWidth := max(24, width-4)
	lines := []string{networkProcessHeader(contentWidth)}
	limit = min(max(1, limit), len(m.snapshot.NetworkProcesses))
	for _, process := range m.snapshot.NetworkProcesses[:limit] {
		lines = append(lines, renderNetworkProcessRow(process, contentWidth))
	}
	if len(m.snapshot.NetworkProcesses) == 0 {
		message := "Collecting per-process traffic attribution…"
		if m.snapshot.NetworkProcessError != "" {
			message = m.snapshot.NetworkProcessError
		}
		lines = append(lines, dimStyle.Render(truncate(message, contentWidth)))
	}
	return pagePanelSpec{
		title: "TOP APPLICATIONS", meta: fmt.Sprintf("%d ATTRIBUTED", len(m.snapshot.NetworkProcesses)),
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func (m *monitorModel) networkInterfacesPanelSpec(width, limit int) pagePanelSpec {
	contentWidth := max(24, width-4)
	lines := []string{networkInterfaceDetailedHeader(contentWidth)}
	limit = min(max(1, limit), len(m.snapshot.NetworkInterfaces))
	for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
		lines = append(lines, renderNetworkInterfaceDetailedRow(networkInterface, contentWidth))
	}
	if len(m.snapshot.NetworkInterfaces) == 0 {
		lines = append(lines, dimStyle.Render("No network interface counters are available."))
	}
	return pagePanelSpec{
		title: "INTERFACE COUNTERS", meta: fmt.Sprintf("%d DETECTED", len(m.snapshot.NetworkInterfaces)),
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func networkInterfaceDetailedHeader(width int) string {
	if width < 88 {
		return networkInterfaceHeader(width)
	}
	nameWidth := max(8, width-66)
	header := fmt.Sprintf("%-*s %11s %11s %12s %12s %14s",
		nameWidth, "INTERFACE", "DOWN NOW", "UP NOW", "RX TOTAL", "TX TOTAL", "ERRORS/DROPS")
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderNetworkInterfaceDetailedRow(networkInterface networkInterfaceInfo, width int) string {
	if width < 88 {
		return renderNetworkInterfaceRow(networkInterface, width)
	}
	nameWidth := max(8, width-66)
	errorsAndDrops := networkInterface.RXErrors + networkInterface.TXErrors +
		networkInterface.RXDrops + networkInterface.TXDrops
	return strings.Join([]string{
		valueStyle.Render(fixedCell(networkInterface.Name, nameWidth, false)),
		networkRXStyle.Render(fixedCell(bytes(networkInterface.RX)+"/s", 11, true)),
		networkTXStyle.Render(fixedCell(bytes(networkInterface.TX)+"/s", 11, true)),
		dimStyle.Render(fixedCell(bytes(networkInterface.RXTotal), 12, true)),
		dimStyle.Render(fixedCell(bytes(networkInterface.TXTotal), 12, true)),
		dimStyle.Render(fixedCell(fmt.Sprint(errorsAndDrops), 14, true)),
	}, " ")
}

func (m *monitorModel) availableMonitorPages() []monitorPage {
	pages := []monitorPage{monitorPageOverview, monitorPageCompute, monitorPageNetwork}
	if m.storage != nil {
		pages = append(pages, monitorPageStorage)
	}
	return pages
}

func (m *monitorModel) switchMonitorPage(page monitorPage) tea.Cmd {
	available := false
	for _, candidate := range m.availableMonitorPages() {
		if candidate == page {
			available = true
			break
		}
	}
	if !available {
		return nil
	}
	if m.monitorPage == monitorPageStorage && page != monitorPageStorage &&
		m.storage != nil && m.storage.Scanning {
		m.storage.cacheCurrentResult()
		m.cancelStorageScan()
		m.storage.Generation++
		m.storage.Scanning = false
		m.storage.Err = nil
	}
	m.monitorPage = page
	m.dashboardScroll = 0
	m.monitorFocus = monitorFocusProcesses
	m.status = page.label() + " page."
	m.filtering = false
	if page == monitorPageStorage && m.storage != nil &&
		!m.storage.Scanning && m.storage.Result.FinishedAt.IsZero() {
		return m.beginStorageScan(m.storage.Path)
	}
	return nil
}

func (m *monitorModel) cycleMonitorPage(delta int) tea.Cmd {
	pages := m.availableMonitorPages()
	index := 0
	for candidate := range pages {
		if pages[candidate] == m.monitorPage {
			index = candidate
			break
		}
	}
	index = (index + delta + len(pages)) % len(pages)
	return m.switchMonitorPage(pages[index])
}
