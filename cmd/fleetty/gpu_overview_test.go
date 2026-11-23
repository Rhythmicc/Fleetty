package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestGPUOverviewReadingDensity(t *testing.T) {
	apple := gpuInfo{Name: "Apple M5 Max", Platform: "apple", CoreCount: 40,
		Utilization: 33, RendererUtilization: 38, TilerUtilization: 26,
		MemoryUsed: 3200 << 20, MemoryTotal: 9 << 30}
	nvidia := gpuInfo{Name: "NVIDIA RTX 5090", Utilization: 75,
		MemoryUsed: 16 << 30, MemoryTotal: 32 << 30, Temperature: 65, Power: 220, ClockMHz: 2100}
	for _, gpu := range []gpuInfo{apple, nvidia} {
		for _, width := range []int{28, 46, 60, 80, 104, 180, 240} {
			spec := buildGPUOverviewPanelSpec([]gpuInfo{gpu}, "", width, 0)
			view := ansi.Strip(strings.Join(spec.lines, "\n"))
			if spec.title != "GPU" || spec.meta != "" {
				t.Fatal("single GPU retains redundant section/device labels")
			}
			for _, unwanted := range []string{"API ", "ARCH ", "SOURCE ", "Unified memory architecture", "Refresh", "Live telemetry", "NO ACTIVE JOB", "IDLE"} {
				if strings.Contains(view, unwanted) {
					t.Fatalf("GPU retains secondary prose %q: %s", unwanted, view)
				}
			}
			for _, line := range spec.lines {
				if lipgloss.Width(line) > width-4 {
					t.Fatalf("GPU overflows width=%d: %q", width, ansi.Strip(line))
				}
			}
			if width >= 104 && len(spec.lines) != 3 {
				t.Fatalf("wide GPU should use 3 content rows, got %d", len(spec.lines))
			}
			if gpu.Platform == "apple" {
				for _, value := range []string{"Apple M5 Max", "40 cores", "LOAD 33%", "UMA", "RENDER 38%", "TILER 26%"} {
					if !strings.Contains(view, value) {
						t.Fatalf("GPU lost %q: %s", value, view)
					}
				}
				if strings.Contains(view, "0W") || strings.Contains(view, "0°C") {
					t.Fatal("Apple GPU invents thermal/power readings")
				}
			} else if width >= 80 {
				for _, value := range []string{"VRAM", "65°C", "220W", "2100MHz"} {
					if !strings.Contains(view, value) {
						t.Fatalf("GPU lost telemetry %s", value)
					}
				}
			}
		}
	}
}

func TestOverviewSummaryHeightDoesNotGrow(t *testing.T) {
	for _, width := range []int{144, 160, 180, 240} {
		for _, withGPU := range []bool{false, true} {
			m := cpuChartTestModel(18, width, 40)
			m.monitorPage = monitorPageOverview
			m.snapshot.CPUModel = "Apple Silicon CPU"
			m.cpuHistory = []float64{10, 20, 30}
			m.hourCPUHistory = []float64{20, 30, 40}
			if withGPU {
				m.snapshot.GPUs = []gpuInfo{{Name: "Apple M5 Max", Platform: "apple", CoreCount: 40}}
			}
			for i := 0; i < 20; i++ {
				m.snapshot.NetworkProcesses = append(m.snapshot.NetworkProcesses, processNetworkInfo{
					PID: i + 100, Name: fmt.Sprintf("net-%02d", i), RX: 2048, TX: 1024})
			}
			previousY, previousRows := -1, 0
			for _, height := range []int{40, 50, 70} {
				m.height = height
				page, placements := m.renderOverviewPage(width)
				placement := placements[0]
				if placement.Y > 15 {
					t.Fatalf("upper panels exceed 15 rows: width=%d height=%d y=%d", width, height, placement.Y)
				}
				if previousY >= 0 && (placement.Y != previousY || placement.ProcessRows <= previousRows) {
					t.Fatal("extra height went to summaries rather than processes")
				}
				if strings.Count(ansi.Strip(page), "HIST · CPU %") != 1 || strings.Contains(page, "CPU % · 1H") {
					t.Fatal("overview should contain one CPU history")
				}
				if !strings.Contains(page, "net-03") || strings.Contains(page, "net-04") {
					t.Fatal("network summary did not match host height without growing")
				}
				for _, line := range strings.Split(page, "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("compact overview overflow at width=%d", width)
					}
				}
				previousY, previousRows = placement.Y, placement.ProcessRows
			}
		}
	}
	m := cpuChartTestModel(18, 180, 50)
	m.snapshot.NetworkConnectionMode = true
	m.snapshot.NetworkConnections = []networkConnectionInfo{{Name: "ssh", RX: 9000, TX: 8000}}
	_, rows := m.networkOverviewDetails(36, 3)
	if plain := ansi.Strip(strings.Join(rows, "\n")); strings.Count(plain, "--") != 2 || strings.Contains(plain, "0 B/s") {
		t.Fatalf("unavailable socket rates became zero: %s", plain)
	}
}

func TestNetworkOverviewUsesWidthForTrafficSources(t *testing.T) {
	m := cpuChartTestModel(18, 180, 50)
	for i := 0; i < 12; i++ {
		m.snapshot.NetworkProcesses = append(m.snapshot.NetworkProcesses, processNetworkInfo{
			PID: i + 100, Name: fmt.Sprintf("net-%02d", i), RX: 2048, TX: 1024})
	}
	for _, width := range []int{60, 70, 79, 89, 120, 180} {
		filled := renderPagePanel(width, m.networkActivityPanelSpec(width, true, 5))
		if width >= 70 {
			if lipgloss.Height(filled) > 10 || !strings.Contains(filled, "net-04") || strings.Contains(filled, "net-05") {
				t.Fatal("wide network summary did not fill exactly five rows")
			}
		} else if strings.Contains(filled, "net-03") {
			t.Fatal("stacked narrow summary grew beyond three rows")
		}
	}
}

func TestNetworkOverviewShowsRatesInsteadOfPlots(t *testing.T) {
	m := cpuChartTestModel(18, 180, 50)
	m.networkRXHistory = []float64{1024, math.NaN(), 3072}
	m.networkTXHistory = []float64{0, 8192, math.Inf(1)}
	for _, width := range []int{36, 60, 70, 79, 89, 120, 180} {
		spec := m.networkActivityPanelSpec(width, true, 5)
		plain := ansi.Strip(strings.Join(spec.lines, "\n"))
		if strings.Contains(plain, "HIST ·") || !strings.Contains(plain, "1m AVG") ||
			!strings.Contains(plain, "PEAK") || !strings.Contains(plain, "TOTAL ↓") {
			t.Fatalf("overview width=%d has incorrect rate summary: %s", width, plain)
		}
		if width >= 120 {
			for _, want := range []string{"1m AVG ↓ 2.0 KiB/s  ↑ 4.0 KiB/s", "PEAK ↓ 3.0 KiB/s  ↑ 8.0 KiB/s"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("missing calculated value %q: %s", want, plain)
				}
			}
		}
		for _, line := range spec.lines {
			if lipgloss.Width(line) > width-4 {
				t.Fatalf("network content overflows at width=%d: %q", width, line)
			}
		}
	}
	detail := strings.Join(m.networkTrafficSummaryPanelSpec(120).lines, "\n")
	for _, label := range []string{"HIST · RX B/s", "HIST · TX B/s"} {
		if !strings.Contains(detail, label) {
			t.Fatalf("Network page lost %q", label)
		}
	}
	warmup := ansi.Strip(strings.Join(networkOverviewWindow(nil, []float64{0}, 70), "\n"))
	if !strings.Contains(warmup, "↓ --") || !strings.Contains(warmup, "↑ 0 B/s") {
		t.Fatalf("missing history and measured idle must look different: %q", warmup)
	}
	history := make([]float64, networkHistorySamples+1)
	history[0] = 1024
	if avg, peak, ok := networkWindowRates(history); !ok || avg != 0 || peak != 0 {
		t.Fatalf("network summary included a sample outside its window: avg=%d peak=%d valid=%t", avg, peak, ok)
	}
}

func TestMultiGPUOverviewClockFitsAndAligns(t *testing.T) {
	gpus := []gpuInfo{
		{Name: "A100-PCIE-40GB", ClockMHz: 345, Temperature: 34},
		{Name: "TITAN RTX", ClockMHz: 1980, Temperature: 28},
		{Name: "No clock", Temperature: 45},
	}
	for _, width := range []int{36, 68, multiGPUOverviewClockMinWidth - 1, multiGPUOverviewClockMinWidth, 140} {
		lines := multiGPUOverviewLines(gpus, width, 0)
		header := ansi.Strip(lines[0])
		clkEnd := strings.Index(header, "CLK")
		if (clkEnd >= 0) != (width >= multiGPUOverviewClockMinWidth) {
			t.Fatalf("incorrect CLK visibility at width %d: %s", width, header)
		}
		for i, clock := range []string{"345MHz", "1980MHz", "--"} {
			row := ansi.Strip(lines[i+1])
			if lipgloss.Width(row) > width {
				t.Fatalf("clock overflow at width %d: %s", width, row)
			}
			if clkEnd < 0 {
				if strings.Contains(row, "MHz") {
					t.Fatalf("clock shown in compact row: %s", row)
				}
				continue
			}
			at := strings.Index(row, clock)
			if at < 0 || lipgloss.Width(row[:at])+len(clock) != clkEnd+len("CLK") {
				t.Fatalf("clock is missing or not aligned at width %d: %s", width, row)
			}
		}
	}
}

func TestGPUOverviewDoesNotStretchCard(t *testing.T) {
	m := cpuChartTestModel(18, 180, 50)
	m.monitorPage = monitorPageOverview
	m.snapshot.GPUs = []gpuInfo{{Name: "Apple M5 Max", Platform: "apple", CoreCount: 40}}
	m.snapshot.CollectedAt = time.Now()
	m.snapshot.NetworkProcesses = []processNetworkInfo{{PID: 1, Name: "sync", RX: 1024}}
	page, placements := m.renderOverviewPage(180)
	lines := strings.Split(ansi.Strip(page), "\n")
	gpuStart, gpuEnd := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "╭─ GPU ") {
			gpuStart = i
		}
		if gpuStart >= 0 && i > gpuStart && strings.HasPrefix(line, "╰") {
			gpuEnd = i
			break
		}
	}
	if gpuStart < 0 || gpuEnd-gpuStart+1 != 5 {
		t.Fatalf("GPU still stretched to host height: start=%d end=%d", gpuStart, gpuEnd)
	}
	if len(placements) != 1 || placements[0].ProcessRows < 15 {
		t.Fatal("simplified overview still crowds the process table")
	}
	for _, unwanted := range []string{"Rates refresh", "TOTAL RX", "TOTAL TX", "60 SECOND HISTORY", "SOURCE IOKit"} {
		if strings.Contains(page, unwanted) {
			t.Fatalf("overview retains duplicate label %s", unwanted)
		}
	}
}

func TestOverviewGPUAndSlurmFitsWithoutClipping(t *testing.T) {
	makeGPUs := func(count int) []gpuInfo {
		gpus := make([]gpuInfo, count)
		for i := range gpus {
			gpus[i] = gpuInfo{
				Index: i, Name: "NVIDIA L40S", Utilization: float64(90 - i*5),
				MemoryUsed: uint64(40-i) << 30, MemoryTotal: 48 << 30,
				Power: 280, PowerLimit: 300, ClockMHz: 2400, Temperature: 70,
				Workloads: []gpuWorkloadInfo{{PID: 4812 + i, User: "alice", Name: "train.py"}},
			}
		}
		return gpus
	}
	procs := make([]processInfo, 12)
	for i := range procs {
		procs[i] = processInfo{
			PID: 4800 + i, User: "alice", State: "R",
			CPU: float64(180 - i*10), RSS: 16 << 30,
			Command: fmt.Sprintf("proc-%02d", i),
		}
	}
	cases := []struct {
		width, height, gpus, minProcRows int
	}{
		{142, 34, 2, 8},
		{142, 34, 4, 6},
		{142, 34, 8, 6},
		{142, 24, 2, 3},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d/gpus=%d", tc.width, tc.height, tc.gpus), func(t *testing.T) {
			m := &monitorModel{
				screen: screenMonitor, profile: machineProfileGPU, nodeName: "gpu-a01",
				width: tc.width, height: tc.height,
				cpuHistory: []float64{45, 58, 62},
				snapshot: monitorSnapshot{
					CollectedAt: time.Now(), Profile: machineProfileGPU,
					CPUModel: "AMD EPYC 9354 32-Core Processor", CPUCores: 32, CPUPercent: 62,
					LoadAverage: "load 10.33 · 7.75 · 6.20",
					MemoryUsed:  364 << 30, MemoryTotal: 512 << 30,
					DiskUsed: 1500 << 30, DiskTotal: 3500 << 30,
					NetworkRX: 86 << 20, NetworkTX: 14 << 20,
					NetworkRXTotal: 12 << 30, NetworkTXTotal: 3 << 30,
					NetworkInterfaces: []networkInterfaceInfo{{Name: "eth0"}},
					GPUs:              makeGPUs(tc.gpus),
					Processes:         procs,
				},
				slurmQueue: &nodeSlurmQueue{
					Cluster: "Acme GPU", Node: "gpu-a01", CollectedAt: time.Now().Add(-8 * time.Second),
					Jobs: []slurmDisplayJob{
						{Job: slurmJob{ID: "18421", Partition: "gpu", Name: "protein-fold", User: "alice", State: "RUNNING", Priority: 24000, QOS: "normal", Elapsed: "5:12:08", TimeLimit: "12:00:00"}},
						{Job: slurmJob{ID: "18455", Partition: "gpu", Name: "embed-corpus", User: "bob", State: "RUNNING", Priority: 18000, QOS: "normal", Elapsed: "2:04:41", TimeLimit: "8:00:00"}},
						{Job: slurmJob{ID: "18502", Partition: "gpu", Name: "eval-suite", User: "carol", State: "PENDING", Priority: 12000, QOS: "normal", Elapsed: "0:00", TimeLimit: "4:00:00", Reason: "Resources"}},
					},
				},
			}
			rendered := m.monitorView()
			plain := ansi.Strip(rendered)
			if lipgloss.Height(rendered) != tc.height {
				t.Fatalf("height = %d, want %d", lipgloss.Height(rendered), tc.height)
			}
			if m.dashboardContent > m.dashboardViewport || strings.Contains(plain, "↕") {
				t.Fatalf("overview clipped: content=%d viewport=%d\n%s", m.dashboardContent, m.dashboardViewport, plain)
			}
			for _, want := range []string{"HOST HEALTH", "NODE QUEUE / SLURM", "GPU", "TOP PROCESSES", "NET ", "refresh", "quit"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("missing %q:\n%s", want, plain)
				}
			}
			if tc.height >= 34 && !strings.Contains(plain, "RESOURCES") {
				t.Fatalf("missing pending blocker on 34-row view:\n%s", plain)
			}
			if tc.height < 28 && !strings.Contains(plain, "1–2/3 JOBS") {
				t.Fatalf("missing queue count range on compact 24-row view:\n%s", plain)
			}
			if strings.Contains(plain, "NETWORK ACTIVITY") {
				t.Fatalf("unexpected duplicated NETWORK ACTIVITY card:\n%s", plain)
			}
			if m.monitorRows < tc.minProcRows {
				t.Fatalf("process rows = %d, want >= %d\n%s", m.monitorRows, tc.minProcRows, plain)
			}
			for i, line := range strings.Split(rendered, "\n") {
				if lipgloss.Width(line) > tc.width {
					t.Fatalf("line %d overflows width %d: %q", i, tc.width, ansi.Strip(line))
				}
			}
		})
	}
}
