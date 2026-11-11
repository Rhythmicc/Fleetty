//go:build demo

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"time"
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
	screen := flags.String("screen", "hub", "hub, gpu, or nas")
	theme := flags.String("theme", "dark", "dark or light")
	width := flags.Int("width", 0, "terminal columns (0 uses the screen default)")
	height := flags.Int("height", 0, "terminal rows (0 uses the screen default)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("demo does not accept positional arguments")
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
	case "gpu":
		return demoGPUNode(mode, width, height).View().Content, nil
	case "nas":
		return demoNASNode(mode, width, height).View().Content, nil
	default:
		return "", fmt.Errorf("unknown demo screen %q (hub, gpu, or nas)", screen)
	}
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
	case "gpu":
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
		if item.name == "gpu-a01" || item.name == "gpu-b03" {
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
		state := demoOnline(now, latency, demoGPUSnapshot(now, name, cpu, mem, disk, 4, 1, gpus))
		state.Spark = demoSpark(demoPattern(name), cpu, mem, disk, util)
		return state
	}
	return []hubNodeState{
		func() hubNodeState {
			state := demoOnline(now, 18*time.Millisecond, demoGPUSnapshot(now, "gpu-a01", 62, 71, 44, 86, 14, []gpuInfo{
				demoGPU(0, "NVIDIA L40S", 48, 41, 91, 71, 2400, 280, 300, "alice", "train.py"),
				demoGPU(1, "NVIDIA L40S", 48, 18, 47, 58, 1800, 160, 300, "bob", "embed.py"),
			}))
			state.Spark = demoSpark("busy", 62, 71, 44, 91)
			state.Snapshot.Processes = []processInfo{{
				PID: 4812, User: "alice", State: "R", Command: "train.py", CPU: 186, Elapsed: 5*3600 + 12*60,
			}}
			return state
		}(),
		gpu("gpu-a02", 24*time.Millisecond, 11, 22, 18, 4, 1, 42),
		gpu("gpu-a03", 31*time.Millisecond, 8, 19, 21, 0, 2, 39),
		gpu("gpu-b01", 44*time.Millisecond, 6, 14, 17, 2, 1, 41),
		gpu("gpu-b02", 27*time.Millisecond, 4, 28, 22, 0, 1, 37),
		gpu("gpu-b03", 36*time.Millisecond, 15, 33, 26, 67, 4, 54),
		offline("192.0.2.17"),
		offline("192.0.2.18"),
		gpu("gpu-d01", 26*time.Millisecond, 3, 18, 20, 0, 2, 56),
		gpu("gpu-d02", 19*time.Millisecond, 2, 11, 16, 1, 1, 38),
		gpu("gpu-d03", 22*time.Millisecond, 5, 16, 19, 0, 1, 40),
		gpu("gpu-e01", 29*time.Millisecond, 9, 21, 24, 3, 1, 44),
		gpu("gpu-e02", 33*time.Millisecond, 7, 24, 18, 0, 1, 41),
		func() hubNodeState {
			state := demoOnline(now, 12*time.Millisecond, demoCPUSnapshot(now, "cpu-01", 32, 18, "load 1.20  0.94  0.81", 31, 24, 1.4, 0.4))
			state.Spark = demoSpark("wave", 18, 31, 24, 0)
			state.Snapshot.Processes = []processInfo{{
				PID: 3300, User: "carol", State: "R", Command: "compile", CPU: 18, Elapsed: 2*3600 + 13*60,
			}}
			return state
		}(),
		func() hubNodeState {
			state := demoOnline(now, 22*time.Millisecond, demoNASSnapshot(now))
			state.Spark = demoSpark("disk", 23, 37.5, 87.5, 0)
			return state
		}(),
		gpu("edge-01", 16*time.Millisecond, 7, 61, 12, 0, 1, 41),
	}
}

func demoPattern(name string) string {
	switch name {
	case "gpu-b03":
		return "burst"
	case "gpu-d03":
		return "climb"
	case "gpu-e01":
		return "wave"
	case "edge-01":
		return "mem"
	default:
		return "idle"
	}
}

func demoSpark(pattern string, cpu, mem, disk, gpu float64) []hubSparkSample {
	samples := make([]hubSparkSample, hubSparkCap)
	for index := range samples {
		x := float64(index)
		switch pattern {
		case "busy":
			samples[index] = hubSparkSample{
				CPU:  clampSpark(cpu*0.55 + 16*math.Sin(x/3.1)),
				Mem:  clampSpark(mem*0.9 + 4*math.Sin(x/4.8)),
				Disk: disk,
				GPU:  clampSpark(gpu*0.72 + 10*math.Sin(x/2.6+0.7)),
			}
		case "burst":
			spike := 0.0
			if index > len(samples)-16 && index < len(samples)-5 {
				spike = 1
			}
			samples[index] = hubSparkSample{
				CPU: clampSpark(3 + spike*cpu), Mem: mem, Disk: disk,
				GPU: clampSpark(1 + spike*gpu),
			}
		case "climb":
			fraction := x / float64(len(samples)-1)
			samples[index] = hubSparkSample{CPU: cpu * fraction, Mem: mem, Disk: disk, GPU: gpu * fraction}
		case "wave":
			samples[index] = hubSparkSample{
				CPU:  clampSpark(cpu*0.35 + cpu*0.65*math.Sin(x/3.4+1.2)),
				Mem:  mem,
				Disk: disk,
				GPU:  clampSpark(6 + 28*math.Sin(x/2.8)),
			}
		case "mem":
			samples[index] = hubSparkSample{
				CPU: clampSpark(2 + 3*math.Sin(x/5)), Mem: clampSpark(mem*0.75 + 12*math.Sin(x/3.6)),
				Disk: disk, GPU: clampSpark(0.5 + math.Sin(x/4)),
			}
		case "disk":
			samples[index] = hubSparkSample{
				CPU: 6, Mem: 28, Disk: clampSpark(disk*0.9 + 5*math.Sin(x/6.5)), GPU: 0,
			}
		default:
			samples[index] = hubSparkSample{
				CPU:  clampSpark(cpu*0.12 + 0.4*math.Sin(x/2+cpu)),
				Mem:  mem,
				Disk: disk,
				GPU:  clampSpark(gpu*0.15 + 0.3*math.Sin(x/3+mem)),
			}
		}
	}
	samples[len(samples)-1] = hubSparkSample{CPU: cpu, Mem: mem, Disk: disk, GPU: gpu}
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
			Jobs: []slurmJob{
				{ID: "18421", Name: "protein-fold", User: "alice", State: "RUNNING", Elapsed: "5:12:08", NodeList: "gpu-a01", Partition: "gpu"},
				{ID: "18455", Name: "embed-corpus", User: "bob", State: "RUNNING", Elapsed: "2:04:41", NodeList: "gpu-b03", Partition: "gpu"},
			},
		}, Latency: 40 * time.Millisecond, Checked: now, LastSeen: now},
		{Snapshot: slurmSnapshot{Name: "Acme CPU", CollectedAt: now}, Latency: 55 * time.Millisecond, Checked: now, LastSeen: now},
	}
}

func demoGPUSnapshot(now time.Time, name string, cpu, memoryPercent, diskPercent, rxMiB, txMiB float64, gpus []gpuInfo) monitorSnapshot {
	memoryTotal := gib(512)
	diskTotal := gib(3600)
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileGPU,
		NodeName:       name,
		OSName:         "Linux",
		CPUModel:       "Demo EPYC 64-core",
		CPUCores:       64,
		Uptime:         18*24*3600 + 6*3600,
		CPUPercent:     cpu,
		CPUFrequency:   &cpuFrequencyInfo{AverageMHz: 2850, MinMHz: 1500, MaxMHz: 3700, SampledCores: 64, Source: "demo"},
		LoadAverage:    fmt.Sprintf("load %.2f  %.2f  %.2f", cpu/12, cpu/16, cpu/20),
		MemoryUsed:     uint64(float64(memoryTotal) * memoryPercent / 100),
		MemoryTotal:    memoryTotal,
		DiskUsed:       uint64(float64(diskTotal) * diskPercent / 100),
		DiskTotal:      diskTotal,
		NetworkRX:      mib(rxMiB),
		NetworkTX:      mib(txMiB),
		NetworkRXTotal: gib(12.4),
		NetworkTXTotal: gib(3.1),
		GPUs:           gpus,
	}
}

func demoCPUSnapshot(now time.Time, name string, cores int, cpu float64, load string, memoryPercent, diskPercent, rxMiB, txMiB float64) monitorSnapshot {
	memoryTotal := gib(256)
	diskTotal := gib(1800)
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileCPU,
		NodeName:       name,
		OSName:         "Linux",
		CPUModel:       "Demo Xeon",
		CPUCores:       cores,
		Uptime:         40 * 24 * 3600,
		CPUPercent:     cpu,
		LoadAverage:    load,
		MemoryUsed:     uint64(float64(memoryTotal) * memoryPercent / 100),
		MemoryTotal:    memoryTotal,
		DiskUsed:       uint64(float64(diskTotal) * diskPercent / 100),
		DiskTotal:      diskTotal,
		NetworkRX:      mib(rxMiB),
		NetworkTX:      mib(txMiB),
		NetworkRXTotal: gib(1.8),
		NetworkTXTotal: gib(0.6),
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
	return monitorSnapshot{
		CollectedAt:    now,
		Profile:        machineProfileNAS,
		NodeName:       "storage-01",
		OSName:         "Linux",
		CPUModel:       "Demo Xeon",
		CPUCores:       16,
		Uptime:         90 * 24 * 3600,
		CPUPercent:     23,
		CPUFrequency:   &cpuFrequencyInfo{AverageMHz: 2100, MinMHz: 800, MaxMHz: 3200, SampledCores: 16, Source: "demo"},
		LoadAverage:    "load 1.84  1.52  1.40",
		MemoryUsed:     gib(48),
		MemoryTotal:    gib(128),
		DiskUsed:       gib(140),
		DiskTotal:      gib(200),
		NetworkRX:      mib(116),
		NetworkTX:      mib(18),
		NetworkRXTotal: gib(84),
		NetworkTXTotal: gib(21),
		NetworkInterfaces: []networkInterfaceInfo{{
			Name: "bond0", RX: mib(116), TX: mib(18), RXTotal: gib(84), TXTotal: gib(21),
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
			{Name: "acme-web", Image: "registry.example/acme/web:1.4.2", State: "running", Health: "healthy", Running: true, CPU: 6, MemoryUsed: gib(1.2), MemoryLimit: gib(4), Ports: "8080", PIDs: 12, Uptime: 14 * 24 * 3600},
			{Name: "acme-sync", Image: "registry.example/acme/sync:0.9.1", State: "running", Health: "unhealthy", Running: true, CPU: 40, MemoryUsed: gib(2.4), MemoryLimit: gib(4), Ports: "—", PIDs: 4, Restarts: 3, Uptime: 2 * 3600},
			{Name: "acme-metrics", Image: "registry.example/acme/metrics:2.1.0", State: "running", Health: "healthy", Running: true, CPU: 3, MemoryUsed: mib(480), MemoryLimit: gib(1), Ports: "9090", PIDs: 8, Uptime: 30 * 24 * 3600},
		},
		PM2Processes: []pm2ProcessInfo{
			{ID: 0, PID: 2210, Name: "catalog", Status: "online", Mode: "fork", CPU: 2, Memory: mib(180), Uptime: 20 * 24 * 3600},
			{ID: 1, PID: 2218, Name: "preview", Status: "errored", Mode: "fork", CPU: 0, Memory: mib(40), Restarts: 6, Uptime: 15 * 60},
		},
	}
}

func demoGPUNode(mode colorMode, width, height int) *monitorModel {
	now := time.Now()
	snapshot := demoGPUSnapshot(now, "gpu-a01", 62, 71, 44, 86, 14, []gpuInfo{
		demoGPU(0, "NVIDIA L40S", 48, 41, 91, 71, 2400, 280, 300, "alice", "train.py"),
		demoGPU(1, "NVIDIA L40S", 48, 18, 47, 58, 1800, 160, 300, "bob", "embed.py"),
	})
	snapshot.Processes = []processInfo{
		{PID: 4812, User: "alice", State: "R", Command: "train.py", CPU: 186, Memory: 22, RSS: gib(112), Elapsed: 5*3600 + 12*60},
		{PID: 5104, User: "bob", State: "R", Command: "embed.py", CPU: 74, Memory: 9, RSS: gib(46), Elapsed: 2*3600 + 4*60},
		{PID: 2201, User: "alice", State: "S", Command: "jupyter-lab", CPU: 8, Memory: 3.2, RSS: gib(16), Elapsed: 26 * 3600},
		{PID: 904, User: "root", State: "S", Command: "containerd", CPU: 2.1, Memory: 0.8, RSS: gib(4), Elapsed: 18 * 24 * 3600},
		{PID: 6630, User: "bob", State: "S", Command: "rclone", CPU: 1.4, Memory: 0.4, RSS: gib(2), Elapsed: 40 * 60},
		{PID: 1188, User: "alice", State: "S", Command: "sshd", CPU: 0.2, Memory: 0.1, RSS: mib(48), Elapsed: 6 * 3600},
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
		slurmQueue: &nodeSlurmQueue{
			Cluster:     "Acme GPU",
			Node:        "gpu-a01",
			CollectedAt: now.Add(-8 * time.Second),
			Jobs: []slurmDisplayJob{
				{Job: slurmJob{ID: "18421", Partition: "gpu", Name: "protein-fold", User: "alice", State: "RUNNING", Priority: 24000, QOS: "normal", Elapsed: "5:12:08", TimeLimit: "12:00:00"}},
				{Job: slurmJob{ID: "18455", Partition: "gpu", Name: "embed-corpus", User: "bob", State: "RUNNING", Priority: 18000, QOS: "normal", Elapsed: "2:04:41", TimeLimit: "8:00:00"}},
				{Next: true, Job: slurmJob{ID: "18502", Partition: "gpu", Name: "eval-suite", User: "carol", State: "PENDING", Priority: 12000, QOS: "normal", Elapsed: "0:00", TimeLimit: "4:00:00", Reason: "Resources"}},
			},
		},
	}
	return model
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
