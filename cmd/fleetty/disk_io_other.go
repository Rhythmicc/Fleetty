//go:build !linux

package main

func readDiskIO() (uint64, uint64, error) {
	return 0, 0, nil
}
