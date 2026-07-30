//go:build linux

package main

import "testing"

func TestLinuxStorageMountClassificationAndEscaping(t *testing.T) {
	for _, fileSystem := range []string{"nfs4", "cifs", "fuse.sshfs", "ceph"} {
		if got := excludedStorageFileSystem(fileSystem); got != "remote" {
			t.Fatalf("%s classified as %q, want remote", fileSystem, got)
		}
	}
	for _, fileSystem := range []string{"proc", "tmpfs", "cgroup2", "devtmpfs"} {
		if got := excludedStorageFileSystem(fileSystem); got != "virtual" {
			t.Fatalf("%s classified as %q, want virtual", fileSystem, got)
		}
	}
	for _, fileSystem := range []string{"ext4", "xfs", "btrfs", "overlay"} {
		if got := excludedStorageFileSystem(fileSystem); got != "" {
			t.Fatalf("%s classified as %q, want local", fileSystem, got)
		}
	}
	if got := unescapeLinuxMountField(`/mnt/lab\040storage`); got != "/mnt/lab storage" {
		t.Fatalf("mount path unescaped as %q", got)
	}
}
