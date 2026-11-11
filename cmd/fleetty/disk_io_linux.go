//go:build linux

package main

import "os"

func readDiskIO() (uint64, uint64, error) {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0, err
	}
	read, write := parseDiskstats(string(b))
	return read, write, nil
}
