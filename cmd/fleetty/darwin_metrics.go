package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

func readDarwinCPUPercent() (float64, error) {
	output, err := commandOutput(2*time.Second, "top", "-l", "1", "-n", "0", "-s", "0")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "CPU usage:") {
			continue
		}
		for _, field := range strings.Split(line, ",") {
			if !strings.Contains(field, "idle") {
				continue
			}
			parts := strings.Fields(strings.TrimSpace(field))
			if len(parts) < 2 {
				break
			}
			idle, parseErr := strconv.ParseFloat(strings.TrimSuffix(parts[0], "%"), 64)
			if parseErr == nil {
				return 100 - idle, nil
			}
			break
		}
	}
	return 0, errors.New("top CPU usage line missing")
}

func readDarwinMemory() (used, total uint64, err error) {
	totalOutput, err := commandOutput(2*time.Second, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, 0, err
	}
	total, err = strconv.ParseUint(strings.TrimSpace(string(totalOutput)), 10, 64)
	if err != nil || total == 0 {
		return 0, 0, errors.New("hw.memsize returned an invalid value")
	}
	vmOutput, err := commandOutput(2*time.Second, "vm_stat")
	if err != nil {
		return 0, 0, err
	}
	pageSize, pages, err := parseDarwinVMStat(vmOutput)
	if err != nil {
		return 0, 0, err
	}
	availablePages := pages["Pages free"] + pages["Pages inactive"] + pages["Pages speculative"]
	available := availablePages * pageSize
	if available > total {
		available = total
	}
	return total - available, total, nil
}

func parseDarwinVMStat(output []byte) (uint64, map[string]uint64, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return 0, nil, errors.New("vm_stat returned no counters")
	}
	const marker = "page size of "
	start := strings.Index(lines[0], marker)
	if start < 0 {
		return 0, nil, errors.New("vm_stat page size missing")
	}
	pageField := lines[0][start+len(marker):]
	end := strings.IndexByte(pageField, ' ')
	if end >= 0 {
		pageField = pageField[:end]
	}
	pageSize, err := strconv.ParseUint(strings.TrimSpace(pageField), 10, 64)
	if err != nil || pageSize == 0 {
		return 0, nil, errors.New("vm_stat page size invalid")
	}
	pages := make(map[string]uint64)
	for _, line := range lines[1:] {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, "."))
		parsed, parseErr := strconv.ParseUint(value, 10, 64)
		if parseErr == nil {
			pages[strings.TrimSpace(name)] = parsed
		}
	}
	return pageSize, pages, nil
}

func readDarwinNetworkDevices() (map[string]networkDeviceCounters, error) {
	output, err := commandOutput(2*time.Second, "netstat", "-ibn")
	if err != nil {
		return nil, err
	}
	return parseDarwinNetstat(output)
}

func parseDarwinNetstat(output []byte) (map[string]networkDeviceCounters, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return nil, errors.New("netstat returned no interfaces")
	}
	header := strings.Fields(lines[0])
	indexes := make(map[string]int, len(header))
	for index, name := range header {
		indexes[name] = index
	}
	required := []string{"Name", "Ierrs", "Ibytes", "Oerrs", "Obytes"}
	for _, name := range required {
		if _, ok := indexes[name]; !ok {
			return nil, fmt.Errorf("netstat column %s missing", name)
		}
	}
	devices := make(map[string]networkDeviceCounters)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		maxIndex := 0
		for _, name := range required {
			if indexes[name] > maxIndex {
				maxIndex = indexes[name]
			}
		}
		if len(fields) <= maxIndex {
			continue
		}
		value := func(name string) uint64 {
			parsed, _ := strconv.ParseUint(fields[indexes[name]], 10, 64)
			return parsed
		}
		name := sanitizeTerminalText(fields[indexes["Name"]])
		current := devices[name]
		candidate := networkDeviceCounters{
			rx: value("Ibytes"), rxErrors: value("Ierrs"),
			tx: value("Obytes"), txErrors: value("Oerrs"),
		}
		// netstat can emit one row per address family. Those rows contain the
		// same interface counters, so retain the greatest observed values.
		current.rx = maxUint64(current.rx, candidate.rx)
		current.tx = maxUint64(current.tx, candidate.tx)
		current.rxErrors = maxUint64(current.rxErrors, candidate.rxErrors)
		current.txErrors = maxUint64(current.txErrors, candidate.txErrors)
		devices[name] = current
	}
	return devices, nil
}

func maxUint64(first, second uint64) uint64 {
	if first > second {
		return first
	}
	return second
}

func readDarwinDefaultNetworkInterface() string {
	output, err := commandOutput(2*time.Second, "route", "-n", "get", "default")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		name, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(name) == "interface" {
			return sanitizeTerminalText(value)
		}
	}
	return ""
}

func readDarwinMountedDevices() map[string]string {
	output, err := commandOutput(2*time.Second, "mount")
	if err != nil {
		return nil
	}
	devices := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		device, remainder, found := strings.Cut(line, " on ")
		if !found {
			continue
		}
		mount, _, found := strings.Cut(remainder, " (")
		if found {
			devices[sanitizeTerminalText(mount)] = sanitizeTerminalText(device)
		}
	}
	return devices
}

func readDarwinLoadAverage() string {
	output, err := commandOutput(2*time.Second, "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return "load unavailable"
	}
	fields := strings.Fields(strings.Trim(string(output), "{} \n\t"))
	if len(fields) < 3 {
		return "load unavailable"
	}
	return "load " + strings.Join(fields[:3], " · ")
}

func readDarwinProcesses() ([]processInfo, error) {
	output, err := commandOutput(3*time.Second, "ps", "-axo", "pid=,user=,state=,%cpu=,%mem=,rss=,etime=,comm=")
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		memory, _ := strconv.ParseFloat(fields[4], 64)
		rss, _ := strconv.ParseUint(fields[5], 10, 64)
		elapsed, parseErr := parseBSDProcessElapsed(fields[6])
		if parseErr != nil || fields[7] == "ps" {
			continue
		}
		processes = append(processes, processInfo{
			PID: pid, User: sanitizeTerminalText(fields[1]), State: sanitizeTerminalText(fields[2]),
			CPU: cpu, Memory: memory, RSS: rss * 1024, Elapsed: elapsed,
			Command: sanitizeTerminalText(strings.Join(fields[7:], " ")),
		})
	}
	sort.SliceStable(processes, func(i, j int) bool { return processes[i].CPU > processes[j].CPU })
	return processes, nil
}

func readDarwinProcessDetail(pid int, includeSensitive bool) (processDetail, error) {
	if pid <= 0 {
		return processDetail{}, errors.New("invalid PID")
	}
	output, err := commandOutput(
		3*time.Second, "ps", "-p", strconv.Itoa(pid),
		"-o", "pid=,ppid=,uid=,user=,state=,%cpu=,%mem=,rss=,etime=,comm=",
	)
	if err != nil {
		return processDetail{}, errors.New("process no longer exists")
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) < 10 {
		return processDetail{}, errors.New("incomplete process information")
	}
	var detail processDetail
	detail.PID, _ = strconv.Atoi(fields[0])
	detail.PPID, _ = strconv.Atoi(fields[1])
	detail.UID, _ = strconv.Atoi(fields[2])
	detail.User = sanitizeTerminalText(fields[3])
	detail.State = sanitizeTerminalText(fields[4])
	detail.CPU, _ = strconv.ParseFloat(fields[5], 64)
	detail.Memory, _ = strconv.ParseFloat(fields[6], 64)
	rss, _ := strconv.ParseUint(fields[7], 10, 64)
	detail.RSS = rss * 1024
	detail.Elapsed, _ = parseBSDProcessElapsed(fields[8])
	detail.Name = sanitizeTerminalText(strings.Join(fields[9:], " "))
	detail.Executable = detail.Name
	detail.CommandLine = detail.Name
	if commandLine, commandErr := commandOutput(2*time.Second, "ps", "-p", strconv.Itoa(pid), "-o", "command="); commandErr == nil {
		detail.CommandLine = sanitizeTerminalText(string(commandLine))
	}
	detail.CWD = "unavailable"
	if cwdOutput, cwdErr := commandOutput(2*time.Second, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn"); cwdErr == nil {
		for _, line := range strings.Split(string(cwdOutput), "\n") {
			if strings.HasPrefix(line, "n") && len(line) > 1 {
				detail.CWD = sanitizeTerminalText(line[1:])
				break
			}
		}
	}
	applyProcessDetailPolicy(&detail, includeSensitive)
	detail.StartTimeTicks, _ = readDarwinProcessStart(pid)
	return detail, nil
}

func readDarwinProcessStart(pid int) (uint64, error) {
	output, err := commandOutput(2*time.Second, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	if err != nil {
		return 0, err
	}
	started, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(output)), time.Local)
	if err != nil {
		return 0, err
	}
	return uint64(started.UnixNano()), nil
}

func parseBSDProcessElapsed(value string) (uint64, error) {
	dayParts := strings.Split(value, "-")
	if len(dayParts) > 2 {
		return 0, errors.New("invalid elapsed time")
	}
	var days uint64
	clock := dayParts[0]
	if len(dayParts) == 2 {
		parsed, err := strconv.ParseUint(dayParts[0], 10, 64)
		if err != nil {
			return 0, err
		}
		days, clock = parsed, dayParts[1]
	}
	clockParts := strings.Split(clock, ":")
	if len(clockParts) < 2 || len(clockParts) > 3 {
		return 0, errors.New("invalid elapsed time")
	}
	values := make([]uint64, len(clockParts))
	for index, part := range clockParts {
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return 0, err
		}
		values[index] = parsed
	}
	var hours, minutes, seconds uint64
	if len(values) == 3 {
		hours, minutes, seconds = values[0], values[1], values[2]
	} else {
		minutes, seconds = values[0], values[1]
	}
	return days*86400 + hours*3600 + minutes*60 + seconds, nil
}

func commandOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}
