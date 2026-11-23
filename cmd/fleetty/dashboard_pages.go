package main

import (
	"fmt"
	"image/color"
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
		GPU:     len(m.snapshot.GPUs) > 0 || m.snapshot.GPUError != "",
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
		body := strings.Join([]string{
			header,
			panelStyle(width).Render("Collecting system metrics…"),
		}, "\n")
		return terminalFrame(body, footer, width, m.height)
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
	return terminalFrame(strings.Join(sections, "\n"), footer, width, m.height)
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
		batteryText := ""
		if battery.PowerSource != "" {
			batteryText += " " + compactPowerSource(battery.PowerSource)
		}
		if battery.TimeRemaining != "" {
			batteryText += " " + battery.TimeRemaining
		}
		percentStyle := valueStyle
		if level := batteryLevel(battery.Percent); level != stateOK {
			percentStyle = stateStyle(level)
		}
		capabilityLabels = append(capabilityLabels, dimStyle.Render("BAT ")+
			percentStyle.Render(fmt.Sprintf("%.0f%%", battery.Percent))+
			dimStyle.Render(batteryText))
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
			rendered = keycapStyle.Render("[" + label + "]")
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
	if m.useCPUChart() && m.cpuCoreFocused {
		return ansi.Truncate(strings.Join([]string{
			keyHint("←→↑↓", "core"), keyHint("home/end", "first/last"),
			keyHint("esc/f", "processes"), keyHint("v", "CPU detail"),
			keyHint("tab", "next page"), keyHint("q", "quit"),
		}, "  "), width, "")
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
		if m.storage != nil && m.storage.DuplicateMode {
			hints = append(hints,
				keyHint("↑↓", "groups"),
				keyHint("c", "choose keeper"),
				keyHint("g", "script"),
				keyHint("G", "script all"),
				keyHint("u/esc", "storage map"),
			)
		} else {
			hints = append(hints,
				keyHint("↑↓", "select"),
				keyHint("enter/click", "open"),
				keyHint("pg↑↓", "pages"),
				keyHint("u", "duplicates"),
				keyHint("d", "delete"),
				keyHint("z", "7z + delete"),
				keyHint("←/⌫", "up"),
				keyHint("home", "root"),
			)
		}
	} else if m.monitorPage == monitorPageOverview ||
		m.monitorPage == monitorPageCompute {
		if m.monitorPage == monitorPageCompute {
			hints = append(hints, keyHint("pg↑↓", "scroll"))
		}
		hints = append(hints,
			keyHint("↑↓", "select"),
			keyHint("o/O", "sort/reverse"),
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
		joined = strings.Join(hints, " ")
	}
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

const overviewHistoryPlotHeight = 2

func overviewSplitWidths(width int) (int, int) {
	available := max(2, width-1)
	base := available / 2
	extra := available % 2
	return base + extra, base
}

func overviewQueueRowBudget(targetLines, panelWidth int, queue *nodeSlurmQueue) int {
	overhead := 2 // summary + header
	if max(20, panelWidth-4) < slurmCompactWidth && queue != nil && len(queue.Jobs) > 0 {
		overhead++ // QOS legend
	}
	if queue != nil && queue.Warning != "" {
		overhead++
	}
	return max(1, targetLines-overhead)
}

func (m *monitorModel) renderOverviewPage(width int) (string, []widgetPlacement) {
	const (
		headerHeight    = 2
		footerHeight    = 1
		processOverhead = 4 // Borders, header and the state-marker legend.
	)
	capabilities := m.capabilities()
	columns := width >= 96
	rich := columns && (m.height == 0 || m.height >= 28)
	var sections []string
	y := 0
	appendSection := func(section string) {
		if section == "" {
			return
		}
		sections = append(sections, section)
		y += lipgloss.Height(section)
	}

	if columns {
		leftWidth, rightWidth := overviewSplitWidths(width)
		if capabilities.Slurm {
			host := m.hostHealthPanelSpec(leftWidth, rich, true)
			queueRows := overviewQueueRowBudget(pagePanelLineHeight(host), rightWidth, m.slurmQueue)
			queue := m.nodeQueuePanelSpec(queueRows, rightWidth)
			appendSection(renderPagePanelRow(width, host, queue))
		} else {
			host := m.hostHealthPanelSpec(leftWidth, rich, false)
			netLimit := max(1, min(5, pagePanelLineHeight(host)-3))
			network := m.networkActivityPanelSpec(rightWidth, rich, netLimit)
			appendSection(renderPagePanelRow(width, host, network))
		}
	} else {
		appendSection(renderPagePanel(width, m.hostHealthPanelSpec(width, false, capabilities.Slurm)))
	}

	if capabilities.GPU {
		gpuLimit := 4
		if m.height > 0 && len(m.snapshot.GPUs) > 1 {
			viewportHeight := max(1, m.height-headerHeight-footerHeight)
			maxGPUHeight := viewportHeight - y - (processOverhead + 3)
			gpuLimit = min(4, max(2, maxGPUHeight-5))
		}
		appendSection(renderPagePanel(width, m.computeActivityPanelSpec(width, gpuLimit)))
	}

	if !columns {
		if capabilities.Slurm {
			appendSection(renderPagePanel(width, m.nodeQueuePanelSpec(3, width)))
		} else {
			appendSection(renderPagePanel(width, m.networkActivityPanelSpec(width, false, 3)))
		}
	}

	availableRows := m.height - headerHeight - footerHeight - y - processOverhead
	// The overview stays bounded; extra terminal height belongs to processes.
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

func (m *monitorModel) hostNetworkSummaryLine(width int) string {
	iface := sanitizeTerminalText(m.primaryNetworkSource())
	if iface == "" || iface == "aggregate" {
		iface = ""
	}
	rx := bytes(m.snapshot.NetworkRX) + "/s"
	tx := bytes(m.snapshot.NetworkTX) + "/s"
	rxTotal := bytes(m.snapshot.NetworkRXTotal)
	txTotal := bytes(m.snapshot.NetworkTXTotal)
	compactRX := compactByteCount(m.snapshot.NetworkRX) + "/s"
	compactTX := compactByteCount(m.snapshot.NetworkTX) + "/s"
	compactRXTotal := compactByteCount(m.snapshot.NetworkRXTotal)
	compactTXTotal := compactByteCount(m.snapshot.NetworkTXTotal)

	prefix := networkTitleStyle.Render("NET") + " "
	if iface != "" {
		prefix += dimStyle.Render(iface + " ")
	}
	fullLive := prefix + networkRXStyle.Render("↓ "+rx) + "  " + networkTXStyle.Render("↑ "+tx)
	fullTotals := dimStyle.Render("TOTAL ↓ " + rxTotal + "  ↑ " + txTotal)
	if lipgloss.Width(fullLive)+2+lipgloss.Width(fullTotals) <= width {
		return alignedPageLine(fullLive, fullTotals, width)
	}
	shortTotals := dimStyle.Render("TOTAL ↓ " + compactRXTotal + " ↑ " + compactTXTotal)
	if lipgloss.Width(fullLive)+2+lipgloss.Width(shortTotals) <= width {
		return alignedPageLine(fullLive, shortTotals, width)
	}
	compactLive := prefix + networkRXStyle.Render("↓"+compactRX) + " " + networkTXStyle.Render("↑"+compactTX)
	if lipgloss.Width(compactLive)+2+lipgloss.Width(shortTotals) <= width {
		return alignedPageLine(compactLive, shortTotals, width)
	}
	if lipgloss.Width(fullLive) <= width {
		return fullLive
	}
	return ansi.Truncate(compactLive, width, "…")
}

func (m *monitorModel) hostHealthPanelSpec(width int, rich, includeNet bool) pagePanelSpec {
	contentWidth := max(24, width-4)
	var lines []string
	if rich {
		lines = append(m.overviewCPUHeader(contentWidth), pageRule(contentWidth))
		if contentWidth >= 54 {
			leftWidth := (contentWidth - 3) / 2
			rightWidth := contentWidth - leftWidth - 3
			memory := overviewCapacityLines("MEM", m.snapshot.MemoryUsed, m.snapshot.MemoryTotal, leftWidth, memoryTitleStyle)
			disk := overviewCapacityLines("DISK /", m.snapshot.DiskUsed, m.snapshot.DiskTotal, rightWidth, diskTitleStyle)
			lines = append(lines, strings.Split(renderWidgetColumns(memory, disk, leftWidth, rightWidth), "\n")...)
		} else {
			lines = append(lines, overviewCapacityLines("MEM", m.snapshot.MemoryUsed, m.snapshot.MemoryTotal, contentWidth, memoryTitleStyle)...)
			lines = append(lines, overviewCapacityLines("DISK /", m.snapshot.DiskUsed, m.snapshot.DiskTotal, contentWidth, diskTitleStyle)...)
		}
	} else {
		memoryUsage := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
		diskUsage := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
		memoryAvailable := counterDelta(m.snapshot.MemoryTotal, m.snapshot.MemoryUsed)
		diskFree := counterDelta(m.snapshot.DiskTotal, m.snapshot.DiskUsed)
		memoryState, memoryStateStyle := healthStatus(memoryUsage)
		diskState, diskStateStyle := capacityStatus(diskUsage)
		lines = []string{
			fmt.Sprintf("%s  %s  %s",
				cpuTitleStyle.Render("CPU"),
				usageReading(m.snapshot.CPUPercent, fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent)),
				valueStyle.Render(cpuClockSummary(m.snapshot.CPUFrequency))),
			dimStyle.Render(truncate(m.snapshot.LoadAverage, contentWidth)),
		}
		if m.height == 0 || m.height >= 28 {
			lines = append(lines, renderHistoryChart(m.cpuHistory, contentWidth, overviewHistoryPlotHeight, "CPU %", 100, utilizationStyle(m.snapshot.CPUPercent))...)
		}
		if contentWidth >= 54 && m.height > 0 && m.height < 28 {
			leftWidth := (contentWidth - 3) / 2
			rightWidth := contentWidth - leftWidth - 3
			memory := overviewCapacityLines("MEM", m.snapshot.MemoryUsed, m.snapshot.MemoryTotal, leftWidth, memoryTitleStyle)
			disk := overviewCapacityLines("DISK /", m.snapshot.DiskUsed, m.snapshot.DiskTotal, rightWidth, diskTitleStyle)
			lines = append(lines, strings.Split(renderWidgetColumns(memory, disk, leftWidth, rightWidth), "\n")...)
		} else {
			barWidth := min(28, contentWidth)
			lines = append(lines,
				ansi.Truncate(fmt.Sprintf("%s  %s  %s  %s",
					memoryTitleStyle.Render("MEMORY"),
					valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal))),
					memoryStateStyle.Render(memoryState),
					dimStyle.Render("AVAILABLE "+bytes(memoryAvailable))), contentWidth, "…"),
				bar(memoryUsage, barWidth),
				ansi.Truncate(fmt.Sprintf("%s  %s  %s  %s",
					diskTitleStyle.Render("DISK /"),
					valueStyle.Render(fmt.Sprintf("%s / %s", bytes(m.snapshot.DiskUsed), bytes(m.snapshot.DiskTotal))),
					diskStateStyle.Render(diskState),
					dimStyle.Render("FREE "+bytes(diskFree))), contentWidth, "…"),
				bar(diskUsage, barWidth),
			)
		}
	}
	if includeNet {
		lines = append(lines, m.hostNetworkSummaryLine(contentWidth))
	}
	return pagePanelSpec{
		title: "HOST HEALTH", meta: "CURRENT",
		lines: strings.Split(strings.Join(lines, "\n"), "\n"), titleStyle: sectionStyle, borderColor: colorBorder,
	}
}

func overviewCapacityLines(label string, used, total uint64, width int, style lipgloss.Style) []string {
	usage := percent(used, total)
	reading := style.Render(label+" ") + usageReading(usage, fmt.Sprintf("%.1f%%", usage)) +
		dimStyle.Render("  "+compactByteCount(used)+" / "+compactByteCount(total))
	return []string{ansi.Truncate(reading, width, "…"), bar(usage, width)}
}

// Keep hardware, live readings and trends on separate rows. Secondary details
// disappear as the panel narrows, without truncating the primary readings.
func (m *monitorModel) overviewCPUHeader(width int) []string {
	historyWidth := 0
	if width >= 64 {
		historyWidth = min(24, width/3)
		width -= historyWidth + 3
	}
	model := m.hostCPUModelLabel()
	if model == "" {
		model = "model unavailable"
	}
	count := ""
	if m.snapshot.CPUCores > 0 {
		count = fmt.Sprintf("%d logical CPUs", m.snapshot.CPUCores)
		if lipgloss.Width(count) > width/2 {
			count = fmt.Sprintf("%d logical", m.snapshot.CPUCores)
		}
	}
	hardware := alignedPageLine(cpuTitleStyle.Render("CPU")+"  "+dimStyle.Render(model),
		dimStyle.Render(count), width)
	readings := dimStyle.Render("USAGE ") + usageReading(m.snapshot.CPUPercent, fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent)) +
		"  " + valueStyle.Render(cpuClockSummary(m.snapshot.CPUFrequency))
	if frequency := m.snapshot.CPUFrequency; frequency != nil && validCPUFrequency(frequency.AverageMHz) {
		clockRange := dimStyle.Render(fmt.Sprintf("  RANGE %.2f–%.2fGHz", frequency.MinMHz/1000, frequency.MaxMHz/1000))
		if lipgloss.Width(readings+clockRange) <= width {
			readings += clockRange
		}
	}
	load := strings.TrimSpace(strings.TrimPrefix(m.snapshot.LoadAverage, "load"))
	if load == "" {
		load = "--"
	}
	trend := dimStyle.Render("LOAD ") + valueStyle.Render(load)
	for _, legend := range []string{"  (1m 5m 15m)", " (1/5/15m)"} {
		if lipgloss.Width(trend+legend) <= width {
			trend += dimStyle.Render(legend)
			break
		}
	}
	lines := []string{hardware, ansi.Truncate(readings, width, "…"), ansi.Truncate(trend, width, "…")}
	if historyWidth > 0 {
		history := renderHistoryChart(m.cpuHistory, historyWidth, overviewHistoryPlotHeight, "CPU %", 100, utilizationStyle(m.snapshot.CPUPercent))
		return historyColumns(lines, history, width)
	}
	return lines
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
	return capacityStatus(usage)
}

func (m *monitorModel) computeActivityPanelSpec(width, deviceLimit int) pagePanelSpec {
	return newGPUCardComponent(m.snapshot).panelSpec(gpuCardOverview, width, deviceLimit)
}

func buildGPUOverviewPanelSpec(gpus []gpuInfo, gpuError string, width, deviceLimit int) pagePanelSpec {
	spec := pagePanelSpec{title: "GPU", titleStyle: gpuTitleStyle, borderColor: colorGPUBorder}
	switch {
	case len(gpus) > 1:
		spec.meta = gpuDeviceCountMeta(len(gpus))
		spec.lines = multiGPUOverviewLines(gpus, max(24, width-4), deviceLimit)
	case len(gpus) == 1:
		spec.lines = singleGPUOverviewLines(gpus[0], max(4, width-4))
	case gpuError != "":
		spec.lines = []string{warningStyle.Render(truncate(gpuError, max(4, width-4)))}
	default:
		spec.lines = []string{dimStyle.Render("Unavailable")}
	}
	return spec
}

func multiGPUOverviewLines(gpus []gpuInfo, width, deviceLimit int) []string {
	width = max(24, width)
	limit := 4
	if deviceLimit > 0 {
		limit = min(limit, deviceLimit)
	}
	limit = min(limit, len(gpus))
	lines := []string{multiGPUOverviewHeader(width)}
	var (
		active       int
		workloads    int
		totalMemory  uint64
		usedMemory   uint64
		totalPower   float64
		totalLimit   float64
		totalUtil    float64
		busiestIndex int
		busiestUtil  float64
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
		if index < limit {
			lines = append(lines, multiGPUOverviewRow(gpu, width))
		}
	}

	lines = append(lines, pageRule(width))
	average := totalUtil / float64(max(1, len(gpus)))
	leftSummary := fmt.Sprintf("%s  %s  %s",
		gpuTitleStyle.Render(fmt.Sprintf("ACTIVE %d/%d", active, len(gpus))),
		dimStyle.Render(fmt.Sprintf("AVG %.0f%%", average)),
		gpuLoadStyle(busiestUtil).Render(fmt.Sprintf("PEAK #%d %.0f%%", busiestIndex, busiestUtil)))
	power := fmt.Sprintf("%.0fW", totalPower)
	if totalLimit > 0 {
		power = fmt.Sprintf("%.0f/%.0fW", totalPower, totalLimit)
	}
	rightSummary := fmt.Sprintf("%s  %s  %s",
		memoryTitleStyle.Render("VRAM "+compactByteCount(usedMemory)+"/"+compactByteCount(totalMemory)),
		dimStyle.Render("PWR "+power),
		dimStyle.Render(fmt.Sprintf("JOBS %d", workloads)))
	joinPair := func(left, right string) string {
		gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
		return left + strings.Repeat(" ", gap) + right
	}
	if len(gpus) > limit {
		overflow := dimStyle.Render(fmt.Sprintf("+%d · [2 COMPUTE]", len(gpus)-limit))
		rightWithOverflow := rightSummary + dimStyle.Render("  ·  ") + overflow
		switch {
		case lipgloss.Width(leftSummary)+2+lipgloss.Width(rightWithOverflow) <= width:
			lines = append(lines, joinPair(leftSummary, rightWithOverflow))
		case lipgloss.Width(leftSummary)+2+lipgloss.Width(rightSummary) <= width:
			lines = append(lines, joinPair(leftSummary, rightSummary), ansi.Truncate(overflow, width, "…"))
		default:
			lines = append(lines,
				ansi.Truncate(leftSummary, width, "…"),
				ansi.Truncate(rightSummary, width, "…"),
				ansi.Truncate(overflow, width, "…"),
			)
		}
	} else if lipgloss.Width(leftSummary)+2+lipgloss.Width(rightSummary) <= width {
		lines = append(lines, joinPair(leftSummary, rightSummary))
	} else {
		lines = append(lines,
			ansi.Truncate(leftSummary, width, "…"),
			ansi.Truncate(rightSummary, width, "…"),
		)
	}
	return lines
}

const (
	multiGPUOverviewClockMinWidth = 80
	multiGPUOverviewClockWidth    = 8
)

func multiGPUOverviewHeader(width int) string {
	var header string
	switch {
	case width >= 68:
		header = fixedCell("GPU", 3, false) + " " +
			fixedCell("MODEL", 14, false) + " " +
			fixedCell("LOAD", 9, false) + " " +
			fixedCell("VRAM", 16, false) + " " +
			fixedCell("PWR", 9, true)
		if width >= multiGPUOverviewClockMinWidth {
			header += " " + fixedCell("CLK", multiGPUOverviewClockWidth, true)
		}
		header += " " + fixedCell("TEMP", 5, true) + " WORKLOAD"
	case width >= 50:
		header = fixedCell("GPU", 3, false) + " " +
			fixedCell("MODEL", 14, false) + " " +
			fixedCell("LOAD", 9, false) + " " +
			fixedCell("VRAM", 13, false) + " WORKLOAD"
	default:
		header = fixedCell("GPU / MODEL", max(10, width-17), false) + " LOAD / VRAM"
	}
	return dimStyle.Copy().Bold(true).Render(ansi.Truncate(header, width, ""))
}

func multiGPUOverviewRow(gpu gpuInfo, width int) string {
	statusStyle := gpuLoadStyle(gpu.Utilization)
	load := loadBar(gpu.Utilization, 5, statusStyle) +
		statusStyle.Render(fixedCell(fmt.Sprintf("%.0f%%", gpu.Utilization), 4, true))
	memoryUsage := percent(gpu.MemoryUsed, gpu.MemoryTotal)
	memoryStyle := gpuLoadStyle(memoryUsage)
	memory := compactByteCount(gpu.MemoryUsed) + "/" + compactByteCount(gpu.MemoryTotal)
	vramCell := loadBar(memoryUsage, 5, memoryStyle) + " " +
		memoryStyle.Render(fixedCell(memory, 10, false))
	powerText := fmt.Sprintf("%.0fW", gpu.Power)
	if gpu.PowerLimit > 0 {
		powerText = fmt.Sprintf("%.0f/%.0fW", gpu.Power, gpu.PowerLimit)
	}
	job := gpuOverviewWorkload(gpu)
	id := valueStyle.Render(fmt.Sprintf("#%d", gpu.Index))
	model := shortGPUName(gpu.Name)
	var row string
	switch {
	case width >= 68:
		row = fixedCell(id, 3, false) + " " +
			fixedCell(model, 14, false) + " " +
			load + " " +
			vramCell + " " +
			valueStyle.Render(fixedCell(powerText, 9, true))
		if width >= multiGPUOverviewClockMinWidth {
			clock, clockStyle := "--", dimStyle
			if gpu.ClockMHz > 0 {
				clock, clockStyle = fmt.Sprintf("%dMHz", gpu.ClockMHz), gpuClockStyle(gpu.ClockMHz)
			}
			row += " " + clockStyle.Render(fixedCell(clock, multiGPUOverviewClockWidth, true))
		}
		jobText := dimStyle.Render("—")
		if job != "—" {
			jobText = gpuTitleStyle.Render("● ") + valueStyle.Render(job)
		}
		row += " " + gpuTemperatureStyle(gpu.Temperature).Render(fixedCell(fmt.Sprintf("%d°C", gpu.Temperature), 5, true)) + " " +
			jobText
	case width >= 50:
		row = fixedCell(id, 3, false) + " " +
			fixedCell(model, 14, false) + " " +
			load + " " +
			memoryStyle.Render(fixedCell(memory, 13, false)) + " " +
			valueStyle.Render(job)
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

func (m *monitorModel) networkActivityPanelSpec(width int, rich bool, detailLimit int) pagePanelSpec {
	contentWidth := max(24, width-4)
	detailLimit = max(1, min(5, detailLimit))
	if !rich || width < 70 {
		detailLimit = min(detailLimit, 3)
	}
	live := dimStyle.Render("LIVE ") +
		networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
		networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s")
	totals := dimStyle.Render(fmt.Sprintf("TOTAL ↓ %s  ↑ %s",
		bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal)))
	windowLines := networkOverviewWindow(m.networkRXHistory, m.networkTXHistory, contentWidth)

	lines := append([]string{}, windowLines...)
	detailTitle, details := m.networkOverviewDetails(contentWidth, detailLimit)
	if len(details) > 0 {
		if detailTitle != "TRAFFIC SOURCES" {
			lines = append(lines, m.networkOverviewHeader(contentWidth))
		}
		lines = append(lines, details...)
	}
	if summary := live + "    " + totals; lipgloss.Width(summary) <= contentWidth {
		lines = append(lines, alignedPageLine(live, totals, contentWidth))
	} else {
		lines = append(lines, ansi.Truncate(live, contentWidth, "…"), ansi.Truncate(totals, contentWidth, "…"))
	}
	return pagePanelSpec{
		title: "NETWORK ACTIVITY", meta: m.primaryNetworkSource(),
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func networkOverviewWindow(rx, tx []float64, width int) []string {
	rxAvg, rxPeak, rxOK := networkWindowRates(rx)
	txAvg, txPeak, txOK := networkWindowRates(tx)
	pair := func(down, up uint64, compact bool) string {
		format := bytes
		gap := " "
		if compact {
			format, gap = compactByteCount, ""
		}
		reading := func(value uint64, ok bool) string {
			if !ok {
				return "--"
			}
			return format(value) + "/s"
		}
		return networkRXStyle.Render("↓"+gap+reading(down, rxOK)) + "  " +
			networkTXStyle.Render("↑"+gap+reading(up, txOK))
	}
	for _, compact := range []bool{false, true} {
		avg := dimStyle.Render("1m AVG ") + pair(rxAvg, txAvg, compact)
		peak := dimStyle.Render("PEAK ") + pair(rxPeak, txPeak, compact)
		if line := avg + dimStyle.Render("  ·  ") + peak; lipgloss.Width(line) <= width {
			return []string{line}
		}
	}
	return []string{
		ansi.Truncate(dimStyle.Render("1m AVG ")+pair(rxAvg, txAvg, true), width, "…"),
		ansi.Truncate(dimStyle.Render("1m PEAK ")+pair(rxPeak, txPeak, true), width, "…"),
	}
}

func (m *monitorModel) networkOverviewDetails(width, limit int) (string, []string) {
	var lines []string
	switch {
	case m.snapshot.NetworkConnectionMode:
		if width < 48 {
			for _, connection := range m.snapshot.NetworkConnections[:min(limit, len(m.snapshot.NetworkConnections))] {
				lines = append(lines, compactNetworkOverviewRow(connection.Name, connection.RX, connection.TX, connection.RateAvailable, width))
			}
			return "PROCESS CONNECTIONS", lines
		}
		return "PROCESS CONNECTIONS", m.networkApplicationRows(width, limit)
	case len(m.snapshot.NetworkProcesses) > 0:
		limit = min(limit, len(m.snapshot.NetworkProcesses))
		for _, process := range m.snapshot.NetworkProcesses[:limit] {
			if width < 48 {
				lines = append(lines, compactNetworkOverviewRow(process.Name, process.RX, process.TX, true, width))
			} else {
				lines = append(lines, renderNetworkProcessRow(process, width))
			}
		}
		return "TOP APPLICATIONS / PROCESSES", lines
	case len(m.snapshot.NetworkInterfaces) > 0:
		limit = min(limit, len(m.snapshot.NetworkInterfaces))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
			if width < 48 {
				lines = append(lines, compactNetworkOverviewRow(networkInterface.Name, networkInterface.RX, networkInterface.TX, true, width))
			} else {
				lines = append(lines, renderNetworkInterfaceRow(networkInterface, width))
			}
		}
		return "NETWORK INTERFACES", lines
	default:
		if m.snapshot.NetworkProcessError != "" {
			return "TRAFFIC SOURCES", []string{dimStyle.Render(truncate(m.snapshot.NetworkProcessError, width))}
		}
		return "TRAFFIC SOURCES", nil
	}
}

func (m *monitorModel) networkOverviewHeader(width int) string {
	if width < 48 {
		return dimStyle.Render(fixedCell("NAME", max(1, width-22), false) + " " + fixedCell("DOWN", 10, true) + " " + fixedCell("UP", 10, true))
	}
	if m.snapshot.NetworkConnectionMode || len(m.snapshot.NetworkProcesses) > 0 {
		return m.networkApplicationHeader(width)
	}
	return networkInterfaceHeader(width)
}

func compactNetworkOverviewRow(name string, rx, tx uint64, available bool, width int) string {
	down, up := "--", "--"
	if available {
		down, up = bytes(rx)+"/s", bytes(tx)+"/s"
	}
	return valueStyle.Render(fixedCell(sanitizeTerminalText(name), max(1, width-22), false)) + " " +
		networkRateStyle(rx, available, networkRXStyle).Render(fixedCell(down, 10, true)) + " " +
		networkRateStyle(tx, available, networkTXStyle).Render(fixedCell(up, 10, true))
}

func (m *monitorModel) primaryNetworkSource() string {
	if len(m.snapshot.NetworkInterfaces) > 0 {
		return m.snapshot.NetworkInterfaces[0].Name
	}
	return "aggregate"
}

func (m *monitorModel) nodeQueuePanelSpec(rowLimit, width int) pagePanelSpec {
	queue := m.slurmQueue
	if queue == nil {
		return pagePanelSpec{}
	}
	contentWidth := max(20, width-4)
	limit := min(max(0, rowLimit), len(queue.Jobs))
	jobMeta := fmt.Sprintf("%d JOBS", len(queue.Jobs))
	if len(queue.Jobs) > limit && limit > 0 {
		jobMeta = fmt.Sprintf("1–%d/%d JOBS", limit, len(queue.Jobs))
	}
	meta := fmt.Sprintf("%s · %s · %s", jobMeta, queue.Cluster, queue.Node)
	if !queue.CollectedAt.IsZero() {
		age := time.Since(queue.CollectedAt)
		if !m.snapshot.CollectedAt.IsZero() {
			age = m.snapshot.CollectedAt.Sub(queue.CollectedAt)
			if age < 0 {
				age = 0
			}
		}
		meta = fmt.Sprintf("%s · %s AGO · %s · %s", jobMeta, hubAge(age), queue.Cluster, queue.Node)
	}
	lines := []string{
		slurmNodeStateSummary(queue.Jobs, true),
		slurmNodeJobHeader(contentWidth),
	}
	if queue.Warning != "" {
		lines = append(lines, warningStyle.Render(truncate(queue.Warning, contentWidth)))
	}
	for _, job := range queue.Jobs[:limit] {
		lines = append(lines, renderSlurmNodeJobRow(job, contentWidth))
	}
	if len(queue.Jobs) == 0 {
		lines = append(lines, dimStyle.Render(truncate("No running or eligible queued jobs for this node.", contentWidth)))
	}
	return pagePanelSpec{
		title: "NODE QUEUE / SLURM", meta: meta,
		lines: appendSlurmQOSLegend(lines, queue.Jobs, contentWidth), titleStyle: processTitleStyle, borderColor: colorProcessBorder,
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
		maxLines = max(maxLines, lipgloss.Height(strings.Join(spec.lines, "\n")))
	}
	maxLines = max(maxLines, lineCount)
	parts := make([]string, 0, len(specs)*2)
	for index, spec := range specs {
		panelWidth := base
		if index < extra {
			panelWidth++
		}
		lines := strings.Split(strings.Join(spec.lines, "\n"), "\n")
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
		height = max(height, lipgloss.Height(strings.Join(spec.lines, "\n")))
	}
	return height
}

func padPagePanelSpec(spec pagePanelSpec, lineCount int) pagePanelSpec {
	spec.lines = strings.Split(strings.Join(spec.lines, "\n"), "\n")
	for len(spec.lines) < lineCount {
		spec.lines = append(spec.lines, "")
	}
	return spec
}

func (m *monitorModel) renderOverviewProcesses(width, panelRows, visibleRows int) string {
	return m.renderProcessPreviewRows(
		width, panelRows, visibleRows, "TOP PROCESSES",
		"FULL LIST [2 COMPUTE]",
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
	lines := []string{format.renderHeader(m.processSort)}
	end := min(len(processes), m.monitorOffset+visibleRows)
	for index := m.monitorOffset; index < end; index++ {
		row := format.renderRow(processes[index], index+1, index == m.monitorCursor,
			m.colorMode, m.snapshot.MemoryTotal)
		lines = append(lines, row)
	}
	if len(processes) == 0 {
		lines = append(lines, dimStyle.Render("No process data available."))
	}
	for len(lines) < panelRows+1 {
		lines = append(lines, "")
	}
	lines = append(lines, processLegend(width-4))
	counts := m.processTableMeta(m.monitorOffset, visibleRows, width, title)
	if meta != "" && lipgloss.Width(title)+lipgloss.Width(counts)+lipgloss.Width(meta)+10 < width {
		counts += " · " + meta
	}
	return btopPanel(width, title, counts, strings.Join(lines, "\n"),
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
	cpuCard, _ := m.metricWidgetCard(dashboardPanelCPU)
	cpuPanel := m.renderCPUWidget(cpuCard, width, widgetSizeMedium)
	placements := []widgetPlacement{{ID: dashboardPanelCPU, X: 0, Y: 0, Width: width, Height: lipgloss.Height(cpuPanel)}}
	appendSection(cpuPanel)
	if m.capabilities().GPU {
		layout := dashboardLayout{width: width, height: m.height, compactGPU: width < 112}
		appendSection(m.gpuPanel(layout))
	}
	appendSection(m.renderComputeMemoryAndStorage(width))
	if m.slurmQueue != nil {
		appendSection(renderPagePanel(width, m.nodeQueuePanelSpec(min(6, max(3, m.height/5)), width)))
	}
	const (
		headerHeight    = 2
		footerHeight    = 1
		processOverhead = 4
	)
	processRows := max(4, m.height-headerHeight-footerHeight-y-processOverhead)
	processY := y
	workloads := m.renderProcessPreview(
		width, processRows, "COMPUTE WORKLOADS",
		"CLICK HEADER TO SORT",
	)
	appendSection(workloads)
	return strings.Join(sections, "\n"), append(placements, widgetPlacement{
		ID: dashboardPanelProcesses, X: 0, Y: processY, Width: width,
		Height: lipgloss.Height(workloads), ProcessRows: processRows, ProcessRowStart: 2,
	})
}

func (m *monitorModel) renderComputeMemoryAndStorage(width int) string {
	memPct := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
	memAvail := uint64(0)
	if m.snapshot.MemoryTotal > m.snapshot.MemoryUsed {
		memAvail = m.snapshot.MemoryTotal - m.snapshot.MemoryUsed
	}
	diskPct := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
	diskFree := uint64(0)
	if m.snapshot.DiskTotal > m.snapshot.DiskUsed {
		diskFree = m.snapshot.DiskTotal - m.snapshot.DiskUsed
	}

	topRSS := ""
	rssCount := 0
	for _, proc := range m.snapshot.Processes {
		if proc.RSS > 0 && rssCount < 2 {
			if topRSS != "" {
				topRSS += " · "
			}
			topRSS += fmt.Sprintf("%s (%s)", proc.Command, compactByteCount(proc.RSS))
			rssCount++
		}
	}

	if width >= 88 {
		available := width - 1
		leftW := available/2 + available%2
		rightW := available / 2
		leftContentW := max(20, leftW-4)
		rightContentW := max(20, rightW-4)

		memLeft := memoryTitleStyle.Render("USED ") +
			usageReading(memPct, fmt.Sprintf("%.1f%%", memPct)) +
			dimStyle.Render("  "+bytes(m.snapshot.MemoryUsed)+" / "+bytes(m.snapshot.MemoryTotal))
		memRight := dimStyle.Render("AVAIL ") + valueStyle.Render(bytes(memAvail))
		if topRSS != "" {
			memRight = dimStyle.Render("TOP RSS ") + valueStyle.Render(topRSS)
		}
		memSpec := pagePanelSpec{
			title:       "MEMORY",
			meta:        fmt.Sprintf("%.1f%% USED · %s AVAIL", memPct, bytes(memAvail)),
			lines:       []string{alignedPageLine(memLeft, memRight, leftContentW), bar(memPct, leftContentW)},
			titleStyle:  memoryTitleStyle,
			borderColor: colorMemoryBorder,
		}

		diskLeft := diskTitleStyle.Render("DISK / ") +
			usageReading(diskPct, fmt.Sprintf("%.1f%%", diskPct)) +
			dimStyle.Render("  "+bytes(m.snapshot.DiskUsed)+" / "+bytes(m.snapshot.DiskTotal))
		diskRight := dimStyle.Render("IO ") +
			networkRXStyle.Render("↓ "+compactByteCount(m.snapshot.DiskRead)+"/s") + " " +
			networkTXStyle.Render("↑ "+compactByteCount(m.snapshot.DiskWrite)+"/s")
		diskSpec := pagePanelSpec{
			title:       "STORAGE & IO",
			meta:        fmt.Sprintf("%.1f%% USED · %s FREE", diskPct, bytes(diskFree)),
			lines:       []string{alignedPageLine(diskLeft, diskRight, rightContentW), bar(diskPct, rightContentW)},
			titleStyle:  diskTitleStyle,
			borderColor: colorDiskBorder,
		}
		return renderPagePanelRow(width, memSpec, diskSpec)
	}

	contentW := max(20, width-4)
	memLeft := memoryTitleStyle.Render("USED ") +
		usageReading(memPct, fmt.Sprintf("%.1f%%", memPct)) +
		dimStyle.Render("  "+bytes(m.snapshot.MemoryUsed)+" / "+bytes(m.snapshot.MemoryTotal))
	memRight := dimStyle.Render("AVAIL ") + valueStyle.Render(bytes(memAvail))
	return renderPagePanel(width, pagePanelSpec{
		title:       "MEMORY",
		meta:        fmt.Sprintf("%.1f%% USED", memPct),
		lines:       []string{alignedPageLine(memLeft, memRight, contentW), bar(memPct, contentW)},
		titleStyle:  memoryTitleStyle,
		borderColor: colorMemoryBorder,
	})
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
	extraRows := 0
	if m.snapshot.NetworkConnectionMode && m.networkApplicationCount() > 0 {
		extraRows++ // Rate color legend and sort key.
		if m.snapshot.NetworkProcessError != "" {
			extraRows++
		}
	}

	applicationHeight := min(
		m.networkApplicationCount()+1+extraRows+panelOverhead,
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
		m.networkApplicationsPanelSpec(width, max(1, applicationHeight-panelOverhead-1-extraRows)),
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
	processState := m.networkApplicationMeta()
	if m.networkApplicationCount() == 0 && m.snapshot.NetworkProcessError != "" {
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
		strings.Join(renderNetworkHistories(m.networkRXHistory, m.networkTXHistory, contentWidth), "\n"),
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
	lines := []string{m.networkApplicationHeader(contentWidth)}
	lines = append(lines, m.networkApplicationRows(contentWidth, max(1, limit))...)
	if m.networkApplicationCount() == 0 {
		message := "Collecting per-process traffic attribution…"
		if m.snapshot.NetworkConnectionMode {
			message = "No TCP/UDP connections visible in this network namespace."
		}
		if m.snapshot.NetworkProcessError != "" {
			message = m.snapshot.NetworkProcessError
		}
		lines = append(lines, dimStyle.Render(truncate(message, contentWidth)))
	}
	title := "TOP APPLICATIONS"
	if m.snapshot.NetworkConnectionMode {
		title = "PROCESS CONNECTIONS"
		if m.networkApplicationCount() > 0 {
			lines = append(lines, networkConnectionRateLegend(contentWidth))
		}
		if m.networkApplicationCount() > 0 && m.snapshot.NetworkProcessError != "" {
			lines = append(lines, warningStyle.Render(truncate(m.snapshot.NetworkProcessError, contentWidth)))
		}
	}
	return pagePanelSpec{
		title: title, meta: m.networkApplicationMeta(),
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
		networkRateStyle(networkInterface.RX, true, networkRXStyle).Render(fixedCell(bytes(networkInterface.RX)+"/s", 11, true)),
		networkRateStyle(networkInterface.TX, true, networkTXStyle).Render(fixedCell(bytes(networkInterface.TX)+"/s", 11, true)),
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
	if m.monitorPage == monitorPageStorage && page != monitorPageStorage &&
		m.storage != nil && m.storage.DuplicateMode {
		m.cancelStorageDuplicateScan()
		if m.storage.Duplicates != nil {
			m.storage.Duplicates.Generation++
			m.storage.Duplicates.Scanning = false
		}
		m.storage.DuplicateMode = false
	}
	m.monitorPage = page
	m.cpuCoreFocused = false
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
