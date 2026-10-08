//go:build linux

package main

import (
	"net"
	"os"
	"testing"
)

func TestLinuxCollectorFindsOwnTCPAndUDPSockets(t *testing.T) {
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
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	var snapshot monitorSnapshot
	c := &metricsCollector{}
	c.collectLinuxNetworkConnections(&snapshot)
	for _, address := range []string{listener.Addr().String(), udp.LocalAddr().String()} {
		found := false
		for _, connection := range snapshot.NetworkConnections {
			if connection.PID == os.Getpid() && connection.Local == address && connection.Name != "--" {
				found = true
			}
		}
		if !found {
			t.Fatalf("own socket %s missing (error=%s)", address, snapshot.NetworkProcessError)
		}
	}
	found := false
	for _, connection := range snapshot.NetworkConnections {
		if connection.PID == os.Getpid() && connection.Local == client.LocalAddr().String() && connection.Remote == client.RemoteAddr().String() && connection.State == "ESTABLISHED" {
			found = true
		}
	}
	if !found {
		t.Fatal("established TCP connection missing")
	}
}
