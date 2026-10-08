//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const inetDiagInfo = 2

// INET_DIAG is the same kernel interface used by ss. TCP_INFO reports
// per-socket payload bytes, not NIC bytes or receive/send queue lengths.
func readLinuxConnectionCounters(ctx context.Context) (map[string]connectionNetworkCounters, error) {
	result := make(map[string]connectionNetworkCounters)
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_INET_DIAG)
	if err != nil {
		return result, err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return result, err
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 200000}); err != nil {
		return result, err
	}
	seq := uint32(0)
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		for _, protocol := range []int{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
			seq++
			if err := dumpConnectionCounters(ctx, fd, family, protocol, seq, result); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func dumpConnectionCounters(ctx context.Context, fd, family, protocol int, seq uint32, result map[string]connectionNetworkCounters) error {
	// nlmsghdr (16 bytes) followed by inet_diag_req_v2 (56 bytes).
	request := make([]byte, 72)
	binary.NativeEndian.PutUint32(request[0:4], uint32(len(request)))
	binary.NativeEndian.PutUint16(request[4:6], unix.SOCK_DIAG_BY_FAMILY)
	binary.NativeEndian.PutUint16(request[6:8], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(request[8:12], seq)
	request[16], request[17] = byte(family), byte(protocol)
	request[18] = 1 << (inetDiagInfo - 1)
	binary.NativeEndian.PutUint32(request[20:24], ^uint32(0)) // all states
	binary.NativeEndian.PutUint64(request[64:72], ^uint64(0)) // no socket cookie filter
	if err := unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, from, err := unix.Recvfrom(fd, buffer, 0)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return err
		}
		if sender, ok := from.(*unix.SockaddrNetlink); !ok || sender.Pid != 0 {
			return fmt.Errorf("unexpected socket diagnostics sender")
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:n])
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.Header.Seq != seq {
				continue
			}
			if message.Header.Flags&unix.NLM_F_DUMP_INTR != 0 {
				return fmt.Errorf("socket diagnostics dump interrupted")
			}
			switch message.Header.Type {
			case unix.NLMSG_DONE:
				if len(message.Data) >= 4 {
					if code := int32(binary.NativeEndian.Uint32(message.Data[:4])); code < 0 {
						return syscall.Errno(-code)
					}
				}
				return nil
			case unix.NLMSG_ERROR:
				if len(message.Data) < 4 {
					return fmt.Errorf("short socket diagnostics error")
				}
				if code := int32(binary.NativeEndian.Uint32(message.Data[:4])); code != 0 {
					return syscall.Errno(-code)
				}
			case unix.SOCK_DIAG_BY_FAMILY:
				key, counters, err := parseConnectionCounters(message.Data, protocol)
				if err != nil {
					return err
				}
				result[key] = counters
			}
		}
	}
}

func parseConnectionCounters(data []byte, protocol int) (string, connectionNetworkCounters, error) {
	// Fixed inet_diag_msg layout from linux/inet_diag.h. Multi-byte socket
	// addresses/ports use network byte order; counters use native byte order.
	if len(data) < 72 {
		return "", connectionNetworkCounters{}, fmt.Errorf("short socket diagnostics message")
	}
	addressSize := 4
	name := "TCP"
	if protocol == unix.IPPROTO_UDP {
		name = "UDP"
	}
	if data[0] == unix.AF_INET6 {
		addressSize = 16
		name += "6"
	}
	local := net.JoinHostPort(net.IP(data[8:8+addressSize]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(data[4:6]))))
	remote := "--"
	if port := binary.BigEndian.Uint16(data[6:8]); port != 0 {
		remote = net.JoinHostPort(net.IP(data[24:24+addressSize]).String(), strconv.Itoa(int(port)))
	}
	counters := connectionNetworkCounters{cookie: binary.NativeEndian.Uint64(data[44:52]), uid: binary.NativeEndian.Uint32(data[64:68])}
	for attributes := data[72:]; len(attributes) > 0; {
		if len(attributes) < 4 {
			return "", counters, fmt.Errorf("short socket diagnostics attribute")
		}
		length := int(binary.NativeEndian.Uint16(attributes[:2]))
		if length < 4 || length > len(attributes) {
			return "", counters, fmt.Errorf("invalid socket diagnostics attribute length")
		}
		kind := binary.NativeEndian.Uint16(attributes[2:4]) & 0x3fff
		info := attributes[4:length]
		// tcp_info: bytes_received at offset 128 and bytes_sent at 200.
		// Do not substitute bytes_acked (which measures ACKed rather than sent data).
		if protocol == unix.IPPROTO_TCP && kind == inetDiagInfo && len(info) >= 208 && data[1] != 10 {
			counters.rx = binary.NativeEndian.Uint64(info[128:136])
			counters.tx = binary.NativeEndian.Uint64(info[200:208])
			counters.available = true
		}
		aligned := (length + 3) &^ 3
		if aligned > len(attributes) {
			return "", counters, fmt.Errorf("short socket diagnostics padding")
		}
		attributes = attributes[aligned:]
	}
	return connectionNetworkKey(name, local, remote), counters, nil
}
