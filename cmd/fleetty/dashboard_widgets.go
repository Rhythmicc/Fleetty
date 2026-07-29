package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type widgetPlacement struct {
	ID          dashboardPanelID
	X, Y        int
	Width       int
	Height      int
	ProcessRows int
}

type renderedDashboardWidget struct {
	preference dashboardPanelPreference
	content    string
	span       int
	placement  widgetPlacement
}

func (m *monitorModel) widgetDashboardView() string {
	m.ensurePanelLayout()
	width := usableWidth(m.width)
	header := dashboardHeader(width, m.snapshot.CollectedAt, m.colorMode, m.nodeName)
	if m.snapshot.CollectedAt.IsZero() {
		return strings.Join([]string{header, "", panelStyle(width).Render("Collecting system metrics…")}, "\n")
	}

	footer := m.renderMonitorFooter(width)
	if m.loadErr != nil {
		footer = ansi.Truncate(warningStyle.Render("Metric warning: "+m.loadErr.Error()), width, "")
	}
	body, placements := m.renderWidgetGrid(width)
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
		footer = ansi.Truncate(footer, max(1, width-lipgloss.Width(scroll)), "") + dimStyle.Render(scroll)
	}

	sections := []string{header}
	if len(visible) > 0 {
		sections = append(sections, strings.Join(visible, "\n"))
	}
	sections = append(sections, footer)
	return strings.Join(sections, "\n")
}

func (m *monitorModel) renderWidgetGrid(width int) (string, []widgetPlacement) {
	columns := widgetGridColumns(width)
	const gap = 1
	availableWidth := width - (columns-1)*gap
	baseColumnWidth := availableWidth / columns
	extraColumns := availableWidth % columns
	columnWidths := make([]int, columns)
	for index := range columnWidths {
		columnWidths[index] = baseColumnWidth
		if index < extraColumns {
			columnWidths[index]++
		}
	}
	preferences := m.orderedDashboardPanels()
	var (
		rows       []string
		placements []widgetPlacement
		row        []renderedDashboardWidget
		used       int
		rowY       int
	)

	flush := func() {
		if len(row) == 0 {
			return
		}
		parts := make([]string, 0, len(row)*2)
		rowHeight := 0
		for index, widget := range row {
			if index > 0 {
				parts = append(parts, strings.Repeat(" ", gap))
			}
			parts = append(parts, widget.content)
			rowHeight = max(rowHeight, lipgloss.Height(widget.content))
			placement := widget.placement
			placement.Y = rowY
			placements = append(placements, placement)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
		rowY += rowHeight + gap
		row = nil
		used = 0
	}

	pending := make([]dashboardPanelPreference, 0, len(preferences))
	for _, preference := range preferences {
		if !preference.Collapsed {
			pending = append(pending, preference)
		}
	}
	for len(pending) > 0 {
		candidate := 0
		span := widgetColumnSpan(pending[candidate], columns)
		if used > 0 && used+span > columns {
			candidate = -1
			remaining := columns - used
			for index := 1; index < len(pending); index++ {
				if widgetColumnSpan(pending[index], columns) <= remaining {
					candidate = index
					break
				}
			}
			if candidate < 0 {
				flush()
				continue
			}
			span = widgetColumnSpan(pending[candidate], columns)
		}
		preference := pending[candidate]
		pending = append(pending[:candidate], pending[candidate+1:]...)
		widgetWidth := (span - 1) * gap
		for _, partWidth := range columnWidths[used : used+span] {
			widgetWidth += partWidth
		}
		content, processRows := m.renderDashboardWidget(preference, widgetWidth)
		if content == "" {
			continue
		}
		x := used * gap
		for _, partWidth := range columnWidths[:used] {
			x += partWidth
		}
		row = append(row, renderedDashboardWidget{
			preference: preference, content: content, span: span,
			placement: widgetPlacement{
				ID: preference.ID, X: x, Width: widgetWidth,
				Height: lipgloss.Height(content), ProcessRows: processRows,
			},
		})
		used += span
		if used >= columns {
			flush()
		}
	}
	flush()
	if len(rows) == 0 {
		return panelStyle(width).Render("No widgets are visible. Press l to edit the layout."), nil
	}
	return strings.Join(rows, "\n\n"), placements
}

func widgetGridColumns(width int) int {
	switch {
	case width >= 148:
		return 4
	case width >= 100:
		return 3
	case width >= 64:
		return 2
	default:
		return 1
	}
}

func widgetColumnSpan(preference dashboardPanelPreference, columns int) int {
	structural := preference.ID == dashboardPanelNetwork ||
		preference.ID == dashboardPanelGPU ||
		preference.ID == dashboardPanelNodeQueue ||
		preference.ID == dashboardPanelProcesses
	switch preference.Size {
	case widgetSizeMedium:
		return min(2, columns)
	case widgetSizeLarge:
		if structural {
			return columns
		}
		return min(2, columns)
	default:
		return 1
	}
}

func (m *monitorModel) renderDashboardWidget(preference dashboardPanelPreference, width int) (string, int) {
	switch preference.ID {
	case dashboardPanelNetwork:
		card, _ := m.metricWidgetCard(preference.ID)
		if preference.Size == widgetSizeLarge {
			return m.renderLargeNetworkWidget(card, width), 0
		}
		return renderMetricWidget(card, width, preference.Size), 0
	case dashboardPanelCPU, dashboardPanelMemory, dashboardPanelDisk, dashboardPanelBattery:
		card, ok := m.metricWidgetCard(preference.ID)
		if !ok {
			return "", 0
		}
		if preference.Size == widgetSizeLarge {
			return m.renderLargeSystemMetricWidget(preference.ID, card, width), 0
		}
		return renderMetricWidget(card, width, preference.Size), 0
	case dashboardPanelGPU:
		return m.renderGPUWidget(width, preference.Size), 0
	case dashboardPanelNodeQueue:
		rows := widgetTableRows(preference.Size, m.height)
		return m.slurmNodePanel(width, rows), 0
	case dashboardPanelProcesses:
		rows := widgetTableRows(preference.Size, m.height)
		layout := dashboardLayout{
			width: width, height: m.height,
			processRows: rows, compactGPU: width < 112,
		}
		m.clampMonitorProcessCursor(rows)
		return m.processPanel(layout), rows
	default:
		return "", 0
	}
}

func widgetTableRows(size widgetSize, terminalHeight int) int {
	switch size {
	case widgetSizeSmall:
		return 1
	case widgetSizeMedium:
		return 4
	default:
		return max(8, min(16, terminalHeight/3))
	}
}

func (m *monitorModel) metricWidgetCard(id dashboardPanelID) (metricCard, bool) {
	cards := m.systemMetricCards()
	index := map[dashboardPanelID]int{
		dashboardPanelCPU: 0, dashboardPanelMemory: 1,
		dashboardPanelDisk: 2, dashboardPanelNetwork: 3,
	}
	if cardIndex, ok := index[id]; ok {
		return cards[cardIndex], true
	}
	if id != dashboardPanelBattery || m.snapshot.Battery == nil {
		return metricCard{}, false
	}
	battery := m.snapshot.Battery
	detail := battery.PowerSource
	if battery.TimeRemaining != "" {
		if detail != "" {
			detail += " · "
		}
		detail += battery.TimeRemaining
	}
	if detail == "" {
		detail = "power source unavailable"
	}
	return metricCard{
		title:  "BATTERY",
		value:  fmt.Sprintf("%.0f%%  %s", battery.Percent, strings.ToUpper(battery.Status)),
		detail: detail, visual: metricVisualBattery, usage: battery.Percent,
		titleStyle: batteryTitleStyle, borderColor: colorBatteryBorder,
	}, true
}

func renderMetricWidget(card metricCard, width int, size widgetSize) string {
	contentWidth := max(4, width-4)
	title := card.titleStyle
	if title.GetForeground() == nil {
		title = sectionStyle
	}
	border := card.borderColor
	if border == nil {
		border = colorPanelBorder
	}
	lines := []string{
		valueStyle.Render(truncate(card.value, contentWidth)),
		dimStyle.Render(truncate(card.detail, contentWidth)),
		renderMetricVisual(card, contentWidth),
	}
	if size == widgetSizeLarge {
		lines = []string{
			valueStyle.Render(truncate(card.value, contentWidth)),
			dimStyle.Render(truncate(card.detail, contentWidth)),
			"",
			dimStyle.Render("RECENT HISTORY"),
			renderMetricVisual(card, contentWidth),
			"",
			dimStyle.Render("CURRENT LEVEL"),
			renderMetricCurrentLevel(card, contentWidth),
		}
	}
	return btopPanel(width, card.title, strings.ToUpper(string(size)), strings.Join(lines, "\n"), title, border)
}

func renderMetricCurrentLevel(card metricCard, width int) string {
	switch card.visual {
	case metricVisualNetwork:
		return renderMetricVisual(card, width)
	case metricVisualBattery:
		return batteryBar(card.usage, width)
	default:
		return bar(card.usage, width)
	}
}

func (m *monitorModel) renderLargeSystemMetricWidget(id dashboardPanelID, card metricCard, width int) string {
	switch id {
	case dashboardPanelCPU:
		return m.renderLargeProcessMetricWidget(card, width, false)
	case dashboardPanelMemory:
		return m.renderLargeProcessMetricWidget(card, width, true)
	case dashboardPanelDisk:
		return m.renderLargeDiskWidget(card, width)
	case dashboardPanelBattery:
		return m.renderLargeBatteryWidget(card, width)
	default:
		return renderMetricWidget(card, width, widgetSizeLarge)
	}
}

func (m *monitorModel) renderLargeProcessMetricWidget(card metricCard, width int, byMemory bool) string {
	contentWidth := max(4, width-4)
	processes := append([]processInfo(nil), m.snapshot.Processes...)
	sort.SliceStable(processes, func(i, j int) bool {
		if byMemory {
			if processes[i].RSS != processes[j].RSS {
				return processes[i].RSS > processes[j].RSS
			}
		} else if processes[i].CPU != processes[j].CPU {
			return processes[i].CPU > processes[j].CPU
		}
		return processes[i].PID < processes[j].PID
	})
	section := "TOP CPU PROCESSES"
	if byMemory {
		section = "TOP MEMORY PROCESSES"
	}
	lines := []string{
		valueStyle.Render(truncate(card.value, contentWidth)),
		dimStyle.Render(truncate(card.detail, contentWidth)),
		renderMetricVisual(card, contentWidth),
		"",
		card.titleStyle.Render(section),
		metricProcessHeader(contentWidth),
	}
	if len(processes) == 0 {
		lines = append(lines, dimStyle.Render("Process attribution is unavailable for this sample."))
	} else {
		limit := min(5, len(processes))
		for _, process := range processes[:limit] {
			lines = append(lines, renderMetricProcessRow(process, contentWidth))
		}
	}
	return btopPanel(
		width, card.title, "LARGE · TOP PROCESSES", strings.Join(lines, "\n"),
		card.titleStyle, card.borderColor,
	)
}

func metricProcessHeader(width int) string {
	nameWidth := max(8, width-31)
	header := fmt.Sprintf("%-7s %-*s %9s %11s",
		"PID", nameWidth, "APPLICATION", "CPU", "RSS")
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderMetricProcessRow(process processInfo, width int) string {
	nameWidth := max(8, width-31)
	return strings.Join([]string{
		dimStyle.Render(fixedCell(strconv.Itoa(process.PID), 7, false)),
		processStateStyle(process.State).Render(fixedCell(metricProcessName(process.Command), nameWidth, false)),
		valueStyle.Render(fixedCell(fmt.Sprintf("%.1f%%", process.CPU), 9, true)),
		memoryTitleStyle.Render(fixedCell(bytes(process.RSS), 11, true)),
	}, " ")
}

func metricProcessName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "unknown"
	}
	name := filepath.Base(fields[0])
	switch name {
	case "python", "python3", "node", "bash", "sh":
		if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") {
			name += " " + filepath.Base(fields[1])
		}
	}
	return sanitizeTerminalText(name)
}

func (m *monitorModel) renderLargeDiskWidget(card metricCard, width int) string {
	contentWidth := max(4, width-4)
	free := counterDelta(m.snapshot.DiskTotal, m.snapshot.DiskUsed)
	usage := percent(m.snapshot.DiskUsed, m.snapshot.DiskTotal)
	status, statusStyle := capacityStatus(usage)
	lines := []string{
		valueStyle.Render(truncate(card.value, contentWidth)),
		dimStyle.Render(fmt.Sprintf("FREE %s  ·  %s USED", bytes(free), card.detail)),
		renderMetricVisual(card, contentWidth),
		"",
		diskTitleStyle.Render("CAPACITY BREAKDOWN"),
		fmt.Sprintf("%-10s %s", "USED", valueStyle.Render(bytes(m.snapshot.DiskUsed))),
		fmt.Sprintf("%-10s %s", "AVAILABLE", valueStyle.Render(bytes(free))),
		fmt.Sprintf("%-10s %s", "TOTAL", valueStyle.Render(bytes(m.snapshot.DiskTotal))),
		fmt.Sprintf("%-10s %s", "PRESSURE", statusStyle.Render(status)),
	}
	return btopPanel(
		width, card.title, "LARGE · CAPACITY", strings.Join(lines, "\n"),
		card.titleStyle, card.borderColor,
	)
}

func capacityStatus(usage float64) (string, lipgloss.Style) {
	switch {
	case usage >= 95:
		return "CRITICAL", dangerStyle
	case usage >= 85:
		return "HIGH", processWaitingStyle
	case usage >= 70:
		return "WATCH", warningStyle
	default:
		return "HEALTHY", processRunningStyle
	}
}

func (m *monitorModel) renderLargeBatteryWidget(card metricCard, width int) string {
	contentWidth := max(4, width-4)
	battery := m.snapshot.Battery
	if battery == nil {
		return ""
	}
	remaining := battery.TimeRemaining
	if remaining == "" {
		remaining = "not reported"
	}
	source := battery.PowerSource
	if source == "" {
		source = "unknown"
	}
	lines := []string{
		valueStyle.Render(truncate(card.value, contentWidth)),
		batteryBar(card.usage, contentWidth),
		"",
		batteryTitleStyle.Render("POWER DETAILS"),
		fmt.Sprintf("%-14s %s", "SOURCE", valueStyle.Render(source)),
		fmt.Sprintf("%-14s %s", "STATE", valueStyle.Render(strings.ToUpper(battery.Status))),
		fmt.Sprintf("%-14s %s", "REMAINING", valueStyle.Render(remaining)),
	}
	return btopPanel(
		width, card.title, "LARGE · POWER", strings.Join(lines, "\n"),
		card.titleStyle, card.borderColor,
	)
}

func (m *monitorModel) renderLargeNetworkWidget(card metricCard, width int) string {
	contentWidth := max(4, width-4)
	meta, details := m.largeNetworkDetails(contentWidth)
	var lines []string
	if contentWidth >= 100 {
		leftWidth := min(50, max(38, contentWidth/3))
		rightWidth := max(40, contentWidth-leftWidth-3)
		summary := []string{
			networkTitleStyle.Render("TRAFFIC SUMMARY"),
			networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
				networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s"),
			dimStyle.Render(fmt.Sprintf(
				"TOTAL  ↓ %s  ↑ %s",
				bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal),
			)),
			"",
			networkTitleStyle.Render("RECENT 60 SECONDS"),
			renderMetricVisual(card, leftWidth),
		}
		lines = strings.Split(renderWidgetColumns(summary, details, leftWidth, rightWidth), "\n")
	} else {
		lines = []string{
			networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
				networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s"),
			dimStyle.Render(fmt.Sprintf(
				"TOTAL  ↓ %s  ↑ %s",
				bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal),
			)),
			renderMetricVisual(card, contentWidth),
			"",
		}
		lines = append(lines, details...)
	}
	return btopPanel(
		width, "NETWORK", meta, strings.Join(lines, "\n"),
		networkTitleStyle, colorNetworkBorder,
	)
}

func (m *monitorModel) largeNetworkDetails(width int) (string, []string) {
	meta := "LARGE"
	var lines []string
	switch {
	case len(m.snapshot.NetworkProcesses) > 0:
		meta += fmt.Sprintf(" · %d PROCESSES", len(m.snapshot.NetworkProcesses))
		lines = []string{
			networkTitleStyle.Render("TOP APPLICATIONS / PROCESSES"),
			networkProcessHeader(width),
		}
		limit := min(6, len(m.snapshot.NetworkProcesses))
		for _, process := range m.snapshot.NetworkProcesses[:limit] {
			lines = append(lines, renderNetworkProcessRow(process, width))
		}
	case len(m.snapshot.NetworkInterfaces) > 0:
		meta += fmt.Sprintf(" · %d INTERFACES", len(m.snapshot.NetworkInterfaces))
		lines = []string{
			networkTitleStyle.Render("NETWORK INTERFACES"),
			networkInterfaceHeader(width),
		}
		limit := min(5, len(m.snapshot.NetworkInterfaces))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
			lines = append(lines, renderNetworkInterfaceRow(networkInterface, width))
		}
		if m.snapshot.NetworkProcessError != "" {
			lines = append(lines, dimStyle.Render(truncate(m.snapshot.NetworkProcessError, width)))
		}
	default:
		message := "Collecting per-process traffic…"
		if m.snapshot.NetworkProcessError != "" {
			message = m.snapshot.NetworkProcessError
		}
		lines = []string{
			networkTitleStyle.Render("TOP APPLICATIONS / PROCESSES"),
			dimStyle.Render(truncate(message, width)),
		}
	}
	return meta, lines
}

func renderWidgetColumns(left, right []string, leftWidth, rightWidth int) string {
	height := max(len(left), len(right))
	rows := make([]string, 0, height)
	leftStyle := lipgloss.NewStyle().Width(leftWidth).MaxWidth(leftWidth)
	rightStyle := lipgloss.NewStyle().Width(rightWidth).MaxWidth(rightWidth)
	divider := dimStyle.Render(" │ ")
	for index := 0; index < height; index++ {
		leftLine, rightLine := "", ""
		if index < len(left) {
			leftLine = ansi.Truncate(left[index], leftWidth, "")
		}
		if index < len(right) {
			rightLine = ansi.Truncate(right[index], rightWidth, "")
		}
		rows = append(rows, leftStyle.Render(leftLine)+divider+rightStyle.Render(rightLine))
	}
	return strings.Join(rows, "\n")
}

func networkProcessHeader(width int) string {
	var header string
	if width >= 64 {
		nameWidth := max(10, width-48)
		header = fmt.Sprintf("%-7s %-*s %12s %12s %13s",
			"PID", nameWidth, "APPLICATION", "DOWN", "UP", "TOTAL")
	} else {
		nameWidth := max(8, width-30)
		header = fmt.Sprintf("%-7s %-*s %10s %10s",
			"PID", nameWidth, "APPLICATION", "DOWN", "UP")
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderNetworkProcessRow(process processNetworkInfo, width int) string {
	if width >= 64 {
		nameWidth := max(10, width-48)
		return strings.Join([]string{
			dimStyle.Render(fixedCell(strconv.Itoa(process.PID), 7, false)),
			valueStyle.Render(fixedCell(process.Name, nameWidth, false)),
			networkRXStyle.Render(fixedCell(bytes(process.RX)+"/s", 12, true)),
			networkTXStyle.Render(fixedCell(bytes(process.TX)+"/s", 12, true)),
			dimStyle.Render(fixedCell(bytes(process.RXTotal+process.TXTotal), 13, true)),
		}, " ")
	}
	nameWidth := max(8, width-30)
	return strings.Join([]string{
		dimStyle.Render(fixedCell(strconv.Itoa(process.PID), 7, false)),
		valueStyle.Render(fixedCell(process.Name, nameWidth, false)),
		networkRXStyle.Render(fixedCell(bytes(process.RX)+"/s", 10, true)),
		networkTXStyle.Render(fixedCell(bytes(process.TX)+"/s", 10, true)),
	}, " ")
}

func networkInterfaceHeader(width int) string {
	nameWidth := max(8, width-42)
	header := fmt.Sprintf("%-*s %11s %11s %16s",
		nameWidth, "INTERFACE", "DOWN", "UP", "ERRORS/DROPS")
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderNetworkInterfaceRow(networkInterface networkInterfaceInfo, width int) string {
	nameWidth := max(8, width-42)
	errorsAndDrops := networkInterface.RXErrors + networkInterface.TXErrors +
		networkInterface.RXDrops + networkInterface.TXDrops
	return strings.Join([]string{
		valueStyle.Render(fixedCell(networkInterface.Name, nameWidth, false)),
		networkRXStyle.Render(fixedCell(bytes(networkInterface.RX)+"/s", 11, true)),
		networkTXStyle.Render(fixedCell(bytes(networkInterface.TX)+"/s", 11, true)),
		dimStyle.Render(fixedCell(strconv.FormatUint(errorsAndDrops, 10), 16, true)),
	}, " ")
}

func (m *monitorModel) renderGPUWidget(width int, size widgetSize) string {
	if size == widgetSizeSmall {
		if m.snapshot.GPUError != "" {
			return btopPanel(width, "GPU", "SMALL",
				dimStyle.Render(truncate(m.snapshot.GPUError, max(4, width-4)))+"\n\n"+
					dimStyle.Render(strings.Repeat("─", max(1, width-4))),
				gpuTitleStyle, colorGPUBorder)
		}
		if len(m.snapshot.GPUs) == 0 {
			return ""
		}
		gpu := m.snapshot.GPUs[0]
		status, style := gpuLoadStatus(gpu.Utilization)
		card := metricCard{
			title: "GPU", value: fmt.Sprintf("%.0f%%  %s", gpu.Utilization, status),
			detail: truncate(gpu.Name, max(4, width-4)),
			visual: metricVisualMeter, usage: gpu.Utilization,
			titleStyle: style, borderColor: colorGPUBorder,
		}
		return renderMetricWidget(card, width, widgetSizeSmall)
	}
	copyModel := *m
	if size == widgetSizeMedium && len(copyModel.snapshot.GPUs) > 1 {
		copyModel.snapshot.GPUs = append([]gpuInfo(nil), copyModel.snapshot.GPUs[:1]...)
	}
	layout := dashboardLayout{width: width, height: m.height, compactGPU: true}
	return copyModel.gpuPanel(layout)
}

func (m *monitorModel) adjustDashboardScroll(delta int) {
	maxScroll := max(0, m.dashboardContent-m.dashboardViewport)
	m.dashboardScroll = min(max(0, m.dashboardScroll+delta), maxScroll)
	if maxScroll == 0 {
		m.status = "All widgets fit in the current terminal."
		return
	}
	m.status = fmt.Sprintf("Dashboard rows %d–%d of %d.",
		m.dashboardScroll+1,
		min(m.dashboardContent, m.dashboardScroll+m.dashboardViewport),
		m.dashboardContent)
}

func (m *monitorModel) visibleWidgetPlacement(id dashboardPanelID) (widgetPlacement, int, bool) {
	headerHeight := lipgloss.Height(dashboardHeader(
		usableWidth(m.width), m.snapshot.CollectedAt, m.colorMode, m.nodeName,
	))
	for _, placement := range m.widgetPlacements {
		if placement.ID != id {
			continue
		}
		screenY := headerHeight + placement.Y - m.dashboardScroll
		if screenY+placement.Height <= headerHeight ||
			screenY >= headerHeight+m.dashboardViewport {
			return widgetPlacement{}, 0, false
		}
		return placement, screenY, true
	}
	return widgetPlacement{}, 0, false
}
