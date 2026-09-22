package main

import (
	"errors"
	"math"

	"github.com/shirou/gopsutil/v4/cpu"
)

// CPU is the OS logical CPU identifier, not a physical socket/core number.
type cpuCoreUsage struct {
	CPU       string  `json:"cpu"`
	Percent   float64 `json:"percent"`
	Available bool    `json:"available"`
}

type cpuCounters struct{ total, idle float64 }

func (c *metricsCollector) collectCPU(snapshot *monitorSnapshot) error {
	times, err := cpu.Times(true)
	if err == nil && len(times) == 0 {
		err = errors.New("logical CPU counters unavailable")
	}
	if err != nil {
		c.previousCPU, c.haveCPU = nil, false
		return err
	}
	c.updateCPU(snapshot, times)
	return nil
}

// Keep baselines per collector and CPU ID: global Percent() baselines would
// couple independent collectors, and slice indexes break on CPU hotplug.
func (c *metricsCollector) updateCPU(snapshot *monitorSnapshot, times []cpu.TimesStat) {
	next := make(map[string]cpuCounters, len(times))
	cores := make([]cpuCoreUsage, 0, len(times))
	var totalDelta, busyDelta float64
	for _, sample := range times {
		// Guest/GuestNice are already included in User/Nice on Linux.
		current := cpuCounters{
			total: sample.User + sample.Nice + sample.System + sample.Idle +
				sample.Iowait + sample.Irq + sample.Softirq + sample.Steal,
			idle: sample.Idle + sample.Iowait,
		}
		usage := cpuCoreUsage{CPU: sample.CPU}
		if previous, ok := c.previousCPU[sample.CPU]; ok {
			total, idle := current.total-previous.total, current.idle-previous.idle
			if total > 0 && idle >= 0 && idle <= total &&
				!math.IsInf(total, 0) && !math.IsNaN(total) {
				usage.Percent = 100 * (total - idle) / total
				usage.Available = true
				totalDelta += total
				busyDelta += total - idle
			}
		}
		cores = append(cores, usage)
		next[sample.CPU] = current
	}
	snapshot.CPUPercent = 0
	if totalDelta > 0 {
		snapshot.CPUPercent = 100 * busyDelta / totalDelta
	}
	snapshot.CPUCoreUsage = cores
	snapshot.CPUCores = len(cores)
	c.previousCPU, c.haveCPU = next, len(next) > 0
}
