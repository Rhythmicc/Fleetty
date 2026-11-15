package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func compactTestHub(width, height int) *hubModel {
	names := []string{"A100", "4090", "5090", "B580", "n1", "n2", "n3", "n4", "s80", "Z100", "9070XT", "7G100", "LG200", "intel9462", "NAS", "ssslab-login-1"}
	m := &hubModel{width: width, height: height, config: hubConfig{Name: "SSSLab Machine Hub", Groups: productionHubGroups()}}
	for i, name := range names {
		profile, group := machineProfileGPU, "gpu"
		if i == 13 {
			profile, group = machineProfileCPU, "cpu"
		} else if i >= 14 {
			group = "services"
			if i == 14 {
				profile = machineProfileNAS
			}
		}
		m.config.Nodes = append(m.config.Nodes, hubNodeConfig{Name: name, Profile: profile, Group: group, Address: fmt.Sprintf("192.168.31.%d:23234", 141+i)})
		m.states = append(m.states, hubNodeState{Latency: time.Duration(20+i*5) * time.Millisecond, Snapshot: monitorSnapshot{
			CollectedAt: time.Now(), CPUPercent: 2.6, CPUCores: 64, LoadAverage: "load 5.09 · 7.48 · 5.74",
			MemoryUsed: 4 << 30, MemoryTotal: 64 << 30, DiskUsed: 50 << 30, DiskTotal: 500 << 30,
			NetworkRX: 18 << 20, NetworkTX: 1200, NetworkRXTotal: 4 << 40, NetworkTXTotal: 5 << 40,
			GPUs: []gpuInfo{{Utilization: float64(i%3) * 50, MemoryUsed: 489 << 20, MemoryTotal: 64 << 30, Temperature: 48}},
		}})
	}
	m.states[12] = hubNodeState{Error: "connection refused"}
	return m
}

func TestHubRackPaginationGeometryAndClickTargets(t *testing.T) {
	for _, width := range []int{24, 36, 60, 80, 93, 94, 140, 220, 512} {
		for _, height := range []int{8, 10, 18, 26, 48} {
			m := compactTestHub(width, height)
			left, right := hubRackWidths(width)
			var seen []int
			for page, rows := range m.pages() {
				if len(rows) < 2 || rows[0].group < 0 || rows[0].node >= 0 {
					t.Fatalf("page %d missing group context: %v", page, rows)
				}
				m.cursor = rows[1].node
				view := m.hubView()
				if lipgloss.Height(view) != height || lipgloss.Width(view) > width {
					t.Fatalf("%dx%d overflow: %dx%d", width, height, lipgloss.Width(view), lipgloss.Height(view))
				}
				plain := ansi.Strip(view)
				startRow := m.hubRackStartRow()
				firstRackLine := strings.TrimPrefix(strings.Split(plain, "\n")[startRow], "│ ")
				if !strings.HasPrefix(firstRackLine, m.nodeGroups()[rows[0].group].title) {
					t.Fatalf("rack should start immediately after its header rule:\n%s", plain)
				}
				if strings.Contains(plain, "NODE ·") != (right > 0) {
					t.Fatalf("%dx%d wrong sidebar visibility: %s", width, height, plain)
				}
				for i, row := range rows {
					y := startRow + i
					for _, x := range []int{0, left - 1} {
						got, ok := m.nodeAt(x, y)
						if ok != (row.node >= 0) || ok && got != row.node {
							t.Fatalf("%dx%d page %d click (%d,%d) = %d,%v want row %v", width, height, page, x, y, got, ok, row)
						}
					}
					if _, ok := m.nodeAt(left, y); ok {
						t.Fatal("sidebar or outside list is clickable")
					}
					if row.node >= 0 {
						seen = append(seen, row.node)
					}
				}
				if _, ok := m.nodeAt(0, height-1); ok {
					t.Fatal("footer is clickable")
				}
			}
			if fmt.Sprint(seen) != fmt.Sprint(m.nodeOrder()) {
				t.Fatalf("%dx%d pagination lost, reordered or repeated nodes: %v", width, height, seen)
			}
		}
	}
}

func TestHubRackKeepsSelectionVisibleOnResize(t *testing.T) {
	m := compactTestHub(140, 48)
	m.cursor = 13
	for _, size := range []struct{ w, h int }{{140, 9}, {60, 10}, {100, 18}, {220, 48}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		if m.cursor != 13 || !strings.Contains(ansi.Strip(m.hubView()), "intel9462") {
			t.Fatalf("resize to %dx%d lost selection", size.w, size.h)
		}
	}
}

func TestHubRackOnlineOfflineAndChecking(t *testing.T) {
	m := compactTestHub(140, 30)
	m.states[1] = hubNodeState{Error: "connect 4090: dial tcp 192.168.31.142:23234: i/o timeout", LastSeen: time.Now().Add(-time.Minute), NextRetry: time.Now().Add(26 * time.Second)}
	m.states[2] = hubNodeState{}
	m.cursor = 1
	view := ansi.Strip(m.hubView())
	for _, want := range []string{"13/16 LIVE", "2 OFFLINE", "i/o timeout", "CHECKING", "Last seen", "Retry in", "192.168.31.142"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in view:\n%s", want, view)
		}
	}
	if strings.Contains(view, "dial tcp") {
		t.Fatal("shown raw offline address error")
	}
}

func TestHubRackKeepsStaleReadingsAndNodeAccess(t *testing.T) {
	m := compactTestHub(140, 30)
	m.cursor = 2
	state := &m.states[2]
	state.Delay = "context deadline exceeded"
	now := time.Now()
	state.LastSeen = now.Add(-4 * time.Second)
	for _, width := range []int{42, 52, 100} {
		row := m.renderHubRackRow(2, width, now)
		plain := ansi.Strip(row)
		if !strings.Contains(plain, "! 5090") || !strings.Contains(plain, "STALE") ||
			!strings.Contains(plain, "GPU×1") || !strings.Contains(plain, "100%") ||
			strings.Contains(plain, "OFFLINE") || lipgloss.Width(row) > width {
			t.Errorf("width %d: stale reading should still be visible: %q", width, plain)
		}
	}
	if head := strings.SplitN(ansi.Strip(m.hubView()), "\n", 2)[0]; !strings.Contains(head, "14/16 LIVE") || !strings.Contains(head, "1 STALE") {
		t.Errorf("header does not separate stale data from live: %q", head)
	}
	detail := ansi.Strip(strings.Join(m.renderHubRackDetail(2, 36), "\n"))
	for _, want := range []string{"! STALE", "4s ago", state.Delay, "MEMORY"} {
		if !strings.Contains(detail, want) {
			t.Errorf("stale detail missing %q: %q", want, detail)
		}
	}
	if command := m.openSelected(); command == nil || m.detail == nil {
		t.Fatal("a stale node should still open its live dashboard")
	}
}

func TestHubRackPrimaryMetricAndDetail(t *testing.T) {
	m := compactTestHub(140, 30)
	m.cursor = 2
	width, detailWidth := hubRackWidths(m.width)
	now := time.Now()
	m.states[2].Spark = []hubSparkSample{{At: now, GPU: 100}}
	row := ansi.Strip(m.renderHubRackRow(2, width, now))
	for _, want := range []string{"5090", "ONLINE", "GPU×1", "100%", "·█", "0/64G", "3%", "6%", "48°C"} {
		if !strings.Contains(row, want) {
			t.Errorf("missing %q in %q", want, row)
		}
	}
	if strings.Count(row, "█") != 1 {
		t.Fatalf("one 100%% sample should fill exactly one column: %q", row)
	}
	m.states[2].Snapshot.Processes = []processInfo{{User: "alice", CPU: 60, Command: "protein-fold"}}
	if row := ansi.Strip(m.renderHubRackRow(2, width, now)); !strings.Contains(row, "alice") || strings.Contains(row, "protein-fold") {
		t.Fatalf("rack row should show the active user, not the command: %s", row)
	}
	detail := ansi.Strip(strings.Join(m.renderHubRackDetail(2, detailWidth), "\n"))
	for _, want := range []string{"192.168.31.143", "CPU", "MEMORY", "6%", "DEVICES", "0/64G", "48°C", "alice", "↓18M ↑1K"} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q in %q", want, detail)
		}
	}
	if strings.Contains(detail, "protein-fold") {
		t.Fatal("preview should display the user, not command")
	}
	m.cursor = 13
	m.states[13].Snapshot.GPUs = nil
	cpu := ansi.Strip(m.renderHubRackRow(13, width, now))
	if !strings.Contains(cpu, "CPU") || strings.Contains(cpu, "GPU×") || !strings.Contains(cpu, "DSK 10%") {
		t.Fatalf("CPU row: %s", cpu)
	}
}

func TestHubSlurmStateDoesNotImplyGPUAvailability(t *testing.T) {
	now := time.Now()
	m := compactTestHub(165, 40)
	m.config.Nodes[1].SlurmCluster = "GPU"
	m.config.Nodes[1].SlurmNode = "worker"
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU"}}
	m.states[1].Snapshot.GPUs[0].Utilization = 0
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{
		CollectedAt: now, Nodes: []slurmNode{{Name: "worker", State: "drng"}},
	}}}
	for _, width := range []int{36, 62, 114, 160} {
		row := func() string { return ansi.Strip(m.renderHubRackRow(1, width, now)) }
		m.slurmStates[0].Snapshot.Nodes[0].State = "drng"
		got := row()
		if !strings.Contains(got, "4090 DRAIN") || !strings.Contains(got, "GPU×1") || !strings.Contains(got, "0%") {
			t.Fatalf("width %d: expected both scheduler exception and telemetry: %q", width, got)
		}
		if detail := ansi.Strip(strings.Join(m.renderHubRackDetail(1, 48), "\n")); !strings.Contains(detail, "DRNG") {
			t.Fatalf("node metrics must show the full reported state: %q", detail)
		}
		m.slurmStates[0].Snapshot.Nodes[0].State = "idle"
		if got := row(); strings.Contains(got, "IDLE") {
			t.Fatalf("routine scheduler states should not clutter the rack: %q", got)
		}
		m.slurmStates[0].Snapshot.Nodes[0].State = "drng"
		m.slurmStates[0].Warning = "timeout"
		if got := row(); strings.Contains(got, "—") || strings.Contains(got, "DRAIN") {
			t.Fatalf("stale scheduler state must not add rack placeholders or alerts: %q", got)
		}
		m.slurmStates[0].Warning = ""
	}
}

func TestHubRackTemperature(t *testing.T) {
	for _, tc := range []struct {
		temp int
		want string
	}{
		{0, "   —"},
		{42, "42°C"},
		{71, "71°C"},
		{87, "87°C"},
	} {
		got := hubRackTemperature(tc.temp)
		if ansi.Strip(got) != tc.want || lipgloss.Width(got) != 4 {
			t.Errorf("%d: got %q, want 4-cell %q", tc.temp, ansi.Strip(got), tc.want)
		}
	}
}

func TestHubRackMeterIsProportional(t *testing.T) {
	for _, width := range []int{5, 18, 40} {
		for _, value := range []float64{0, 50, 100} {
			_, styled := hubRackMeter(value, width)
			meter := ansi.Strip(styled)
			if lipgloss.Width(meter) != width || strings.Count(meter, "█") != int(value)*width/100 {
				t.Fatalf("width %d, value %.0f: %q", width, value, meter)
			}
		}
	}
}

func TestNASHubRackShowsNetworkAndKeepsStorageWarning(t *testing.T) {
	m := compactTestHub(140, 30)
	m.cursor = 14
	s := &m.states[14].Snapshot
	s.Filesystems = []filesystemInfo{{Mount: "/mnt/NAS01", Used: 75, Total: 100}, {Mount: "/mnt/NAS02", Used: 100, Total: 100}}
	sample := hubSparkFrom(*s)
	if sample.Net != s.NetworkRX+s.NetworkTX {
		t.Fatalf("NAS history should include both network directions: %v", sample)
	}
	sample.At = time.Now()
	m.states[14].Spark = []hubSparkSample{sample}
	width, detailWidth := hubRackWidths(m.width)
	row := m.renderHubRackRow(14, width, sample.At)
	plain := ansi.Strip(row)
	if !strings.HasPrefix(plain, "▸▲ NAS") || !strings.Contains(row, warningStyle.Render("▲")) ||
		!strings.Contains(plain, "NET") || !strings.Contains(plain, "18M/s") ||
		!strings.Contains(plain, "·█") || !strings.Contains(plain, "DISK!") || strings.Contains(plain, "HTTP") {
		t.Fatalf("NAS row should show a yellow storage warning, network history and DISK!: %s", row)
	}
	detail := ansi.Strip(strings.Join(m.renderHubRackDetail(14, detailWidth), "\n"))
	if !strings.Contains(detail, "Disk /mnt/NAS02 100% used") || !strings.Contains(detail, "▲ WARN") {
		t.Fatalf("NAS panel lost the storage warning: %s", detail)
	}
}

func TestHubRackUsersFromSlurmGPUAndCPU(t *testing.T) {
	m := compactTestHub(160, 30)
	node := &m.config.Nodes[0]
	node.SlurmNode, node.SlurmCluster = "node2", "cluster"
	m.config.SlurmClusters = []slurmClusterConfig{{Name: "cluster"}}
	m.slurmStates = []slurmClusterState{{Snapshot: slurmSnapshot{Jobs: []slurmJob{
		{State: "PENDING", NodeList: "node2", User: "pending-user"},
		{State: "RUNNING", NodeList: "node[1-3]", User: "alice"},
		{State: "RUNNING", NodeList: "node4", User: "other-node"},
	}}}}
	s := &m.states[0].Snapshot
	s.GPUs[0].Workloads = []gpuWorkloadInfo{{User: "alice", Name: "train"}, {User: "bob", Name: "work"}}
	s.Processes = []processInfo{{CPU: 30, User: "carol", Command: "build"}, {CPU: 1, User: "idle-user", Command: "sshd"}}
	if got := m.hubOccupant(0, *s); got != "alice +2" {
		t.Fatalf("active users = %q, want Slurm first and two distinct others", got)
	}
}

func TestHubNetworkHistoryUsesVisibleRateRange(t *testing.T) {
	at := time.Unix(100, 0)
	samples := []hubSparkSample{
		{At: at.Add(-4 * time.Second), Net: 1024 << 20}, // outside the window
		{At: at.Add(-2 * time.Second), Net: 2 << 20},
		{At: at, Net: 4 << 20},
	}
	chart := hubSparkline(samples, sparkNet, 4, time.Second, at)
	if got := ansi.Strip(chart); got != "·▅·█" || !strings.Contains(chart, networkRXStyle.Render("█")) {
		t.Fatalf("network history lost its scale or timing: %q", chart)
	}
}

func TestHubSparklineIsFixedScaleAndOneColumnPerSample(t *testing.T) {
	idle := ansi.Strip(hubFixedSparkline([]float64{0, 1, 2, 3}, 4))
	if idle != "▁▁▁▁" {
		t.Fatalf("idle samples lost the baseline or were scaled up: %q", idle)
	}
	idleColored := hubFixedSparkline([]float64{0, 1, 2, 3}, 4)
	if strings.Contains(idleColored, "38;2;158;228;147") || !strings.Contains(idleColored, "38;2;52;60;73") {
		t.Fatalf("idle baseline should be the faint track, not green: %q", idleColored)
	}
	neutral := hubFixedSparkline([]float64{20}, 1)
	if strings.Contains(neutral, "38;2;158;228;147") || !strings.Contains(neutral, "38;2;156;163;188") {
		t.Fatalf("under-30 column should be muted: %q", neutral)
	}
	green := hubFixedSparkline([]float64{50}, 1)
	if !strings.Contains(green, "38;2;158;228;147") {
		t.Fatalf("30–70 column should be green: %q", green)
	}
	if !strings.Contains(hubFixedSparkline([]float64{75}, 1), "38;2;255;209;128") {
		t.Fatal("70–90 column should be yellow")
	}
	if !strings.Contains(loadBandStyle(percentBand(20), false).Render("20%"), "38;2;245;247;255") {
		t.Fatal("neutral percent should use the normal foreground")
	}
	if !strings.Contains(loadBandStyle(percentBand(50), false).Render("50%"), "38;2;158;228;147") {
		t.Fatal("green percent should use the ok color")
	}
	hot := hubFixedSparkline([]float64{4, 100}, 4)
	plain := ansi.Strip(hot)
	if !strings.HasPrefix(plain, "··") || !strings.Contains(plain, "█") || !strings.Contains(plain, "▁") {
		t.Fatalf("short history should pad on the left and keep 100%% tall: %q", plain)
	}
	if !strings.Contains(hot, "38;2;255;138;128") {
		t.Fatalf("crit sample stayed gray: %q", hot)
	}
	if throughputBand(100<<20, 0) != loadNeutral || throughputBand(200<<20, 0) != loadGreen || throughputBand(600<<20, 0) != loadWarn || throughputBand(1<<30, 0) != loadCrit {
		t.Fatal("absolute throughput bands drifted")
	}
	link := uint64(10000) * 1000 * 1000 / 8
	if throughputBand(link*2/10, link) != loadNeutral || throughputBand(link*5/10, link) != loadGreen || throughputBand(link*8/10, link) != loadWarn || throughputBand(link*95/100, link) != loadCrit {
		t.Fatal("link-speed throughput bands drifted")
	}
	if vramBand(20, 100) != loadNeutral || vramBand(59, 96) != loadGreen || vramBand(80, 100) != loadWarn || vramBand(95, 100) != loadCrit {
		t.Fatal("VRAM bands drifted")
	}
	if temperatureBand(42) != loadNeutral || temperatureBand(71) != loadGreen || temperatureBand(78) != loadWarn || temperatureBand(87) != loadCrit {
		t.Fatal("temperature bands drifted")
	}
}

func TestHubRackHistoryKeepsRefreshSlots(t *testing.T) {
	interval := 2 * time.Second
	at := time.Unix(100, 0)
	first := appendSpark(nil, hubSparkSample{At: at, GPU: 0})
	samples := appendSpark(first, hubSparkSample{At: at, GPU: 20})
	samples = appendSpark(samples, hubSparkSample{At: at.Add(2 * interval), GPU: 100})
	if first[0].GPU != 0 || len(samples) != 2 {
		t.Fatalf("same-interval refresh grew or mutated shared history: first=%v new=%v", first, samples)
	}
	if got := ansi.Strip(hubSparkline(samples, sparkGPU, 4, interval, at.Add(2*interval))); got != "·▂·█" {
		t.Fatalf("wide chart lost missing interval or latest value: %q", got)
	}
	if got := ansi.Strip(hubSparkline(samples, sparkGPU, 2, interval, at.Add(2*interval))); got != "·█" {
		t.Fatalf("narrow chart should show only the newest intervals: %q", got)
	}
	if got := ansi.Strip(hubSparkline(samples, sparkGPU, 4, interval, at.Add(3*interval))); got != "▂·█·" {
		t.Fatalf("missing latest slot should appear at the right: %q", got)
	}
	for i := 3; i < hubSparkCap+5; i++ {
		samples = appendSpark(samples, hubSparkSample{At: at.Add(time.Duration(i) * interval), CPU: float64(i)})
	}
	if len(samples) != hubSparkCap || samples[len(samples)-1].CPU != float64(hubSparkCap+4) {
		t.Fatalf("spark ring = %d last %.0f", len(samples), samples[len(samples)-1].CPU)
	}
}

func TestHubRackWindowMatchesVisibleHistoryWidth(t *testing.T) {
	for _, width := range []int{80, 100, 140, 220} {
		m := compactTestHub(width, 30)
		m.config.RefreshSeconds = 3
		list, _ := hubRackWidths(width)
		rackWidth := list
		if m.hubFramedMode() {
			rackWidth = max(20, list-4)
		}
		cells := hubRackLayout(rackWidth).history
		window := fmt.Sprintf("HIST %s", time.Duration(cells)*m.config.refreshInterval())
		view := ansi.Strip(m.hubView())
		if !strings.Contains(view, window) || !strings.Contains(view, strings.Repeat("·", cells)) {
			t.Fatalf("%d columns: want %q and %d chart cells:\n%s", width, window, cells, view)
		}
	}
}
