package main

import (
	stdbytes "bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func progressPeer(conn *net.UnixConn) (pid, uid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if socketErr == nil {
			pid, uid = int(cred.Pid), int(cred.Uid)
		}
	})
	if err == nil {
		err = socketErr
	}
	return
}

func processProgressIdentity(pid int) (string, int, error) {
	// Read stat and ownership through the same proc directory so a reused
	// PID cannot mix fields from a different process.
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return "", -1, err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return "", -1, err
	}
	stat, err := root.ReadFile("stat")
	if err != nil {
		return "", -1, err
	}
	start, err := parseProcessStartTicks(stat)
	return fmt.Sprint(start), int(info.Sys().(*syscall.Stat_t).Uid), err
}

func processSlurmJobID(pid, uid int, start string) uint32 {
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return 0
	}
	defer root.Close()
	return slurmJobIDFromProc(root, uid, start)
}

func slurmJobIDFromProc(root *os.Root, uid int, start string) uint32 {
	info, err := root.Stat(".")
	if err != nil || int(info.Sys().(*syscall.Stat_t).Uid) != uid {
		return 0
	}
	stat, err := root.ReadFile("stat")
	if err != nil {
		return 0
	}
	ticks, err := parseProcessStartTicks(stat)
	if err != nil || strconv.FormatUint(ticks, 10) != start {
		return 0
	}
	state := stat[stdbytes.LastIndexByte(stat, ')')+2]
	if state == 'Z' || state == 'X' {
		return 0
	}
	cgroup, err := root.ReadFile("cgroup")
	if err != nil {
		return 0
	}
	return slurmCgroupJobID(string(cgroup))
}

func processProgressFileIdentity(pid int) (string, int, error) {
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return "", -1, err
	}
	defer root.Close()
	stat, err := root.ReadFile("stat")
	if err != nil {
		return "", -1, err
	}
	start, err := parseProcessStartTicks(stat)
	if err != nil {
		return "", -1, err
	}
	state := stat[stdbytes.LastIndexByte(stat, ')')+2]
	if state == 'Z' || state == 'X' {
		return "", -1, os.ErrNotExist
	}
	status, err := root.ReadFile("status")
	if err != nil {
		return "", -1, err
	}
	for line := range stdbytes.SplitSeq(status, []byte{'\n'}) {
		if fields := stdbytes.Fields(line); len(fields) == 5 && string(fields[0]) == "Uid:" {
			uid, err := strconv.ParseUint(string(fields[2]), 10, 32)
			return strconv.FormatUint(start, 10), int(uid), err
		}
	}
	return "", -1, errors.New("target process UID unavailable")
}

func openProcessProgressFile(pid int, path string, uid int) (*os.File, error) {
	root, err := unix.Open(fmt.Sprintf("/proc/%d/root", pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	// O_PATH checks the resolved inode without opening a possible device, FIFO,
	// or other nonregular file. Resolve the registered path inside the target's
	// mount namespace and reject symlinks in any component.
	pathFD, err := unix.Openat2(root, path, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	defer unix.Close(pathFD)
	var stat unix.Stat_t
	if err := unix.Fstat(pathFD, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != uid {
		return nil, errors.New("progress file must be regular and owned by the target UID")
	}
	// Reopen the checked inode rather than resolving the workload's path again.
	return os.Open(fmt.Sprintf("/proc/self/fd/%d", pathFD))
}
