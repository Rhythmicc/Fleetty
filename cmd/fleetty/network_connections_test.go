package main

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	gopsnet "github.com/shirou/gopsutil/v4/net"
)

func TestNetworkConnectionSummaryAndRendering(t *testing.T) {
	connections := summarizeNetworkConnections([]gopsnet.ConnectionStat{
		{Pid: 10, Family: syscall.AF_INET, Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gopsnet.Addr{IP: "0.0.0.0", Port: 8080}},
		{Pid: 20, Family: syscall.AF_INET6, Type: syscall.SOCK_STREAM, Status: "ESTABLISHED", Laddr: gopsnet.Addr{IP: "::1", Port: 1234}, Raddr: gopsnet.Addr{IP: "2001:db8::1", Port: 443}},
		{Pid: 30, Family: syscall.AF_INET, Type: syscall.SOCK_DGRAM, Status: "NONE", Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 5353}},
	}, map[int32]string{10: "web-server", 20: "sync-worker", 30: "dns-client"})
	if connections[0].PID != 20 || connections[0].Protocol != "TCP6" || connections[0].Remote != "[2001:db8::1]:443" {
		t.Fatalf("active IPv6 connection: %#v", connections[0])
	}
	if connections[2].Protocol != "UDP" || connections[2].State != "UNCONN" || connections[2].Remote != "--" {
		t.Fatalf("UDP connection: %#v", connections[2])
	}
	for _, width := range []int{24, 36, 48, 64, 99, 100, 160, 300} {
		for _, connection := range connections {
			row := renderNetworkConnectionRow(connection, width)
			if lipgloss.Width(row) > width || lipgloss.Width(networkConnectionHeader(width)) > width {
				t.Fatalf("connection table overflows width %d", width)
			}
			if strings.Contains(row, "B/s") {
				t.Fatal("connections presented as traffic counters")
			}
		}
	}
	m := &monitorModel{snapshot: monitorSnapshot{NetworkConnectionMode: true, NetworkConnections: connections}}
	panel := m.networkApplicationsPanelSpec(160, 3)
	plain := ansi.Strip(strings.Join(panel.lines, "\n"))
	for _, want := range []string{"sync-worker", "ESTABLISHED", "LOCAL", "REMOTE", "[2001:db8::1]:443"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %s: %s", want, plain)
		}
	}
	if panel.title != "PROCESS CONNECTIONS" || !strings.Contains(panel.meta, "CONNECTIONS ONLY") {
		t.Fatalf("incorrect connection labels: %#v", panel)
	}
	exported := exportSnapshot(m.snapshot)
	if !exported.NetworkConnectionMode || len(exported.NetworkConnections) != 3 {
		t.Fatal("snapshot lost network connections")
	}
}

func TestLinuxConnectionCacheAndEmptyState(t *testing.T) {
	c := &metricsCollector{
		lastProcessNetAt: time.Now(), processNetRefreshInterval: time.Minute,
		cachedNetworkConnections: []networkConnectionInfo{{PID: 42, Name: "worker"}},
		cachedConnectionWarning:  "Some owners are unavailable",
	}
	var snapshot monitorSnapshot
	c.collectLinuxNetworkConnections(&snapshot)
	if !snapshot.NetworkConnectionMode || len(snapshot.NetworkConnections) != 1 || snapshot.NetworkProcessError != c.cachedConnectionWarning {
		t.Fatalf("cache not reused: %#v", snapshot)
	}
	snapshot.NetworkConnections[0].Name = "modified"
	if c.cachedNetworkConnections[0].Name != "worker" {
		t.Fatal("cache aliases snapshot")
	}
	m := &monitorModel{snapshot: monitorSnapshot{NetworkConnectionMode: true}}
	panel := m.networkApplicationsPanelSpec(160, 10)
	if !strings.Contains(strings.Join(panel.lines, "\n"), "No TCP/UDP connections") {
		t.Fatal("empty connection state is misleading")
	}
	m.snapshot.NetworkProcessError = "Permission denied"
	if !strings.Contains(strings.Join(m.networkApplicationsPanelSpec(160, 10).lines, "\n"), "Permission denied") {
		t.Fatal("connection read error hidden")
	}
}
