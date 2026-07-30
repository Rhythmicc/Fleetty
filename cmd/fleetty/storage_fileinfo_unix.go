//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"syscall"
)

func storageAllocatedSize(info os.FileInfo) (uint64, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return uint64(max64(0, info.Size())), ""
	}
	allocated := uint64(0)
	if stat.Blocks > 0 {
		allocated = uint64(stat.Blocks) * 512
	}
	return allocated, fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

func storageFileIdentity(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	_, inode := storageAllocatedSize(info)
	if inode == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d:%d",
		inode, info.Size(), info.ModTime().UnixNano(), uint32(info.Mode().Type()))
}

func storageFileOwner(info os.FileInfo) (uint32, uint32, bool) {
	if info == nil {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Uid, stat.Gid, true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
