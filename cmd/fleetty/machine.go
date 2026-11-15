package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	machineProfileGPU     = "gpu"
	machineProfileCPU     = "cpu"
	machineProfileNAS     = "nas"
	machineProfileGeneral = "general"

	serviceRefreshInterval = 5 * time.Second
	serviceCheckTimeout    = 700 * time.Millisecond
)

var (
	errPM2DaemonNotFound = errors.New("running PM2 daemon not found")

	serviceHTTPClient = &http.Client{
		Timeout: serviceCheckTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			MaxIdleConns:      16,
			IdleConnTimeout:   30 * time.Second,
			DisableKeepAlives: false,
		},
	}

	dockerSocketDialer     = &net.Dialer{Timeout: serviceCheckTimeout}
	dockerSocketHTTPClient = &http.Client{
		Timeout: serviceCheckTimeout,
		Transport: &http.Transport{
			MaxIdleConns:      4,
			IdleConnTimeout:   30 * time.Second,
			DisableKeepAlives: false,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dockerSocketDialer.DialContext(ctx, "unix", "/var/run/docker.sock")
			},
		},
	}
)

type machineConfig struct {
	Name              string            `json:"name,omitempty"`
	Profile           string            `json:"profile,omitempty"`
	NetworkInterfaces []string          `json:"network_interfaces,omitempty"`
	Mounts            []string          `json:"mounts,omitempty"`
	Docker            bool              `json:"docker,omitempty"`
	PM2User           string            `json:"pm2_user,omitempty"`
	HTTPChecks        []httpCheckConfig `json:"http_checks,omitempty"`
}

type httpCheckConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func loadMachineConfig(path string) (machineConfig, error) {
	config := machineConfig{Profile: machineProfileGPU}
	path = strings.TrimSpace(path)
	if path == "" {
		return config, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("parse %s: %w", path, err)
	}
	config.Name = sanitizeTerminalText(config.Name)
	config.Profile = normalizeMachineProfile(config.Profile)
	if config.Profile == "" {
		return config, errors.New("machine profile must be gpu, cpu, nas, or general")
	}
	config.NetworkInterfaces = cleanUniqueValues(config.NetworkInterfaces, false)
	config.Mounts = cleanUniqueValues(config.Mounts, true)
	config.PM2User = sanitizeTerminalText(config.PM2User)
	if strings.IndexFunc(config.PM2User, func(r rune) bool {
		return !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) >= 0 {
		return config, errors.New("pm2_user contains invalid characters")
	}
	for index := range config.HTTPChecks {
		check := &config.HTTPChecks[index]
		check.Name = sanitizeTerminalText(check.Name)
		check.URL = strings.TrimSpace(check.URL)
		parsed, parseErr := url.Parse(check.URL)
		if check.Name == "" || parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return config, fmt.Errorf("invalid http_checks entry %d", index+1)
		}
	}
	return config, nil
}

func cleanUniqueValues(values []string, requireAbsolute bool) []string {
	seen := make(map[string]struct{}, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = sanitizeTerminalText(value)
		if value == "" || requireAbsolute && !strings.HasPrefix(value, "/") {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func normalizeMachineProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", machineProfileGPU:
		return machineProfileGPU
	case machineProfileCPU:
		return machineProfileCPU
	case machineProfileNAS, "storage":
		return machineProfileNAS
	case machineProfileGeneral, "server":
		return machineProfileGeneral
	default:
		return ""
	}
}

type networkInterfaceInfo struct {
	Name               string
	RX, TX             uint64
	RXTotal, TXTotal   uint64
	RXErrors, TXErrors uint64
	RXDrops, TXDrops   uint64
	// SpeedMbps is the NIC link speed. Zero means the host did not report it.
	SpeedMbps uint64
}

type filesystemInfo struct {
	Mount, Device string
	Used, Total   uint64
	Error         string
}

type serviceHealth struct {
	Name, Kind, Detail string
	Healthy            bool
	Latency            time.Duration
}

type containerInfo struct {
	ID, Name, Image, State, Status, Health, Ports string
	Running                                       bool
	CPU                                           float64
	MemoryUsed, MemoryLimit                       uint64
	NetworkRX, NetworkTX                          uint64
	BlockRead, BlockWrite                         uint64
	PIDs, Restarts                                int
	Uptime                                        uint64
}

type pm2ProcessInfo struct {
	ID, PID, Restarts, UnstableRestarts int
	Name, Namespace, Status, Mode       string
	CPU                                 float64
	Memory, Uptime                      uint64
}

type networkDeviceCounters struct {
	rx, tx             uint64
	rxErrors, txErrors uint64
	rxDrops, txDrops   uint64
	speedMbps          uint64
}

type processNetworkCounters struct {
	name   string
	rx, tx uint64
}

func (c *metricsCollector) collectProcessNetwork(snapshot *monitorSnapshot) {
	if runtime.GOOS == "linux" {
		c.collectLinuxNetworkConnections(snapshot)
		return
	}
	if runtime.GOOS != "darwin" {
		snapshot.NetworkProcessError = "Per-process traffic attribution is not available from the host OS."
		return
	}
	if !c.lastProcessNetAt.IsZero() &&
		time.Since(c.lastProcessNetAt) < c.processNetRefreshInterval {
		snapshot.NetworkProcesses = append(
			[]processNetworkInfo(nil), c.cachedNetworkProcesses...,
		)
		return
	}
	current, err := readDarwinProcessNetwork()
	if err != nil {
		snapshot.NetworkProcessError = err.Error()
		return
	}
	now := time.Now()
	seconds := now.Sub(c.lastProcessNetAt).Seconds()
	processes := make([]processNetworkInfo, 0, len(current))
	for pid, counters := range current {
		info := processNetworkInfo{
			PID: pid, Name: counters.name,
			RXTotal: counters.rx, TXTotal: counters.tx,
		}
		if previous, ok := c.previousProcessNet[pid]; ok &&
			previous.name == counters.name && !c.lastProcessNetAt.IsZero() && seconds > 0 {
			info.RX = uint64(float64(counterDelta(counters.rx, previous.rx)) / seconds)
			info.TX = uint64(float64(counterDelta(counters.tx, previous.tx)) / seconds)
		}
		if info.RX+info.TX > 0 || info.RXTotal+info.TXTotal > 0 {
			processes = append(processes, info)
		}
	}
	sort.SliceStable(processes, func(i, j int) bool {
		firstRate := processes[i].RX + processes[i].TX
		secondRate := processes[j].RX + processes[j].TX
		if firstRate != secondRate {
			return firstRate > secondRate
		}
		firstTotal := processes[i].RXTotal + processes[i].TXTotal
		secondTotal := processes[j].RXTotal + processes[j].TXTotal
		if firstTotal != secondTotal {
			return firstTotal > secondTotal
		}
		return processes[i].PID < processes[j].PID
	})
	const snapshotLimit = 12
	if len(processes) > snapshotLimit {
		processes = processes[:snapshotLimit]
	}
	snapshot.NetworkProcesses = processes
	c.cachedNetworkProcesses = append([]processNetworkInfo(nil), processes...)
	c.previousProcessNet = current
	c.lastProcessNetAt = now
}

func (c *metricsCollector) collectNetwork(snapshot *monitorSnapshot) error {
	devices, err := readNetworkDevices()
	if err != nil {
		return err
	}
	selected := c.config.NetworkInterfaces
	if len(selected) == 0 && c.config.Profile == machineProfileNAS {
		if defaultInterface := readDefaultNetworkInterface(); defaultInterface != "" {
			selected = []string{defaultInterface}
		}
	}

	aggregateNames := selected
	if len(aggregateNames) == 0 {
		for name := range devices {
			if name != "lo" {
				aggregateNames = append(aggregateNames, name)
			}
		}
	}
	now := time.Now()
	seconds := now.Sub(c.lastNetAt).Seconds()
	var aggregate netCounters
	var missing []string
	for _, name := range aggregateNames {
		counters, exists := devices[name]
		if !exists {
			missing = append(missing, name)
			continue
		}
		aggregate.rx += counters.rx
		aggregate.tx += counters.tx
	}
	snapshot.NetworkRXTotal, snapshot.NetworkTXTotal = aggregate.rx, aggregate.tx
	if c.haveNet && seconds > 0 {
		snapshot.NetworkRX = uint64(float64(counterDelta(aggregate.rx, c.previousNet.rx)) / seconds)
		snapshot.NetworkTX = uint64(float64(counterDelta(aggregate.tx, c.previousNet.tx)) / seconds)
	}

	detailNames := selected
	if len(detailNames) == 0 {
		detailNames = aggregateNames
		sort.Strings(detailNames)
	}
	if len(detailNames) > 0 {
		snapshot.NetworkInterfaces = make([]networkInterfaceInfo, 0, len(detailNames))
		for _, name := range detailNames {
			counters, exists := devices[name]
			if !exists {
				continue
			}
			info := networkInterfaceInfo{
				Name: name, RXTotal: counters.rx, TXTotal: counters.tx,
				RXErrors: counters.rxErrors, TXErrors: counters.txErrors,
				RXDrops: counters.rxDrops, TXDrops: counters.txDrops,
				SpeedMbps: counters.speedMbps,
			}
			if previous, ok := c.previousInterfaces[name]; ok && c.haveNet && seconds > 0 {
				info.RX = uint64(float64(counterDelta(counters.rx, previous.rx)) / seconds)
				info.TX = uint64(float64(counterDelta(counters.tx, previous.tx)) / seconds)
			}
			snapshot.NetworkInterfaces = append(snapshot.NetworkInterfaces, info)
		}
		if len(selected) == 0 {
			sort.SliceStable(snapshot.NetworkInterfaces, func(i, j int) bool {
				first := snapshot.NetworkInterfaces[i]
				second := snapshot.NetworkInterfaces[j]
				firstTraffic := first.RX + first.TX
				secondTraffic := second.RX + second.TX
				if firstTraffic != secondTraffic {
					return firstTraffic > secondTraffic
				}
				return first.Name < second.Name
			})
		}
	}

	c.previousNet = aggregate
	c.previousInterfaces = devices
	c.haveNet = true
	c.lastNetAt = now
	if len(missing) > 0 {
		return fmt.Errorf("interfaces not found: %s", strings.Join(missing, ", "))
	}
	return nil
}

func readNetworkDevices() (map[string]networkDeviceCounters, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinNetworkDevices()
	}
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	devices := make(map[string]networkDeviceCounters)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if name == "" || len(fields) < 12 {
			continue
		}
		value := func(index int) uint64 {
			parsed, _ := strconv.ParseUint(fields[index], 10, 64)
			return parsed
		}
		devices[name] = networkDeviceCounters{
			rx: value(0), rxErrors: value(2), rxDrops: value(3),
			tx: value(8), txErrors: value(10), txDrops: value(11),
			speedMbps: readLinkSpeedMbps(name),
		}
	}
	return devices, nil
}

// readLinkSpeedMbps reads the Linux sysfs NIC speed. Zero means unknown
// (missing file, down link, or a non-Linux host).
func readLinkSpeedMbps(name string) uint64 {
	if runtime.GOOS != "linux" || name == "" || strings.Contains(name, "/") {
		return 0
	}
	data, err := os.ReadFile("/sys/class/net/" + name + "/speed")
	if err != nil {
		return 0
	}
	speed, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || speed <= 0 {
		return 0
	}
	return uint64(speed)
}

func readDefaultNetworkInterface() string {
	if runtime.GOOS == "darwin" {
		return readDarwinDefaultNetworkInterface()
	}
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "00000000" {
			return sanitizeTerminalText(fields[0])
		}
	}
	return ""
}

func (c *metricsCollector) collectMachineDetails(snapshot *monitorSnapshot) {
	snapshot.Profile = c.config.Profile
	snapshot.NodeName = c.config.Name
	if c.config.Profile != machineProfileNAS {
		return
	}
	snapshot.Filesystems = readFilesystems(c.config.Mounts)
	if time.Since(c.lastServiceAt) >= serviceRefreshInterval || c.lastServiceAt.IsZero() {
		c.cachedServices, c.cachedContainers, c.cachedDockerError,
			c.cachedPM2Processes, c.cachedPM2Error = collectServices(c.config)
		c.lastServiceAt = time.Now()
	}
	snapshot.Services = append([]serviceHealth(nil), c.cachedServices...)
	snapshot.Containers = append([]containerInfo(nil), c.cachedContainers...)
	snapshot.DockerError = c.cachedDockerError
	snapshot.PM2Processes = append([]pm2ProcessInfo(nil), c.cachedPM2Processes...)
	snapshot.PM2Error = c.cachedPM2Error
}

func readFilesystems(configured []string) []filesystemInfo {
	mounts := configured
	if len(mounts) == 0 {
		mounts = []string{"/"}
	}
	devices := mountedDevices()
	filesystems := make([]filesystemInfo, 0, len(mounts))
	for _, mount := range mounts {
		info := filesystemInfo{Mount: mount, Device: devices[mount]}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(mount, &stat); err != nil {
			info.Error = compactCommandError(err)
		} else {
			info.Total = stat.Blocks * uint64(stat.Bsize)
			info.Used = (stat.Blocks - stat.Bavail) * uint64(stat.Bsize)
		}
		filesystems = append(filesystems, info)
	}
	return filesystems
}

func mountedDevices() map[string]string {
	if runtime.GOOS == "darwin" {
		return readDarwinMountedDevices()
	}
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	devices := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mount := strings.ReplaceAll(fields[1], `\040`, " ")
		devices[mount] = sanitizeTerminalText(fields[0])
	}
	return devices
}

func collectServices(config machineConfig) ([]serviceHealth, []containerInfo, string, []pm2ProcessInfo, string) {
	var services []serviceHealth
	var containers []containerInfo
	var dockerError string
	var pm2Processes []pm2ProcessInfo
	var pm2Error string
	var wait sync.WaitGroup
	if len(config.HTTPChecks) > 0 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			services = checkHTTPServices(config.HTTPChecks)
		}()
	}
	if config.Docker {
		wait.Add(1)
		go func() {
			defer wait.Done()
			containers, dockerError = readDockerContainers()
		}()
	}
	if config.PM2User != "" {
		wait.Add(1)
		go func() {
			defer wait.Done()
			pm2Processes, pm2Error = readPM2Processes(config.PM2User)
		}()
	}
	wait.Wait()
	return services, containers, dockerError, pm2Processes, pm2Error
}

func checkHTTPServices(checks []httpCheckConfig) []serviceHealth {
	results := make([]serviceHealth, len(checks))
	var wait sync.WaitGroup
	for index := range checks {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			check := checks[index]
			started := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), serviceCheckTimeout)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
			if err != nil {
				results[index] = serviceHealth{Name: check.Name, Kind: "http", Detail: "invalid request"}
				return
			}
			response, err := serviceHTTPClient.Do(request)
			latency := time.Since(started)
			if err != nil {
				results[index] = serviceHealth{Name: check.Name, Kind: "http", Detail: "unreachable", Latency: latency}
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			results[index] = serviceHealth{
				Name: check.Name, Kind: "http", Healthy: response.StatusCode >= 200 && response.StatusCode < 400,
				Detail: strconv.Itoa(response.StatusCode), Latency: latency,
			}
		}(index)
	}
	wait.Wait()
	return results
}

func readDockerContainers() ([]containerInfo, string) {
	ctx, cancel := context.WithTimeout(context.Background(), serviceCheckTimeout)
	defer cancel()
	containers, err := readDockerContainersFrom(ctx, dockerSocketHTTPClient, "http://docker")
	if err != nil {
		return nil, compactCommandError(err)
	}
	return containers, ""
}

type dockerContainerListItem struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

type dockerContainerInspect struct {
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type dockerContainerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
		Stats struct {
			Cache        uint64 `json:"cache"`
			InactiveFile uint64 `json:"inactive_file"`
		} `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RXBytes uint64 `json:"rx_bytes"`
		TXBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkIOStats struct {
		IOServiceBytes []struct {
			Operation string `json:"op"`
			Value     uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PIDsStats struct {
		Current int `json:"current"`
	} `json:"pids_stats"`
}

func readDockerContainersFrom(ctx context.Context, client *http.Client, baseURL string) ([]containerInfo, error) {
	var listed []dockerContainerListItem
	if err := getJSON(ctx, client, baseURL+"/containers/json?all=1", &listed); err != nil {
		return nil, fmt.Errorf("docker API: %w", err)
	}
	containers := make([]containerInfo, len(listed))
	var wait sync.WaitGroup
	for index := range listed {
		item := listed[index]
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		containers[index] = containerInfo{
			ID: item.ID, Name: sanitizeTerminalText(name), Image: sanitizeTerminalText(item.Image),
			State: sanitizeTerminalText(item.State), Status: sanitizeTerminalText(item.Status),
			Running: strings.EqualFold(item.State, "running"), Ports: dockerPorts(item),
		}
		wait.Add(1)
		go func(index int, item dockerContainerListItem) {
			defer wait.Done()
			var inspect dockerContainerInspect
			if err := getJSON(ctx, client, baseURL+"/containers/"+url.PathEscape(item.ID)+"/json", &inspect); err == nil {
				container := &containers[index]
				container.Restarts = inspect.RestartCount
				container.Running = inspect.State.Running
				if inspect.State.Status != "" {
					container.State = sanitizeTerminalText(inspect.State.Status)
				}
				if inspect.State.Health != nil {
					container.Health = sanitizeTerminalText(inspect.State.Health.Status)
				}
				if started, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil && container.Running {
					container.Uptime = uint64(max(0, int(time.Since(started).Seconds())))
				}
			}
			if !containers[index].Running {
				return
			}
			var stats dockerContainerStats
			if err := getJSON(ctx, client, baseURL+"/containers/"+url.PathEscape(item.ID)+"/stats?stream=false&one-shot=true", &stats); err != nil {
				return
			}
			container := &containers[index]
			container.CPU = dockerCPUPercent(stats)
			cache := stats.MemoryStats.Stats.InactiveFile
			if cache == 0 {
				cache = stats.MemoryStats.Stats.Cache
			}
			container.MemoryUsed = counterDelta(stats.MemoryStats.Usage, cache)
			container.MemoryLimit = stats.MemoryStats.Limit
			for _, network := range stats.Networks {
				container.NetworkRX += network.RXBytes
				container.NetworkTX += network.TXBytes
			}
			for _, operation := range stats.BlkIOStats.IOServiceBytes {
				switch strings.ToLower(operation.Operation) {
				case "read":
					container.BlockRead += operation.Value
				case "write":
					container.BlockWrite += operation.Value
				}
			}
			container.PIDs = stats.PIDsStats.Current
		}(index, item)
	}
	wait.Wait()
	sort.SliceStable(containers, func(i, j int) bool {
		if containers[i].Running != containers[j].Running {
			return containers[i].Running
		}
		return strings.ToLower(containers[i].Name) < strings.ToLower(containers[j].Name)
	})
	return containers, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(target)
}

func dockerPorts(item dockerContainerListItem) string {
	ports := make([]string, 0, len(item.Ports))
	for _, port := range item.Ports {
		protocol := port.Type
		if protocol == "" {
			protocol = "tcp"
		}
		if port.PublicPort > 0 {
			ports = append(ports, fmt.Sprintf("%d→%d/%s", port.PublicPort, port.PrivatePort, protocol))
		} else {
			ports = append(ports, fmt.Sprintf("%d/%s", port.PrivatePort, protocol))
		}
	}
	return sanitizeTerminalText(strings.Join(ports, ","))
}

func dockerCPUPercent(stats dockerContainerStats) float64 {
	cpuDelta := counterDelta(stats.CPUStats.CPUUsage.TotalUsage, stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := counterDelta(stats.CPUStats.SystemUsage, stats.PreCPUStats.SystemUsage)
	onlineCPUs := stats.CPUStats.OnlineCPUs
	if onlineCPUs == 0 {
		onlineCPUs = uint64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta == 0 || systemDelta == 0 || onlineCPUs == 0 {
		return 0
	}
	return float64(cpuDelta) / float64(systemDelta) * float64(onlineCPUs) * 100
}

func readPM2Processes(userName string) ([]pm2ProcessInfo, string) {
	ctx, cancel := context.WithTimeout(context.Background(), serviceCheckTimeout)
	defer cancel()
	account, err := user.Lookup(userName)
	if err != nil {
		return nil, "user not found"
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return nil, "invalid user id"
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return nil, "invalid group id"
	}
	executable, environment, err := findPM2DaemonEnvironment(uint32(uid))
	if err != nil {
		return nil, compactPM2Error(err)
	}
	command := exec.CommandContext(ctx, executable, "jlist")
	command.Env = environment
	if os.Geteuid() == 0 && uint32(os.Geteuid()) != uint32(uid) {
		command.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
		}
	}
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, compactPM2Error(ctx.Err())
		}
		return nil, compactPM2Error(err)
	}
	processes, err := parsePM2Processes(output, time.Now())
	if err != nil {
		return nil, "invalid pm2 response"
	}
	return processes, ""
}

func compactPM2Error(err error) string {
	switch {
	case errors.Is(err, errPM2DaemonNotFound):
		return "running PM2 daemon not found"
	case errors.Is(err, syscall.EPERM):
		return "cannot switch to PM2 user"
	case errors.Is(err, exec.ErrNotFound):
		return "pm2 command not found"
	case errors.Is(err, context.DeadlineExceeded):
		return "pm2 check timed out"
	default:
		return "pm2 jlist failed"
	}
}

func findPM2DaemonEnvironment(uid uint32) (string, []string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		procPath := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(procPath)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid {
			continue
		}
		commandLine, err := os.ReadFile(filepath.Join(procPath, "cmdline"))
		if err != nil || !strings.Contains(strings.ReplaceAll(string(commandLine), "\x00", " "), "God Daemon") {
			continue
		}
		environ, err := os.ReadFile(filepath.Join(procPath, "environ"))
		if err != nil {
			continue
		}
		values := parseNullEnvironment(environ)
		pm2Home, path := values["PM2_HOME"], values["PATH"]
		if pm2Home == "" || path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(pm2Home, "rpc.sock")); err != nil {
			continue
		}
		executable := executableInPath("pm2", path)
		if executable == "" {
			continue
		}
		environment := []string{
			"HOME=" + values["HOME"],
			"PM2_HOME=" + pm2Home,
			"PATH=" + path,
			"USER=" + values["USER"],
			"LOGNAME=" + values["LOGNAME"],
		}
		return executable, environment, nil
	}
	return "", nil, errPM2DaemonNotFound
}

func parseNullEnvironment(data []byte) map[string]string {
	values := make(map[string]string)
	for _, entry := range strings.Split(string(data), "\x00") {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	return values
}

func executableInPath(name, path string) string {
	for _, directory := range filepath.SplitList(path) {
		candidate := filepath.Join(directory, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func parsePM2Processes(data []byte, now time.Time) ([]pm2ProcessInfo, error) {
	type rawPM2Process struct {
		PID   int    `json:"pid"`
		Name  string `json:"name"`
		ID    int    `json:"pm_id"`
		Monit struct {
			CPU    float64 `json:"cpu"`
			Memory uint64  `json:"memory"`
		} `json:"monit"`
		Environment struct {
			Status           string `json:"status"`
			Namespace        string `json:"namespace"`
			Mode             string `json:"exec_mode"`
			Uptime           int64  `json:"pm_uptime"`
			Restarts         int    `json:"restart_time"`
			UnstableRestarts int    `json:"unstable_restarts"`
		} `json:"pm2_env"`
	}
	start := strings.IndexByte(string(data), '[')
	if start < 0 {
		return nil, errors.New("PM2 returned invalid JSON")
	}
	var raw []rawPM2Process
	if err := json.Unmarshal(data[start:], &raw); err != nil {
		return nil, err
	}
	processes := make([]pm2ProcessInfo, 0, len(raw))
	for _, process := range raw {
		uptime := uint64(0)
		if process.Environment.Uptime > 0 && strings.EqualFold(process.Environment.Status, "online") {
			started := time.UnixMilli(process.Environment.Uptime)
			if now.After(started) {
				uptime = uint64(now.Sub(started).Seconds())
			}
		}
		processes = append(processes, pm2ProcessInfo{
			ID: process.ID, PID: process.PID, Name: sanitizeTerminalText(process.Name),
			Namespace: sanitizeTerminalText(process.Environment.Namespace),
			Status:    sanitizeTerminalText(process.Environment.Status),
			Mode:      sanitizeTerminalText(strings.TrimSuffix(process.Environment.Mode, "_mode")),
			CPU:       process.Monit.CPU, Memory: process.Monit.Memory, Uptime: uptime,
			Restarts: process.Environment.Restarts, UnstableRestarts: process.Environment.UnstableRestarts,
		})
	}
	sort.SliceStable(processes, func(i, j int) bool { return processes[i].ID < processes[j].ID })
	return processes, nil
}

type metricsCollector struct {
	config                     machineConfig
	identity                   systemIdentity
	previousCPU                map[string]cpuCounters
	cpuFrequencyReader         cpuFrequencyReader
	cpuFrequencyInitialized    bool
	previousNet                netCounters
	previousDiskRead           uint64
	previousDiskWrite          uint64
	haveDiskIO                 bool
	lastDiskIOAt               time.Time
	previousInterfaces         map[string]networkDeviceCounters
	previousProcessNet         map[int]processNetworkCounters
	previousConnectionNet      map[string]connectionNetworkCounters
	haveCPU                    bool
	haveNet                    bool
	lastNetAt                  time.Time
	lastProcessNetAt           time.Time
	lastServiceAt              time.Time
	cachedServices             []serviceHealth
	cachedContainers           []containerInfo
	cachedDockerError          string
	cachedPM2Processes         []pm2ProcessInfo
	cachedPM2Error             string
	processRefreshInterval     time.Duration
	gpuWorkloadRefreshInterval time.Duration
	processNetRefreshInterval  time.Duration
	lastProcessesAt            time.Time
	cachedProcesses            []processInfo
	lastGPUWorkloadsAt         time.Time
	cachedGPUWorkloads         map[string][]gpuWorkloadInfo
	cachedNetworkProcesses     []processNetworkInfo
	cachedNetworkConnections   []networkConnectionInfo
	cachedConnectionWarning    string
	progress                   *progressReceiver
}

type netCounters struct{ rx, tx uint64 }

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func newMetricsCollector(config machineConfig) *metricsCollector {
	processNetInterval := 5 * time.Second
	if runtime.GOOS == "linux" {
		processNetInterval = time.Second
	}
	return &metricsCollector{
		config:                     config,
		identity:                   readSystemIdentity(),
		previousInterfaces:         make(map[string]networkDeviceCounters),
		previousProcessNet:         make(map[int]processNetworkCounters),
		processRefreshInterval:     3 * time.Second,
		gpuWorkloadRefreshInterval: 5 * time.Second,
		processNetRefreshInterval:  processNetInterval,
	}
}

func (c *metricsCollector) needsWarmup() bool {
	return !c.haveCPU || !c.haveNet
}

func (c *metricsCollector) collect() (monitorSnapshot, error) {
	return c.collectWithProcesses(true)
}

func (c *metricsCollector) collectSummary() (monitorSnapshot, error) {
	return c.collectWithProcesses(false)
}

func (c *metricsCollector) collectWithProcesses(includeProcesses bool) (monitorSnapshot, error) {
	s := monitorSnapshot{CollectedAt: time.Now()}
	s.OSName = c.identity.OSName
	s.CPUModel = c.identity.CPUModel
	s.CPUCores = c.identity.CPUCores
	s.Uptime = uptimeSeconds(c.identity, s.CollectedAt)
	var errs []string
	if err := c.collectCPU(&s); err != nil {
		errs = append(errs, "cpu: "+err.Error())
	}
	c.collectCPUFrequency(&s)
	if used, total, err := readMemory(); err != nil {
		errs = append(errs, "memory: "+err.Error())
	} else {
		s.MemoryUsed, s.MemoryTotal = used, total
	}
	if used, total, err := readDisk(); err != nil {
		errs = append(errs, "disk: "+err.Error())
	} else {
		s.DiskUsed, s.DiskTotal = used, total
	}
	c.collectDiskIO(&s)
	if err := c.collectNetwork(&s); err != nil {
		errs = append(errs, "network: "+err.Error())
	}
	if includeProcesses {
		c.collectProcessNetwork(&s)
	}
	s.LoadAverage = readLoadAverage()
	if runtime.GOOS == "darwin" {
		s.Battery, _ = readDarwinBattery()
		s.GPUs, _ = readDarwinAppleGPU()
	} else if c.config.Profile == machineProfileGPU {
		s.GPUs, s.GPUError = c.collectGPUs()
	}
	c.collectMachineDetails(&s)
	if includeProcesses {
		c.collectProcesses(&s, &errs)
		attachGPUWorkloadUsers(s.GPUs, s.Processes)
	} else {
		// The hub card names who is on the machine without shipping the
		// full process table. Older hubs ignore a short Processes list.
		var ignored []string
		c.collectProcesses(&s, &ignored)
		attachGPUWorkloadUsers(s.GPUs, s.Processes)
		s.Processes = topProcesses(s.Processes, 3)
	}
	if len(errs) > 0 {
		return s, errors.New(strings.Join(errs, "; "))
	}
	return s, nil
}

func readMemory() (used, total uint64, err error) {
	if runtime.GOOS == "darwin" {
		return readDarwinMemory()
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	total = values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, 0, errors.New("MemTotal missing")
	}
	return total - available, total, nil
}

func readDisk() (used, total uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, err
	}
	total = stat.Blocks * uint64(stat.Bsize)
	used = (stat.Blocks - stat.Bavail) * uint64(stat.Bsize)
	return used, total, nil
}

func readLoadAverage() string {
	if runtime.GOOS == "darwin" {
		return readDarwinLoadAverage()
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "load unavailable"
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return "load unavailable"
	}
	return "load " + strings.Join(fields[:3], " · ")
}

// collectGPUs refreshes the per-GPU summary on every cycle and reuses the
// heavier compute-apps attribution until gpuWorkloadRefreshInterval elapses.
func (c *metricsCollector) collectGPUs() ([]gpuInfo, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,uuid,name,utilization.gpu,memory.used,memory.total,temperature.gpu,clocks.current.graphics,power.draw,power.limit,driver_version", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, gpuMetricsError(err, "/sys/bus/pci/devices")
	}
	gpus := parseGPUs(output)
	c.attachGPUWorkloads(gpus)
	return gpus, ""
}

func (c *metricsCollector) attachGPUWorkloads(gpus []gpuInfo) {
	due := c.lastGPUWorkloadsAt.IsZero() ||
		time.Since(c.lastGPUWorkloadsAt) >= c.gpuWorkloadRefreshInterval
	if !due {
		for index := range gpus {
			gpus[index].Workloads = append(
				[]gpuWorkloadInfo(nil), c.cachedGPUWorkloads[gpus[index].UUID]...,
			)
		}
		return
	}
	workloadCtx, workloadCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer workloadCancel()
	workloadOutput, workloadErr := exec.CommandContext(
		workloadCtx, "nvidia-smi",
		"--query-compute-apps=gpu_uuid,pid,process_name,used_gpu_memory",
		"--format=csv,noheader,nounits",
	).Output()
	if workloadErr == nil {
		workloads := parseGPUWorkloads(workloadOutput)
		c.cachedGPUWorkloads = workloads
	}
	c.lastGPUWorkloadsAt = time.Now()
	for index := range gpus {
		gpus[index].Workloads = append(
			[]gpuWorkloadInfo(nil), c.cachedGPUWorkloads[gpus[index].UUID]...,
		)
	}
}

// collectProcesses refreshes the full process table at a lower cadence than
// the host metrics and reuses the last successful table in between.
func (c *metricsCollector) collectProcesses(snapshot *monitorSnapshot, errs *[]string) {
	if c.lastProcessesAt.IsZero() ||
		time.Since(c.lastProcessesAt) >= c.processRefreshInterval {
		processes, err := readProcesses()
		if err != nil {
			*errs = append(*errs, "processes: "+err.Error())
		} else {
			c.cachedProcesses = processes
			c.lastProcessesAt = time.Now()
		}
	}
	snapshot.Processes = append([]processInfo(nil), c.cachedProcesses...)
	c.progress.attach(snapshot.Processes)
}

func parseGPUs(output []byte) []gpuInfo {
	var gpus []gpuInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 9 && len(parts) != 11 {
			continue
		}
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		index, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		nameIndex, utilIndex := 1, 2
		uuid, driver := "", ""
		if len(parts) == 11 {
			uuid, nameIndex, utilIndex, driver = parts[1], 2, 3, parts[10]
		}
		memUsed, _ := strconv.ParseUint(parts[utilIndex+1], 10, 64)
		memTotal, _ := strconv.ParseUint(parts[utilIndex+2], 10, 64)
		util, _ := strconv.ParseFloat(parts[utilIndex], 64)
		temp, _ := strconv.Atoi(parts[utilIndex+3])
		clockMHz, _ := strconv.Atoi(parts[utilIndex+4])
		power, _ := strconv.ParseFloat(parts[utilIndex+5], 64)
		powerLimit, _ := strconv.ParseFloat(parts[utilIndex+6], 64)
		gpus = append(gpus, gpuInfo{
			Index: index, UUID: uuid, Name: parts[nameIndex], DriverVersion: driver,
			Utilization: util,
			MemoryUsed:  memUsed * 1024 * 1024, MemoryTotal: memTotal * 1024 * 1024,
			Temperature: temp, ClockMHz: clockMHz, Power: power, PowerLimit: powerLimit,
		})
	}
	return gpus
}

func parseGPUWorkloads(output []byte) map[string][]gpuWorkloadInfo {
	workloads := make(map[string][]gpuWorkloadInfo)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 4 {
			continue
		}
		uuid := strings.TrimSpace(parts[0])
		pid, pidErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		memoryMiB, memoryErr := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
		if uuid == "" || pidErr != nil || pid <= 0 || memoryErr != nil {
			continue
		}
		workloads[uuid] = append(workloads[uuid], gpuWorkloadInfo{
			PID: pid, Name: sanitizeTerminalText(parts[2]), MemoryUsed: memoryMiB << 20,
		})
	}
	return workloads
}

func topProcesses(processes []processInfo, limit int) []processInfo {
	if limit <= 0 || len(processes) == 0 {
		return nil
	}
	remaining := append([]processInfo(nil), processes...)
	picked := make([]processInfo, 0, min(limit, len(remaining)))
	for len(picked) < limit && len(remaining) > 0 {
		best := 0
		for index := 1; index < len(remaining); index++ {
			if remaining[index].CPU > remaining[best].CPU {
				best = index
			}
		}
		if remaining[best].CPU <= 0 {
			break
		}
		picked = append(picked, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return picked
}

func attachGPUWorkloadUsers(gpus []gpuInfo, processes []processInfo) {
	users := make(map[int]string, len(processes))
	for _, process := range processes {
		users[process.PID] = process.User
	}
	for gpuIndex := range gpus {
		for workloadIndex := range gpus[gpuIndex].Workloads {
			gpus[gpuIndex].Workloads[workloadIndex].User =
				users[gpus[gpuIndex].Workloads[workloadIndex].PID]
		}
	}
}

func readProcesses() ([]processInfo, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcesses()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,uid=,user=,stat=,pcpu=,pmem=,rss=,etimes=,comm=", "--sort=-pcpu").Output()
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		uid, uidErr := strconv.Atoi(fields[1])
		if uidErr != nil {
			uid = -1
		}
		cpu, _ := strconv.ParseFloat(fields[4], 64)
		memory, _ := strconv.ParseFloat(fields[5], 64)
		rss, _ := strconv.ParseUint(fields[6], 10, 64)
		elapsed, _ := strconv.ParseUint(fields[7], 10, 64)
		// ps can briefly appear as the busiest process because it is sampling
		// the whole process table. It is collector noise, not a useful host task.
		if fields[8] == "ps" {
			continue
		}
		processes = append(processes, processInfo{
			PID: pid, UID: uid, User: sanitizeTerminalText(fields[2]), State: sanitizeTerminalText(fields[3]), CPU: cpu, Memory: memory,
			RSS: rss * 1024, Elapsed: elapsed, Command: sanitizeTerminalText(strings.Join(fields[8:], " ")),
		})
	}
	sort.SliceStable(processes, func(i, j int) bool { return processes[i].CPU > processes[j].CPU })
	return processes, nil
}

func readProcessDetailWithSensitive(pid int, includeSensitive bool) (processDetail, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcessDetail(pid, includeSensitive)
	}
	if pid <= 0 {
		return processDetail{}, errors.New("invalid PID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,uid=,user=,stat=,pcpu=,pmem=,rss=,etimes=,nlwp=,comm=").Output()
	if err != nil {
		return processDetail{}, fmt.Errorf("process no longer exists")
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) < 11 {
		return processDetail{}, errors.New("incomplete process information")
	}
	detail := processDetail{}
	detail.PID, _ = strconv.Atoi(fields[0])
	detail.PPID, _ = strconv.Atoi(fields[1])
	detail.UID, _ = strconv.Atoi(fields[2])
	detail.User = sanitizeTerminalText(fields[3])
	detail.State = sanitizeTerminalText(fields[4])
	detail.CPU, _ = strconv.ParseFloat(fields[5], 64)
	detail.Memory, _ = strconv.ParseFloat(fields[6], 64)
	rss, _ := strconv.ParseUint(fields[7], 10, 64)
	detail.RSS = rss * 1024
	detail.Elapsed, _ = strconv.ParseUint(fields[8], 10, 64)
	detail.Threads, _ = strconv.Atoi(fields[9])
	detail.Name = sanitizeTerminalText(strings.Join(fields[10:], " "))

	procRoot := fmt.Sprintf("/proc/%d", pid)
	if commandLine, readErr := os.ReadFile(procRoot + "/cmdline"); readErr == nil {
		detail.CommandLine = sanitizeTerminalText(strings.ReplaceAll(string(commandLine), "\x00", " "))
	}
	if detail.CommandLine == "" {
		detail.CommandLine = detail.Name
	}
	if executable, readErr := os.Readlink(procRoot + "/exe"); readErr == nil {
		detail.Executable = sanitizeTerminalText(executable)
	} else {
		detail.Executable = "unavailable"
	}
	if cwd, readErr := os.Readlink(procRoot + "/cwd"); readErr == nil {
		detail.CWD = sanitizeTerminalText(cwd)
	} else {
		detail.CWD = "unavailable"
	}
	applyProcessDetailPolicy(&detail, includeSensitive)
	detail.StartTimeTicks, _ = readProcessStartTicks(pid)
	return detail, nil
}

func applyProcessDetailPolicy(detail *processDetail, includeSensitive bool) {
	if detail == nil || includeSensitive {
		return
	}
	detail.CommandLine = redactProcessCommandLine(detail.CommandLine)
	detail.CWD = redactedProcessDetailMessage
	detail.Redacted = true
}

func readProcessStartTicks(pid int) (uint64, error) {
	if runtime.GOOS == "darwin" {
		return readDarwinProcessStart(pid)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseProcessStartTicks(stat)
}

func parseProcessStartTicks(stat []byte) (uint64, error) {
	// The command name in field 2 may contain spaces. Everything after its
	// closing parenthesis starts at field 3; starttime is field 22.
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+2 >= len(stat) {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(stat[closing+2:]))
	if len(fields) <= 19 {
		return 0, errors.New("incomplete process stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func compactCommandError(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "command not found"
	}
	return "not available"
}

func gpuMetricsError(commandErr error, pciRoot string) string {
	// Missing tools or broken drivers do not prove that hardware is absent.
	// Clear the error only when Linux's PCI inventory confirms there is no
	// NVIDIA display/3D controller. Unreadable inventories retain the warning.
	present, err := hasNVIDIAGPU(pciRoot)
	if err == nil && !present {
		return ""
	}
	return compactCommandError(commandErr)
}

func hasNVIDIAGPU(pciRoot string) (bool, error) {
	devices, err := os.ReadDir(pciRoot)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		class, err := readPCIAttribute(filepath.Join(pciRoot, device.Name(), "class"), 24)
		if err != nil {
			return false, err
		}
		if class>>16 != 0x03 {
			continue
		}
		vendor, err := readPCIAttribute(filepath.Join(pciRoot, device.Name(), "vendor"), 16)
		if err != nil {
			return false, err
		}
		if vendor == 0x10de {
			return true, nil
		}
	}
	return false, nil
}

func readPCIAttribute(path string, bits int) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 0, bits)
}
