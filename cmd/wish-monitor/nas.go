package main

import (
	"fmt"
	"strings"
	"time"

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
	filesystemCount := max(1, len(m.snapshot.Filesystems))
	storageRows := min(filesystemCount, max(1, available-2))
	serviceRows := 0
	if desired := m.nasServiceContentRows(); desired > 0 && available >= 7 {
		maxServiceRows := available - storageRows - 4
		if maxServiceRows < 2 {
			storageRows = max(1, available-6)
			maxServiceRows = available - storageRows - 4
		}
		serviceRows = min(desired, max(2, maxServiceRows))
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
	healthyContainers := 0
	for _, container := range m.snapshot.Containers {
		if dockerContainerHealthy(container) {
			healthyContainers++
		}
	}
	containerHealth := healthCount(healthyContainers, len(m.snapshot.Containers))
	if m.snapshot.DockerError != "" {
		containerHealth = dangerStyle.Render("unavailable")
	}
	healthyPM2 := 0
	for _, process := range m.snapshot.PM2Processes {
		if strings.EqualFold(process.Status, "online") {
			healthyPM2++
		}
	}
	pm2Health := healthCount(healthyPM2, len(m.snapshot.PM2Processes))
	if m.snapshot.PM2Error != "" {
		pm2Health = dangerStyle.Render("unavailable")
	}
	lines := []string{
		fmt.Sprintf("%s %s   %s %s   %s %s",
			processTitleStyle.Render("HTTP"),
			healthCount(healthyHTTP, len(m.snapshot.Services)),
			accentStyle.Render("DOCKER"),
			containerHealth,
			gpuTitleStyle.Render("PM2"),
			pm2Health),
	}

	type serviceSection struct {
		header string
		rows   []string
	}
	var sections []serviceSection
	if len(m.snapshot.Containers) > 0 || m.snapshot.DockerError != "" {
		dockerRows := make([]string, 0, len(m.snapshot.Containers))
		for _, container := range m.snapshot.Containers {
			dockerRows = append(dockerRows, renderDockerContainerRow(container, width-4))
		}
		if m.snapshot.DockerError != "" {
			dockerRows = []string{dangerStyle.Render("Docker unavailable: " + m.snapshot.DockerError)}
		}
		sections = append(sections, serviceSection{
			header: accentStyle.Render("DOCKER") + dimStyle.Render("  NAME · IMAGE · CPU · MEMORY · NET I/O · PIDS · RESTARTS · PORTS"),
			rows:   dockerRows,
		})
	}
	if len(m.snapshot.PM2Processes) > 0 || m.snapshot.PM2Error != "" {
		pm2Rows := make([]string, 0, len(m.snapshot.PM2Processes))
		for _, process := range m.snapshot.PM2Processes {
			pm2Rows = append(pm2Rows, renderPM2ProcessRow(process, width-4))
		}
		if m.snapshot.PM2Error != "" {
			pm2Rows = []string{dangerStyle.Render("PM2 unavailable: " + m.snapshot.PM2Error)}
		}
		sections = append(sections, serviceSection{
			header: gpuTitleStyle.Render("PM2") + dimStyle.Render("  ID · NAME · PID · CPU · MEMORY · UPTIME · RESTARTS · MODE"),
			rows:   pm2Rows,
		})
	}
	if len(m.snapshot.Services) > 0 {
		httpRows := make([]string, 0, len(m.snapshot.Services))
		for _, service := range m.snapshot.Services {
			httpRows = append(httpRows, renderHTTPServiceRow(service, width-4))
		}
		sections = append(sections, serviceSection{
			header: processTitleStyle.Render("HTTP") + dimStyle.Render("  ENDPOINT · RESPONSE · LATENCY"),
			rows:   httpRows,
		})
	}

	rows = max(1, rows)
	remaining := rows - len(lines)
	included := make([]bool, len(sections))
	allocated := make([]int, len(sections))
	for index, section := range sections {
		cost := 1
		if len(section.rows) > 0 {
			cost++
		}
		if remaining < cost {
			continue
		}
		included[index] = true
		allocated[index] = min(1, len(section.rows))
		remaining -= cost
	}
	for remaining > 0 {
		progress := false
		for index, section := range sections {
			if !included[index] || allocated[index] >= len(section.rows) {
				continue
			}
			allocated[index]++
			remaining--
			progress = true
			if remaining == 0 {
				break
			}
		}
		if !progress {
			break
		}
	}
	for index, section := range sections {
		if !included[index] {
			continue
		}
		lines = append(lines, section.header)
		lines = append(lines, section.rows[:allocated[index]]...)
	}
	return btopPanel(width, "SERVICES", "HEALTH", strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
}

func (m *monitorModel) nasServiceContentRows() int {
	rows := 1
	if len(m.snapshot.Containers) > 0 || m.snapshot.DockerError != "" {
		rows += 1 + max(1, len(m.snapshot.Containers))
	}
	if len(m.snapshot.PM2Processes) > 0 || m.snapshot.PM2Error != "" {
		rows += 1 + max(1, len(m.snapshot.PM2Processes))
	}
	if len(m.snapshot.Services) > 0 {
		rows += 1 + len(m.snapshot.Services)
	}
	return rows
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
	image := truncate(container.Image, 18)
	name := truncate(container.Name, 20)
	ports := container.Ports
	if ports == "" {
		ports = "no ports"
	}
	tail := ports
	if !container.Running && container.Status != "" {
		tail = container.Status
	}
	var line string
	switch {
	case width >= 132:
		line = fmt.Sprintf("%-5s %-18s %-16s CPU %5.1f%%  MEM %s/%s  NET ↓%s ↑%s  BLK ↓%s ↑%s  UP %-7s  PID %d  RST %d  %s",
			state, truncate(name, 18), truncate(image, 16), container.CPU,
			bytes(container.MemoryUsed), bytes(container.MemoryLimit),
			bytes(container.NetworkRX), bytes(container.NetworkTX),
			bytes(container.BlockRead), bytes(container.BlockWrite),
			elapsed(container.Uptime), container.PIDs, container.Restarts, tail)
	case width >= 92:
		line = fmt.Sprintf("%-5s %-18s CPU %5.1f%%  MEM %s/%s  UP %-7s  RST %2d  %s",
			state, name, container.CPU, bytes(container.MemoryUsed), bytes(container.MemoryLimit),
			elapsed(container.Uptime), container.Restarts, tail)
	case width >= 62:
		line = fmt.Sprintf("%-5s %-18s CPU %5.1f%%  MEM %-9s  RST %d",
			state, name, container.CPU, bytes(container.MemoryUsed), container.Restarts)
	default:
		line = fmt.Sprintf("%-5s %s · %s", state, name, tail)
	}
	return style.Render(truncate(line, width))
}

func renderPM2ProcessRow(process pm2ProcessInfo, width int) string {
	state := strings.ToUpper(process.Status)
	style := processStoppedStyle
	if strings.EqualFold(process.Status, "online") {
		state, style = "ONLINE", processRunningStyle
	} else if strings.EqualFold(process.Status, "launching") {
		style = processWaitingStyle
	}
	name := truncate(process.Name, 26)
	var line string
	switch {
	case width >= 112:
		line = fmt.Sprintf("%-7s %2d  %-26s PID %-7d CPU %5.1f%%  MEM %-9s  UP %-8s  RST %2d  %s",
			state, process.ID, name, process.PID, process.CPU, bytes(process.Memory),
			elapsed(process.Uptime), process.Restarts, process.Mode)
	case width >= 76:
		line = fmt.Sprintf("%-7s %2d  %-22s CPU %5.1f%%  MEM %-9s  UP %-8s  RST %d",
			state, process.ID, name, process.CPU, bytes(process.Memory), elapsed(process.Uptime), process.Restarts)
	default:
		line = fmt.Sprintf("%-7s %2d  %s · %s · RST %d",
			state, process.ID, name, bytes(process.Memory), process.Restarts)
	}
	return style.Render(truncate(line, width))
}

func renderHTTPServiceRow(service serviceHealth, width int) string {
	state, style := "DOWN", processStoppedStyle
	if service.Healthy {
		state, style = "UP", processRunningStyle
	}
	line := fmt.Sprintf("%-5s %-28s HTTP %-4s  %s",
		state, truncate(service.Name, 28), service.Detail, service.Latency.Round(time.Millisecond))
	return style.Render(truncate(line, width))
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
