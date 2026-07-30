//go:build darwin

package main

import "testing"

func TestDarwinStorageMountClassification(t *testing.T) {
	for _, fileSystem := range []string{"nfs", "smbfs", "webdav", "sshfs"} {
		if got := excludedDarwinStorageFileSystem(fileSystem, "server:/share"); got != "remote" {
			t.Fatalf("%s classified as %q, want remote", fileSystem, got)
		}
	}
	for _, fileSystem := range []string{"devfs", "autofs"} {
		if got := excludedDarwinStorageFileSystem(fileSystem, "devfs"); got != "virtual" {
			t.Fatalf("%s classified as %q, want virtual", fileSystem, got)
		}
	}
	if got := excludedDarwinStorageFileSystem("apfs", "/dev/disk3s1"); got != "" {
		t.Fatalf("APFS classified as %q, want local", got)
	}
}
