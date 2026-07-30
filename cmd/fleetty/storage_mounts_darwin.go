//go:build darwin

package main

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func localStorageMountPolicy() (storageMountPolicy, error) {
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return storageMountPolicy{
			excluded: make(map[string]string), mounts: make(map[string]string),
		}, err
	}
	if count == 0 {
		return storageMountPolicy{
			excluded: make(map[string]string), mounts: make(map[string]string),
		}, nil
	}
	mounts := make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(mounts, unix.MNT_NOWAIT)
	if err != nil {
		return storageMountPolicy{
			excluded: make(map[string]string), mounts: make(map[string]string),
		}, err
	}
	if count > len(mounts) {
		return storageMountPolicy{
				excluded: make(map[string]string), mounts: make(map[string]string),
			},
			errors.New("mount table changed while it was being inspected")
	}
	policy := storageMountPolicy{
		excluded: make(map[string]string),
		mounts:   make(map[string]string),
	}
	for _, mount := range mounts[:count] {
		fileSystem := strings.ToLower(unix.ByteSliceToString(mount.Fstypename[:]))
		mountPoint := filepath.Clean(unix.ByteSliceToString(mount.Mntonname[:]))
		source := unix.ByteSliceToString(mount.Mntfromname[:])
		policy.mounts[mountPoint] = fileSystem
		reason := excludedDarwinStorageFileSystem(fileSystem, source)
		if reason != "" {
			policy.excluded[mountPoint] = reason
		}
	}
	return policy, nil
}

func excludedDarwinStorageFileSystem(fileSystem, source string) string {
	switch strings.ToLower(strings.TrimSpace(fileSystem)) {
	case "nfs", "smbfs", "webdav", "afpfs", "sshfs", "osxfuse":
		return "remote"
	case "devfs", "autofs", "procfs", "tmpfs":
		return "virtual"
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.HasPrefix(source, "//") || strings.Contains(source, "@") && strings.Contains(source, ":/") {
		return "remote"
	}
	return ""
}
