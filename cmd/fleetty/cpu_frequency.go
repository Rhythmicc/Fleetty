package main

import (
	"math"
	"strconv"
	"strings"
)

type cpuFrequencyInfo struct {
	AverageMHz   float64 `json:"average_mhz"`
	MinMHz       float64 `json:"min_mhz"`
	MaxMHz       float64 `json:"max_mhz"`
	SampledCores int     `json:"sampled_cores"`
	Source       string  `json:"source"`
}

type cpuFrequencyReader interface {
	read() (map[string]float64, string)
}

func (c *metricsCollector) collectCPUFrequency(snapshot *monitorSnapshot) {
	if !c.cpuFrequencyInitialized {
		c.cpuFrequencyReader = newPlatformCPUFrequencyReader()
		c.cpuFrequencyInitialized = true
	}
	var frequencies map[string]float64
	var source string
	if c.cpuFrequencyReader != nil {
		frequencies, source = c.cpuFrequencyReader.read()
	}
	applyCPUFrequencies(snapshot, frequencies, source)
}

func parseCPUFrequencies(data string) map[string]float64 {
	frequencies := make(map[string]float64)
	id := ""
	for _, line := range strings.Split(data, "\n") {
		if strings.TrimSpace(line) == "" {
			id = ""
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "processor":
			id = ""
			if number, err := strconv.Atoi(value); err == nil && number >= 0 {
				id = "cpu" + strconv.Itoa(number)
			}
		case "cpu MHz":
			mhz, err := strconv.ParseFloat(value, 64)
			if id != "" && err == nil && validCPUFrequency(mhz) {
				frequencies[id] = mhz
			}
		}
	}
	return frequencies
}

func validCPUFrequency(mhz float64) bool {
	return mhz > 0 && !math.IsNaN(mhz) && !math.IsInf(mhz, 0)
}

func applyCPUFrequencies(snapshot *monitorSnapshot, frequencies map[string]float64, source string) {
	snapshot.CPUFrequency = nil
	summary := cpuFrequencyInfo{Source: source}
	for index := range snapshot.CPUCoreUsage {
		core := &snapshot.CPUCoreUsage[index]
		core.FrequencyMHz = 0
		mhz := frequencies[core.CPU]
		if !validCPUFrequency(mhz) {
			continue
		}
		core.FrequencyMHz = mhz
		if summary.SampledCores == 0 || mhz < summary.MinMHz {
			summary.MinMHz = mhz
		}
		summary.MaxMHz = math.Max(summary.MaxMHz, mhz)
		summary.SampledCores++
		// Incremental mean avoids overflowing a sum of finite samples.
		summary.AverageMHz += (mhz - summary.AverageMHz) / float64(summary.SampledCores)
	}
	if summary.SampledCores > 0 {
		snapshot.CPUFrequency = &summary
	}
}
