//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxSocketCountersTrackBidirectionalPayload(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := connectionNetworkKey("TCP", client.LocalAddr().String(), client.RemoteAddr().String())
	baseline, err := readLinuxConnectionCounters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline[key].available || baseline[key].uid != uint32(os.Getuid()) {
		t.Fatalf("own socket counters/UID unavailable: %#v", baseline[key])
	}
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(make([]byte, 16384)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(server, make([]byte, 16384)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write(make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	current, err := readLinuxConnectionCounters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, next := baseline[key], current[key]
	if first.cookie != next.cookie || next.rx-first.rx != 8192 || next.tx-first.tx != 16384 {
		t.Fatalf("incorrect payload deltas: before=%#v after=%#v", first, next)
	}
}

func TestParseSocketDiagnosticCounters(t *testing.T) {
	data := make([]byte, 72+4+208)
	data[0], data[1] = unix.AF_INET, 1
	binary.BigEndian.PutUint16(data[4:6], 1234)
	binary.BigEndian.PutUint16(data[6:8], 443)
	copy(data[8:12], []byte{127, 0, 0, 1})
	copy(data[24:28], []byte{192, 0, 2, 1})
	binary.NativeEndian.PutUint64(data[44:52], 99)
	binary.NativeEndian.PutUint32(data[64:68], 1000)
	binary.NativeEndian.PutUint16(data[72:74], 212)
	binary.NativeEndian.PutUint16(data[74:76], inetDiagInfo)
	binary.NativeEndian.PutUint64(data[76+128:76+136], 12345)
	binary.NativeEndian.PutUint64(data[76+200:76+208], 67890)
	key, counters, err := parseConnectionCounters(data, unix.IPPROTO_TCP)
	if err != nil || key != "TCP/127.0.0.1:1234/192.0.2.1:443" || counters.rx != 12345 || counters.tx != 67890 || counters.uid != 1000 || counters.cookie != 99 || !counters.available {
		t.Fatalf("incorrect kernel ABI decoding: %s %#v %v", key, counters, err)
	}
	_, udp, err := parseConnectionCounters(data, unix.IPPROTO_UDP)
	if err != nil || udp.available {
		t.Fatal("TCP info used as UDP counters")
	}
	data[1] = 10
	_, listener, err := parseConnectionCounters(data, unix.IPPROTO_TCP)
	if err != nil || listener.available {
		t.Fatal("listener shown as traffic socket")
	}
	data[1] = 1
	for _, length := range []int{0, 71, 73, 75, len(data) - 1} {
		if _, _, err := parseConnectionCounters(data[:length], unix.IPPROTO_TCP); err == nil {
			t.Fatalf("short data accepted: %d", length)
		}
	}
	data[0] = unix.AF_INET6
	copy(data[8:24], []byte{0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	copy(data[24:40], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	key, _, err = parseConnectionCounters(data, unix.IPPROTO_TCP)
	if err != nil || key != "TCP6/[2001:db8::1]:1234/[::1]:443" {
		t.Fatalf("IPv6 key: %s %v", key, err)
	}
}
