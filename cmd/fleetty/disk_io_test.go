package main

import "testing"

func TestParseDiskstatsSkipsPartitions(t *testing.T) {
	text := `
   8       0 sda 1 0 100 0 2 0 50 0 0 0 0
   8       1 sda1 9 0 9000 0 9 0 9000 0 0 0 0
 259       0 nvme0n1 1 0 10 0 1 0 4 0 0 0 0
 259       1 nvme0n1p1 9 0 9000 0 9 0 9000 0 0 0 0
   7       0 loop0 9 0 9000 0 9 0 9000 0 0 0 0
 253       0 dm-0 9 0 9000 0 9 0 9000 0 0 0 0
 179       0 mmcblk0 1 0 8 0 1 0 2 0 0 0 0
 179       1 mmcblk0p1 9 0 9000 0 9 0 9000 0 0 0 0
`
	read, write := parseDiskstats(text)
	if read != (100+10+8)*512 || write != (50+4+2)*512 {
		t.Fatalf("disk io = %d read, %d write", read, write)
	}
}
