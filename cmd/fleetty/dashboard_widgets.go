package main

import (
	"fmt"
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

	for _, preference := range preferences {
		if preference.Collapsed {
			continue
		}
		span := widgetColumnSpan(preference, columns)
		if used > 0 && used+span > columns {
			flush()
		}
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
	structural := preference.ID == dashboardPanelGPU ||
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

func (m *monitorModel) renderLargeNetworkWidget(card metricCard, width int) string {
	contentWidth := max(4, width-4)
	lines := []string{
		networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s") + "  " +
			networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s"),
		dimStyle.Render(fmt.Sprintf(
			"TOTAL  ↓ %s  ↑ %s",
			bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal),
		)),
		renderMetricVisual(card, contentWidth),
		"",
	}
	meta := "LARGE"
	switch {
	case len(m.snapshot.NetworkProcesses) > 0:
		meta += fmt.Sprintf(" · %d PROCESSES", len(m.snapshot.NetworkProcesses))
		lines = append(lines,
			networkTitleStyle.Render("TOP APPLICATIONS / PROCESSES"),
			networkProcessHeader(contentWidth),
		)
		limit := min(6, len(m.snapshot.NetworkProcesses))
		for _, process := range m.snapshot.NetworkProcesses[:limit] {
			lines = append(lines, renderNetworkProcessRow(process, contentWidth))
		}
	case len(m.snapshot.NetworkInterfaces) > 0:
		meta += fmt.Sprintf(" · %d INTERFACES", len(m.snapshot.NetworkInterfaces))
		lines = append(lines,
			networkTitleStyle.Render("NETWORK INTERFACES"),
			networkInterfaceHeader(contentWidth),
		)
		limit := min(5, len(m.snapshot.NetworkInterfaces))
		for _, networkInterface := range m.snapshot.NetworkInterfaces[:limit] {
			lines = append(lines, renderNetworkInterfaceRow(networkInterface, contentWidth))
		}
		if m.snapshot.NetworkProcessError != "" {
			lines = append(lines, dimStyle.Render(truncate(m.snapshot.NetworkProcessError, contentWidth)))
		}
	default:
		message := "Collecting per-process traffic…"
		if m.snapshot.NetworkProcessError != "" {
			message = m.snapshot.NetworkProcessError
		}
		lines = append(lines,
			networkTitleStyle.Render("TOP APPLICATIONS / PROCESSES"),
			dimStyle.Render(truncate(message, contentWidth)),
		)
	}
	return btopPanel(
		width, "NETWORK", meta, strings.Join(lines, "\n"),
		networkTitleStyle, colorNetworkBorder,
	)
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
