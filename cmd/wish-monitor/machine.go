package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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

type machineConfig struct {
	Name              string            `json:"name,omitempty"`
	Profile           string            `json:"profile,omitempty"`
	NetworkInterfaces []string          `json:"network_interfaces,omitempty"`
	Mounts            []string          `json:"mounts,omitempty"`
	Docker            bool              `json:"docker,omitempty"`
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
	Name, Image, State, Status string
	Running                    bool
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
		c.cachedServices, c.cachedContainers, c.cachedDockerError = collectServices(c.config)
		c.lastServiceAt = time.Now()
	}
	snapshot.Services = append([]serviceHealth(nil), c.cachedServices...)
	snapshot.Containers = append([]containerInfo(nil), c.cachedContainers...)
	snapshot.DockerError = c.cachedDockerError
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

func collectServices(config machineConfig) ([]serviceHealth, []containerInfo, string) {
	var services []serviceHealth
	var containers []containerInfo
	var dockerError string
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
	wait.Wait()
	return services, containers, dockerError
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
	output, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format",
		"{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}").Output()
	if err != nil {
		return nil, compactCommandError(err)
	}
	var containers []containerInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		state := sanitizeTerminalText(fields[2])
		containers = append(containers, containerInfo{
			Name: sanitizeTerminalText(fields[0]), Image: sanitizeTerminalText(fields[1]),
			State: state, Status: sanitizeTerminalText(fields[3]), Running: state == "running",
		})
	}
	return containers, ""
}
