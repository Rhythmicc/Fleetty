package main

import "os"

type linuxCPUFrequencyReader struct{}

func newPlatformCPUFrequencyReader() cpuFrequencyReader { return linuxCPUFrequencyReader{} }

func (linuxCPUFrequencyReader) read() (map[string]float64, string) {
	// gopsutil Info.Mhz prefers cpuinfo_max_freq, not the current clock.
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return nil, "/proc/cpuinfo"
	}
	return parseCPUFrequencies(string(data)), "/proc/cpuinfo"
}
