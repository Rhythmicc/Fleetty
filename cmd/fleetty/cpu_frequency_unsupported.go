//go:build !linux && !(darwin && arm64)

package main

func newPlatformCPUFrequencyReader() cpuFrequencyReader { return nil }
