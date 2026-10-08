package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

type networkConnectionInfo struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
}

func (c *metricsCollector) collectLinuxNetworkConnections(snapshot *monitorSnapshot) {
	snapshot.NetworkConnectionMode = true
	if !c.lastProcessNetAt.IsZero() && time.Since(c.lastProcessNetAt) < c.processNetRefreshInterval {
		snapshot.NetworkConnections = append([]networkConnectionInfo(nil), c.cachedNetworkConnections...)
		snapshot.NetworkProcessError = c.cachedConnectionWarning
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connections, err := gopsnet.ConnectionsWithoutUidsWithContext(ctx, "inet")
	if err != nil {
		snapshot.NetworkProcessError = "Cannot read TCP/UDP connections: " + sanitizeTerminalText(err.Error())
		return
	}
	names := make(map[int32]string)
	unattributed := false
	for _, connection := range connections {
		if _, ok := names[connection.Pid]; !ok {
			name := "--"
			if connection.Pid > 0 {
				if p, err := process.NewProcessWithContext(ctx, connection.Pid); err == nil {
					if value, err := p.NameWithContext(ctx); err == nil {
						name = sanitizeTerminalText(value)
					}
				}
			}
			names[connection.Pid] = name
		}
		if connection.Pid == 0 && connection.Status != "TIME_WAIT" {
			unattributed = true
		}
	}
	snapshot.NetworkConnections = summarizeNetworkConnections(connections, names)
	if unattributed {
		snapshot.NetworkProcessError = "Some owners are unavailable; root can inspect other users' sockets."
	}
	c.cachedNetworkConnections = append([]networkConnectionInfo(nil), snapshot.NetworkConnections...)
	c.cachedConnectionWarning = snapshot.NetworkProcessError
	c.lastProcessNetAt = time.Now()
}

func summarizeNetworkConnections(connections []gopsnet.ConnectionStat, names map[int32]string) []networkConnectionInfo {
	result := make([]networkConnectionInfo, 0, len(connections))
	for _, c := range connections {
		protocol := "TCP"
		if c.Type == syscall.SOCK_DGRAM {
			protocol = "UDP"
		}
		if c.Family == syscall.AF_INET6 {
			protocol += "6"
		}
		state := c.Status
		if state == "NONE" || state == "" {
			state = "UNCONN"
		}
		remote := "--"
		if c.Raddr.Port != 0 {
			remote = net.JoinHostPort(c.Raddr.IP, strconv.FormatUint(uint64(c.Raddr.Port), 10))
		}
		result = append(result, networkConnectionInfo{
			PID: int(c.Pid), Name: names[c.Pid], Protocol: protocol, State: state,
			Local: net.JoinHostPort(c.Laddr.IP, strconv.FormatUint(uint64(c.Laddr.Port), 10)), Remote: remote,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if (a.State == "ESTABLISHED") != (b.State == "ESTABLISHED") {
			return a.State == "ESTABLISHED"
		}
		if (a.PID > 0) != (b.PID > 0) {
			return a.PID > 0
		}
		if a.PID != b.PID {
			return a.PID < b.PID
		}
		return a.Protocol+"/"+a.Local+"/"+a.Remote+"/"+a.State < b.Protocol+"/"+b.Local+"/"+b.Remote+"/"+b.State
	})
	return result
}

func networkConnectionCells(width int, values []string) string {
	sizes := []int{6, 8, 4, 5, max(5, width-27)}
	if width >= 100 {
		endpoint := (width - 43) / 2
		sizes = []int{7, 16, 4, 11, endpoint, width - 43 - endpoint}
	} else if width >= 64 {
		sizes = []int{7, 14, 4, 11, width - 40}
	}
	cells := make([]string, len(sizes))
	for i, size := range sizes {
		cells[i] = fixedCell(values[i], size, false)
	}
	return ansi.Truncate(strings.Join(cells, " "), width, "")
}

func networkConnectionHeader(width int) string {
	values := []string{"PID", "PROCESS", "TYPE", "STATE", "REMOTE"}
	if width >= 100 {
		values = []string{"PID", "PROCESS", "TYPE", "STATE", "LOCAL", "REMOTE"}
	}
	return dimStyle.Copy().Bold(true).Render(networkConnectionCells(width, values))
}

func renderNetworkConnectionRow(connection networkConnectionInfo, width int) string {
	pid := "--"
	if connection.PID > 0 {
		pid = strconv.Itoa(connection.PID)
	}
	state := connection.State
	if width < 64 {
		switch state {
		case "ESTABLISHED":
			state = "ESTAB"
		case "LISTEN":
			state = "LISTN"
		case "TIME_WAIT":
			state = "TWAIT"
		case "UNCONN":
			state = "UNCON"
		}
	}
	values := []string{pid, sanitizeTerminalText(connection.Name), connection.Protocol, state, connection.Remote}
	if width >= 100 {
		values = []string{pid, sanitizeTerminalText(connection.Name), connection.Protocol, state, connection.Local, connection.Remote}
	}
	return valueStyle.Render(networkConnectionCells(width, values))
}

func (m *monitorModel) networkApplicationCount() int {
	if m.snapshot.NetworkConnectionMode {
		return len(m.snapshot.NetworkConnections)
	}
	return len(m.snapshot.NetworkProcesses)
}

func (m *monitorModel) networkApplicationMeta() string {
	if m.snapshot.NetworkConnectionMode {
		return fmt.Sprintf("%d TCP/UDP · CONNECTIONS ONLY", m.networkApplicationCount())
	}
	return fmt.Sprintf("%d ATTRIBUTED", m.networkApplicationCount())
}

func (m *monitorModel) networkApplicationHeader(width int) string {
	if m.snapshot.NetworkConnectionMode {
		return networkConnectionHeader(width)
	}
	return networkProcessHeader(width)
}

func (m *monitorModel) networkApplicationRows(width, limit int) []string {
	var lines []string
	limit = min(max(0, limit), m.networkApplicationCount())
	for i := 0; i < limit; i++ {
		if m.snapshot.NetworkConnectionMode {
			lines = append(lines, renderNetworkConnectionRow(m.snapshot.NetworkConnections[i], width))
		} else {
			lines = append(lines, renderNetworkProcessRow(m.snapshot.NetworkProcesses[i], width))
		}
	}
	return lines
}
