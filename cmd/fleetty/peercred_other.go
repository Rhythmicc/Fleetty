//go:build !linux

package main

import (
	"errors"
	"net"
)

type peerCredentials struct {
	PID int32
	UID uint32
	GID uint32
}

func readPeerCredentials(_ *net.UnixConn) (peerCredentials, error) {
	return peerCredentials{}, errors.New("peer credentials are unsupported on this platform")
}
