package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
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
	header := dashboardHeaderNamed("NAS MONITOR", width, m.snapshot.CollectedAt, m.colorMode, nodeName)
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
	serviceRows := 0
	if available >= 7 {
		serviceRows = min(2, available-5)
	}
	serviceHeight := 0
	if serviceRows > 0 {
		serviceHeight = serviceRows + 2
	}
	storageRows := max(1, available-serviceHeight-2)
	if len(m.snapshot.Filesystems) > 0 {
		storageRows = min(storageRows, len(m.snapshot.Filesystems))
	}

	sections := []string{
		header,
		system,
		network,
		m.nasStoragePanel(width, storageRows),
	}
	if serviceRows > 0 {
		sections = append(sections, m.nasServicesPanel(width, serviceRows))
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
			line += "  " + bar(usage, min(24, width-lipgloss.Width(line)-6))
		}
		lines = append(lines, line)
	}
	meta := fmt.Sprintf("%d MOUNTS", len(filesystems))
	if rows < len(filesystems) {
		meta = fmt.Sprintf("%d/%d SHOWN", rows, len(filesystems))
	}
	return btopPanel(width, "STORAGE", meta, strings.Join(lines, "\n"), diskTitleStyle, colorDiskBorder)
}

func (m *monitorModel) nasServicesPanel(width, rows int) string {
	healthyHTTP := 0
	for _, service := range m.snapshot.Services {
		if service.Healthy {
			healthyHTTP++
		}
	}
	runningContainers := 0
	for _, container := range m.snapshot.Containers {
		if container.Running {
			runningContainers++
		}
	}
	containerHealth := healthCount(runningContainers, len(m.snapshot.Containers))
	if m.snapshot.DockerError != "" {
		containerHealth = dangerStyle.Render("unavailable")
	}
	lines := []string{
		fmt.Sprintf("%s %s   %s %s",
			processTitleStyle.Render("HTTP"),
			healthCount(healthyHTTP, len(m.snapshot.Services)),
			accentStyle.Render("DOCKER"),
			containerHealth),
	}
	var problems []string
	for _, service := range m.snapshot.Services {
		if !service.Healthy {
			problems = append(problems, service.Name+" "+service.Detail)
		}
	}
	for _, container := range m.snapshot.Containers {
		if !container.Running {
			problems = append(problems, container.Name+" "+container.State)
		}
	}
	if m.snapshot.DockerError != "" {
		problems = append(problems, "docker "+m.snapshot.DockerError)
	}
	if len(problems) == 0 {
		lines = append(lines, processRunningStyle.Render("● All configured services are healthy"))
	} else {
		lines = append(lines, dangerStyle.Render("● "+strings.Join(problems, " · ")))
	}
	rows = max(1, rows)
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return btopPanel(width, "SERVICES", "HEALTH", strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
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
