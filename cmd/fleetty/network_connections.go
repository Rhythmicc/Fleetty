package main

import (
	"context"
	"fmt"
	"net"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

type networkConnectionInfo struct {
	PID              int    `json:"pid"`
	Name             string `json:"name"`
	Protocol         string `json:"protocol"`
	State            string `json:"state"`
	Local            string `json:"local"`
	Remote           string `json:"remote"`
	User             string `json:"user"`
	RX               uint64 `json:"rx_bytes_per_second"`
	TX               uint64 `json:"tx_bytes_per_second"`
	RXTotal          uint64 `json:"rx_bytes_total"`
	TXTotal          uint64 `json:"tx_bytes_total"`
	TrafficAvailable bool   `json:"traffic_available"`
	RateAvailable    bool   `json:"rate_available"`
}

func (c *metricsCollector) applyConnectionTraffic(connections []networkConnectionInfo, current map[string]connectionNetworkCounters, now time.Time) {
	seconds := now.Sub(c.lastProcessNetAt).Seconds()
	users := make(map[uint32]string)
	for i := range connections {
		connection := &connections[i]
		key := connectionNetworkKey(connection.Protocol, connection.Local, connection.Remote)
		counters, ok := current[key]
		if !ok {
			continue
		}
		if connection.State != "TIME_WAIT" {
			name, ok := users[counters.uid]
			if !ok {
				name = strconv.FormatUint(uint64(counters.uid), 10)
				if owner, err := user.LookupId(name); err == nil {
					name = sanitizeTerminalText(owner.Username)
				}
				users[counters.uid] = name
			}
			connection.User = name
		}
		connection.TrafficAvailable = counters.available
		connection.RXTotal, connection.TXTotal = counters.rx, counters.tx
		if previous, ok := c.previousConnectionNet[key]; ok && counters.available && previous.available &&
			previous.cookie == counters.cookie && !c.lastProcessNetAt.IsZero() && seconds > 0 &&
			counters.rx >= previous.rx && counters.tx >= previous.tx {
			connection.RateAvailable = true
			connection.RX = uint64(float64(counters.rx-previous.rx) / seconds)
			connection.TX = uint64(float64(counters.tx-previous.tx) / seconds)
		}
	}
	c.previousConnectionNet = current
	sort.SliceStable(connections, func(i, j int) bool {
		return connections[i].RX+connections[i].TX > connections[j].RX+connections[j].TX
	})
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
	owners := make(map[int32]string)
	unattributed := false
	for _, connection := range connections {
		if _, ok := names[connection.Pid]; !ok {
			name := "--"
			owner := "--"
			if connection.Pid > 0 {
				if p, err := process.NewProcessWithContext(ctx, connection.Pid); err == nil {
					if value, err := p.NameWithContext(ctx); err == nil {
						name = sanitizeTerminalText(value)
					}
					if value, err := p.UsernameWithContext(ctx); err == nil {
						owner = sanitizeTerminalText(value)
					}
				}
			}
			names[connection.Pid] = name
			owners[connection.Pid] = owner
		}
		if connection.Pid == 0 && connection.Status != "TIME_WAIT" {
			unattributed = true
		}
	}
	snapshot.NetworkConnections = summarizeNetworkConnections(connections, names)
	for i := range snapshot.NetworkConnections {
		snapshot.NetworkConnections[i].User = owners[int32(snapshot.NetworkConnections[i].PID)]
	}
	current, trafficErr := readLinuxConnectionCounters(ctx)
	now := time.Now()
	c.applyConnectionTraffic(snapshot.NetworkConnections, current, now)
	if unattributed {
		snapshot.NetworkProcessError = "Some owners are unavailable; root can inspect other users' sockets."
	}
	if trafficErr != nil {
		snapshot.NetworkProcessError = strings.TrimSpace(snapshot.NetworkProcessError + " TCP traffic unavailable: " + sanitizeTerminalText(trafficErr.Error()))
	}
	c.cachedNetworkConnections = append([]networkConnectionInfo(nil), snapshot.NetworkConnections...)
	c.cachedConnectionWarning = snapshot.NetworkProcessError
	c.lastProcessNetAt = now
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

type connectionColumn struct {
	label string
	size  int
}

func networkConnectionColumns(width int) []connectionColumn {
	columns := []connectionColumn{{"PID", 7}, {"PROCESS", 14}, {"USER", 10}, {"DOWN", 11}, {"UP", 11}}
	if width < 80 {
		columns = []connectionColumn{{"PID", 6}, {"PROCESS", 9}, {"USER", 8}, {"DOWN", 9}, {"UP", 9}}
	}
	if width < 52 {
		columns = []connectionColumn{{"PID", 5}, {"PROCESS", 7}, {"USER", 6}, {"DOWN", 7}, {"UP", 7}}
	}
	if width >= 100 {
		columns = append(columns, connectionColumn{"TYPE", 4}, connectionColumn{"STATE", 11})
	}
	used := len(columns)
	for _, col := range columns {
		used += col.size
	}
	if width >= 140 {
		localWidth := min(39, (width-used-1)/2)
		columns = append(columns, connectionColumn{"LOCAL", localWidth})
		used += localWidth + 1
	}
	if width > used+6 {
		columns = append(columns, connectionColumn{"REMOTE", width - used})
	}
	return columns
}

func networkConnectionCells(width int, values map[string]string, connection *networkConnectionInfo) string {
	header := connection == nil
	var cells []string
	for _, col := range networkConnectionColumns(width) {
		cell := fixedCell(values[col.label], col.size, !header && (col.label == "DOWN" || col.label == "UP"))
		if !header {
			switch col.label {
			case "DOWN":
				cell = networkRateStyle(connection.RX, connection.RateAvailable, networkRXStyle).Render(cell)
			case "UP":
				cell = networkRateStyle(connection.TX, connection.RateAvailable, networkTXStyle).Render(cell)
			case "USER", "TYPE", "STATE", "LOCAL":
				cell = dimStyle.Render(cell)
			default:
				cell = valueStyle.Render(cell)
			}
		}
		cells = append(cells, cell)
	}
	return ansi.Truncate(strings.Join(cells, " "), width, "")
}

func networkConnectionHeader(width int) string {
	values := make(map[string]string)
	for _, col := range networkConnectionColumns(width) {
		values[col.label] = col.label
	}
	values["DOWN"] = networkRXStyle.Render("DOWN")
	values["UP"] = networkTXStyle.Render("UP")
	return dimStyle.Copy().Bold(true).Render(networkConnectionCells(width, values, nil))
}

// These are absolute traffic levels, not link saturation or health alarms:
// the speed of a socket's underlying link is not known here.
func networkRateStyle(rate uint64, available bool, direction lipgloss.Style) lipgloss.Style {
	switch {
	case !available || rate < 1024:
		return dimStyle
	case rate >= 10*1024*1024:
		return diskTitleStyle
	case rate >= 1024*1024:
		return warningStyle
	case rate >= 100*1024:
		return cpuTitleStyle
	default:
		return direction
	}
}

func networkConnectionRateLegend(width int) string {
	return ansi.Truncate(dimStyle.Render("↓ DOWN+UP · RATE/s ")+
		dimStyle.Render("<1KiB ")+networkRXStyle.Render("1KiB+ ")+
		cpuTitleStyle.Render("100KiB+ ")+warningStyle.Render("1MiB+ ")+
		diskTitleStyle.Render("10MiB+"), width, "")
}

func renderNetworkConnectionRow(connection networkConnectionInfo, width int) string {
	pid := "--"
	if connection.PID > 0 {
		pid = strconv.Itoa(connection.PID)
	}
	down, up := "--", "--"
	if connection.TrafficAvailable {
		down, up = "sample…", "sample…"
		if connection.RateAvailable {
			down, up = bytes(connection.RX)+"/s", bytes(connection.TX)+"/s"
		}
	}
	owner := connection.User
	if owner == "" {
		owner = "--"
	}
	values := map[string]string{"PID": pid, "PROCESS": sanitizeTerminalText(connection.Name), "USER": sanitizeTerminalText(owner), "TYPE": connection.Protocol, "STATE": connection.State, "LOCAL": connection.Local, "REMOTE": connection.Remote, "DOWN": down, "UP": up}
	return networkConnectionCells(width, values, &connection)
}

func (m *monitorModel) networkApplicationCount() int {
	if m.snapshot.NetworkConnectionMode {
		return len(m.snapshot.NetworkConnections)
	}
	return len(m.snapshot.NetworkProcesses)
}

func (m *monitorModel) networkApplicationMeta() string {
	if m.snapshot.NetworkConnectionMode {
		return fmt.Sprintf("SORT ↓ DOWN+UP · %d TCP/UDP · TCP PAYLOAD · UDP --", m.networkApplicationCount())
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
