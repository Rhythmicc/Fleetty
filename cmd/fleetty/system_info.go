package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type systemIdentity struct {
	OSName   string
	CPUModel string
	CPUCores int
	BootTime time.Time
}

func readSystemIdentity() systemIdentity {
	identity := systemIdentity{
		OSName:   runtime.GOOS + "/" + runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}
	if runtime.GOOS == "darwin" {
		if output, err := commandOutput(2*time.Second, "sw_vers", "-productVersion"); err == nil {
			if version := sanitizeTerminalText(string(output)); version != "" {
				identity.OSName = "macOS " + version
			}
		}
		if output, err := commandOutput(2*time.Second, "sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
			identity.CPUModel = sanitizeTerminalText(string(output))
		}
		if output, err := commandOutput(2*time.Second, "sysctl", "-n", "kern.boottime"); err == nil {
			identity.BootTime = parseDarwinBootTime(string(output))
		}
		return identity
	}

	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		if pretty := osReleaseValue(string(data), "PRETTY_NAME"); pretty != "" {
			identity.OSName = pretty
		}
	}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		identity.CPUModel = cpuInfoModel(string(data))
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil && seconds >= 0 {
				identity.BootTime = time.Now().Add(-time.Duration(seconds * float64(time.Second)))
			}
		}
	}
	return identity
}

func osReleaseValue(contents, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(contents, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		return sanitizeTerminalText(value)
	}
	return ""
}

func cpuInfoModel(contents string) string {
	for _, line := range strings.Split(contents, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(name) {
		case "model name", "Model", "Hardware":
			return sanitizeTerminalText(value)
		}
	}
	return ""
}

func parseDarwinBootTime(output string) time.Time {
	marker := "sec = "
	start := strings.Index(output, marker)
	if start < 0 {
		return time.Time{}
	}
	value := output[start+len(marker):]
	end := strings.IndexAny(value, ",}")
	if end >= 0 {
		value = value[:end]
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func uptimeSeconds(identity systemIdentity, now time.Time) uint64 {
	if identity.BootTime.IsZero() || now.Before(identity.BootTime) {
		return 0
	}
	return uint64(now.Sub(identity.BootTime).Seconds())
}
