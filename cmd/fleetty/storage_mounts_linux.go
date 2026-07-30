//go:build linux

package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func localStorageMountPolicy() (storageMountPolicy, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return storageMountPolicy{
			excluded: defaultLinuxVirtualMounts(),
		}, errors.New("could not inspect mount types; using conservative virtual mount exclusions")
	}
	defer file.Close()

	policy := storageMountPolicy{excluded: make(map[string]string)}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		left, right, ok := strings.Cut(scanner.Text(), " - ")
		if !ok {
			continue
		}
		leftFields := strings.Fields(left)
		rightFields := strings.Fields(right)
		if len(leftFields) < 5 || len(rightFields) < 1 {
			continue
		}
		mountPoint := filepath.Clean(unescapeLinuxMountField(leftFields[4]))
		fileSystem := strings.ToLower(rightFields[0])
		if reason := excludedStorageFileSystem(fileSystem); reason != "" {
			policy.excluded[mountPoint] = reason
		}
	}
	if err := scanner.Err(); err != nil {
		return policy, err
	}
	return policy, nil
}

func defaultLinuxVirtualMounts() map[string]string {
	return map[string]string{
		"/proc": "virtual", "/sys": "virtual", "/dev": "virtual", "/run": "virtual",
	}
}

func unescapeLinuxMountField(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\\' && index+3 < len(value) {
			if number, err := strconv.ParseUint(value[index+1:index+4], 8, 8); err == nil {
				output.WriteByte(byte(number))
				index += 4
				continue
			}
		}
		output.WriteByte(value[index])
		index++
	}
	return output.String()
}

func excludedStorageFileSystem(fileSystem string) string {
	switch strings.ToLower(strings.TrimSpace(fileSystem)) {
	case "nfs", "nfs4", "cifs", "smb3", "smbfs", "sshfs", "fuse.sshfs",
		"9p", "afs", "ceph", "glusterfs", "lustre", "orangefs", "davfs",
		"fuse.rclone":
		return "remote"
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "ramfs", "cgroup",
		"cgroup2", "pstore", "securityfs", "debugfs", "tracefs", "configfs",
		"hugetlbfs", "mqueue", "autofs", "fusectl", "binfmt_misc", "nsfs",
		"bpf", "efivarfs":
		return "virtual"
	default:
		return ""
	}
}
