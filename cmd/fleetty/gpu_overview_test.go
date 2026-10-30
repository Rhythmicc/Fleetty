package main

import (
	"fmt"
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
			spec := buildGPUOverviewPanelSpec([]gpuInfo{gpu}, "", width)
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
				if !strings.Contains(page, "net-04") || strings.Contains(page, "net-05") {
					t.Fatal("network summary should fill five rows without growing")
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

func TestNetworkOverviewFillsSpareRows(t *testing.T) {
	m := cpuChartTestModel(18, 180, 50)
	for i := 0; i < 12; i++ {
		m.snapshot.NetworkProcesses = append(m.snapshot.NetworkProcesses, processNetworkInfo{
			PID: i + 100, Name: fmt.Sprintf("net-%02d", i), RX: 2048, TX: 1024})
	}
	for _, width := range []int{60, 70, 79, 89, 120, 180} {
		three := renderPagePanel(width, m.networkActivityPanelSpec(width, true, 3))
		filled := renderPagePanel(width, m.networkActivityPanelSpec(width, true, 5))
		if lipgloss.Height(three) != lipgloss.Height(filled) {
			t.Fatalf("filling unused rows grew the panel at width=%d", width)
		}
		if width >= 70 {
			if !strings.Contains(filled, "net-04") || strings.Contains(filled, "net-05") {
				t.Fatal("wide network summary did not fill exactly five rows")
			}
		} else if strings.Contains(filled, "net-03") {
			t.Fatal("stacked narrow summary grew beyond three rows")
		}
	}
}

func TestNetworkOverviewSummaryBelowHistories(t *testing.T) {
	m := cpuChartTestModel(18, 180, 50)
	m.cpuHistory = []float64{10, 20, 30}
	m.networkRXHistory = []float64{1024, 2048, 4096}
	m.networkTXHistory = []float64{4096, 2048, 1024}
	for _, width := range []int{60, 70, 79, 89, 120, 180} {
		spec := m.networkActivityPanelSpec(width, true, 5)
		lines := strings.Split(ansi.Strip(strings.Join(spec.lines, "\n")), "\n")
		if !strings.Contains(lines[0], "HIST · RX B/s") ||
			!strings.Contains(lines[len(lines)-1], "TOTAL ↓") {
			t.Fatalf("network summary is not below the charts at width=%d: %v", width, lines)
		}
		if width >= 70 && len(lines) != 8 {
			t.Fatalf("wide network panel height changed at width=%d: %d", width, len(lines))
		}
		for _, line := range lines {
			if lipgloss.Width(line) > width-4 {
				t.Fatalf("network content overflows at width=%d: %q", width, line)
			}
		}
	}
	for _, width := range []int{70, 79, 89, 120, 180} {
		host := m.hostHealthPanelSpec(width, true)
		network := m.networkActivityPanelSpec(width, true, 5)
		hostLines := strings.Split(ansi.Strip(strings.Join(host.lines, "\n")), "\n")
		networkLines := strings.Split(ansi.Strip(strings.Join(network.lines, "\n")), "\n")
		if !strings.Contains(hostLines[0], "HIST · CPU %") ||
			!strings.Contains(networkLines[0], "HIST · RX B/s") ||
			!strings.Contains(hostLines[4], "→ now") ||
			!strings.Contains(networkLines[4], "→ now") {
			t.Fatalf("CPU and network charts are not vertically aligned at width=%d", width)
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
