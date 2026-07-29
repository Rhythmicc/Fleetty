package main

import (
	"fmt"
	"image/color"
	"strings"

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
	case monitorPageProcesses:
		return "PROCESSES"
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
		title += dimStyle.Render(" / ") + accentStyle.Render(truncate(m.nodeName, max(8, width/4)))
	}
	left := title + "  " + liveBadgeStyle.Render("● LIVE")

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
		label := fmt.Sprintf("%d GPU", len(m.snapshot.GPUs))
		if len(m.snapshot.GPUs) != 1 {
			label += "S"
		}
		capabilityLabels = append(capabilityLabels, gpuTitleStyle.Render(label))
	}
	if capabilities.Slurm {
		capabilityLabels = append(capabilityLabels, processRunningStyle.Render("SLURM"))
	}
	if capabilities.Services {
		capabilityLabels = append(capabilityLabels, networkTitleStyle.Render("SERVICES"))
	}
	capabilityText := strings.Join(capabilityLabels, dimStyle.Render(" · "))
	clock := "--:--:--"
	if !m.snapshot.CollectedAt.IsZero() {
		clock = m.snapshot.CollectedAt.Format("15:04:05")
	}
	right := dimStyle.Render("1s · "+strings.ToUpper(m.colorMode.String())) +
		"  " + clockStyle.Render(clock)
	if capabilityText != "" && width >= 88 {
		right = capabilityText + dimStyle.Render("  ·  ") + right
	}
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	firstLine := ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "")

	pages := []monitorPage{
		monitorPageOverview, monitorPageCompute, monitorPageNetwork, monitorPageProcesses,
	}
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
	hints := []string{
		keyHint("1–4", "pages"),
		keyHint("tab", "next"),
		keyHint("m", "management"),
	}
	if m.monitorPage == monitorPageOverview ||
		m.monitorPage == monitorPageCompute ||
		m.monitorPage == monitorPageProcesses {
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
	case monitorPageProcesses:
		return m.renderProcessesPage(width)
	default:
		return m.renderOverviewPage(width)
	}
}

func (m *monitorModel) renderOverviewPage(width int) (string, []widgetPlacement) {
	capabilities := m.capabilities()
	columns := width >= 96
	var sections []string
	y := 0
	appendSection := func(section string) {
		if section == "" {
			return
		}
		if len(sections) > 0 {
			y++
		}
		sections = append(sections, section)
		y += lipgloss.Height(section)
	}

	host := m.hostHealthPanelSpec()
	compute := m.computeActivityPanelSpec()
	detailWidth := width
	if columns && (capabilities.Slurm || !capabilities.GPU) {
		detailWidth = (width - 1) / 2
	}
	network := m.networkActivityPanelSpec(detailWidth)
	queue := m.nodeQueuePanelSpec(4, detailWidth)
	networkRendered := false

	switch {
	case columns && capabilities.GPU:
		appendSection(renderPagePanelRow(width, host, compute))
	case columns:
		appendSection(renderPagePanelRow(width, host, network))
		networkRendered = true
	default:
		appendSection(renderPagePanel(width, host))
		if capabilities.GPU {
			appendSection(renderPagePanel(width, compute))
		}
	}

	if !networkRendered {
		if columns && capabilities.Slurm {
			appendSection(renderPagePanelRow(width, network, queue))
		} else {
			appendSection(renderPagePanel(width, network))
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
	gapBeforeProcesses := 1
	availableRows := m.height - headerHeight - footerHeight - y - gapBeforeProcesses - processOverhead
	// The overview's summary panels stay information-dense while the process
	// preview absorbs any remaining terminal height. This keeps the page flush
	// with the footer without stretching charts or leaving an empty lower half.
	processRows := max(3, availableRows)
	processPanel := m.renderOverviewProcesses(width, processRows)
	processY := y
	if len(sections) > 0 {
		processY++
	}
	appendSection(processPanel)
	placement := widgetPlacement{
		ID: dashboardPanelProcesses, X: 0, Y: processY,
		Width: width, Height: lipgloss.Height(processPanel),
		ProcessRows: processRows, ProcessRowStart: 2,
	}
	return strings.Join(sections, "\n\n"), []widgetPlacement{placement}
}

func (m *monitorModel) hostHealthPanelSpec() pagePanelSpec {
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
	return pagePanelSpec{
		title: "HOST HEALTH", meta: "CURRENT",
		lines: lines, titleStyle: processRunningStyle, borderColor: colorPanelBorder,
	}
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

func (m *monitorModel) computeActivityPanelSpec() pagePanelSpec {
	var lines []string
	if m.snapshot.GPUError != "" && len(m.snapshot.GPUs) == 0 {
		lines = append(lines, warningStyle.Render("GPU metrics unavailable: "+m.snapshot.GPUError))
	}
	gpus := m.snapshot.GPUs
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
					gpuTitleStyle.Render(fmt.Sprintf("CORES %d", gpu.CoreCount))+
						dimStyle.Render("  ·  Unified memory architecture"),
				)
			} else {
				lines = append(lines,
					gpuMetricLine("CLOCK", "", fmt.Sprintf("%d MHz", gpu.ClockMHz)),
					gpuMetricLine("POWER", "", fmt.Sprintf("%.0f / %.0f W", gpu.Power, gpu.PowerLimit)),
					gpuMetricLine("TEMP", "", fmt.Sprintf("%d°C", gpu.Temperature)),
				)
			}
		}
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

func (m *monitorModel) networkActivityPanelSpec(width int) pagePanelSpec {
	lines := []string{
		networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
			networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s") + "    " +
			dimStyle.Render(fmt.Sprintf("TOTAL ↓ %s  ↑ %s",
				bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal))),
		renderMetricVisual(metricCard{
			visual:         metricVisualNetwork,
			primaryHistory: m.networkRXHistory, secondaryHistory: m.networkTXHistory,
		}, max(12, min(72, width-4))),
	}
	detailWidth := max(24, width-4)
	switch {
	case len(m.snapshot.NetworkProcesses) > 0:
		lines = append(lines, networkTitleStyle.Render("TOP APPLICATIONS"))
		lines = append(lines, networkProcessHeader(detailWidth))
		limit := min(3, len(m.snapshot.NetworkProcesses))
		for _, process := range m.snapshot.NetworkProcesses[:limit] {
			lines = append(lines, renderNetworkProcessRow(process, detailWidth))
		}
	case len(m.snapshot.NetworkInterfaces) > 0:
		lines = append(lines, networkTitleStyle.Render("INTERFACES"))
		lines = append(lines, networkInterfaceHeader(detailWidth))
		limit := min(3, len(m.snapshot.NetworkInterfaces))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
			lines = append(lines, renderNetworkInterfaceRow(networkInterface, detailWidth))
		}
	default:
		lines = append(lines,
			networkTitleStyle.Render("TRAFFIC SOURCES"),
			dimStyle.Render(truncate(m.snapshot.NetworkProcessError, detailWidth)),
		)
	}
	return pagePanelSpec{
		title: "NETWORK ACTIVITY", meta: "NOW + TOTAL",
		lines: lines, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder,
	}
}

func (m *monitorModel) nodeQueuePanelSpec(rowLimit, width int) pagePanelSpec {
	queue := m.slurmQueue
	if queue == nil {
		return pagePanelSpec{}
	}
	meta := fmt.Sprintf("%s · %s", queue.Cluster, queue.Node)
	lines := []string{
		strings.Join([]string{
			processRunningStyle.Render("● RUNNING"),
			processWaitingStyle.Render("◆ NEXT"),
			gpuTitleStyle.Render("○ QUEUED"),
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
	return pagePanelSpec{
		title: "NODE QUEUE / SLURM", meta: meta,
		lines: lines, titleStyle: processTitleStyle, borderColor: colorProcessBorder,
	}
}

func renderPagePanel(width int, spec pagePanelSpec) string {
	return btopPanel(width, spec.title, spec.meta, strings.Join(spec.lines, "\n"),
		spec.titleStyle, spec.borderColor)
}

func renderPagePanelRow(width int, specs ...pagePanelSpec) string {
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

func (m *monitorModel) renderOverviewProcesses(width, rows int) string {
	return m.renderProcessPreview(
		width, rows, "TOP PROCESSES",
		fmt.Sprintf("TOP %d · FULL LIST [4 PROCESSES]", min(rows, len(m.filteredProcesses()))),
	)
}

func (m *monitorModel) renderProcessPreview(width, rows int, title, meta string) string {
	processes := m.filteredProcesses()
	m.clampMonitorProcessCursor(rows)
	format := newProcessFormat(width)
	lines := []string{processTableHeader(format.header(), width-4)}
	end := min(len(processes), m.monitorOffset+rows)
	for index := m.monitorOffset; index < end; index++ {
		row := format.row(processes[index])
		if index == m.monitorCursor {
			row = processStateStyle(processes[index].State).
				Copy().
				Background(lipgloss.Color("#24283B")).
				Bold(true).
				Render(row)
		} else {
			row = processStateStyle(processes[index].State).Render(row)
		}
		lines = append(lines, row)
	}
	if len(processes) == 0 {
		lines = append(lines, dimStyle.Render("No process data available."))
	}
	for len(lines) < rows+1 {
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
		if len(sections) > 0 {
			y++
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
	processRows := min(12, max(4, m.height-2-1-y-2-3))
	processY := y
	if len(sections) > 0 {
		processY++
	}
	workloads := m.renderProcessPreview(
		width, processRows, "COMPUTE WORKLOADS",
		fmt.Sprintf("TOP %d BY CPU · DETAILS [4 PROCESSES]", min(processRows, len(m.filteredProcesses()))),
	)
	appendSection(workloads)
	return strings.Join(sections, "\n\n"), []widgetPlacement{{
		ID: dashboardPanelProcesses, X: 0, Y: processY, Width: width,
		Height: lipgloss.Height(workloads), ProcessRows: processRows, ProcessRowStart: 2,
	}}
}

func (m *monitorModel) renderNetworkPage(width int) (string, []widgetPlacement) {
	card, _ := m.metricWidgetCard(dashboardPanelNetwork)
	detailRows := min(12, max(6, m.height/3))
	sections := []string{m.renderLargeNetworkWidgetWithLimit(card, width, detailRows)}
	if len(m.snapshot.NetworkInterfaces) > 0 && len(m.snapshot.NetworkProcesses) > 0 {
		lines := []string{networkInterfaceHeader(width - 4)}
		interfaceLimit := min(len(m.snapshot.NetworkInterfaces), max(3, m.height/4))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:interfaceLimit] {
			lines = append(lines, renderNetworkInterfaceRow(networkInterface, width-4))
		}
		sections = append(sections, btopPanel(
			width, "INTERFACES", fmt.Sprintf("%d ACTIVE", len(m.snapshot.NetworkInterfaces)),
			strings.Join(lines, "\n"), networkTitleStyle, colorNetworkBorder,
		))
	}
	return strings.Join(sections, "\n\n"), nil
}

func (m *monitorModel) renderProcessesPage(width int) (string, []widgetPlacement) {
	headerHeight := 2
	footerHeight := 1
	panelOverhead := 4
	rows := max(3, m.height-headerHeight-footerHeight-panelOverhead)
	m.clampMonitorProcessCursor(rows)
	panel := m.processPanel(dashboardLayout{
		width: width, height: m.height, processRows: rows, compactGPU: width < 112,
	})
	return panel, []widgetPlacement{{
		ID: dashboardPanelProcesses, X: 0, Y: 0, Width: width,
		Height: lipgloss.Height(panel), ProcessRows: rows, ProcessRowStart: 3,
	}}
}

func (m *monitorModel) switchMonitorPage(page monitorPage) {
	if page < monitorPageOverview || page > monitorPageProcesses {
		return
	}
	m.monitorPage = page
	m.dashboardScroll = 0
	m.monitorFocus = monitorFocusProcesses
	m.status = page.label() + " page."
	if page != monitorPageProcesses {
		m.filtering = false
	}
}

func (m *monitorModel) cycleMonitorPage(delta int) {
	page := int(m.monitorPage)
	if page < int(monitorPageOverview) || page > int(monitorPageProcesses) {
		page = int(monitorPageOverview)
	}
	page = (page + delta + 4) % 4
	m.switchMonitorPage(monitorPage(page))
}
