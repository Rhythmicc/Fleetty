//go:build demo

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/wish/v2"
	"charm.land/wish/v2/accesscontrol"
	"charm.land/wish/v2/activeterm"
	bubblewish "charm.land/wish/v2/bubbletea"
	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// dispatchDemo renders a screenshot fixture with the real TUI. It is compiled
// only with -tags demo and is omitted from fleetty help.
func dispatchDemo(args []string, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "demo" {
		return false, nil
	}
	return true, runDemoCommand(args[1:], stdout, stderr)
}

func runDemoCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	screen := flags.String("screen", "hub", "hub, hub-links, hub-queue, gpu, gpu-compute, or nas")
	theme := flags.String("theme", "dark", "dark or light")
	width := flags.Int("width", 0, "terminal columns (0 uses the screen default)")
	height := flags.Int("height", 0, "terminal rows (0 uses the screen default)")
	port := flags.Int("listen", 0, "serve the simulated Hub over SSH on this loopback port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("demo does not accept positional arguments")
	}
	if *port != 0 {
		mode, err := demoColorMode(*theme)
		if err != nil {
			return err
		}
		return serveDemoSSH(*port, *screen, mode, stderr)
	}
	frame, err := renderDemoFrame(*screen, *theme, *width, *height)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, frame)
	return err
}

func renderDemoFrame(screen, theme string, width, height int) (string, error) {
	mode, err := demoColorMode(theme)
	if err != nil {
		return "", err
	}
	if width == 0 || height == 0 {
		defaultWidth, defaultHeight := demoSize(screen)
		if width == 0 {
			width = defaultWidth
		}
		if height == 0 {
			height = defaultHeight
		}
	}
	switch screen {
	case "hub":
		return demoHub(mode, width, height).View().Content, nil
	case "hub-links":
		return demoHubWithLinks(mode, width, height).View().Content, nil
	case "hub-queue":
		m := demoHubSession(mode, width, height)
		m.queueExpanded = true
		return m.View().Content, nil
	case "gpu", "gpu-compute":
		return demoGPUPreview(mode, width, height, screen == "gpu-compute").View().Content, nil
	case "gpu-network":
		m := demoGPUPreview(mode, width, height, false)
		m.monitorPage = monitorPageNetwork
		return m.View().Content, nil
	case "gpu-storage", "storage":
		m := demoGPUPreview(mode, width, height, false)
		m.monitorPage = monitorPageStorage
		return m.View().Content, nil
	case "nas":
		return demoNASNode(mode, width, height).View().Content, nil
	default:
		return "", fmt.Errorf("unknown demo screen %q (hub, hub-links, hub-queue, gpu, gpu-compute, gpu-network, gpu-storage, or nas)", screen)
	}
}

func demoHubWithLinks(mode colorMode, width, height int) *hubModel {
	m := demoHub(mode, width, height)
	m.linksView, m.linksExplicit = true, true
	m.config.Nodes[14].Name = "NAS"
	m.config.Nodes[15].Name = "login-01"
	// Give the preview both a stale measurement and a distinct Slurm state.
	m.states[3].Delay = "context deadline exceeded"
	m.states[3].LastSeen = time.Now().Add(-4 * time.Second)
	m.states[3].Spark = m.states[3].Spark[:len(m.states[3].Spark)-4]
	m.updateOnlineStatus()
	m.links = mergeHubLinks([]hubLinkObservation{
		{node: 14, available: true, peers: []hubPeerLink{
			{node: 0, sockets: 3, sampled: 3, rx: mib(82), tx: mib(6)},
			{node: 5, sockets: 1, sampled: 1, rx: mib(1), tx: mib(28)},
			{node: 1, sockets: 1, sampled: 1},
			{node: 2, sockets: 1, sampled: 1, rx: 470},
			{node: 4, sockets: 1, sampled: 1, tx: 32000},
			{node: 15, sockets: 1, sampled: 1, rx: mib(13), tx: mib(4)},
			{node: 13, sockets: 1},
		}},
		{node: 15, available: true, peers: []hubPeerLink{
			{node: 0, sockets: 2, sampled: 2, rx: mib(11), tx: mib(2)},
			{node: 1, sockets: 2, sampled: 2, rx: mib(7), tx: mib(18)},
			{node: 2, sockets: 2, sampled: 1, rx: mib(5), tx: mib(2)},
			{node: 5, sockets: 1, sampled: 1},
		}},
	})
	m.links.eligible, m.links.sampled, m.links.sampledAt = 14, 12, time.Now()
	return m
}

func demoHubSession(mode colorMode, width, height int) *hubModel {
	m := demoHubWithLinks(mode, width, height)
	m.config.Name = "Fleetty Hub · DEMO"
	m.config.Nodes[14].Name = "NAS"
	m.config.Nodes[15].Name = "login-01"
	for i := range 36 {
		m.slurmStates[i%2].Snapshot.Jobs = append(m.slurmStates[i%2].Snapshot.Jobs,
			slurmJob{ID: strconv.Itoa(18600 + i), User: []string{"sam", "lee", "kai"}[i%3],
				Name:  []string{"mesh-build", "spectral-fit", "batch-encode"}[i%3],
				State: "PENDING", Priority: uint64(14200 - i*150), QOS: "normal",
				Elapsed: "0:00", TimeLimit: []string{"4:00:00", "8:00:00", "2:00:00"}[i%3],
				Partition: []string{"gpu", "cpu"}[i%2],
				Reason: []string{"Resources", "Priority", "Dependency"}[i%3], Nodes: 1 + i%2})
	}
	m.detailFixture = func(index int) *monitorModel {
		if index == 0 {
			return demoGPUPreview(m.colorMode, m.width, m.height, false)
		}
		if index == 14 {
			return demoNASNode(m.colorMode, m.width, m.height)
		}
		node := m.config.Nodes[index]
		return &monitorModel{
			snapshot: m.states[index].Snapshot, profile: node.Profile, nodeName: node.Name,
			width: m.width, height: m.height, colorMode: m.colorMode, theme: m.theme,
			cpuHistory:       demoSeries(72, m.states[index].Snapshot.CPUPercent, 10, float64(index)*0.4),
			networkRXHistory: demoSeries(72, float64(m.states[index].Snapshot.NetworkRX), float64(m.states[index].Snapshot.NetworkRX)*0.3, 0.8),
			networkTXHistory: demoSeries(72, float64(m.states[index].Snapshot.NetworkTX), float64(m.states[index].Snapshot.NetworkTX)*0.3, 0.2),
			storage:          demoStorageMapState(time.Now()),
			admin:            &adminController{}, user: "demo", remote: "demo",
		}
	}
	return m
}

type demoHubProgram struct{ *hubModel }

func (m demoHubProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, tick := msg.(hubTickMsg); tick {
		// The simulation keeps its history visible in long-running demos.
		// Preserve the deliberately stale row's gap and never poll a real node.
		now := time.Now()
		for i := range m.states {
			state := &m.states[i]
			if state.Error != "" || state.Snapshot.CollectedAt.IsZero() {
				continue
			}
			latest := now.Truncate(time.Second)
			if state.Delay != "" {
				latest = latest.Add(-4 * time.Second)
				state.LastSeen = now.Add(-4 * time.Second)
			} else {
				state.LastSeen = now
			}
			for j := range state.Snapshot.SlurmProgress {
				p := &state.Snapshot.SlurmProgress[j].Value
				p.Done = 62 + uint64(now.Unix()%31)
			}
			for j := range state.Spark {
				state.Spark[j].At = latest.Add(-time.Duration(len(state.Spark)-1-j) * time.Second)
			}
		}
		for i := range m.slurmStates {
			state := &m.slurmStates[i]
			if state.Error == "" && state.Warning == "" && !state.Snapshot.CollectedAt.IsZero() {
				state.Snapshot.CollectedAt = now
			}
		}
		if m.detail != nil {
			if backend, ok := m.detail.backend.(*demoProgressBackend); ok {
				// Reuse the Hub's tick; do not create a second timer on each open.
				_, _ = m.detail.Update(snapshotMsg{snapshot: backend.sample(now)})
			}
		}
	}
	_, cmd := m.hubModel.Update(msg)
	return m, cmd
}

// Serve only fictional data on loopback, with no shell, RPC, port forwarding,
// host collectors, or use of the real Hub configuration.
func serveDemoSSH(port int, screen string, mode colorMode, stderr io.Writer) error {
	if port < 1 || port > 65535 {
		return errors.New("demo port must be between 1 and 65535")
	}
	switch screen {
	case "hub", "hub-links", "hub-queue", "gpu", "gpu-compute", "gpu-network", "gpu-storage", "storage", "nas":
	default:
		return fmt.Errorf("unknown demo screen %q", screen)
	}
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		return err
	}
	server, err := wish.NewServer(
		func(s *ssh.Server) error { s.AddHostKey(signer); return nil },
		wish.WithMiddleware(
			bubblewish.Middleware(func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
				pty, _, ok := sess.Pty()
				if !ok {
					return nil, nil
				}
				width, height := pty.Window.Width, pty.Window.Height
				switch screen {
				case "gpu", "gpu-compute":
					return demoGPUPreview(mode, width, height, screen == "gpu-compute"), nil
				case "nas":
					return demoNASNode(mode, width, height), nil
				default:
					hub := demoHubSession(mode, width, height)
					hub.queueExpanded = screen == "hub-queue"
					return demoHubProgram{hub}, nil
				}
			}),
			activeterm.Middleware(),
			accesscontrol.Middleware(),
		),
	)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	fmt.Fprintf(stderr, "Fleetty demo SSH on %s (PID %d)\n", listener.Addr(), os.Getpid())
	fmt.Fprintln(stderr, "Demo host key:", gossh.FingerprintSHA256(signer.PublicKey()))
	err = server.Serve(listener)
	if errors.Is(err, ssh.ErrServerClosed) {
		return nil
	}
	return err
}

func demoColorMode(theme string) (colorMode, error) {
	switch theme {
	case "dark":
		return colorModeDark, nil
	case "light":
		return colorModeLight, nil
	default:
		return colorModeDark, fmt.Errorf("unknown demo theme %q (dark or light)", theme)
	}
}

func demoSize(screen string) (int, int) {
	switch screen {
	case "gpu", "gpu-compute", "gpu-network", "gpu-storage", "storage":
		return 184, 40
	case "nas":
		return 184, 33
	default:
		return 140, 48
	}
}

func demoTheme(mode colorMode) terminalTheme {
	return newTerminalTheme(mode.String())
}

func gib(value float64) uint64 {
	return uint64(value * 1024 * 1024 * 1024)
}

func mib(value float64) uint64 {
	return uint64(value * 1024 * 1024)
}

func demoSeries(count int, base, amplitude, phase float64) []float64 {
	values := make([]float64, count)
	for index := range values {
		step := float64(index)
		value := base + amplitude*math.Sin(step/7+phase) + 0.35*amplitude*math.Sin(step/2.4+phase*1.6)
		if value < 0 {
			value = 0
		}
		values[index] = value
	}
	return values
}

func demoHub(mode colorMode, width, height int) *hubModel {
	now := time.Now()
	config := hubConfig{
		Version:        hubConfigVersion,
		Name:           "Acme Research Cluster",
		RefreshSeconds: 1,
		Groups: []hubGroupConfig{
			{ID: "gpu", Title: "GPU COMPUTE", Style: hubGroupStyleGPU},
			{ID: "cpu", Title: "CPU COMPUTE", Style: hubGroupStyleCPU},
			{ID: "services", Title: "CLUSTER SERVICES", Style: hubGroupStyleNetwork},
		},
		Nodes: demoHubNodes(),
		SlurmClusters: []slurmClusterConfig{
			{Name: "Acme GPU"},
			{Name: "Acme CPU"},
		},
	}
	model := &hubModel{
		config:      config,
		states:      demoHubStates(now),
		slurmStates: demoSlurmStates(now),
		width:       width,
		height:      height,
		colorMode:   mode,
		theme:       demoTheme(mode),
		slurmFilter: -1,
	}
	model.states[0].Snapshot.SlurmProgress = []slurmProcessProgress{
		{JobID: 18501, UID: 1000, PID: 14321, Value: processProgress{Done: 62, Total: 100, Label: "epoch 4/6"}},
	}
	model.states[5].Snapshot.SlurmProgress = []slurmProcessProgress{
		{JobID: 18455, UID: 1001, PID: 5104, Value: processProgress{Done: 7, Total: 32, Label: "shard 7/32"}},
	}
	model.links = mergeHubLinks([]hubLinkObservation{
		{node: 14, available: true, peers: []hubPeerLink{
			{node: 0, sockets: 3, sampled: 3, rx: mib(82), tx: mib(6)},
			{node: 5, sockets: 1, sampled: 1, rx: mib(1), tx: mib(28)},
			{node: 1, sockets: 1, sampled: 1},
			{node: 2, sockets: 1, sampled: 1, rx: 470},
			{node: 4, sockets: 1, sampled: 1, tx: 32000},
			{node: 15, sockets: 1, sampled: 1, rx: mib(13), tx: mib(4)},
			{node: 13, sockets: 1},
		}},
		{node: 15, available: true, peers: []hubPeerLink{
			{node: 0, sockets: 2, sampled: 2, rx: mib(11), tx: mib(2)},
			{node: 1, sockets: 2, sampled: 2, rx: mib(7), tx: mib(18)},
			{node: 2, sockets: 2, sampled: 1, rx: mib(5), tx: mib(2)},
			{node: 5, sockets: 1, sampled: 1},
		}},
	})
	model.links.eligible, model.links.sampled, model.links.sampledAt = 14, 12, now
	model.updateOnlineStatus()
	return model
}

func demoHubNodes() []hubNodeConfig {
	names := []struct {
		name, group, profile string
	}{
		{"gpu-a01", "gpu", machineProfileGPU},
		{"gpu-a02", "gpu", machineProfileGPU},
		{"gpu-a03", "gpu", machineProfileGPU},
		{"gpu-b01", "gpu", machineProfileGPU},
		{"gpu-b02", "gpu", machineProfileGPU},
		{"gpu-b03", "gpu", machineProfileGPU},
		{"gpu-c01", "gpu", machineProfileGPU},
		{"gpu-c02", "gpu", machineProfileGPU},
		{"gpu-d01", "gpu", machineProfileGPU},
		{"gpu-d02", "gpu", machineProfileGPU},
		{"gpu-d03", "gpu", machineProfileGPU},
		{"gpu-e01", "gpu", machineProfileGPU},
		{"gpu-e02", "gpu", machineProfileGPU},
		{"cpu-01", "cpu", machineProfileCPU},
		{"storage-01", "services", machineProfileNAS},
		{"edge-01", "services", machineProfileGPU},
	}
	nodes := make([]hubNodeConfig, len(names))
	for index, item := range names {
		node := hubNodeConfig{
			Name: item.name, Group: item.group, Profile: item.profile,
			Address: fmt.Sprintf("192.0.2.%d:23234", 11+index),
		}
		if item.group == "gpu" {
			node.SlurmCluster = "Acme GPU"
			node.SlurmNode = item.name
		}
		nodes[index] = node
	}
	return nodes
}

func demoHubStates(now time.Time) []hubNodeState {
	offline := func(host string) hubNodeState {
		return hubNodeState{
			Error:     "dial tcp " + host + ":23234: i/o timeout",
			Checked:   now,
			LastSeen:  now.Add(-2 * time.Minute),
			NextRetry: now.Add(26 * time.Second),
		}
	}
	gpu := func(name string, latency time.Duration, cpu, mem, disk, util float64, count, temp int) hubNodeState {
		gpus := make([]gpuInfo, count)
		for index := range gpus {
			used := util / 100 * 48
			gpus[index] = demoGPU(index, "NVIDIA L40S", 48, used, util, temp, 1400, 120, 300, "", "")
		}
		if count > 0 && util > 50 {
			gpus[0].Workloads = []gpuWorkloadInfo{{PID: 4800, User: "alice", Name: "train.py", MemoryUsed: gib(usedOr(util))}}
		}
		state := demoOnline(now, latency, demoGPUSnapshot(now, name, cpu, mem, disk, gpus))
		state.Spark = demoSpark(demoPattern(name), hubSparkFrom(state.Snapshot))
		return state
	}
	return []hubNodeState{
		func() hubNodeState {
			state := demoOnline(now, 18*time.Millisecond, demoGPUSnapshot(now, "gpu-a01", 62, 71, 44, []gpuInfo{
				demoGPU(0, "NVIDIA L40S", 48, 41, 91, 71, 2400, 280, 300, "alice", "train.py"),
				demoGPU(1, "NVIDIA L40S", 48, 18, 47, 58, 1800, 160, 300, "bob", "embed.py"),
			}))
			state.Spark = demoSpark("busy", hubSparkFrom(state.Snapshot))
			state.Snapshot.Processes = []processInfo{{
				PID: 4812, User: "alice", State: "R", Command: "train.py", CPU: 186, Elapsed: 5*3600 + 12*60,
			}}
			// 2 Gbps puts 86 MiB/s in the green band and leaves 14 MiB/s neutral.
			state.Snapshot.NetworkInterfaces = []networkInterfaceInfo{{
				Name: "eth0", RX: state.Snapshot.NetworkRX, TX: state.Snapshot.NetworkTX, SpeedMbps: 2000,
			}}
			return state
		}(),
		gpu("gpu-a02", 24*time.Millisecond, 11, 22, 18, 4, 1, 42),
		gpu("gpu-a03", 31*time.Millisecond, 8, 19, 21, 0, 2, 39),
		gpu("gpu-b01", 44*time.Millisecond, 6, 14, 17, 2, 1, 41),
		func() hubNodeState {
			state := gpu("gpu-b02", 27*time.Millisecond, 4, 28, 22, 0, 1, 37)
			// 50 Mbps makes the 6.2 MiB/s transmit a large share of the link.
			state.Snapshot.NetworkInterfaces = []networkInterfaceInfo{{
				Name: "eth0", RX: state.Snapshot.NetworkRX, TX: state.Snapshot.NetworkTX, SpeedMbps: 50,
			}}
			return state
		}(),
		gpu("gpu-b03", 36*time.Millisecond, 15, 33, 26, 67, 4, 87),
		offline("192.0.2.17"),
		offline("192.0.2.18"),
		gpu("gpu-d01", 26*time.Millisecond, 3, 18, 20, 0, 2, 78),
		gpu("gpu-d02", 19*time.Millisecond, 2, 11, 16, 1, 1, 38),
		gpu("gpu-d03", 22*time.Millisecond, 5, 16, 19, 0, 1, 40),
		func() hubNodeState {
			state := gpu("gpu-e01", 29*time.Millisecond, 9, 50, 24, 3, 1, 44)
			state.Snapshot.GPUs[0].MemoryUsed = uint64(float64(state.Snapshot.GPUs[0].MemoryTotal) * 0.76)
			return state
		}(),
		gpu("gpu-e02", 33*time.Millisecond, 7, 90, 18, 0, 1, 41),
		func() hubNodeState {
			state := demoOnline(now, 12*time.Millisecond, demoCPUSnapshot(now, "cpu-01", 32, 18, "load 1.20  0.94  0.81", 31, 24))
			state.Spark = demoSpark("wave", hubSparkFrom(state.Snapshot))
			state.Snapshot.Processes = []processInfo{{
				PID: 3300, User: "carol", State: "R", Command: "compile", CPU: 18, Elapsed: 2*3600 + 13*60,
			}}
			return state
		}(),
		func() hubNodeState {
			state := demoOnline(now, 22*time.Millisecond, demoNASSnapshot(now))
			state.Spark = demoSpark("net", hubSparkFrom(state.Snapshot))
			return state
		}(),
		gpu("edge-01", 16*time.Millisecond, 7, 61, 12, 0, 1, 41),
	}
}

func demoPattern(name string) string {
	switch name {
	case "gpu-b01":
		return "saw"
	case "gpu-b03":
		return "burst"
	case "gpu-d03":
		return "climb"
	case "gpu-e01":
		return "wave"
	case "gpu-a03", "gpu-d02", "gpu-e02":
		return "flat"
	default:
		return "idle"
	}
}

func demoSpark(pattern string, current hubSparkSample) []hubSparkSample {
	samples := make([]hubSparkSample, hubSparkCap)
	for index := range samples {
		x := float64(index)
		cpu, gpu := current.CPU, current.GPU
		switch pattern {
		case "busy":
			samples[index] = hubSparkSample{
				CPU: clampSpark(cpu*0.55 + 16*math.Sin(x/3.1)),
				GPU: clampSpark(gpu*0.72 + 10*math.Sin(x/2.6+0.7)),
			}
		case "burst":
			spike := 0.0
			if index > len(samples)-16 && index < len(samples)-5 {
				spike = 1
			}
			samples[index] = hubSparkSample{
				CPU: clampSpark(3 + spike*cpu),
				GPU: clampSpark(1 + spike*gpu),
			}
		case "climb":
			fraction := x / float64(len(samples)-1)
			samples[index] = hubSparkSample{CPU: cpu * fraction, GPU: gpu * fraction}
		case "wave":
			samples[index] = hubSparkSample{
				CPU: clampSpark(cpu*0.35 + cpu*0.65*math.Sin(x/3.4+1.2)),
				GPU: clampSpark(6 + 28*math.Sin(x/2.8)),
			}
		case "net":
			samples[index] = hubSparkSample{
				CPU: 6, Net: uint64(float64(current.Net) * (0.65 + 0.35*math.Sin(x/3.6))),
			}
		case "saw":
			samples[index] = hubSparkSample{
				CPU: clampSpark(4 + float64(index%18)*2.2),
				GPU: clampSpark(2 + float64(index%24)*1.4),
			}
		case "flat":
			samples[index] = hubSparkSample{CPU: 0.4, GPU: 0.3}
		default:
			samples[index] = hubSparkSample{
				CPU: clampSpark(cpu*0.12 + 0.4*math.Sin(x/2+cpu)),
				GPU: clampSpark(gpu*0.15 + 0.3*math.Sin(x/3+gpu)),
			}
		}
	}
	samples[len(samples)-1] = current
	latest := time.Now().Truncate(time.Second)
	for index := range samples {
		samples[index].At = latest.Add(-time.Duration(len(samples)-1-index) * time.Second)
	}
	return samples
}

func clampSpark(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func usedOr(util float64) float64 {
	if util <= 0 {
		return 1
	}
	return util / 100 * 20
}

func demoOnline(now time.Time, latency time.Duration, snapshot monitorSnapshot) hubNodeState {
	return hubNodeState{
		Snapshot: snapshot,
		Latency:  latency,
		Checked:  now,
		LastSeen: now,
	}
}

func demoSlurmStates(now time.Time) []slurmClusterState {
	return []slurmClusterState{
		{Snapshot: slurmSnapshot{
			Name: "Acme GPU", CollectedAt: now,
			Nodes: []slurmNode{
				{Name: "gpu-a01", State: "alloc", Partitions: []string{"gpu"}},
				{Name: "gpu-a02", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-a03", State: "drain", Partitions: []string{"gpu"}},
				{Name: "gpu-b01", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-b02", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-b03", State: "mix", Partitions: []string{"gpu"}},
				{Name: "gpu-d01", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-d02", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-d03", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-e01", State: "idle", Partitions: []string{"gpu"}},
				{Name: "gpu-e02", State: "idle", Partitions: []string{"gpu"}},
			},
			Jobs: []slurmJob{
				{ID: "18421_1", RawID: 18501, UID: 1000, Name: "protein-fold", User: "alice", State: "RUNNING", Priority: 24800, QOS: "normal", Elapsed: "5:12:08", TimeLimit: "12:00:00", Nodes: 1, NodeList: "gpu-a01", Partition: "gpu"},
				{ID: "18455", RawID: 18455, UID: 1001, Name: "embed-corpus", User: "bob", State: "RUNNING", Priority: 19400, QOS: "normal", Elapsed: "2:04:41", TimeLimit: "8:00:00", Nodes: 1, NodeList: "gpu-b03", Partition: "gpu"},
				{ID: "18480_[1-8]", Name: "batch-encode", User: "bob", State: "PENDING", Priority: 16200, QOS: "low", Elapsed: "0:00", TimeLimit: "30:00", Nodes: 1, Partition: "gpu", Reason: "Priority", ExpectedStart: now.Add(20 * time.Minute).Format("2006-01-02T15:04:05")},
				{ID: "18421_[2-8]", Name: "eval-suite", User: "carol", State: "PENDING", Priority: 28500, QOS: "high", Elapsed: "0:00", TimeLimit: "2-00:00:00", Nodes: 2, Partition: "gpu", Reason: "PartitionTimeLimit"},
				{ID: "18510", Name: "llm-sft-70b", User: "alice", State: "PENDING", Priority: 22100, QOS: "high", Elapsed: "0:00", TimeLimit: "1-00:00:00", Nodes: 4, Partition: "gpu", Reason: "Resources"},
				{ID: "18514", Name: "diffusion-xl", User: "chen", State: "PENDING", Priority: 15800, QOS: "normal", Elapsed: "0:00", TimeLimit: "16:00:00", Nodes: 2, Partition: "gpu", Reason: "Resources"},
				{ID: "18519", Name: "cryo-em-recon", User: "elena", State: "PENDING", Priority: 14900, QOS: "normal", Elapsed: "0:00", TimeLimit: "6:00:00", Nodes: 1, Partition: "gpu", Reason: "Priority"},
				{ID: "18523", Name: "rlhf-ppo-rollout", User: "alice", State: "PENDING", Priority: 14200, QOS: "normal", Elapsed: "0:00", TimeLimit: "12:00:00", Nodes: 2, Partition: "gpu", Reason: "Dependency"},
				{ID: "18529", Name: "vit-pretrain", User: "david", State: "PENDING", Priority: 11800, QOS: "low", Elapsed: "0:00", TimeLimit: "24:00:00", Nodes: 2, Partition: "gpu", Reason: "Priority"},
				{ID: "18541", Name: "moebius-moe-16b", User: "chen", State: "PENDING", Priority: 11500, QOS: "normal", Elapsed: "0:00", TimeLimit: "18:00:00", Nodes: 4, Partition: "gpu", Reason: "Resources"},
				{ID: "18544", Name: "pointcloud-seg", User: "elena", State: "PENDING", Priority: 10900, QOS: "normal", Elapsed: "0:00", TimeLimit: "4:00:00", Nodes: 1, Partition: "gpu", Reason: "Priority"},
				{ID: "18549", Name: "audio-codec-v3", User: "bob", State: "PENDING", Priority: 10400, QOS: "low", Elapsed: "0:00", TimeLimit: "8:00:00", Nodes: 1, Partition: "gpu", Reason: "Priority"},
				{ID: "18552", Name: "protein-dock-b2", User: "alice", State: "PENDING", Priority: 9800, QOS: "normal", Elapsed: "0:00", TimeLimit: "10:00:00", Nodes: 2, Partition: "gpu", Reason: "Dependency"},
			},
		}, Latency: 40 * time.Millisecond, Checked: now, LastSeen: now},
		{Snapshot: slurmSnapshot{
			Name: "Acme CPU", CollectedAt: now,
			Nodes: []slurmNode{
				{Name: "cpu-01", State: "alloc", Partitions: []string{"cpu"}},
			},
			Jobs: []slurmJob{
				{ID: "18492", Name: "mesh-partition", User: "carol", State: "RUNNING", Priority: 17600, QOS: "normal", Elapsed: "1:42:19", TimeLimit: "4:00:00", Nodes: 1, NodeList: "cpu-01", Partition: "cpu"},
				{ID: "18531", Name: "genomics-align", User: "maya", State: "PENDING", Priority: 13400, QOS: "normal", Elapsed: "0:00", TimeLimit: "8:00:00", Nodes: 1, Partition: "cpu", Reason: "Resources"},
				{ID: "18536", Name: "navier-stokes", User: "liam", State: "PENDING", Priority: 11200, QOS: "low", Elapsed: "0:00", TimeLimit: "12:00:00", Nodes: 2, Partition: "cpu", Reason: "Priority"},
				{ID: "18558", Name: "monte-carlo-sim", User: "maya", State: "PENDING", Priority: 9400, QOS: "low", Elapsed: "0:00", TimeLimit: "6:00:00", Nodes: 1, Partition: "cpu", Reason: "Priority"},
			},
		}, Latency: 55 * time.Millisecond, Checked: now, LastSeen: now},
	}
}

func demoIO(name string) (rx, tx, read, write float64) {
	switch name {
	case "gpu-a01":
		return 86, 14, 210, 64
	case "gpu-a02":
		return 1.4, 0.3, 6, 1.1
	case "gpu-a03":
		return 0.2, 0.08, 0.4, 0.1
	case "gpu-b01":
		return 22, 3.4, 40, 8
	case "gpu-b02":
		return 0.7, 6.2, 1.5, 19
	case "gpu-b03":
		return 48, 9.5, 96, 24
	case "gpu-d01":
		return 3.2, 0.6, 12, 2.4
	case "gpu-d02":
		return 0.35, 0.15, 0.8, 0.2
	case "gpu-d03":
		return 6.8, 1.1, 18, 4.5
	case "gpu-e01":
		return 18, 2.6, 33, 7
	case "gpu-e02":
		return 0.5, 0.4, 2.2, 0.6
	case "cpu-01":
		return 12.3, 1.2, 40, 8
	case "edge-01":
		return 2.7, 0.9, 4.4, 1.3
	case "storage-01":
		return 116, 18, 180, 42
	default:
		return 1, 0.2, 1, 0.2
	}
}

func demoCoreUsage(cores int, base float64) []cpuCoreUsage {
	usage := make([]cpuCoreUsage, cores)
	for index := range usage {
		wave := base + 28*math.Sin(float64(index)*0.7+0.3) + 12*math.Cos(float64(index)*1.3)
		if base < 15 {
			wave = base + 8*math.Sin(float64(index)*0.9)
		}
		pct := clampSpark(wave)
		usage[index] = cpuCoreUsage{
			CPU:          fmt.Sprintf("cpu%d", index),
			Percent:      pct,
			Available:    true,
			FrequencyMHz: 2400 + pct*13.5,
		}
	}
	return usage
}

func demoGPUSnapshot(now time.Time, name string, cpu, memoryPercent, diskPercent float64, gpus []gpuInfo) monitorSnapshot {
	memoryTotal := gib(512)
	diskTotal := gib(3600)
	rx, tx, read, write := demoIO(name)
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileGPU,
		NodeName:       name,
		OSName:         "Linux",
		CPUModel:       "AMD EPYC 9354 32-Core",
		CPUCores:       32,
		Uptime:         18*24*3600 + 6*3600,
		CPUPercent:     cpu,
		CPUCoreUsage:   demoCoreUsage(32, cpu),
		CPUFrequency:   &cpuFrequencyInfo{AverageMHz: 3250, MinMHz: 1500, MaxMHz: 3750, SampledCores: 32, Source: "demo"},
		LoadAverage:    fmt.Sprintf("load %.2f  %.2f  %.2f", cpu/6, cpu/8, cpu/10),
		MemoryUsed:     uint64(float64(memoryTotal) * memoryPercent / 100),
		MemoryTotal:    memoryTotal,
		DiskUsed:       uint64(float64(diskTotal) * diskPercent / 100),
		DiskTotal:      diskTotal,
		DiskRead:       mib(read),
		DiskWrite:      mib(write),
		NetworkRX:      mib(rx),
		NetworkTX:      mib(tx),
		NetworkRXTotal: gib(12.4),
		NetworkTXTotal: gib(3.1),
		NetworkInterfaces: []networkInterfaceInfo{
			{Name: "eth0", RX: mib(rx), TX: mib(tx), RXTotal: gib(12.4), TXTotal: gib(3.1), SpeedMbps: 25000},
			{Name: "ib0", RX: mib(rx * 0.35), TX: mib(tx * 0.4), RXTotal: gib(48.2), TXTotal: gib(41.8), SpeedMbps: 100000},
		},
		GPUs: gpus,
	}
}

func demoCPUSnapshot(now time.Time, name string, cores int, cpu float64, load string, memoryPercent, diskPercent float64) monitorSnapshot {
	memoryTotal := gib(256)
	diskTotal := gib(1800)
	rx, tx, read, write := demoIO(name)
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileCPU,
		NodeName:       name,
		OSName:         "Linux",
		CPUModel:       "Intel Xeon Gold 6430",
		CPUCores:       cores,
		Uptime:         40 * 24 * 3600,
		CPUPercent:     cpu,
		CPUCoreUsage:   demoCoreUsage(cores, cpu),
		CPUFrequency:   &cpuFrequencyInfo{AverageMHz: 2600, MinMHz: 1200, MaxMHz: 3400, SampledCores: cores, Source: "demo"},
		LoadAverage:    load,
		MemoryUsed:     uint64(float64(memoryTotal) * memoryPercent / 100),
		MemoryTotal:    memoryTotal,
		DiskUsed:       uint64(float64(diskTotal) * diskPercent / 100),
		DiskTotal:      diskTotal,
		DiskRead:       mib(read),
		DiskWrite:      mib(write),
		NetworkRX:      mib(rx),
		NetworkTX:      mib(tx),
		NetworkRXTotal: gib(1.8),
		NetworkTXTotal: gib(0.6),
		NetworkInterfaces: []networkInterfaceInfo{
			{Name: "eno1", RX: mib(rx), TX: mib(tx), RXTotal: gib(1.8), TXTotal: gib(0.6), SpeedMbps: 10000},
		},
	}
}

func demoGPU(index int, name string, totalGiB, usedGiB, utilization float64, temperature, clock int, power, limit float64, user, workload string) gpuInfo {
	info := gpuInfo{
		Index:       index,
		Name:        name,
		Utilization: utilization,
		MemoryUsed:  gib(usedGiB),
		MemoryTotal: gib(totalGiB),
		Temperature: temperature,
		ClockMHz:    clock,
		Power:       power,
		PowerLimit:  limit,
	}
	if workload != "" {
		info.Workloads = []gpuWorkloadInfo{{
			PID: 4800 + index, User: user, Name: workload, MemoryUsed: gib(usedGiB * 0.9),
		}}
	}
	return info
}

func demoNASSnapshot(now time.Time) monitorSnapshot {
	rx, tx, read, write := demoIO("storage-01")
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileNAS,
		NodeName:       "storage-01",
		OSName:         "Linux",
		CPUModel:       "Intel Xeon Silver 4314",
		CPUCores:       16,
		Uptime:         90 * 24 * 3600,
		CPUPercent:     23,
		CPUCoreUsage:   demoCoreUsage(16, 23),
		CPUFrequency:   &cpuFrequencyInfo{AverageMHz: 2100, MinMHz: 800, MaxMHz: 3200, SampledCores: 16, Source: "demo"},
		LoadAverage:    "load 1.84  1.52  1.40",
		MemoryUsed:     gib(48),
		MemoryTotal:    gib(128),
		DiskUsed:       gib(140),
		DiskTotal:      gib(200),
		DiskRead:       mib(read),
		DiskWrite:      mib(write),
		NetworkRX:      mib(rx),
		NetworkTX:      mib(tx),
		NetworkRXTotal: gib(84),
		NetworkTXTotal: gib(21),
		NetworkInterfaces: []networkInterfaceInfo{{
			Name: "bond0", RX: mib(116), TX: mib(18), RXTotal: gib(84), TXTotal: gib(21), SpeedMbps: 20000,
		}},
		Filesystems: []filesystemInfo{
			{Mount: "/", Device: "/dev/nvme0n1p2", Used: gib(42), Total: gib(100)},
			{Mount: "/data/shared", Device: "/dev/md0", Used: gib(18), Total: gib(80)},
			{Mount: "/data/vault", Device: "/dev/md1", Used: gib(70), Total: gib(80)},
		},
		Services: []serviceHealth{
			{Name: "Files", Kind: "http", Detail: "https://files.acme.test/health", Healthy: true, Latency: 18 * time.Millisecond},
			{Name: "Console", Kind: "http", Detail: "https://console.acme.test/", Healthy: false, Latency: 2 * time.Second},
		},
		Containers: []containerInfo{
			{Name: "acme-web", Image: "registry.example/acme/web:1.4.2", State: "running", Health: "healthy", Running: true, CPU: 6, MemoryUsed: gib(1.2), MemoryLimit: gib(4), NetworkRX: gib(18.4), NetworkTX: gib(6.2), BlockRead: gib(4.1), BlockWrite: mib(820), Ports: "8080/tcp", PIDs: 12, Uptime: 14 * 24 * 3600},
			{Name: "acme-sync", Image: "registry.example/acme/sync:0.9.1", State: "running", Health: "unhealthy", Running: true, CPU: 40, MemoryUsed: gib(2.4), MemoryLimit: gib(4), NetworkRX: gib(62.1), NetworkTX: gib(14.0), BlockRead: gib(94.0), BlockWrite: gib(38.5), Ports: "9400/tcp", PIDs: 4, Restarts: 3, Uptime: 2 * 3600},
			{Name: "acme-metrics", Image: "registry.example/acme/metrics:2.1.0", State: "running", Health: "healthy", Running: true, CPU: 3, MemoryUsed: mib(480), MemoryLimit: gib(1), NetworkRX: gib(3.5), NetworkTX: mib(910), BlockRead: mib(640), BlockWrite: gib(2.8), Ports: "9090/tcp", PIDs: 8, Uptime: 30 * 24 * 3600},
		},
		PM2Processes: []pm2ProcessInfo{
			{ID: 0, PID: 2210, Name: "catalog", Status: "online", Mode: "fork", CPU: 2, Memory: mib(180), Uptime: 20 * 24 * 3600},
			{ID: 1, PID: 2218, Name: "preview", Status: "errored", Mode: "fork", CPU: 0, Memory: mib(40), Restarts: 6, Uptime: 15 * 60},
		},
	}
}

func demoGPUNode(mode colorMode, width, height int) *monitorModel {
	now := time.Now()
	snapshot := demoGPUSnapshot(now, "gpu-a01", 62, 71, 44, []gpuInfo{
		demoGPU(0, "NVIDIA L40S", 48, 41, 91, 71, 2400, 280, 300, "alice", "train.py"),
		demoGPU(1, "NVIDIA L40S", 48, 18, 47, 58, 1800, 160, 300, "bob", "embed.py"),
	})
	snapshot.Processes = []processInfo{
		{PID: 4812, User: "alice", State: "R", Command: "train.py", CPU: 186, Memory: 22, RSS: gib(112), Elapsed: 5*3600 + 12*60,
			Progress: &processProgress{Done: 62, Total: 100, Label: "epoch 4/6"}},
		{PID: 5104, User: "bob", State: "R", Command: "embed.py", CPU: 74, Memory: 9, RSS: gib(46), Elapsed: 2*3600 + 4*60,
			Progress: &processProgress{Done: 7, Total: 32, Label: "encode"}},
		{PID: 2201, User: "alice", State: "S", Command: "jupyter-lab", CPU: 8, Memory: 3.2, RSS: gib(16), Elapsed: 26 * 3600},
		{PID: 904, User: "root", State: "S", Command: "containerd", CPU: 2.1, Memory: 0.8, RSS: gib(4), Elapsed: 18 * 24 * 3600},
		{PID: 6630, User: "bob", State: "S", Command: "rclone", CPU: 1.4, Memory: 0.4, RSS: gib(2), Elapsed: 40 * 60},
		{PID: 1188, User: "alice", State: "S", Command: "sshd", CPU: 0.2, Memory: 0.1, RSS: mib(48), Elapsed: 6 * 3600},
	}
	snapshot.NetworkProcesses = []processNetworkInfo{
		{PID: 4812, Name: "train.py", RX: mib(64.2), TX: mib(10.8), RXTotal: gib(8.4), TXTotal: gib(2.1)},
		{PID: 5104, Name: "embed.py", RX: mib(16.4), TX: mib(2.1), RXTotal: gib(2.9), TXTotal: mib(640)},
		{PID: 6630, Name: "rclone", RX: mib(5.1), TX: mib(0.9), RXTotal: gib(1.0), TXTotal: mib(310)},
		{PID: 2201, Name: "jupyter-lab", RX: mib(0.3), TX: mib(0.2), RXTotal: mib(92), TXTotal: mib(48)},
	}
	model := &monitorModel{
		user:             "monitor",
		remote:           "hub",
		nodeName:         "gpu-a01",
		profile:          machineProfileGPU,
		width:            width,
		height:           height,
		snapshot:         snapshot,
		status:           "Connected through the Hub to gpu-a01. Esc returns to the server list.",
		colorMode:        mode,
		theme:            demoTheme(mode),
		cpuHistory:       demoSeries(72, 58, 18, 0.4),
		networkRXHistory: demoSeries(72, 70*1024*1024, 28*1024*1024, 1.1),
		networkTXHistory: demoSeries(72, 12*1024*1024, 5*1024*1024, 0.2),
		storage:          demoStorageMapState(now),
		slurmQueue: &nodeSlurmQueue{
			Cluster:     "Acme GPU",
			Node:        "gpu-a01",
			CollectedAt: now.Add(-8 * time.Second),
			Jobs: []slurmDisplayJob{
				{Job: slurmJob{ID: "18421", Partition: "gpu", Name: "protein-fold", User: "alice", State: "RUNNING", Priority: 24000, QOS: "normal", Elapsed: "5:12:08", TimeLimit: "12:00:00"}},
				{Job: slurmJob{ID: "18455", Partition: "gpu", Name: "embed-corpus", User: "bob", State: "RUNNING", Priority: 18000, QOS: "normal", Elapsed: "2:04:41", TimeLimit: "8:00:00"}},
				{Job: slurmJob{ID: "18502", Partition: "gpu", Name: "eval-suite", User: "carol", State: "PENDING", Priority: 12000, QOS: "normal", Elapsed: "0:00", TimeLimit: "4:00:00", Reason: "Resources"}},
			},
		},
	}
	return model
}

func demoStorageMapState(now time.Time) *storageMapState {
	entries := []storageEntry{
		{Name: "datasets", Path: "/data/datasets", Size: gib(640), Files: 148200, Directories: 412, IsDir: true},
		{Name: "checkpoints", Path: "/data/checkpoints", Size: gib(420), Files: 1840, Directories: 96, IsDir: true},
		{Name: "models", Path: "/data/models", Size: gib(285), Files: 620, Directories: 48, IsDir: true},
		{Name: "users", Path: "/data/users", Size: gib(142), Files: 84300, Directories: 1240, IsDir: true},
		{Name: "cache", Path: "/data/cache", Size: gib(68), Files: 39100, Directories: 310, IsDir: true},
		{Name: "containers", Path: "/data/containers", Size: gib(34), Files: 22400, Directories: 890, IsDir: true},
		{Name: "logs", Path: "/data/logs", Size: gib(14.5), Files: 9420, Directories: 64, IsDir: true},
		{Name: "scratch-dump.tar.zst", Path: "/data/scratch-dump.tar.zst", Size: gib(8.2), Files: 1, Directories: 0, IsDir: false},
	}
	result := storageScanResult{
		Path:           "/data",
		Size:           gib(1611.7),
		Files:          305881,
		Directories:    3061,
		Skipped:        0,
		ExcludedMounts: 2,
		Entries:        entries,
		FinishedAt:     now,
		Duration:       420 * time.Millisecond,
		Workers:        8,
	}
	return &storageMapState{
		Root:   "/data",
		Path:   "/data",
		Scope:  "DATA VOLUME",
		Result: result,
		Cache: map[string]storageCachedResult{
			"/data": {Result: result, Complete: true, CachedAt: now},
		},
	}
}

func demoNASNode(mode colorMode, width, height int) *monitorModel {
	now := time.Now()
	snapshot := demoNASSnapshot(now)
	model := &monitorModel{
		user:             "monitor",
		remote:           "hub",
		nodeName:         "storage-01",
		profile:          machineProfileNAS,
		width:            width,
		height:           height,
		snapshot:         snapshot,
		status:           "Connected through the Hub to storage-01. Esc returns to the server list.",
		colorMode:        mode,
		theme:            demoTheme(mode),
		cpuHistory:       demoSeries(72, 22, 8, 0.8),
		networkRXHistory: demoSeries(72, 100*1024*1024, 30*1024*1024, 0.6),
		networkTXHistory: demoSeries(72, 16*1024*1024, 6*1024*1024, 1.4),
	}
	return model
}

type demoProgressBackend struct {
	initial monitorSnapshot
	started time.Time
}

func demoGPUPreview(mode colorMode, width, height int, compute bool) *monitorModel {
	m := demoGPUNode(mode, width, height)
	m.backend = &demoProgressBackend{initial: m.snapshot, started: time.Now()}
	m.nodeName = "gpu-a01 · DEMO"
	m.admin = &adminController{}
	m.status = "Simulated workloads. 1 Overview · 2 Compute · Enter details"
	if compute {
		m.monitorPage = monitorPageCompute
	}
	return m
}

func (b *demoProgressBackend) sample(now time.Time) monitorSnapshot {
	snapshot := b.initial
	snapshot.CollectedAt = now
	snapshot.Processes = slices.Clone(b.initial.Processes)
	seconds := uint64(max(0, int(now.Sub(b.started)/time.Second)))
	for i := range snapshot.Processes {
		p := &snapshot.Processes[i]
		if p.Progress != nil {
			progress := *p.Progress
			// Hold 100% for several seconds so completion is easy to see.
			progress.Done = (progress.Done + seconds) % (progress.Total + 6)
			if progress.Done > progress.Total {
				progress.Done = progress.Total
			}
			p.Progress = &progress
		}
	}
	return snapshot
}

func (b *demoProgressBackend) Collect() (monitorSnapshot, error) {
	return b.sample(time.Now()), nil
}

func (*demoProgressBackend) History(int) ([]historySample, error) { return nil, nil }
func (*demoProgressBackend) Authenticate(string) (bool, error)    { return false, nil }

func (b *demoProgressBackend) ProcessDetail(pid int, _ string) (processDetail, error) {
	for _, p := range b.sample(time.Now()).Processes {
		if p.PID == pid {
			return processDetail{
				PID: p.PID, Name: p.Command, CommandLine: p.Command, User: p.User,
				State: p.State, CPU: p.CPU, Memory: p.Memory, RSS: p.RSS,
				Elapsed: p.Elapsed, Progress: p.Progress,
			}, nil
		}
	}
	return processDetail{}, errors.New("unknown demo process")
}

func (*demoProgressBackend) TerminateProcess(int, uint64, string) error {
	return errors.New("demo is read-only")
}

func (*demoProgressBackend) RunAction(int, string) (string, error) {
	return "", errors.New("demo is read-only")
}
