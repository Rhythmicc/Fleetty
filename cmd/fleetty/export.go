package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func runSnapshotCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "optional machine configuration")
	processes := flags.Bool("processes", false, "include the process table")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("snapshot does not accept positional arguments")
	}
	machine, err := loadExportMachineConfig(*configPath)
	if err != nil {
		return err
	}
	snapshot, err := newSnapshotCache(newMetricsCollector(machine)).Get(*processes)
	export := exportSnapshot(snapshot)
	if err != nil {
		export.Warning = err.Error()
	}
	return json.NewEncoder(stdout).Encode(export)
}

func runMetricsCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("metrics", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "optional machine configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("metrics does not accept positional arguments")
	}
	machine, err := loadExportMachineConfig(*configPath)
	if err != nil {
		return err
	}
	snapshot, err := newSnapshotCache(newMetricsCollector(machine)).Get(false)
	_, writeErr := io.WriteString(stdout, renderPrometheusMetrics(snapshot))
	if writeErr != nil {
		return writeErr
	}
	if err != nil {
		_, writeErr = fmt.Fprintf(
			stdout, "# fleetty warning: %s\n", prometheusEscape(err.Error()),
		)
	}
	return writeErr
}

func loadExportMachineConfig(configPath string) (machineConfig, error) {
	machine := machineConfig{Profile: machineProfileGPU}
	if runtime.GOOS == "darwin" {
		machine.Profile = machineProfileGeneral
	}
	configPath = strings.TrimSpace(configPath)
	if configPath != "" {
		loaded, err := loadMachineConfig(configPath)
		if err != nil && !os.IsNotExist(err) {
			return machineConfig{}, err
		}
		if err == nil {
			machine = loaded
		}
	}
	if machine.Name == "" {
		machine.Name, _ = os.Hostname()
		machine.Name = sanitizeTerminalText(machine.Name)
	}
	return machine, nil
}

type snapshotExport struct {
	CollectedAt         time.Time          `json:"collected_at"`
	NodeName            string             `json:"node_name,omitempty"`
	Profile             string             `json:"profile,omitempty"`
	OSName              string             `json:"os_name,omitempty"`
	CPUModel            string             `json:"cpu_model,omitempty"`
	CPUCores            int                `json:"cpu_cores,omitempty"`
	Uptime              uint64             `json:"uptime_seconds"`
	CPUPercent          float64            `json:"cpu_percent"`
	LoadAverage         string             `json:"load_average,omitempty"`
	MemoryUsed          uint64             `json:"memory_used_bytes"`
	MemoryTotal         uint64             `json:"memory_total_bytes"`
	DiskUsed            uint64             `json:"disk_used_bytes"`
	DiskTotal           uint64             `json:"disk_total_bytes"`
	NetworkRX           uint64             `json:"network_rx_bytes_per_second"`
	NetworkTX           uint64             `json:"network_tx_bytes_per_second"`
	NetworkRXTotal      uint64             `json:"network_rx_total_bytes"`
	NetworkTXTotal      uint64             `json:"network_tx_total_bytes"`
	NetworkProcessError string             `json:"network_process_error,omitempty"`
	Battery             *batteryExport     `json:"battery,omitempty"`
	Filesystems         []filesystemExport `json:"filesystems,omitempty"`
	Services            []serviceExport    `json:"services,omitempty"`
	Containers          []containerExport  `json:"containers,omitempty"`
	PM2Processes        []pm2Export        `json:"pm2_processes,omitempty"`
	GPUs                []gpuExport        `json:"gpus,omitempty"`
	Processes           []processExport    `json:"processes,omitempty"`
	Warning             string             `json:"warning,omitempty"`
}

type batteryExport struct {
	Percent       float64 `json:"percent"`
	Status        string  `json:"status,omitempty"`
	TimeRemaining string  `json:"time_remaining,omitempty"`
	PowerSource   string  `json:"power_source,omitempty"`
}

type filesystemExport struct {
	Mount  string `json:"mount"`
	Device string `json:"device,omitempty"`
	Used   uint64 `json:"used_bytes"`
	Total  uint64 `json:"total_bytes"`
	Error  string `json:"error,omitempty"`
}

type serviceExport struct {
	Name                string `json:"name"`
	Kind                string `json:"kind,omitempty"`
	Detail              string `json:"detail,omitempty"`
	Healthy             bool   `json:"healthy"`
	LatencyMilliseconds int64  `json:"latency_ms,omitempty"`
}

type containerExport struct {
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name"`
	Image       string  `json:"image,omitempty"`
	State       string  `json:"state,omitempty"`
	Status      string  `json:"status,omitempty"`
	Health      string  `json:"health,omitempty"`
	Ports       string  `json:"ports,omitempty"`
	Running     bool    `json:"running"`
	CPU         float64 `json:"cpu_percent"`
	MemoryUsed  uint64  `json:"memory_used_bytes"`
	MemoryLimit uint64  `json:"memory_limit_bytes"`
	NetworkRX   uint64  `json:"network_rx_bytes"`
	NetworkTX   uint64  `json:"network_tx_bytes"`
	BlockRead   uint64  `json:"block_read_bytes"`
	BlockWrite  uint64  `json:"block_write_bytes"`
	PIDs        int     `json:"pids,omitempty"`
	Restarts    int     `json:"restarts,omitempty"`
	Uptime      uint64  `json:"uptime_seconds"`
}

type pm2Export struct {
	ID               int     `json:"id,omitempty"`
	PID              int     `json:"pid,omitempty"`
	Name             string  `json:"name"`
	Namespace        string  `json:"namespace,omitempty"`
	Status           string  `json:"status,omitempty"`
	Mode             string  `json:"mode,omitempty"`
	CPU              float64 `json:"cpu_percent"`
	Memory           uint64  `json:"memory_bytes"`
	Uptime           uint64  `json:"uptime_seconds"`
	Restarts         int     `json:"restarts,omitempty"`
	UnstableRestarts int     `json:"unstable_restarts,omitempty"`
}

type gpuExport struct {
	Index               int                 `json:"index"`
	UUID                string              `json:"uuid,omitempty"`
	Name                string              `json:"name,omitempty"`
	DriverVersion       string              `json:"driver_version,omitempty"`
	Utilization         float64             `json:"utilization_percent"`
	RendererUtilization float64             `json:"renderer_utilization_percent,omitempty"`
	TilerUtilization    float64             `json:"tiler_utilization_percent,omitempty"`
	MemoryUsed          uint64              `json:"memory_used_bytes"`
	MemoryTotal         uint64              `json:"memory_total_bytes"`
	CoreCount           int                 `json:"core_count,omitempty"`
	Temperature         int                 `json:"temperature_celsius"`
	ClockMHz            int                 `json:"clock_mhz"`
	Power               float64             `json:"power_watts"`
	PowerLimit          float64             `json:"power_limit_watts,omitempty"`
	Workloads           []gpuWorkloadExport `json:"workloads,omitempty"`
}

type gpuWorkloadExport struct {
	PID        int    `json:"pid"`
	User       string `json:"user,omitempty"`
	Name       string `json:"name,omitempty"`
	MemoryUsed uint64 `json:"memory_used_bytes"`
}

type processExport struct {
	PID     int     `json:"pid"`
	User    string  `json:"user,omitempty"`
	State   string  `json:"state,omitempty"`
	Command string  `json:"command,omitempty"`
	CPU     float64 `json:"cpu_percent"`
	Memory  float64 `json:"memory_percent"`
	RSS     uint64  `json:"rss_bytes"`
	Elapsed uint64  `json:"elapsed_seconds"`
}

func exportSnapshot(snapshot monitorSnapshot) snapshotExport {
	export := snapshotExport{
		CollectedAt:         snapshot.CollectedAt,
		NodeName:            snapshot.NodeName,
		Profile:             snapshot.Profile,
		OSName:              snapshot.OSName,
		CPUModel:            snapshot.CPUModel,
		CPUCores:            snapshot.CPUCores,
		Uptime:              snapshot.Uptime,
		CPUPercent:          snapshot.CPUPercent,
		LoadAverage:         snapshot.LoadAverage,
		MemoryUsed:          snapshot.MemoryUsed,
		MemoryTotal:         snapshot.MemoryTotal,
		DiskUsed:            snapshot.DiskUsed,
		DiskTotal:           snapshot.DiskTotal,
		NetworkRX:           snapshot.NetworkRX,
		NetworkTX:           snapshot.NetworkTX,
		NetworkRXTotal:      snapshot.NetworkRXTotal,
		NetworkTXTotal:      snapshot.NetworkTXTotal,
		NetworkProcessError: snapshot.NetworkProcessError,
	}
	if snapshot.Battery != nil {
		export.Battery = &batteryExport{
			Percent: snapshot.Battery.Percent, Status: snapshot.Battery.Status,
			TimeRemaining: snapshot.Battery.TimeRemaining,
			PowerSource:   snapshot.Battery.PowerSource,
		}
	}
	for _, filesystem := range snapshot.Filesystems {
		export.Filesystems = append(export.Filesystems, filesystemExport{
			Mount: filesystem.Mount, Device: filesystem.Device,
			Used: filesystem.Used, Total: filesystem.Total, Error: filesystem.Error,
		})
	}
	for _, service := range snapshot.Services {
		export.Services = append(export.Services, serviceExport{
			Name: service.Name, Kind: service.Kind, Detail: service.Detail,
			Healthy: service.Healthy, LatencyMilliseconds: service.Latency.Milliseconds(),
		})
	}
	for _, container := range snapshot.Containers {
		export.Containers = append(export.Containers, containerExport{
			ID: container.ID, Name: container.Name, Image: container.Image,
			State: container.State, Status: container.Status, Health: container.Health,
			Ports: container.Ports, Running: container.Running, CPU: container.CPU,
			MemoryUsed: container.MemoryUsed, MemoryLimit: container.MemoryLimit,
			NetworkRX: container.NetworkRX, NetworkTX: container.NetworkTX,
			BlockRead: container.BlockRead, BlockWrite: container.BlockWrite,
			PIDs: container.PIDs, Restarts: container.Restarts, Uptime: container.Uptime,
		})
	}
	for _, process := range snapshot.PM2Processes {
		export.PM2Processes = append(export.PM2Processes, pm2Export{
			ID: process.ID, PID: process.PID, Name: process.Name,
			Namespace: process.Namespace, Status: process.Status, Mode: process.Mode,
			CPU: process.CPU, Memory: process.Memory, Uptime: process.Uptime,
			Restarts: process.Restarts, UnstableRestarts: process.UnstableRestarts,
		})
	}
	for _, gpu := range snapshot.GPUs {
		item := gpuExport{
			Index: gpu.Index, UUID: gpu.UUID, Name: gpu.Name,
			DriverVersion: gpu.DriverVersion, Utilization: gpu.Utilization,
			RendererUtilization: gpu.RendererUtilization,
			TilerUtilization:    gpu.TilerUtilization,
			MemoryUsed:          gpu.MemoryUsed, MemoryTotal: gpu.MemoryTotal,
			CoreCount: gpu.CoreCount, Temperature: gpu.Temperature,
			ClockMHz: gpu.ClockMHz, Power: gpu.Power, PowerLimit: gpu.PowerLimit,
		}
		for _, workload := range gpu.Workloads {
			item.Workloads = append(item.Workloads, gpuWorkloadExport{
				PID: workload.PID, User: workload.User, Name: workload.Name,
				MemoryUsed: workload.MemoryUsed,
			})
		}
		export.GPUs = append(export.GPUs, item)
	}
	for _, process := range snapshot.Processes {
		export.Processes = append(export.Processes, processExport{
			PID: process.PID, User: process.User, State: process.State,
			Command: process.Command, CPU: process.CPU, Memory: process.Memory,
			RSS: process.RSS, Elapsed: process.Elapsed,
		})
	}
	return export
}

func renderPrometheusMetrics(snapshot monitorSnapshot) string {
	node := prometheusLabelValue(snapshot.NodeName)
	labels := fmt.Sprintf(`node="%s"`, node)
	var builder strings.Builder
	writePrometheusGauge(&builder, "fleetty_uptime_seconds", "Node uptime in seconds.", labels, strconv.FormatUint(snapshot.Uptime, 10))
	writePrometheusGauge(&builder, "fleetty_cpu_percent", "CPU utilization percentage.", labels, strconv.FormatFloat(snapshot.CPUPercent, 'g', -1, 64))
	writePrometheusGauge(&builder, "fleetty_memory_used_bytes", "Used physical memory in bytes.", labels, strconv.FormatUint(snapshot.MemoryUsed, 10))
	writePrometheusGauge(&builder, "fleetty_memory_total_bytes", "Total physical memory in bytes.", labels, strconv.FormatUint(snapshot.MemoryTotal, 10))
	writePrometheusGauge(&builder, "fleetty_disk_used_bytes", "Used root filesystem bytes.", labels, strconv.FormatUint(snapshot.DiskUsed, 10))
	writePrometheusGauge(&builder, "fleetty_disk_total_bytes", "Total root filesystem bytes.", labels, strconv.FormatUint(snapshot.DiskTotal, 10))
	writePrometheusCounter(&builder, "fleetty_network_rx_total_bytes", "Cumulative received bytes.", labels, strconv.FormatUint(snapshot.NetworkRXTotal, 10))
	writePrometheusCounter(&builder, "fleetty_network_tx_total_bytes", "Cumulative transmitted bytes.", labels, strconv.FormatUint(snapshot.NetworkTXTotal, 10))
	writePrometheusGauge(&builder, "fleetty_network_rx_bytes_per_second", "Current receive rate in bytes per second.", labels, strconv.FormatUint(snapshot.NetworkRX, 10))
	writePrometheusGauge(&builder, "fleetty_network_tx_bytes_per_second", "Current transmit rate in bytes per second.", labels, strconv.FormatUint(snapshot.NetworkTX, 10))
	for _, gpu := range snapshot.GPUs {
		gpuLabels := fmt.Sprintf(`node="%s",gpu="%d"`, node, gpu.Index)
		if gpu.Name != "" {
			gpuLabels += fmt.Sprintf(`,name="%s"`, prometheusLabelValue(gpu.Name))
		}
		writePrometheusGauge(&builder, "fleetty_gpu_utilization_percent", "GPU utilization percentage.", gpuLabels, strconv.FormatFloat(gpu.Utilization, 'g', -1, 64))
		writePrometheusGauge(&builder, "fleetty_gpu_memory_used_bytes", "GPU memory used in bytes.", gpuLabels, strconv.FormatUint(gpu.MemoryUsed, 10))
		writePrometheusGauge(&builder, "fleetty_gpu_memory_total_bytes", "GPU total memory in bytes.", gpuLabels, strconv.FormatUint(gpu.MemoryTotal, 10))
		writePrometheusGauge(&builder, "fleetty_gpu_temperature_celsius", "GPU temperature in degrees Celsius.", gpuLabels, strconv.Itoa(gpu.Temperature))
		writePrometheusGauge(&builder, "fleetty_gpu_clock_mhz", "GPU graphics clock in MHz.", gpuLabels, strconv.Itoa(gpu.ClockMHz))
		writePrometheusGauge(&builder, "fleetty_gpu_power_watts", "GPU power draw in watts.", gpuLabels, strconv.FormatFloat(gpu.Power, 'g', -1, 64))
		writePrometheusGauge(&builder, "fleetty_gpu_power_limit_watts", "GPU power limit in watts.", gpuLabels, strconv.FormatFloat(gpu.PowerLimit, 'g', -1, 64))
	}
	for _, filesystem := range snapshot.Filesystems {
		fsLabels := fmt.Sprintf(`node="%s",mount="%s"`, node, prometheusLabelValue(filesystem.Mount))
		writePrometheusGauge(&builder, "fleetty_filesystem_used_bytes", "Filesystem used bytes.", fsLabels, strconv.FormatUint(filesystem.Used, 10))
		writePrometheusGauge(&builder, "fleetty_filesystem_total_bytes", "Filesystem total bytes.", fsLabels, strconv.FormatUint(filesystem.Total, 10))
	}
	for _, service := range snapshot.Services {
		serviceLabels := fmt.Sprintf(
			`node="%s",service="%s",kind="%s"`,
			node, prometheusLabelValue(service.Name), prometheusLabelValue(service.Kind),
		)
		healthy := "0"
		if service.Healthy {
			healthy = "1"
		}
		writePrometheusGauge(&builder, "fleetty_service_healthy", "Service health check result (1 healthy, 0 unhealthy).", serviceLabels, healthy)
	}
	writePrometheusGauge(&builder, "fleetty_containers_total", "Number of discovered Docker containers.", labels, strconv.Itoa(len(snapshot.Containers)))
	runningContainers := 0
	for _, container := range snapshot.Containers {
		if container.Running {
			runningContainers++
		}
	}
	writePrometheusGauge(&builder, "fleetty_containers_running", "Number of running Docker containers.", labels, strconv.Itoa(runningContainers))
	writePrometheusGauge(&builder, "fleetty_pm2_processes_total", "Number of discovered PM2 processes.", labels, strconv.Itoa(len(snapshot.PM2Processes)))
	runningPM2 := 0
	for _, process := range snapshot.PM2Processes {
		if process.Status == "online" {
			runningPM2++
		}
	}
	writePrometheusGauge(&builder, "fleetty_pm2_running", "Number of online PM2 processes.", labels, strconv.Itoa(runningPM2))
	return builder.String()
}

func writePrometheusGauge(builder *strings.Builder, name, help, labels, value string) {
	writePrometheusMetric(builder, name, help, "gauge", labels, value)
}

func writePrometheusCounter(builder *strings.Builder, name, help, labels, value string) {
	writePrometheusMetric(builder, name, help, "counter", labels, value)
}

func writePrometheusMetric(builder *strings.Builder, name, help, metricType, labels, value string) {
	fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	fmt.Fprintf(builder, "# TYPE %s %s\n", name, metricType)
	fmt.Fprintf(builder, "%s{%s} %s\n", name, labels, value)
}

func prometheusEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func prometheusLabelValue(value string) string {
	return prometheusEscape(value)
}
