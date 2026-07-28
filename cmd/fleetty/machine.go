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
	machineProfileNAS     = "nas"
	machineProfileGeneral = "general"

	serviceRefreshInterval = 5 * time.Second
	serviceCheckTimeout    = 700 * time.Millisecond
)

var errPM2DaemonNotFound = errors.New("running PM2 daemon not found")

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
		return config, errors.New("machine profile must be gpu, nas, or general")
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

	if len(selected) > 0 {
		snapshot.NetworkInterfaces = make([]networkInterfaceInfo, 0, len(selected))
		for _, name := range selected {
			counters, exists := devices[name]
			if !exists {
				continue
			}
			info := networkInterfaceInfo{
				Name: name, RXTotal: counters.rx, TXTotal: counters.tx,
				RXErrors: counters.rxErrors, TXErrors: counters.txErrors,
				RXDrops: counters.rxDrops, TXDrops: counters.txDrops,
			}
			if previous, ok := c.previousInterfaces[name]; ok && c.haveNet && seconds > 0 {
				info.RX = uint64(float64(counterDelta(counters.rx, previous.rx)) / seconds)
				info.TX = uint64(float64(counterDelta(counters.tx, previous.tx)) / seconds)
			}
			snapshot.NetworkInterfaces = append(snapshot.NetworkInterfaces, info)
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
		}
	}
	return devices, nil
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
			client := &http.Client{
				Timeout: serviceCheckTimeout,
				Transport: &http.Transport{
					Proxy:             nil,
					DisableKeepAlives: true,
				},
			}
			response, err := client.Do(request)
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
	dialer := &net.Dialer{Timeout: serviceCheckTimeout}
	client := &http.Client{
		Timeout: serviceCheckTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", "/var/run/docker.sock")
			},
		},
	}
	containers, err := readDockerContainersFrom(ctx, client, "http://docker")
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
