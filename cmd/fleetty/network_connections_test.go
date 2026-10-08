package main

import (
	"reflect"
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
	for _, want := range []string{"sync-worker", "ESTABLISHED", "USER", "DOWN", "UP", "LOCAL", "REMOTE", "[2001:db8::1]:443"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %s: %s", want, plain)
		}
	}
	if panel.title != "PROCESS CONNECTIONS" || !strings.Contains(panel.meta, "TCP PAYLOAD") || !strings.Contains(panel.meta, "UDP --") || !strings.Contains(panel.meta, "SORT ↓ DOWN+UP") {
		t.Fatalf("incorrect connection labels: %#v", panel)
	}
	exported := exportSnapshot(m.snapshot)
	if !exported.NetworkConnectionMode || len(exported.NetworkConnections) != 3 {
		t.Fatal("snapshot lost network connections")
	}
}

func TestConnectionSortUsesCombinedRateNotDownloadOrLifetimeTraffic(t *testing.T) {
	now := time.Now()
	c := &metricsCollector{lastProcessNetAt: now.Add(-time.Second), previousConnectionNet: make(map[string]connectionNetworkCounters)}
	connections := []networkConnectionInfo{
		{PID: 1, Protocol: "TCP", Local: "127.0.0.1:1", Remote: "127.0.0.1:9"},
		{PID: 2, Protocol: "TCP", Local: "127.0.0.1:2", Remote: "127.0.0.1:9"},
		{PID: 3, Protocol: "TCP", Local: "127.0.0.1:3", Remote: "127.0.0.1:9"},
	}
	current := make(map[string]connectionNetworkCounters)
	for i, connection := range connections {
		key := connectionNetworkKey(connection.Protocol, connection.Local, connection.Remote)
		previous := connectionNetworkCounters{cookie: uint64(i + 1), available: true}
		if i == 0 {
			previous.rx = 1 << 40 // Lifetime volume must not override current rate.
		}
		c.previousConnectionNet[key] = previous
		next := previous
		next.rx += []uint64{50, 31, 40}[i]
		next.tx += []uint64{0, 40, 10}[i]
		current[key] = next
	}
	c.applyConnectionTraffic(connections, current, now)
	for i, want := range []int{2, 1, 3} {
		if connections[i].PID != want {
			t.Fatalf("combined rate/stable tie sort: %#v", connections)
		}
	}
}

func TestNetworkRateColorLevels(t *testing.T) {
	for _, direction := range []lipgloss.Style{networkRXStyle, networkTXStyle} {
		for _, test := range []struct {
			rate      uint64
			available bool
			want      lipgloss.Style
		}{
			{1 << 30, false, dimStyle}, {0, true, dimStyle}, {1023, true, dimStyle},
			{1024, true, direction}, {100*1024 - 1, true, direction},
			{100 * 1024, true, cpuTitleStyle}, {1024*1024 - 1, true, cpuTitleStyle},
			{1024 * 1024, true, warningStyle}, {10*1024*1024 - 1, true, warningStyle},
			{10 * 1024 * 1024, true, diskTitleStyle},
		} {
			got := networkRateStyle(test.rate, test.available, direction)
			if !reflect.DeepEqual(got.GetForeground(), test.want.GetForeground()) {
				t.Errorf("rate %d available %v has incorrect color", test.rate, test.available)
			}
		}
	}
}

func TestConnectionTrafficRatesAndSocketReuse(t *testing.T) {
	now := time.Now()
	c := &metricsCollector{lastProcessNetAt: now.Add(-2 * time.Second)}
	connections := []networkConnectionInfo{
		{PID: 10, Name: "worker", Protocol: "TCP", Local: "127.0.0.1:1234", Remote: "127.0.0.1:4321"},
		{PID: 10, Name: "worker", Protocol: "TCP", Local: "127.0.0.1:1235", Remote: "127.0.0.1:4321"},
		{PID: 10, Name: "worker", Protocol: "UDP", Local: "127.0.0.1:5353", Remote: "--"},
	}
	first := connectionNetworkKey("TCP", connections[0].Local, connections[0].Remote)
	second := connectionNetworkKey("TCP", connections[1].Local, connections[1].Remote)
	c.previousConnectionNet = map[string]connectionNetworkCounters{
		first:  {cookie: 1, rx: 1000, tx: 2000, available: true},
		second: {cookie: 2, rx: 1000, tx: 2000, available: true},
	}
	current := map[string]connectionNetworkCounters{
		first:  {cookie: 1, rx: 3048, tx: 6096, available: true},
		second: {cookie: 2, rx: 11000, tx: 22000, available: true},
	}
	c.applyConnectionTraffic(connections, current, now)
	if connections[0].Local != "127.0.0.1:1235" || connections[0].RX != 5000 || connections[0].TX != 10000 {
		t.Fatalf("busy connection not ranked first: %#v", connections)
	}
	if connections[1].RX != 1024 || connections[1].TX != 2048 || !connections[1].RateAvailable || connections[1].User == "" {
		t.Fatalf("per-socket rate or user missing: %#v", connections[1])
	}
	row := ansi.Strip(renderNetworkConnectionRow(connections[1], 160))
	if !strings.Contains(row, "1.0 KiB/s") || !strings.Contains(row, "2.0 KiB/s") {
		t.Fatalf("missing real rates: %s", row)
	}
	if strings.Contains(ansi.Strip(renderNetworkConnectionRow(connections[2], 160)), "0 B/s") {
		t.Fatal("UDP unknown shown as zero")
	}
	for _, replacement := range []connectionNetworkCounters{
		{cookie: 3, rx: 20000, tx: 40000, available: true},
		{cookie: 1, rx: 1, tx: 2, available: true},
	} {
		fresh := []networkConnectionInfo{{Protocol: "TCP", Local: "127.0.0.1:1234", Remote: "127.0.0.1:4321"}}
		c.previousConnectionNet = current
		c.applyConnectionTraffic(fresh, map[string]connectionNetworkCounters{first: replacement}, now)
		if fresh[0].RateAvailable || fresh[0].RX != 0 || fresh[0].TX != 0 {
			t.Fatal("socket reuse/reset generated a bogus rate")
		}
		if !strings.Contains(ansi.Strip(renderNetworkConnectionRow(fresh[0], 160)), "sample") {
			t.Fatal("warmup hidden")
		}
	}
	exported := exportSnapshot(monitorSnapshot{NetworkConnectionMode: true, NetworkConnections: connections})
	if exported.NetworkConnections[1].RX != 1024 || exported.NetworkConnections[1].User == "" {
		t.Fatal("traffic/user lost on export")
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
