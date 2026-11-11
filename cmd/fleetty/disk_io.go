package main

import (
	"strconv"
	"strings"
	"time"
)

func (c *metricsCollector) collectDiskIO(snapshot *monitorSnapshot) {
	read, write, err := readDiskIO()
	if err != nil {
		return
	}
	now := time.Now()
	if c.haveDiskIO {
		seconds := now.Sub(c.lastDiskIOAt).Seconds()
		if seconds > 0 {
			snapshot.DiskRead = uint64(float64(counterDelta(read, c.previousDiskRead)) / seconds)
			snapshot.DiskWrite = uint64(float64(counterDelta(write, c.previousDiskWrite)) / seconds)
		}
	}
	c.previousDiskRead = read
	c.previousDiskWrite = write
	c.lastDiskIOAt = now
	c.haveDiskIO = true
}

// parseDiskstats sums bytes for whole disks. Partitions are skipped so a
// volume is not counted twice. Sector size in /proc/diskstats is 512 bytes.
func parseDiskstats(text string) (readBytes, writeBytes uint64) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !wholeDisk(fields[2]) {
			continue
		}
		sectorsRead, _ := strconv.ParseUint(fields[5], 10, 64)
		sectorsWritten, _ := strconv.ParseUint(fields[9], 10, 64)
		readBytes += sectorsRead * 512
		writeBytes += sectorsWritten * 512
	}
	return readBytes, writeBytes
}

func wholeDisk(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "fd"), strings.HasPrefix(name, "sr"),
		strings.HasPrefix(name, "dm-"), strings.HasPrefix(name, "md"):
		return false
	}
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		return !strings.Contains(name, "p")
	}
	if name == "" {
		return false
	}
	last := name[len(name)-1]
	return last < '0' || last > '9'
}
