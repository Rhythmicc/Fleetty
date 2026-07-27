package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *monitorModel) nasView() string {
	width := usableWidth(m.width)
	height := m.height
	if height <= 0 {
		height = 24
	}
	nodeName := m.nodeName
	if m.snapshot.NodeName != "" {
		nodeName = m.snapshot.NodeName
	}
	header := dashboardHeaderNamed("FLEETTY NAS", width, m.snapshot.CollectedAt, m.colorMode, nodeName)
	if m.snapshot.CollectedAt.IsZero() {
		return strings.Join([]string{header, "", panelStyle(width).Render("Collecting NAS metrics…")}, "\n")
	}

	system := m.nasSystemPanel(width)
	network := m.nasNetworkPanel(width)
	footer := renderFooter(width, m.status)
	if m.loadErr != nil {
		footer = warningStyle.Render("Metric warning: " + m.loadErr.Error())
	}

	baseHeight := lipgloss.Height(header) + lipgloss.Height(system) + lipgloss.Height(network) + 1
	available := max(3, height-baseHeight)
	filesystemCount := max(1, len(m.snapshot.Filesystems))
	storageRows := min(filesystemCount, max(1, available-2))
	serviceHeight := 0
	if m.hasNASServices() && available >= 6 {
		storageRows = min(filesystemCount, max(1, available-5))
		serviceHeight = available - storageRows - 2
	}

	sections := []string{
		header,
		system,
		network,
		m.nasStoragePanel(width, storageRows),
	}
	if serviceHeight >= 3 {
		sections = append(sections, m.nasServicesDashboard(width, serviceHeight))
	}
	sections = append(sections, footer)
	return strings.Join(sections, "\n")
}

func (m *monitorModel) nasSystemPanel(width int) string {
	memoryPercent := percent(m.snapshot.MemoryUsed, m.snapshot.MemoryTotal)
	if width < 52 {
		content := []string{
			fmt.Sprintf("%s %5.1f%%  %s", cpuTitleStyle.Render("CPU"), m.snapshot.CPUPercent, dimStyle.Render(m.snapshot.LoadAverage)),
			fmt.Sprintf("%s %s/%s  %.1f%% used",
				memoryTitleStyle.Render("MEM"), bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal), memoryPercent),
		}
		return btopPanel(width, "SYSTEM", "NAS", strings.Join(content, "\n"), titleStyle, colorPanelBorder)
	}
	layout := dashboardLayout{width: width, height: m.height, metricCols: 2}
	return renderMetricRows([]metricCard{
		{
			title: "CPU", value: fmt.Sprintf("%.1f%%", m.snapshot.CPUPercent), detail: m.snapshot.LoadAverage,
			visual: metricVisualCPU, usage: m.snapshot.CPUPercent, primaryHistory: m.cpuHistory,
			titleStyle: cpuTitleStyle, borderColor: colorCPUBorder,
		},
		{
			title: "MEMORY", value: fmt.Sprintf("%s / %s", bytes(m.snapshot.MemoryUsed), bytes(m.snapshot.MemoryTotal)),
			detail: fmt.Sprintf("%.1f%% used", memoryPercent), visual: metricVisualMeter, usage: memoryPercent,
			titleStyle: memoryTitleStyle, borderColor: colorMemoryBorder,
		},
	}, layout)
}

func (m *monitorModel) nasNetworkPanel(width int) string {
	interfaceName := "aggregate"
	var errorsAndDrops uint64
	if len(m.snapshot.NetworkInterfaces) > 0 {
		mainInterface := m.snapshot.NetworkInterfaces[0]
		interfaceName = mainInterface.Name
		errorsAndDrops = mainInterface.RXErrors + mainInterface.TXErrors + mainInterface.RXDrops + mainInterface.TXDrops
	}
	contentWidth := max(4, width-4)
	graphWidth := max(2, (contentWidth-5)/2)
	ceiling := historyMax(m.networkRXHistory, m.networkTXHistory)
	lines := []string{
		fmt.Sprintf("%s  %s  %s",
			accentStyle.Render(interfaceName),
			networkRXStyle.Render("↓ "+bytes(m.snapshot.NetworkRX)+"/s"),
			networkTXStyle.Render("↑ "+bytes(m.snapshot.NetworkTX)+"/s")),
		dimStyle.Render(fmt.Sprintf("TOTAL ↓ %s  ↑ %s  ·  ERR/DROP %d",
			bytes(m.snapshot.NetworkRXTotal), bytes(m.snapshot.NetworkTXTotal), errorsAndDrops)),
		networkRXStyle.Render("↓") +
			sparkline(m.networkRXHistory, graphWidth, ceiling, networkRXStyle) +
			dimStyle.Render("  ") + networkTXStyle.Render("↑") +
			sparkline(m.networkTXHistory, graphWidth, ceiling, networkTXStyle),
	}
	return btopPanel(width, "NETWORK", "PRIMARY", strings.Join(lines, "\n"), networkTitleStyle, colorNetworkBorder)
}

func (m *monitorModel) nasStoragePanel(width, rows int) string {
	filesystems := m.snapshot.Filesystems
	if len(filesystems) == 0 {
		filesystems = []filesystemInfo{{
			Mount: "/", Used: m.snapshot.DiskUsed, Total: m.snapshot.DiskTotal,
		}}
	}
	rows = min(max(1, rows), len(filesystems))
	lines := make([]string, 0, rows)
	for _, filesystem := range filesystems[:rows] {
		if filesystem.Error != "" {
			lines = append(lines, dangerStyle.Render(filesystem.Mount+" unavailable"))
			continue
		}
		usage := percent(filesystem.Used, filesystem.Total)
		usageStyle := valueStyle
		switch {
		case usage >= 95:
			usageStyle = dangerStyle
		case usage >= 85:
			usageStyle = warningStyle
		}
		label := filesystem.Mount
		if label == "/" {
			label = "SYSTEM /"
		}
		line := fmt.Sprintf("%-14s %s  %s/%s",
			truncate(label, 14), usageStyle.Render(fmt.Sprintf("%5.1f%%", usage)),
			bytes(filesystem.Used), bytes(filesystem.Total))
		if width >= 100 {
			line += "  " + bar(usage, min(46, width-lipgloss.Width(line)-6))
		}
		lines = append(lines, line)
	}
	meta := fmt.Sprintf("%d MOUNTS", len(filesystems))
	if rows < len(filesystems) {
		meta = fmt.Sprintf("%d/%d SHOWN", rows, len(filesystems))
	}
	return btopPanel(width, "STORAGE", meta, strings.Join(lines, "\n"), diskTitleStyle, colorDiskBorder)
}

func (m *monitorModel) hasNASServices() bool {
	return len(m.snapshot.Containers) > 0 || m.snapshot.DockerError != "" ||
		len(m.snapshot.PM2Processes) > 0 || m.snapshot.PM2Error != "" ||
		len(m.snapshot.Services) > 0
}

func (m *monitorModel) nasServicesDashboard(width, maxHeight int) string {
	if maxHeight < 7 || width < 84 {
		return m.nasServiceOverview(width)
	}
	if width < 108 {
		return m.nasServicesStacked(width, maxHeight)
	}

	httpColumns := 2
	desiredBottomRows := max(len(m.snapshot.PM2Processes), (len(m.snapshot.Services)+1)/2)
	if maxHeight >= 21 {
		httpColumns = 1
		desiredBottomRows = max(len(m.snapshot.PM2Processes), len(m.snapshot.Services))
	}
	desiredBottomRows = max(1, desiredBottomRows)
	bottomRows := min(desiredBottomRows, max(1, maxHeight/2-3))
	bottomHeight := bottomRows + 3
	dockerRows := max(1, maxHeight-bottomHeight-3)

	docker := m.nasDockerPanel(width, dockerRows)
	leftWidth := (width - 1) / 2
	rightWidth := width - leftWidth - 1
	pm2Rows := bottomRows
	if httpColumns == 1 && bottomRows >= 5 {
		pm2Rows = bottomRows - 4
	}
	pm2 := m.nasPM2Panel(leftWidth, pm2Rows)
	if httpColumns == 1 && bottomRows >= 5 {
		pm2 += "\n" + m.nasAttentionPanel(leftWidth)
	}
	http := m.nasHTTPPanel(rightWidth, bottomRows, httpColumns)
	return docker + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, pm2, " ", http)
}

func (m *monitorModel) nasServicesStacked(width, maxHeight int) string {
	if maxHeight < 10 {
		return m.nasServiceOverview(width)
	}
	dockerRows := min(max(1, len(m.snapshot.Containers)), max(1, maxHeight-7))
	docker := m.nasDockerPanel(width, dockerRows)
	remaining := maxHeight - lipgloss.Height(docker)
	if remaining < 4 {
		return docker
	}
	if len(m.snapshot.PM2Processes) > 0 || m.snapshot.PM2Error != "" {
		return docker + "\n" + m.nasPM2Panel(width, max(1, remaining-3))
	}
	return docker + "\n" + m.nasHTTPPanel(width, max(1, remaining-3), 1)
}

func (m *monitorModel) nasServiceOverview(width int) string {
	healthyContainers := 0
	for _, container := range m.snapshot.Containers {
		if dockerContainerHealthy(container) {
			healthyContainers++
		}
	}
	healthyPM2 := 0
	for _, process := range m.snapshot.PM2Processes {
		if strings.EqualFold(process.Status, "online") {
			healthyPM2++
		}
	}
	healthyHTTP := 0
	for _, service := range m.snapshot.Services {
		if service.Healthy {
			healthyHTTP++
		}
	}
	content := fmt.Sprintf("%s %s   %s %s   %s %s",
		processTitleStyle.Render("DOCKER"), healthCount(healthyContainers, len(m.snapshot.Containers)),
		gpuTitleStyle.Render("PM2"), healthCount(healthyPM2, len(m.snapshot.PM2Processes)),
		networkTitleStyle.Render("HTTP"), healthCount(healthyHTTP, len(m.snapshot.Services)))
	return btopPanel(width, "SERVICES", "OVERVIEW", content, processTitleStyle, colorProcessBorder)
}

func (m *monitorModel) nasDockerPanel(width, maxRows int) string {
	healthy := 0
	for _, container := range m.snapshot.Containers {
		if dockerContainerHealthy(container) {
			healthy++
		}
	}
	meta := fmt.Sprintf("%d/%d HEALTHY", healthy, len(m.snapshot.Containers))
	lines := []string{dockerTableHeader(width - 4)}
	if m.snapshot.DockerError != "" {
		lines = append(lines, dangerStyle.Render("Docker unavailable: "+m.snapshot.DockerError))
		meta = "UNAVAILABLE"
	} else if len(m.snapshot.Containers) == 0 {
		lines = append(lines, dimStyle.Render("No containers"))
		meta = "NOT CONFIGURED"
	} else {
		rows := min(max(1, maxRows), len(m.snapshot.Containers))
		for _, container := range m.snapshot.Containers[:rows] {
			lines = append(lines, renderDockerContainerRow(container, width-4))
		}
		if rows < len(m.snapshot.Containers) {
			meta = fmt.Sprintf("%d/%d SHOWN · %d HEALTHY", rows, len(m.snapshot.Containers), healthy)
		}
	}
	return btopPanel(width, "DOCKER", meta, strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
}

func (m *monitorModel) nasPM2Panel(width, rows int) string {
	healthy := 0
	for _, process := range m.snapshot.PM2Processes {
		if strings.EqualFold(process.Status, "online") {
			healthy++
		}
	}
	meta := fmt.Sprintf("%d/%d ONLINE", healthy, len(m.snapshot.PM2Processes))
	lines := []string{pm2TableHeader(width - 4)}
	if m.snapshot.PM2Error != "" {
		lines = append(lines, dangerStyle.Render("PM2 unavailable: "+m.snapshot.PM2Error))
		meta = "UNAVAILABLE"
	} else if len(m.snapshot.PM2Processes) == 0 {
		lines = append(lines, dimStyle.Render("No PM2 processes"))
		meta = "NOT CONFIGURED"
	} else {
		shown := min(max(1, rows), len(m.snapshot.PM2Processes))
		for _, process := range m.snapshot.PM2Processes[:shown] {
			lines = append(lines, renderPM2ProcessRow(process, width-4))
		}
		for len(lines)-1 < rows {
			lines = append(lines, "")
		}
		if shown < len(m.snapshot.PM2Processes) {
			meta = fmt.Sprintf("%d/%d SHOWN · %d ONLINE", shown, len(m.snapshot.PM2Processes), healthy)
		}
	}
	return btopPanel(width, "PM2", meta, strings.Join(lines, "\n"), gpuTitleStyle, colorGPUBorder)
}

func (m *monitorModel) nasHTTPPanel(width, rows, columns int) string {
	healthy := 0
	for _, service := range m.snapshot.Services {
		if service.Healthy {
			healthy++
		}
	}
	meta := fmt.Sprintf("%d/%d HEALTHY", healthy, len(m.snapshot.Services))
	lines := []string{httpTableHeader(width-4, columns)}
	if len(m.snapshot.Services) == 0 {
		lines = append(lines, dimStyle.Render("No HTTP checks"))
		meta = "NOT CONFIGURED"
	} else if columns == 2 {
		cellWidth := max(12, (width-5)/2)
		shown := min(len(m.snapshot.Services), max(1, rows)*2)
		for index := 0; index < shown; index += 2 {
			left := renderHTTPServiceRow(m.snapshot.Services[index], cellWidth)
			right := ""
			if index+1 < shown {
				right = renderHTTPServiceRow(m.snapshot.Services[index+1], cellWidth)
			}
			lines = append(lines, fixedCell(left, cellWidth, false)+" "+right)
		}
		for len(lines)-1 < rows {
			lines = append(lines, "")
		}
		if shown < len(m.snapshot.Services) {
			meta = fmt.Sprintf("%d/%d SHOWN · %d HEALTHY", shown, len(m.snapshot.Services), healthy)
		}
	} else {
		shown := min(max(1, rows), len(m.snapshot.Services))
		for _, service := range m.snapshot.Services[:shown] {
			lines = append(lines, renderHTTPServiceRow(service, width-4))
		}
		for len(lines)-1 < rows {
			lines = append(lines, "")
		}
		if shown < len(m.snapshot.Services) {
			meta = fmt.Sprintf("%d/%d SHOWN · %d HEALTHY", shown, len(m.snapshot.Services), healthy)
		}
	}
	return btopPanel(width, "HTTP CHECKS", meta, strings.Join(lines, "\n"), networkTitleStyle, colorNetworkBorder)
}

func dockerContainerHealthy(container containerInfo) bool {
	return container.Running && !strings.EqualFold(container.Health, "unhealthy")
}

func renderDockerContainerRow(container containerInfo, width int) string {
	state := strings.ToUpper(container.State)
	style := processStoppedStyle
	switch {
	case container.Running && strings.EqualFold(container.Health, "unhealthy"):
		state, style = "BAD", processZombieStyle
	case container.Running && strings.EqualFold(container.Health, "starting"):
		state, style = "START", processWaitingStyle
	case container.Running:
		state, style = "UP", processRunningStyle
	case state == "":
		state = "DOWN"
	}
	image := container.Image
	name := container.Name
	ports := container.Ports
	if ports == "" {
		ports = "no ports"
	}
	tail := ports
	if !container.Running && container.Status != "" {
		tail = container.Status
	}
	switch {
	case width >= 132:
		return strings.Join([]string{
			style.Render(fixedCell(state, 6, false)),
			valueStyle.Render(fixedCell(name, 18, false)),
			dimStyle.Render(fixedCell(image, 16, false)),
			percentValue(container.CPU, 7),
			memoryValue(container.MemoryUsed, container.MemoryLimit, 17),
			networkRXStyle.Render(fixedCell("↓"+bytes(container.NetworkRX), 10, true)) + " " +
				networkTXStyle.Render(fixedCell("↑"+bytes(container.NetworkTX), 10, true)),
			dimStyle.Render(fixedCell("↓"+bytes(container.BlockRead), 10, true) + " " + fixedCell("↑"+bytes(container.BlockWrite), 10, true)),
			fixedCell(elapsed(container.Uptime), 8, false),
			fixedCell(fmt.Sprintf("%d", container.PIDs), 5, true),
			restartValue(container.Restarts, 4),
			dimStyle.Render(truncate(tail, max(2, width-133))),
		}, " ")
	case width >= 92:
		return strings.Join([]string{
			style.Render(fixedCell(state, 6, false)),
			valueStyle.Render(fixedCell(name, 20, false)),
			percentValue(container.CPU, 7),
			memoryValue(container.MemoryUsed, container.MemoryLimit, 17),
			fixedCell(elapsed(container.Uptime), 8, false),
			restartValue(container.Restarts, 4),
			dimStyle.Render(truncate(tail, max(8, width-68))),
		}, " ")
	case width >= 62:
		return strings.Join([]string{
			style.Render(fixedCell(state, 6, false)),
			valueStyle.Render(fixedCell(name, 18, false)),
			percentValue(container.CPU, 7),
			fixedCell(bytes(container.MemoryUsed), 10, true),
			restartValue(container.Restarts, 4),
		}, " ")
	default:
		return style.Render(fixedCell(state, 6, false)) + " " +
			valueStyle.Render(fixedCell(name, max(8, width-7), false))
	}
}

func renderPM2ProcessRow(process pm2ProcessInfo, width int) string {
	state := strings.ToUpper(process.Status)
	style := processStoppedStyle
	if strings.EqualFold(process.Status, "online") {
		state, style = "ONLINE", processRunningStyle
	} else if strings.EqualFold(process.Status, "launching") {
		style = processWaitingStyle
	}
	name := process.Name
	switch {
	case width >= 86:
		return strings.Join([]string{
			style.Render(fixedCell(state, 8, false)),
			fixedCell(fmt.Sprintf("%d", process.ID), 3, true),
			valueStyle.Render(fixedCell(name, 25, false)),
			fixedCell(fmt.Sprintf("%d", process.PID), 8, true),
			percentValue(process.CPU, 7),
			fixedCell(bytes(process.Memory), 10, true),
			fixedCell(elapsed(process.Uptime), 8, false),
			restartValue(process.Restarts, 4),
			dimStyle.Render(truncate(process.Mode, max(4, width-84))),
		}, " ")
	case width >= 54:
		return strings.Join([]string{
			style.Render(fixedCell(state, 8, false)),
			fixedCell(fmt.Sprintf("%d", process.ID), 3, true),
			valueStyle.Render(fixedCell(name, 22, false)),
			percentValue(process.CPU, 7),
			fixedCell(bytes(process.Memory), 10, true),
			restartValue(process.Restarts, 4),
		}, " ")
	default:
		return style.Render(fixedCell(state, 8, false)) + " " +
			valueStyle.Render(fixedCell(name, max(8, width-9), false))
	}
}

func renderHTTPServiceRow(service serviceHealth, width int) string {
	state, style := "DOWN", processStoppedStyle
	if service.Healthy {
		state, style = "UP", processRunningStyle
	}
	nameWidth := min(32, max(8, width-21))
	return style.Render(fixedCell(state, 5, false)) + " " +
		valueStyle.Render(fixedCell(service.Name, nameWidth, false)) + " " +
		style.Render(fixedCell(service.Detail, 5, true)) + " " +
		dimStyle.Render(fixedCell(service.Latency.Round(time.Millisecond).String(), 7, true))
}

func (m *monitorModel) nasAttentionPanel(width int) string {
	containers := 0
	for _, container := range m.snapshot.Containers {
		if !dockerContainerHealthy(container) {
			containers++
		}
	}
	volumes := 0
	for _, filesystem := range m.snapshot.Filesystems {
		if filesystem.Error != "" || percent(filesystem.Used, filesystem.Total) >= 95 {
			volumes++
		}
	}
	services := 0
	for _, service := range m.snapshot.Services {
		if !service.Healthy {
			services++
		}
	}
	pm2 := 0
	for _, process := range m.snapshot.PM2Processes {
		if !strings.EqualFold(process.Status, "online") {
			pm2++
		}
	}
	networkErrors := uint64(0)
	for _, network := range m.snapshot.NetworkInterfaces {
		networkErrors += network.RXErrors + network.TXErrors + network.RXDrops + network.TXDrops
	}

	var signals []string
	if containers > 0 {
		signals = append(signals, dangerStyle.Render(fmt.Sprintf("%d containers need attention", containers)))
	}
	if volumes > 0 {
		signals = append(signals, dangerStyle.Render(fmt.Sprintf("%d volumes critical", volumes)))
	}
	if services > 0 {
		signals = append(signals, dangerStyle.Render(fmt.Sprintf("%d HTTP checks down", services)))
	}
	if pm2 > 0 {
		signals = append(signals, warningStyle.Render(fmt.Sprintf("%d PM2 processes offline", pm2)))
	}
	if networkErrors > 0 {
		signals = append(signals, warningStyle.Render(fmt.Sprintf("%d network errors/drops", networkErrors)))
	}
	if len(signals) == 0 {
		signals = []string{processRunningStyle.Render("No active service alerts")}
	}
	return btopPanel(width, "ATTENTION", fmt.Sprintf("%d SIGNALS", len(signals)),
		strings.Join(signals, dimStyle.Render("  ·  ")), warningStyle, colorDiskBorder)
}

func dockerTableHeader(width int) string {
	var header string
	switch {
	case width >= 132:
		header = fmt.Sprintf("%-6s %-18s %-16s %7s %17s %21s %21s %-8s %5s %4s %s",
			"STATE", "NAME", "IMAGE", "CPU", "MEMORY", "NET I/O", "BLOCK I/O", "UPTIME", "PIDS", "RST", "PORTS / STATUS")
	case width >= 92:
		header = fmt.Sprintf("%-6s %-20s %7s %17s %-8s %4s %s",
			"STATE", "NAME", "CPU", "MEMORY", "UPTIME", "RST", "PORTS / STATUS")
	case width >= 62:
		header = fmt.Sprintf("%-6s %-18s %7s %10s %4s", "STATE", "NAME", "CPU", "MEMORY", "RST")
	default:
		header = "STATE  NAME"
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func pm2TableHeader(width int) string {
	var header string
	switch {
	case width >= 86:
		header = fmt.Sprintf("%-8s %3s %-25s %8s %7s %10s %-8s %4s %s",
			"STATE", "ID", "NAME", "PID", "CPU", "MEMORY", "UPTIME", "RST", "MODE")
	case width >= 54:
		header = fmt.Sprintf("%-8s %3s %-22s %7s %10s %4s", "STATE", "ID", "NAME", "CPU", "MEMORY", "RST")
	default:
		header = "STATE    NAME"
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func httpTableHeader(width, columns int) string {
	if columns == 2 {
		cellWidth := max(12, (width-1)/2)
		header := fixedCell("STATE  ENDPOINT", cellWidth, false) + " " + "STATE  ENDPOINT"
		return dimStyle.Copy().Bold(true).Render(truncate(header, width))
	}
	return dimStyle.Copy().Bold(true).Render(truncate("STATE ENDPOINT  CODE LATENCY", width))
}

func fixedCell(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "…")
	padding := max(0, width-lipgloss.Width(value))
	if right {
		return strings.Repeat(" ", padding) + value
	}
	return value + strings.Repeat(" ", padding)
}

func percentValue(value float64, width int) string {
	style := dimStyle
	switch {
	case value >= 85:
		style = dangerStyle
	case value >= 55:
		style = warningStyle
	case value >= 5:
		style = processRunningStyle
	}
	return style.Render(fixedCell(fmt.Sprintf("%.1f%%", value), width, true))
}

func memoryValue(used, total uint64, width int) string {
	style := dimStyle
	usage := percent(used, total)
	switch {
	case usage >= 90:
		style = dangerStyle
	case usage >= 75:
		style = warningStyle
	}
	return style.Render(fixedCell(bytes(used)+"/"+bytes(total), width, true))
}

func restartValue(restarts int, width int) string {
	style := dimStyle
	if restarts > 0 {
		style = warningStyle
	}
	if restarts >= 5 {
		style = dangerStyle
	}
	return style.Render(fixedCell(fmt.Sprintf("%d", restarts), width, true))
}

func healthCount(healthy, total int) string {
	label := fmt.Sprintf("%d/%d", healthy, total)
	if total == 0 {
		return dimStyle.Render("not configured")
	}
	if healthy == total {
		return processRunningStyle.Render(label + " healthy")
	}
	return dangerStyle.Render(label + " healthy")
}
