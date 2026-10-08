//go:build linux

package main

import (
	"io"
	"net"
	"os"
	"os/user"
	"testing"
	"time"
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
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
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
	var next monitorSnapshot
	c.collectLinuxNetworkConnections(&next)
	for _, connection := range next.NetworkConnections {
		if connection.Local == client.LocalAddr().String() && connection.Remote == client.RemoteAddr().String() {
			if connection.User != owner.Username || !connection.TrafficAvailable || !connection.RateAvailable || connection.RXTotal < 8192 || connection.TXTotal < 16384 || connection.RX == 0 || connection.TX == 0 {
				t.Fatalf("bidirectional TCP traffic/user missing: %#v (warning=%s)", connection, next.NetworkProcessError)
			}
			return
		}
	}
	t.Fatal("TCP traffic socket missing after transfer")
}
