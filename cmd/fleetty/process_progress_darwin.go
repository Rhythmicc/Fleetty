package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func progressPeer(conn *net.UnixConn) (pid, uid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if socketErr != nil {
			return
		}
		cred, credErr := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr != nil {
			socketErr = credErr
			return
		}
		uid = int(cred.Uid)
	})
	if err == nil {
		err = socketErr
	}
	return
}

func processProgressIdentity(pid int) (string, int, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", -1, err
	}
	if int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec == 0 {
		return "", -1, os.ErrNotExist
	}
	return fmt.Sprintf("%d.%06d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), int(info.Eproc.Ucred.Uid), nil
}

func processProgressFileIdentity(pid int) (string, int, error) {
	return "", -1, errors.New("progress files are available only on Linux")
}

func processSlurmJobID(_, _ int, _ string) uint32 { return 0 }

func openProcessProgressFile(pid int, path string, uid int) (*os.File, error) {
	return nil, errors.New("progress files are available only on Linux")
}
