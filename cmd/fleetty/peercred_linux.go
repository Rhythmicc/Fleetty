//go:build linux

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

type peerCredentials struct {
	PID int32
	UID uint32
	GID uint32
}

func readPeerCredentials(connection *net.UnixConn) (peerCredentials, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return peerCredentials{}, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return peerCredentials{}, err
	}
	if socketErr != nil {
		return peerCredentials{}, socketErr
	}
	return peerCredentials{PID: credentials.Pid, UID: credentials.Uid, GID: credentials.Gid}, nil
}
