package main

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const hubHeaderRows = 2

type hubRackRow struct {
	// Negative node means a group heading or a spacer (negative group).
	group, node int
}

func (m *hubModel) pages() [][]hubRackRow {
	budget := m.hubRackContentBudget()
	pages := [][]hubRackRow{{}}
	if budget < 2 {
		return pages
	}
	for group, section := range m.nodeGroups() {
		for i, node := range section.nodes {
			page := &pages[len(pages)-1]
			needed := 1
			if i == 0 {
				needed++
			}
			if len(*page)+needed > budget {
				pages = append(pages, nil)
				page = &pages[len(pages)-1]
			}
			if i == 0 || len(*page) == 0 {
				*page = append(*page, hubRackRow{group, -1})
			}
			*page = append(*page, hubRackRow{group, node})
		}
	}
	return pages
}

func (m *hubModel) hubFramedMode() bool {
	if m.width < 60 || m.height < 18 {
		return false
	}
	if len(m.config.SlurmClusters) > 0 && !m.queueExpanded {
		natural := 3 + len(m.nodeGroups()) + len(m.config.Nodes)
		if m.height-hubHeaderRows-1 < natural+4 {
			return false
		}
	}
	return true
}

func (m *hubModel) hubRackStartRow() int {
	if m.hubFramedMode() {
		return hubHeaderRows + 2
	}
	return hubHeaderRows
}

// hubRackBudget returns the outer row height allocated to the rack section.
func (m *hubModel) hubRackBudget() int {
	body := max(0, m.height-hubHeaderRows-1)
	if len(m.config.SlurmClusters) == 0 {
		return body
	}
	if len(m.config.Nodes) == 0 {
		return 0
	}
	if m.queueExpanded {
		if body < 6 {
			return 0
		}
		if m.hubFramedMode() {
			return min(body-5, max(5, body/4))
		}
		return min(body-3, min(7, max(2, body/4)))
	}
	if m.hubFramedMode() {
		// Need 2 border rows + 1 column header + group headings + node rows.
		natural := 3 + len(m.nodeGroups()) + len(m.config.Nodes)
		minQueue := 7
		if body < 18 {
			minQueue = 4
		}
		if body-natural >= minQueue {
			return natural
		}
		return max(6, body-minQueue)
	}
	return max(0, body-1)
}

func (m *hubModel) hubRackContentBudget() int {
	budget := m.hubRackBudget()
	if !m.hubFramedMode() {
		return budget
	}
	// Subtract top/bottom panel borders and the table column header row.
	return max(1, budget-3)
}

func hubCursorPage(pages [][]hubRackRow, cursor int) int {
	for p, rows := range pages {
		for _, row := range rows {
			if row.node == cursor {
				return p
			}
		}
	}
	return 0
}

func (m *hubModel) clampCursor() {
	order := m.nodeOrder()
	if len(order) == 0 {
		m.cursor = 0
		return
	}
	for _, index := range order {
		if index == m.cursor {
			return
		}
	}
	m.cursor = order[0]
}

func (m *hubModel) moveCursorVertical(delta int) {
	order := m.nodeOrder()
	for position, index := range order {
		if index == m.cursor {
			m.cursor = order[min(len(order)-1, max(0, position+delta))]
			return
		}
	}
	m.clampCursor()
}

func hubRackWidths(width int) (list, detail int) {
	if width < 94 {
		return width, 0
	}
	if width >= 110 {
		detail = min(54, max(44, width*35/100))
		return width - detail - 1, detail
	}
	detail = min(48, max(40, width*32/100))
	return width - detail - 1, detail
}

func (m *hubModel) nodeAt(x, y int) (int, bool) {
	width := max(1, m.width)
	left, _ := hubRackWidths(width)
	start := m.hubRackStartRow()
	if x < 0 || x >= left || y < start || y >= m.height-1 {
		return 0, false
	}
	pages := m.pages()
	rows := pages[hubCursorPage(pages, m.cursor)]
	line := y - start
	if line < 0 || line >= len(rows) || rows[line].node < 0 {
		return 0, false
	}
	return rows[line].node, true
}

func (m *hubModel) hubView() string {
	width := max(1, m.width)
	if !m.hubFramedMode() {
		return m.hubCompactFallbackView(width)
	}
	listWidth, detailWidth := hubRackWidths(width)
	rackOuterHeight := m.hubRackBudget()
	rackInnerWidth := max(20, listWidth-4)
	columns := hubRackLayout(rackInnerWidth)

	header := m.renderHubTopHeader(width, columns.history)
	var bodySections []string

	if rackOuterHeight > 0 {
		leftPanel := m.renderHubRackPanel(listWidth, rackOuterHeight, columns)
		if detailWidth > 0 && m.cursor >= 0 && m.cursor < len(m.config.Nodes) {
			rightPanel := m.renderHubInspectorPanel(detailWidth, rackOuterHeight)
			bodySections = append(bodySections, lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel))
		} else {
			bodySections = append(bodySections, leftPanel)
		}
	}

	bodyBudget := max(0, m.height-hubHeaderRows-1)
	queueHeight := max(0, bodyBudget-rackOuterHeight)
	if queueHeight > 0 && len(m.config.SlurmClusters) > 0 {
		if m.queueExpanded {
			bodySections = append(bodySections, m.renderHubQueueExpandedPanel(width, queueHeight))
		} else {
			bodySections = append(bodySections, m.renderHubQueuePreviewPanel(width, queueHeight))
		}
	}

	footer := m.renderHubFooter(width, detailWidth)
	fullBody := header
	if len(bodySections) > 0 {
		fullBody += "\n" + strings.Join(bodySections, "\n")
	}
	return terminalFrame(fullBody, footer, width, m.height)
}

func (m *hubModel) renderHubTopHeader(width int, historyCells int) string {
	live, stale, offline := 0, 0, 0
	var (
		totalGPUs, activeGPUs int
		usedVRAM, totalVRAM   uint64
		cpuSum                float64
		cpuNodes              int
		usedMem, totalMem     uint64
		totalRX, totalTX      uint64
		latestAt              time.Time
	)
	for _, state := range m.states {
		if state.Error != "" {
			offline++
			continue
		}
		s := state.Snapshot
		if s.CollectedAt.IsZero() {
			continue
		}
		if s.CollectedAt.After(latestAt) {
			latestAt = s.CollectedAt
		}
		if state.Delay != "" {
			stale++
		} else {
			live++
		}
		cpuSum += s.CPUPercent
		cpuNodes++
		usedMem += s.MemoryUsed
		totalMem += s.MemoryTotal
		totalRX += s.NetworkRX
		totalTX += s.NetworkTX
		for _, gpu := range s.GPUs {
			totalGPUs++
			if gpu.Utilization >= 5 || gpu.MemoryUsed >= 1<<30 {
				activeGPUs++
			}
			usedVRAM += gpu.MemoryUsed
			totalVRAM += gpu.MemoryTotal
		}
	}

	// Row 1: Identity + Live/Stale/Offline badges on left, Slurm/Viewers/Refresh/Theme/Clock on right
	brand := titleStyle.Render("FLEETTY") + dimStyle.Render(" │ ") + valueStyle.Render(m.config.displayName())
	var badges []string
	if live > 0 {
		badges = append(badges, liveBadgeStyle.Render(fmt.Sprintf("● %d/%d LIVE", live, len(m.config.Nodes))))
	} else {
		badges = append(badges, critBadgeStyle.Render(fmt.Sprintf("● 0/%d LIVE", len(m.config.Nodes))))
	}
	if stale > 0 {
		badges = append(badges, warnBadgeStyle.Render(fmt.Sprintf("! %d STALE", stale)))
	}
	if offline > 0 {
		badges = append(badges, critBadgeStyle.Render(fmt.Sprintf("✕ %d OFFLINE", offline)))
	}
	left := brand + dimStyle.Render(" │ ") + strings.Join(badges, " ")

	var rightParts []string
	if len(m.config.SlurmClusters) > 0 {
		slurmOnline := 0
		for _, st := range m.slurmStates {
			if st.Error == "" && !st.Snapshot.CollectedAt.IsZero() {
				slurmOnline++
			}
		}
		slurmStyle := okStyle
		if slurmOnline < len(m.config.SlurmClusters) {
			slurmStyle = warningStyle
		}
		rightParts = append(rightParts, dimStyle.Render("Slurm: ")+slurmStyle.Render(fmt.Sprintf("%d/%d online", slurmOnline, len(m.config.SlurmClusters))))
	}
	if m.service != nil {
		viewers := m.service.viewers.Load()
		label := fmt.Sprintf("%d VIEWERS", viewers)
		if viewers == 1 {
			label = "1 VIEWER"
		}
		rightParts = append(rightParts, hubMetricStyle.Render(label))
	}
	interval := m.config.refreshInterval()
	rightParts = append(rightParts, dimStyle.Render(fmt.Sprintf("Refresh %s", interval)))
	rightParts = append(rightParts, dimStyle.Render(strings.ToUpper(m.colorMode.String())))
	if !latestAt.IsZero() {
		rightParts = append(rightParts, clockStyle.Render(latestAt.Format("15:04:05")))
	}
	right := strings.Join(rightParts, dimStyle.Render(" │ "))
	line1 := hubRackAlign(left, right, width)

	// Row 2: Cluster-wide Aggregate Telemetry Bar
	var pulse []string
	if totalGPUs > 0 {
		gpuPct := float64(activeGPUs) / float64(totalGPUs) * 100
		_, bar := hubCompactInlineBar(gpuPct, 6)
		pulse = append(pulse,
			dimStyle.Render("GPUS ")+bar+" "+
				valueStyle.Render(fmt.Sprintf("%d/%d", activeGPUs, totalGPUs))+
				dimStyle.Render(fmt.Sprintf(" (%.0f%%)", gpuPct)))
	}
	if totalVRAM > 0 {
		vramPct := percent(usedVRAM, totalVRAM)
		vramStyle := loadBandStyle(percentBand(vramPct), false)
		pulse = append(pulse, dimStyle.Render("VRAM ")+vramStyle.Render(hubVRAM(usedVRAM, totalVRAM)))
	}
	if cpuNodes > 0 {
		avgCPU := cpuSum / float64(cpuNodes)
		cpuStyle := loadBandStyle(percentBand(avgCPU), false)
		pulse = append(pulse, dimStyle.Render("CPU ")+cpuStyle.Render(fmt.Sprintf("%.0f%%", avgCPU)))
	}
	if totalMem > 0 {
		memPct := percent(usedMem, totalMem)
		memStyle := loadBandStyle(percentBand(memPct), false)
		pulse = append(pulse, dimStyle.Render("MEM ")+memStyle.Render(fmt.Sprintf("%.0f%%", memPct)))
	}
	if totalRX > 0 || totalTX > 0 {
		pulse = append(pulse,
			dimStyle.Render("NET ")+
				networkRXStyle.Render("↓"+hubCompactRate(totalRX)+"/s")+" "+
				networkTXStyle.Render("↑"+hubCompactRate(totalTX)+"/s"))
	}
	pulseLeft := accentStyle.Render("PULSE")
	if len(pulse) > 0 {
		pulseLeft += dimStyle.Render(" │ ") + strings.Join(pulse, dimStyle.Render(" · "))
	}

	var modePills []string
	if historyCells > 0 {
		modePills = append(modePills, dimStyle.Render(fmt.Sprintf("HIST %s", time.Duration(historyCells)*interval)))
	}
	if len(m.config.SlurmClusters) > 0 {
		jobs := m.slurmJobs(-1)
		r, n, p := slurmDisplayStateCounts(jobs)
		qLabel := fmt.Sprintf("[s QUEUE %dR·%dN·%dP]", r, n, p)
		if m.queueExpanded {
			modePills = append(modePills, keycapStyle.Render(qLabel))
		} else {
			modePills = append(modePills, accentStyle.Render(qLabel))
		}
	}
	line2 := hubRackAlign(pulseLeft, strings.Join(modePills, "  "), width)
	return line1 + "\n" + line2
}

func (m *hubModel) renderHubRackPanel(width, outerHeight int, columns hubRackColumns) string {
	innerWidth := max(20, width-4)
	contentRows := max(1, outerHeight-2)
	pages := m.pages()
	page := hubCursorPage(pages, m.cursor)
	groups := m.nodeGroups()

	lines := make([]string, 0, contentRows)
	lines = append(lines, m.renderHubRackTableHeader(innerWidth, columns))

	now := time.Now()
	if len(m.config.Nodes) == 0 {
		lines = append(lines, warningStyle.Render("No servers are configured."))
	} else {
		for _, row := range pages[page] {
			if len(lines) >= contentRows {
				break
			}
			switch {
			case row.group < 0:
				lines = append(lines, "")
			case row.node < 0:
				lines = append(lines, m.renderHubGroupDivider(groups[row.group], innerWidth))
			default:
				line := m.renderHubRackRowColumns(row.node, innerWidth, now, columns)
				if row.node == m.cursor && !m.queueExpanded {
					line = hubHighlightRow(line, innerWidth, m.colorMode)
				}
				lines = append(lines, line)
			}
		}
	}
	for len(lines) < contentRows {
		lines = append(lines, "")
	}

	live := 0
	for _, st := range m.states {
		if st.Error == "" && !st.Snapshot.CollectedAt.IsZero() {
			live++
		}
	}
	meta := fmt.Sprintf("%d/%d LIVE · %d GROUPS", live, len(m.config.Nodes), len(groups))
	if len(pages) > 1 {
		meta = fmt.Sprintf("PAGE %d/%d · %s", page+1, len(pages), meta)
	}
	borderColor := color.Color(colorBorder)
	if !m.queueExpanded {
		borderColor = colorBorderFocus
	}
	return btopPanel(width, "CLUSTER RACK", meta, strings.Join(lines[:contentRows], "\n"), titleStyle, borderColor)
}

func (m *hubModel) renderHubGroupDivider(group hubNodeGroup, width int) string {
	style := accentStyle
	switch group.style {
	case hubGroupStyleCPU:
		style = okStyle
	case hubGroupStyleNetwork:
		style = networkRXStyle
	}
	live, totalGPUs, totalCores := 0, 0, 0
	for _, idx := range group.nodes {
		st := m.hubState(idx)
		if st.Error == "" && !st.Snapshot.CollectedAt.IsZero() {
			live++
			totalGPUs += len(st.Snapshot.GPUs)
			totalCores += st.Snapshot.CPUCores
		}
	}
	var rightParts []string
	rightParts = append(rightParts, fmt.Sprintf("%d/%d LIVE", live, len(group.nodes)))
	if totalGPUs > 0 {
		rightParts = append(rightParts, fmt.Sprintf("%d GPUS", totalGPUs))
	} else if totalCores > 0 {
		rightParts = append(rightParts, fmt.Sprintf("%d CORES", totalCores))
	}
	right := dimStyle.Render(" " + strings.Join(rightParts, " · "))
	left := style.Render(group.title) + " "
	ruleLen := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + barTrackStyle.Render(strings.Repeat("─", ruleLen)) + right
}

func (m *hubModel) renderHubRackTableHeader(width int, columns hubRackColumns) string {
	parts := []string{fixedCell(" NODE", columns.name, false)}
	if columns.address > 0 {
		parts = append(parts, fixedCell("ADDRESS", columns.address, false))
	}
	if columns.state > 0 {
		parts = append(parts, fixedCell("STATE", columns.state, false))
	}
	parts = append(parts, fixedCell("PRIMARY", hubRackLoadWidth, false))
	if columns.history > 0 {
		parts = append(parts, fixedCell("TREND", columns.history, false))
	}
	if columns.aux > 0 {
		parts = append(parts, fixedCell("VRAM/DSK", columns.aux, true))
	}
	if columns.cpuMem {
		parts = append(parts, fixedCell("CPU", 4, true), fixedCell("MEM", 4, true))
	}
	if columns.temp {
		parts = append(parts, fixedCell("TEMP", 4, true))
	}
	if columns.occupant > 0 {
		parts = append(parts, fixedCell("WORKLOAD", columns.occupant, false))
	}
	line := strings.Join(parts, " ")
	return processHeaderStyle.Render(hubRackPad(line, width))
}

func (m *hubModel) renderHubInspectorPanel(width, outerHeight int) string {
	innerWidth := max(16, width-4)
	contentRows := max(1, outerHeight-2)
	index := m.cursor
	node, state := m.config.Nodes[index], m.hubState(index)
	s := state.Snapshot
	storageWarning := hubStorageWarning(s)
	glyph, status, statusStyle := hubRackHealth(state, storageWarning)

	meta := statusStyle.Render(glyph + " " + status)
	if state.Error == "" && !s.CollectedAt.IsZero() {
		if state.Delay != "" {
			meta = warningStyle.Render("! STALE " + hubAge(time.Since(state.LastSeen)))
		} else if state.Latency > 0 {
			meta = statusStyle.Render(glyph+" "+status) + dimStyle.Render(fmt.Sprintf(" · %dms", state.Latency.Milliseconds()))
		}
	}

	title := "NODE · " + node.Name
	lines := m.renderHubInspectorLines(index, innerWidth, contentRows)
	for len(lines) < contentRows {
		lines = append(lines, "")
	}
	return btopPanel(width, title, meta, strings.Join(lines[:contentRows], "\n"), accentStyle, colorBorder)
}

func (m *hubModel) renderHubInspectorLines(index, width, budget int) []string {
	node, state := m.config.Nodes[index], m.hubState(index)
	s := state.Snapshot
	now := time.Now()
	var lines []string

	// Identity & Slurm row
	addr := strings.TrimPrefix(hubNodeAddressLabel(node.Address), "IP ")
	idLeft := dimStyle.Render("IP ") + valueStyle.Render(addr)
	if s.OSName != "" && width >= 36 {
		idLeft += dimStyle.Render(" · " + s.OSName)
	}
	idRight := ""
	if rawSlurm := m.hubSlurmNodeStateRaw(index, now); rawSlurm != "" && rawSlurm != "—" {
		slurmSty := okStyle
		if hubSlurmAlert(rawSlurm) != "" {
			slurmSty = warningStyle
		} else if strings.HasPrefix(rawSlurm, "IDLE") {
			slurmSty = dimStyle
		}
		idRight = dimStyle.Render("SLURM ") + slurmSty.Render(rawSlurm)
	}
	lines = append(lines, hubRackAlign(idLeft, idRight, width))

	if state.Error != "" {
		lines = append(lines,
			pageRule(width),
			dangerStyle.Render("● CONNECTION FAILED"),
			valueStyle.Render(hubShortError(state.Error, width)),
		)
		if !state.LastSeen.IsZero() {
			lines = append(lines, hubRackAlign(dimStyle.Render("Last seen"), valueStyle.Render(hubAge(time.Since(state.LastSeen))+" ago"), width))
		}
		if wait := time.Until(state.NextRetry); wait > 0 {
			lines = append(lines, hubRackAlign(dimStyle.Render("Retry in"), warningStyle.Render(hubAge(wait)), width))
		}
		lines = append(lines, dimStyle.Render("Press r to retry immediately."))
		return lines
	}
	if s.CollectedAt.IsZero() {
		lines = append(lines, pageRule(width), dimStyle.Render("○ Waiting for initial telemetry snapshot…"))
		return lines
	}

	if state.Delay != "" {
		lines = append(lines,
			hubRackAlign(warningStyle.Render("! STALE"), dimStyle.Render("Last seen "+hubAge(time.Since(state.LastSeen))+" ago"), width),
			warningStyle.Render(hubShortError(state.Delay, max(8, width))),
		)
	} else if warn := hubStorageWarning(s); warn != "" {
		lines = append(lines, hubRackAlign(warningStyle.Render("▲ WARN"), warningStyle.Render(ansi.Truncate(warn, max(8, width-8), "…")), width))
	}

	// Compact 1-line resource gauges
	barWidth := max(8, min(16, width-28))
	gauge := func(label string, pct float64, detail string) string {
		style, bar := hubCompactInlineBar(pct, barWidth)
		left := dimStyle.Render(fixedCell(label, 8, false)) + " " + bar + " " + style.Render(fmt.Sprintf("%4.0f%%", pct))
		return hubRackAlign(left, detail, width)
	}

	_, usage, field := hubPrimary(node, s)
	switch field {
	case sparkGPU:
		_, vramUsed, vramTotal, _ := hubGPUStats(s.GPUs)
		vramText := dimStyle.Render("VRAM ") + loadBandStyle(vramBand(vramUsed, vramTotal), false).Render(hubVRAM(vramUsed, vramTotal))
		lines = append(lines, gauge("GPU PEAK", usage, vramText))
	case sparkNet:
		mount, diskPct := hubNASDiskUsage(s)
		lines = append(lines, gauge("DISK MAX", diskPct, dimStyle.Render(truncate(mount, 12))))
	}

	cpuDetail := ""
	if s.CPUCores > 0 {
		cpuDetail = dimStyle.Render(fmt.Sprintf("%d cores", s.CPUCores))
	}
	lines = append(lines, gauge("CPU", s.CPUPercent, cpuDetail))

	memPct := percent(s.MemoryUsed, s.MemoryTotal)
	memDetail := dimStyle.Render(hubVRAM(s.MemoryUsed, s.MemoryTotal))
	lines = append(lines, gauge("MEMORY", memPct, memDetail))

	if field != sparkNet {
		diskPct := percent(s.DiskUsed, s.DiskTotal)
		lines = append(lines, gauge("DISK /", diskPct, hubNetworkRates(s, 16)))
	} else {
		lines = append(lines, hubRackAlign(dimStyle.Render("NETWORK"), hubNetworkRates(s, width-10), width))
	}

	if len(lines)+2 > budget {
		return lines
	}
	lines = append(lines, pageRule(width))

	// Profile-specific rich breakdown
	switch field {
	case sparkGPU:
		var totalPower, totalLimit float64
		for _, g := range s.GPUs {
			totalPower += g.Power
			totalLimit += g.PowerLimit
		}
		modelSummary, homogeneous := hubGPUModelsSummary(s.GPUs)
		pwrText := ""
		if totalPower > 0 {
			pwrText = dimStyle.Render(fmt.Sprintf("%.0f/%.0fW", totalPower, totalLimit))
		}
		maxSummaryWidth := max(8, width-12-lipgloss.Width(pwrText))
		lines = append(lines, hubRackAlign(
			sectionStyle.Bold(true).Render(fmt.Sprintf("DEVICES (%s)", ansi.Truncate(modelSummary, maxSummaryWidth, "…"))),
			pwrText, width,
		))
		devBarWidth := max(6, min(12, width-32))
		nameCellWidth := 0
		if !homogeneous && width >= 36 {
			nameCellWidth = min(14, max(8, width-34))
			devBarWidth = max(5, min(10, width-33-nameCellWidth))
		}
		for _, g := range s.GPUs {
			if len(lines) >= budget-1 {
				break
			}
			gStyle, gBar := hubCompactInlineBar(g.Utilization, devBarWidth)
			vram := loadBandStyle(vramBand(g.MemoryUsed, g.MemoryTotal), false).Render(hubVRAM(g.MemoryUsed, g.MemoryTotal))
			temp := hubRackTemperature(g.Temperature)
			left := valueStyle.Render(fmt.Sprintf("#%-2d", g.Index))
			if nameCellWidth > 0 {
				devName := compactGPUModelName(g.Name)
				if devName == "" {
					devName = "GPU"
				}
				left += " " + dimStyle.Render(fixedCell(devName, nameCellWidth, false))
			}
			left += " " + gBar + " " + gStyle.Render(fmt.Sprintf("%3.0f%%", g.Utilization))
			right := vram + "  " + temp
			lines = append(lines, hubRackAlign(left, right, width))
		}
		if len(lines) < budget {
			user := m.hubOccupant(index, s)
			if user == "" {
				user = "idle"
			}
			lines = append(lines, hubRackAlign(dimStyle.Render("ACTIVE"), hubMetricStyle.Render(user), width))
		}
	case sparkNet:
		lines = append(lines, hubRackAlign(sectionStyle.Bold(true).Render("STORAGE & SERVICES"), "", width))
		for _, fs := range s.Filesystems {
			if len(lines) >= budget-1 {
				break
			}
			pct := percent(fs.Used, fs.Total)
			st, bar := hubCompactInlineBar(pct, max(6, min(12, width-28)))
			left := valueStyle.Render(fixedCell(fs.Mount, 12, false)) + " " + bar
			right := st.Render(fmt.Sprintf("%3.0f%%", pct)) + " " + dimStyle.Render(hubVRAM(fs.Used, fs.Total))
			lines = append(lines, hubRackAlign(left, right, width))
		}
		if len(lines) < budget {
			lines = append(lines, renderNASServiceLine(s, width))
		}
	default:
		user := m.hubOccupant(index, s)
		if user == "" {
			user = "idle"
		}
		lines = append(lines,
			hubRackAlign(dimStyle.Render("ACTIVE WORKLOAD"), hubMetricStyle.Render(user), width),
			hubRackAlign(dimStyle.Render("LOAD AVERAGE"), valueStyle.Render(strings.TrimPrefix(s.LoadAverage, "load ")), width),
		)
	}

	// If full-height Inspector is open (extra vertical budget available), show host metadata & top processes
	if len(lines)+3 <= budget {
		lines = append(lines, pageRule(width))
		if s.CPUModel != "" && len(lines) < budget {
			lines = append(lines, hubRackAlign(dimStyle.Render("CPU MODEL"), valueStyle.Render(truncate(s.CPUModel, max(10, width-12))), width))
		}
		if s.Uptime > 0 && len(lines) < budget {
			lines = append(lines, hubRackAlign(dimStyle.Render("UPTIME"), valueStyle.Render(elapsed(s.Uptime)), width))
		}
	}
	return lines
}

func compactGPUModelName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"NVIDIA ", "GeForce ", "AMD ", "Radeon ", "Intel(R) ", "Intel "} {
		name = strings.TrimPrefix(name, prefix)
	}
	return strings.TrimSpace(name)
}

func hubGPUModelsSummary(gpus []gpuInfo) (summary string, homogeneous bool) {
	if len(gpus) == 0 {
		return "GPU", true
	}
	type modelGroup struct {
		name  string
		count int
	}
	var groups []modelGroup
	for _, g := range gpus {
		name := compactGPUModelName(g.Name)
		if name == "" {
			name = "GPU"
		}
		found := false
		for i := range groups {
			if groups[i].name == name {
				groups[i].count++
				found = true
				break
			}
		}
		if !found {
			groups = append(groups, modelGroup{name: name, count: 1})
		}
	}
	if len(groups) == 1 {
		return fmt.Sprintf("%d × %s", groups[0].count, groups[0].name), true
	}
	parts := make([]string, 0, len(groups))
	for _, gr := range groups {
		if gr.count > 1 {
			parts = append(parts, fmt.Sprintf("%d×%s", gr.count, gr.name))
		} else {
			parts = append(parts, gr.name)
		}
	}
	return strings.Join(parts, " + "), false
}

func (m *hubModel) renderHubFooter(width, detailWidth int) string {
	var hints []string
	if len(m.config.SlurmClusters) > 0 {
		label := "expand queue"
		if width < 50 {
			label = "queue"
		}
		hints = append(hints, keyHint("s", label))
	}
	hints = append(hints, keyHint("↑↓", "select"), keyHint("enter", "open"))
	if m.queueExpanded {
		hints = []string{keyHint("↑↓", "jobs"), keyHint("enter", "explain")}
		if len(m.config.Nodes) > 0 {
			hints = append([]string{keyHint("s/esc", "collapse")}, hints...)
		}
		if m.slurmExplain {
			hints = []string{keyHint("esc", "back to jobs"), keyHint("↑↓", "jobs")}
		} else {
			hints = append(hints, keyHint("pg↑↓", "page"))
			if len(m.config.SlurmClusters) > 1 {
				hints = append(hints, keyHint("←→", "cluster"), keyHint("a", "all"))
			}
		}
	}
	quitLabel := "quit"
	if m.queueExpanded && (len(m.config.Nodes) > 0 || m.slurmExplain) {
		quitLabel = "back"
	}
	hints = append(hints, keyHint("r", "refresh"), keyHint("t", "theme"), keyHint("q", quitLabel))
	var visibleHints []string
	used := 0
	for _, hint := range hints {
		size := lipgloss.Width(hint)
		if len(visibleHints) > 0 {
			size += 2
		}
		if used+size > width {
			break
		}
		visibleHints = append(visibleHints, hint)
		used += size
	}
	footer := strings.Join(visibleHints, "  ")
	pages := m.pages()
	page := hubCursorPage(pages, m.cursor)
	if !m.queueExpanded && len(pages) > 1 {
		paging := dimStyle.Render(fmt.Sprintf("PAGE %d/%d", page+1, len(pages)))
		footer = hubRackAlign(footer, paging, width)
	} else if room := width - lipgloss.Width(footer) - 2; room >= 12 {
		footer += "  " + dimStyle.Render(truncate(m.status, room))
	}
	return footer
}

// hubCompactFallbackView handles ultra-small terminals (<60x16) so small viewport tests continue to pass.
func (m *hubModel) hubCompactFallbackView(width int) string {
	listWidth, detailWidth := hubRackWidths(width)
	columns := hubRackLayout(listWidth)
	live, stale, offline := 0, 0, 0
	for _, state := range m.states {
		if state.Error != "" {
			offline++
		} else if !state.Snapshot.CollectedAt.IsZero() {
			if state.Delay != "" {
				stale++
			} else {
				live++
			}
		}
	}
	onlineStyle := okStyle
	if live == 0 {
		onlineStyle = dimStyle
	}
	count := onlineStyle.Render(fmt.Sprintf("● %d/%d LIVE", live, len(m.config.Nodes)))
	if stale > 0 {
		count += "  " + warningStyle.Render(fmt.Sprintf("%d STALE", stale))
	}
	if offline > 0 {
		count += "  " + dangerStyle.Render(fmt.Sprintf("%d OFFLINE", offline))
	}
	title := valueStyle.Render(m.config.displayName())
	header := m.hubStatusHeader(title, count, "", width)
	rule := barTrackStyle.Render(strings.Repeat("─", width))

	pages := m.pages()
	page := hubCursorPage(pages, m.cursor)
	groups := m.nodeGroups()
	var rows []string
	now := time.Now()
	for _, row := range pages[page] {
		switch {
		case row.group < 0:
			rows = append(rows, "")
		case row.node < 0:
			rows = append(rows, hubRackHeading(groups[row.group], listWidth))
		default:
			line := m.renderHubRackRowColumns(row.node, listWidth, now, columns)
			if row.node == m.cursor && !m.queueExpanded {
				line = hubHighlightRow(line, listWidth, m.colorMode)
			}
			rows = append(rows, line)
		}
	}
	rackBudget := m.hubRackBudget()
	if detailWidth > 0 && !m.queueExpanded && m.cursor >= 0 && m.cursor < len(m.config.Nodes) {
		detailW := max(1, width-listWidth-4)
		details := append([]string{accentStyle.Render("NODE · " + m.config.Nodes[m.cursor].Name)}, m.renderHubRackDetail(m.cursor, detailW)...)
		for len(rows) < min(len(details), rackBudget) {
			rows = append(rows, "")
		}
		sep := " " + barTrackStyle.Render("│") + "  "
		for i := range rows {
			detail := ""
			if i < len(details) {
				detail = ansi.Truncate(details[i], detailW, "…")
			}
			rows[i] = hubRackPad(rows[i], listWidth) + sep + detail
		}
	}
	if m.queueExpanded {
		for len(rows) < rackBudget {
			rows = append(rows, "")
		}
		rows = append(rows, m.renderHubQueueExpanded(width)...)
	} else {
		remaining := m.height - hubHeaderRows - 1 - len(rows)
		rows = append(rows, m.renderHubQueuePreview(width, remaining)...)
	}
	footer := m.renderHubFooter(width, detailWidth)
	return terminalFrame(header+"\n"+rule+"\n"+strings.Join(rows, "\n"), footer, width, m.height)
}

func (m *hubModel) hubStatusHeader(title, status, meta string, width int) string {
	if m.service != nil {
		viewers := m.service.viewers.Load()
		label := fmt.Sprintf("%d VIEWERS", viewers)
		if viewers == 1 {
			label = "1 VIEWER"
		}
		if min(12, lipgloss.Width(title))+lipgloss.Width(status)+len(label)+3 <= width {
			status += "  " + hubMetricStyle.Render(label)
		}
	}
	if meta != "" && lipgloss.Width(title)+lipgloss.Width(status)+lipgloss.Width(meta)+5 <= width {
		status += "    " + dimStyle.Render(meta)
	}
	return hubRackAlign(title, status, width)
}

func hubRackPad(text string, width int) string {
	text = ansi.Truncate(text, max(0, width), "…")
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

func hubHighlightRow(line string, width int, mode colorMode) string {
	gray := "237"
	if mode == colorModeLight {
		gray = "253"
	}
	bg := ansi.Style{}.BackgroundColor(lipgloss.Color(gray)).String()
	return bg + strings.ReplaceAll(hubRackPad(line, width), ansi.ResetStyle, ansi.ResetStyle+bg) + ansi.ResetStyle
}

func hubRackAlign(left, right string, width int) string {
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	if lipgloss.Width(right) >= width {
		return ansi.Truncate(right, width, "…")
	}
	return hubRackPad(left, width-lipgloss.Width(right)-1) + " " + right
}

func hubRackHeading(group hubNodeGroup, width int) string {
	style := accentStyle
	switch group.style {
	case hubGroupStyleCPU:
		style = okStyle
	case hubGroupStyleNetwork:
		style = networkRXStyle
	}
	count := fmt.Sprintf("%d NODES", len(group.nodes))
	if len(group.nodes) == 1 {
		count = "1 NODE"
	}
	return hubRackAlign(style.Render(group.title), dimStyle.Render(count), width)
}

func (m *hubModel) hubState(index int) hubNodeState {
	if index >= 0 && index < len(m.states) {
		return m.states[index]
	}
	return hubNodeState{}
}

func hubPrimary(node hubNodeConfig, snapshot monitorSnapshot) (string, float64, sparkField) {
	switch {
	case node.profileFor(snapshot) == machineProfileNAS:
		return "NET", float64(hubTotalNetworkRate(snapshot)), sparkNet
	case len(snapshot.GPUs) > 0 && node.profileFor(snapshot) != machineProfileCPU:
		usage, _, _, _ := hubGPUStats(snapshot.GPUs)
		return fmt.Sprintf("GPU×%d", len(snapshot.GPUs)), usage, sparkGPU
	default:
		return "CPU", snapshot.CPUPercent, sparkCPU
	}
}

var horizontalEighthBlocks = [...]string{"▏", "▎", "▍", "▌", "▋", "▊", "▉"}

func hubCompactInlineBar(value float64, width int) (lipgloss.Style, string) {
	value = math.Max(0, math.Min(100, value))
	if math.IsNaN(value) {
		value = 0
	}
	style := loadBandStyle(percentBand(value), false)
	if value == 0 {
		style = dimStyle
	} else if value < 30 {
		style = okStyle
	}
	eighths := int(math.Round(value * float64(max(0, width)*8) / 100))
	if value > 0 && width > 0 {
		eighths = max(1, eighths)
	}
	fill := strings.Repeat("█", eighths/8)
	if rest := eighths % 8; rest > 0 {
		fill += horizontalEighthBlocks[rest-1]
	}
	return style, style.Render(fill) + barTrackStyle.Render(strings.Repeat("─", max(0, width-lipgloss.Width(fill))))
}

func hubRackMeter(value float64, width int) (lipgloss.Style, string) {
	return hubCompactInlineBar(value, width)
}

const (
	hubRackValueWidth = 6
	hubRackLoadWidth  = 5 + 1 + hubRackValueWidth // metric label (5), space (1), reading (6) = 12
)

type hubRackColumns struct {
	name, address, state, history, aux, occupant, status int
	cpuMem, temp, memory                                 bool
}

func hubRackLayout(width int) hubRackColumns {
	columns := hubRackColumns{name: min(16, max(13, width/3))}
	if width >= 48 {
		columns.name = min(16, max(13, width/5))
	}
	free := width - columns.name - 1 - hubRackLoadWidth

	// State badge column (ALLOC, IDLE, MIXED, DRAIN, STALE, DISK!, OFFLINE)
	if free >= 16 {
		columns.state = 7
		free -= columns.state + 1
	}
	// Secondary capacity column (VRAM 59/96G or DSK 88%)
	if free >= 26 {
		columns.aux = 8
		free -= columns.aux + 1
	}
	// Numeric CPU% and MEM% columns (4 chars each)
	if free >= 24 {
		columns.cpuMem = true
		free -= 10 // " 62%  71%"
	}
	// Temperature column (4 chars)
	if free >= 20 {
		columns.temp = true
		free -= 5 // " 71°C"
	}
	// Occupant / Active workload column
	if free >= 18 {
		columns.occupant = min(13, max(9, free/3))
		free -= columns.occupant + 1
	}
	// Only show raw IP column on extra-wide screens where everything else already fits
	if free >= 28 {
		columns.address = 14
		free -= columns.address + 1
	}
	columns.history = min(hubSparkCap, max(0, free-1))
	return columns
}

func hubRackHealth(state hubNodeState, storageWarning string) (glyph, label string, style lipgloss.Style) {
	switch {
	case state.Error != "":
		return "●", "OFFLINE", dangerStyle
	case state.Snapshot.CollectedAt.IsZero():
		return "○", "CHECKING", dimStyle
	case state.Delay != "":
		return "!", "STALE", warningStyle
	case state.Warning != "" || storageWarning != "":
		return "▲", "WARN", warningStyle
	default:
		return "●", "ONLINE", okStyle
	}
}

func (m *hubModel) hubNodeStateBadge(index int, state hubNodeState, storageWarning string, now time.Time) (string, lipgloss.Style) {
	if state.Error != "" {
		return "OFFLINE", dangerStyle
	}
	s := state.Snapshot
	if s.CollectedAt.IsZero() {
		return "WAIT", dimStyle
	}
	if state.Delay != "" {
		return "STALE", warningStyle
	}
	if storageWarning != "" {
		return "DISK!", warningStyle
	}
	rawSlurm := m.hubSlurmNodeStateRaw(index, now)
	if state.Warning != "" || hubSlurmAlert(rawSlurm) != "" {
		return "WARN", warningStyle
	}
	node := m.config.Nodes[index]
	if node.profileFor(s) == machineProfileNAS {
		return "STORAGE", networkRXStyle
	}
	if s.CPUPercent >= 15 {
		return "ACTIVE", okStyle
	}
	return "ONLINE", okStyle
}

func hubRackTemperature(temp int) string {
	if temp <= 0 {
		return dimStyle.Render("   —")
	}
	return loadBandStyle(temperatureBand(temp), false).Render(fixedCell(fmt.Sprintf("%d°C", temp), 4, true))
}

func (m *hubModel) renderHubRackRow(index, width int, now time.Time) string {
	return m.renderHubRackRowColumns(index, width, now, hubRackLayout(width))
}

func (m *hubModel) renderHubRackRowColumns(index, width int, now time.Time, columns hubRackColumns) string {
	node, state := m.config.Nodes[index], m.hubState(index)
	s := state.Snapshot
	storageWarning := hubStorageWarning(s)
	glyph, status, statusStyle := hubRackHealth(state, storageWarning)
	rawSlurm := m.hubSlurmNodeStateRaw(index, now)
	alert := hubSlurmAlert(rawSlurm)
	if alert != "" && state.Error == "" {
		glyph, statusStyle = "▲", warningStyle
	}
	marker := statusStyle.Render(glyph)

	selected := index == m.cursor && !m.queueExpanded
	cursor := " "
	if selected {
		cursor = accentStyle.Render("▸")
	}
	nameStyle := valueStyle
	if state.Error == "" && !s.CollectedAt.IsZero() {
		_, _, _, temp := hubGPUStats(s.GPUs)
		if band := temperatureBand(temp); band != loadNeutral {
			nameStyle = loadBandStyle(band, false)
		}
	}
	nameWidth := max(4, columns.name-3)
	alertSuffix := ""
	alertWidth := 0
	if state.Error == "" {
		tag := alert
		if tag == "" && columns.state == 0 && state.Delay != "" {
			tag = "STALE"
		}
		if tag != "" {
			alertSuffix = " " + warningStyle.Render(tag)
			alertWidth = len(tag) + 1
		}
	}
	nodeName := ansi.Truncate(node.Name, max(2, nameWidth-alertWidth), "…")
	styledName := nameStyle.Underline(selected).Render(nodeName) + alertSuffix
	padWidth := max(0, nameWidth-lipgloss.Width(nodeName)-alertWidth)
	nameCell := cursor + marker + " " + styledName + strings.Repeat(" ", padWidth)
	parts := []string{nameCell}

	if columns.address > 0 {
		addr := strings.TrimPrefix(strings.TrimPrefix(hubNodeAddressLabel(node.Address), "IP "), "HOST ")
		parts = append(parts, dimStyle.Render(fixedCell(addr, columns.address, false)))
	}

	badgeText, badgeStyle := m.hubNodeStateBadge(index, state, storageWarning, now)
	if columns.state > 0 {
		parts = append(parts, badgeStyle.Render(fixedCell(badgeText, columns.state, false)))
	}

	prefix := strings.Join(parts, " ") + " "
	rest := max(0, width-lipgloss.Width(prefix))

	if state.Error != "" {
		addr := strings.TrimPrefix(hubNodeAddressLabel(node.Address), "IP ")
		reason := hubShortError(state.Error, max(8, rest-20))
		offlineDetail := addr + " · " + reason
		if !state.LastSeen.IsZero() {
			offlineDetail += " · seen " + hubAge(now.Sub(state.LastSeen)) + " ago"
		}
		if wait := state.NextRetry.Sub(now); wait > 0 {
			offlineDetail += " · retry " + hubAge(wait)
		}
		if columns.state == 0 {
			return ansi.Truncate(prefix+statusStyle.Render(status)+"  "+dimStyle.Render(offlineDetail), width, "…")
		}
		return ansi.Truncate(prefix+dimStyle.Render(offlineDetail), width, "…")
	}
	if s.CollectedAt.IsZero() {
		return ansi.Truncate(prefix+statusStyle.Render(status), width, "…")
	}

	label, usage, field := hubPrimary(node, s)
	style, _ := hubRackMeter(usage, 0)
	reading := fmt.Sprintf("%.0f%%", usage)
	if field == sparkNet {
		rate := hubTotalNetworkRate(s)
		reading = hubCompactRate(rate) + "/s"
		if rate < 1024 {
			reading = fmt.Sprintf("%dB/s", rate)
		}
		style = networkRXStyle
	}
	if len(s.GPUs) == 0 && s.GPUError != "" && node.profileFor(s) == machineProfileGPU {
		label, reading, field, style = "GPU", "—", sparkGPU, dimStyle
	}
	if rest < hubRackLoadWidth {
		return ansi.Truncate(prefix+dimStyle.Render(label)+" "+style.Render(reading), width, "…")
	}

	primaryCell := dimStyle.Render(fixedCell(label, 5, false)) + " " + style.Render(fixedCell(reading, hubRackValueWidth, true))
	rowCells := []string{primaryCell}

	if columns.history > 0 {
		rowCells = append(rowCells, hubSparkline(state.Spark, field, columns.history, m.config.refreshInterval(), now))
	}

	if columns.aux > 0 {
		auxText := "—"
		auxStyle := dimStyle
		switch field {
		case sparkGPU:
			_, used, total, _ := hubGPUStats(s.GPUs)
			auxText = hubVRAM(used, total)
			auxStyle = loadBandStyle(vramBand(used, total), false)
		case sparkNet:
			_, dskPct := hubNASDiskUsage(s)
			auxText = fmt.Sprintf("DSK %.0f%%", dskPct)
			auxStyle = loadBandStyle(percentBand(dskPct), false)
		default:
			dskPct := percent(s.DiskUsed, s.DiskTotal)
			auxText = fmt.Sprintf("DSK %.0f%%", dskPct)
			auxStyle = loadBandStyle(percentBand(dskPct), false)
		}
		rowCells = append(rowCells, auxStyle.Render(fixedCell(auxText, columns.aux, true)))
	}

	if columns.cpuMem {
		cpuStyle := loadBandStyle(percentBand(s.CPUPercent), false)
		memPct := percent(s.MemoryUsed, s.MemoryTotal)
		memStyle := loadBandStyle(percentBand(memPct), false)
		rowCells = append(rowCells,
			cpuStyle.Render(fixedCell(fmt.Sprintf("%.0f%%", s.CPUPercent), 4, true)),
			memStyle.Render(fixedCell(fmt.Sprintf("%.0f%%", memPct), 4, true)),
		)
	}

	if columns.temp {
		_, _, _, temp := hubGPUStats(s.GPUs)
		rowCells = append(rowCells, hubRackTemperature(temp))
	}

	if columns.occupant > 0 {
		occ := m.hubOccupant(index, s)
		occStyle := hubMetricStyle
		if occ == "" {
			if state.Delay != "" {
				occ = "stale " + hubAge(now.Sub(state.LastSeen))
				occStyle = warningStyle
			} else if node.profileFor(s) == machineProfileNAS {
				occ = hubCompactNASServices(s)
				occStyle = warningStyle
			}
		}
		if occ != "" {
			rowCells = append(rowCells, occStyle.Render(fixedCell(occ, columns.occupant, false)))
		}
	}

	line := prefix + strings.Join(rowCells, " ")
	return strings.TrimRight(ansi.Truncate(line, width, "…"), " ")
}

func hubCompactNASServices(s monitorSnapshot) string {
	okCount, totalCount := 0, 0
	for _, svc := range s.Services {
		totalCount++
		if svc.Healthy {
			okCount++
		}
	}
	for _, c := range s.Containers {
		totalCount++
		if dockerContainerHealthy(c) {
			okCount++
		}
	}
	for _, p := range s.PM2Processes {
		totalCount++
		if strings.EqualFold(p.Status, "online") {
			okCount++
		}
	}
	if totalCount == 0 {
		return ""
	}
	return fmt.Sprintf("SVC %d/%d", okCount, totalCount)
}

func (m *hubModel) renderHubRackDetail(index, width int) []string {
	return m.renderHubInspectorLines(index, width, 18)
}

func slurmSnapshotStale(cluster slurmClusterConfig, state slurmClusterState, now time.Time) bool {
	return state.Warning != "" || now.Sub(state.Snapshot.CollectedAt) > 2*cluster.refreshInterval()+cluster.timeout()
}

// Slurm state is independent of host reachability and sampled utilization.
// An idle node is not a promise that any particular request will fit.
func (m *hubModel) hubSlurmNodeStateRaw(index int, now time.Time) string {
	node := m.config.Nodes[index]
	if node.SlurmNode == "" || node.SlurmCluster == "" {
		return ""
	}
	for i, cluster := range m.config.SlurmClusters {
		if cluster.Name != node.SlurmCluster || i >= len(m.slurmStates) {
			continue
		}
		state := m.slurmStates[i]
		if state.Error != "" || state.Snapshot.CollectedAt.IsZero() || slurmSnapshotStale(cluster, state, now) {
			return "—"
		}
		for _, observed := range state.Snapshot.Nodes {
			if observed.Name != node.SlurmNode || observed.State == "" {
				continue
			}
			return strings.ToUpper(observed.State)
		}
		break
	}
	return "—"
}

func hubSlurmAlert(raw string) string {
	switch {
	case strings.HasPrefix(raw, "DRAIN"), strings.HasPrefix(raw, "DRNG"):
		return "DRAIN"
	case strings.HasPrefix(raw, "DOWN"):
		return "DOWN"
	case strings.HasPrefix(raw, "FAIL"):
		return "FAIL"
	case strings.HasPrefix(raw, "MAINT"):
		return "MAINT"
	case strings.ContainsAny(raw, "*~#%!$@^"):
		return "SCHED!"
	default:
		return ""
	}
}
