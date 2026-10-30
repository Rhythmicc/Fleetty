package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestParseCurrentCPUFrequencies(t *testing.T) {
	data := "cpu MHz: 9999\n\nprocessor : 8\ncpu MHz : 3200.125\n" +
		"processor: 0\ncpu MHz: 800.500\nmodel name: CPU @ 5.0GHz\n" +
		"\ncpu MHz: 9000\n\nprocessor: invalid\ncpu MHz: 7000\n" +
		"\nprocessor: -1\ncpu MHz: 6000\n\nprocessor: 2\ncpu MHz: Unknown"
	want := map[string]float64{"cpu8": 3200.125, "cpu0": 800.5}
	if got := parseCPUFrequencies(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("frequencies = %#v, want %#v", got, want)
	}
	for _, value := range []string{"0", "-10", "NaN", "+Inf", "-Inf", "1e400", "unknown", "2400 MHz"} {
		if got := parseCPUFrequencies("processor: 0\ncpu MHz: " + value); len(got) != 0 {
			t.Fatalf("invalid frequency %q accepted: %#v", value, got)
		}
	}
	if got := parseCPUFrequencies("processor: 0\nclock: 5000\ncpuinfo_max_freq: 6000000"); len(got) != 0 {
		t.Fatalf("static/ambiguous clock used as live frequency: %#v", got)
	}
}

func TestCPUFrequencyAggregationTracksOnlineIDsAndRefreshes(t *testing.T) {
	snapshot := monitorSnapshot{CPUCores: 4, CPUCoreUsage: []cpuCoreUsage{
		{CPU: "cpu8"}, {CPU: "cpu0", Available: true}, {CPU: "cpu2"}, {CPU: "cpu4"},
	}}
	applyCPUFrequencies(&snapshot, map[string]float64{
		"cpu0": 800, "cpu8": 3200, "cpu2": math.NaN(), "cpu4": math.Inf(1), "cpu999": 9000,
	}, "/proc/cpuinfo")
	want := cpuFrequencyInfo{AverageMHz: 2000, MinMHz: 800, MaxMHz: 3200, SampledCores: 2, Source: "/proc/cpuinfo"}
	if snapshot.CPUFrequency == nil || *snapshot.CPUFrequency != want || snapshot.CPUCoreUsage[0].FrequencyMHz != 3200 || snapshot.CPUCoreUsage[1].FrequencyMHz != 800 {
		t.Fatalf("incorrect partial frequency sample: %+v / %+v", snapshot.CPUFrequency, snapshot.CPUCoreUsage)
	}
	previous := snapshot.CPUFrequency
	applyCPUFrequencies(&snapshot, map[string]float64{"cpu2": 2800}, "/proc/cpuinfo")
	if snapshot.CPUFrequency.SampledCores != 1 || snapshot.CPUFrequency.AverageMHz != 2800 || snapshot.CPUCoreUsage[0].FrequencyMHz != 0 || *previous != want {
		t.Fatal("frequency sample is stale or mutated the previous published summary")
	}
	applyCPUFrequencies(&snapshot, nil, "/proc/cpuinfo")
	if snapshot.CPUFrequency != nil {
		t.Fatal("missing data retained the previous frequency")
	}
	for _, core := range snapshot.CPUCoreUsage {
		if core.FrequencyMHz != 0 {
			t.Fatal("missing per-core data retained a stale frequency")
		}
	}
}

func TestCPUFrequencyTransportAndExports(t *testing.T) {
	snapshot := monitorSnapshot{NodeName: "node", CPUCores: 3, CPUCoreUsage: []cpuCoreUsage{
		{CPU: "cpu0"}, {CPU: "cpu1", Available: true}, {CPU: "cpu2"},
	}}
	applyCPUFrequencies(&snapshot, map[string]float64{"cpu0": 1800, "cpu2": 3200}, "/proc/cpuinfo")
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded monitorSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, snapshot) {
		t.Fatalf("RPC lost frequency data: %+v, %v", decoded, err)
	}
	data, err = json.Marshal(exportSnapshot(decoded))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cpu_frequency":{"average_mhz":2500`, `"frequency_mhz":1800`, `"sampled_cores":2`, `"source":"/proc/cpuinfo"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON missing %s: %s", want, data)
		}
	}
	metrics := renderPrometheusMetrics(decoded)
	for _, want := range []string{
		`fleetty_cpu_frequency_mhz{node="node"} 2500`,
		`fleetty_cpu_min_frequency_mhz{node="node"} 1800`,
		`fleetty_cpu_max_frequency_mhz{node="node"} 3200`,
		`fleetty_cpu_frequency_sampled_cores{node="node"} 2`,
		`fleetty_cpu_core_frequency_mhz{node="node",cpu="cpu0"} 1800`,
		`fleetty_cpu_core_frequency_mhz{node="node",cpu="cpu2"} 3200`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing metric %s", want)
		}
	}
	if strings.Contains(metrics, `fleetty_cpu_core_frequency_mhz{node="node",cpu="cpu1"}`) || strings.Count(metrics, "# TYPE fleetty_cpu_core_frequency_mhz ") != 1 {
		t.Fatal("unknown frequency exported as zero or duplicate metric metadata")
	}
	applyCPUFrequencies(&decoded, nil, "/proc/cpuinfo")
	if strings.Contains(renderPrometheusMetrics(decoded), "frequency_mhz") {
		t.Fatal("missing frequency exported as a measurement")
	}
}

func TestCPUFrequencyPanelsFitAndRetainLoad(t *testing.T) {
	for _, available := range []bool{false, true} {
		m := &monitorModel{profile: machineProfileGeneral, height: 40, snapshot: monitorSnapshot{
			CollectedAt: time.Now(), CPUPercent: 42, CPUCores: 2,
			LoadAverage: "load 0.5 · 0.2 · 0.1", MemoryTotal: 1, DiskTotal: 1,
			CPUCoreUsage: []cpuCoreUsage{{CPU: "cpu0", Percent: 80, Available: true}, {CPU: "cpu1"}},
		}}
		want := "CLK --"
		if available {
			applyCPUFrequencies(&m.snapshot, map[string]float64{"cpu0": 1800, "cpu1": 3200}, "/proc/cpuinfo")
			want = "AVG 2.50GHz"
		}
		for _, mode := range []colorMode{colorModeDark, colorModeLight} {
			m.colorMode = mode
			for _, width := range []int{32, 60, 100, 160, 240} {
				m.width = width
				card, _ := m.metricWidgetCard(dashboardPanelCPU)
				for _, size := range []widgetSize{widgetSizeSmall, widgetSizeMedium, widgetSizeLarge} {
					view := m.renderCPUWidget(card, width, size)
					plain := ansi.Strip(view)
					if !strings.Contains(plain, want) || !strings.Contains(plain, "42.0%") || !strings.Contains(plain, "load 0.5") {
						t.Fatalf("available=%v width=%d %s lost CPU summary:\n%s", available, width, size, plain)
					}
					if available && size != widgetSizeSmall && (!strings.Contains(plain, "1.80GHz") || !strings.Contains(plain, "3.20GHz")) {
						t.Fatalf("per-core frequencies missing:\n%s", plain)
					}
					for _, line := range strings.Split(view, "\n") {
						if lipgloss.Width(line) > width {
							t.Fatalf("CPU frequency row exceeds %d columns", width)
						}
					}
				}
				for _, rich := range []bool{false, true} {
					view := ansi.Strip(renderPagePanel(width, m.hostHealthPanelSpec(width, rich)))
					if !strings.Contains(view, want) {
						t.Fatalf("overview lost frequency at width=%d rich=%v:\n%s", width, rich, view)
					}
				}
			}
		}
	}
}

func TestCurrentPlatformCPUFrequency(t *testing.T) {
	if runtime.GOOS != "linux" && !(runtime.GOOS == "darwin" && runtime.GOARCH == "arm64") {
		snapshot := monitorSnapshot{CPUFrequency: &cpuFrequencyInfo{AverageMHz: 2400},
			CPUCoreUsage: []cpuCoreUsage{{CPU: "cpu0", FrequencyMHz: 2400}}}
		collector := newMetricsCollector(machineConfig{Profile: machineProfileGeneral})
		collector.collectCPUFrequency(&snapshot)
		if snapshot.CPUFrequency != nil || snapshot.CPUCoreUsage[0].FrequencyMHz != 0 {
			t.Fatal("unsupported platform must not invent a live frequency")
		}
		return
	}
	collector := newMetricsCollector(machineConfig{Profile: machineProfileGeneral})
	snapshot, _ := collector.collectSummary()
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/cpuinfo")
		if err != nil || len(parseCPUFrequencies(string(data))) == 0 {
			t.Skip("kernel does not report cpu MHz")
		}
	} else {
		if collector.cpuFrequencyReader == nil {
			t.Skip("IOReport CPU frequency channels unavailable on this host")
		}
		time.Sleep(150 * time.Millisecond)
		snapshot, _ = collector.collectSummary()
	}
	if snapshot.CPUFrequency == nil || !validCPUFrequency(snapshot.CPUFrequency.AverageMHz) {
		t.Fatal("available native frequency was not collected")
	}
	t.Log(fmt.Sprintf("current CPU: %+v", snapshot.CPUFrequency))
}

func TestCPUFrequencyCoreGridIsComplete(t *testing.T) {
	for _, count := range []int{1, 18, 128, 256} {
		cores := make([]cpuCoreUsage, count)
		for index := range cores {
			cores[index] = cpuCoreUsage{CPU: fmt.Sprintf("cpu%d", index), Percent: 42, Available: true}
			if index%3 != 0 {
				cores[index].FrequencyMHz = 12500
			}
		}
		for _, width := range []int{16, 24, 36, 56, 96, 156, 236} {
			lines := renderCPUCoreLines(cores, width)
			seen := make(map[string]int)
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("%d cores overflow %d columns", count, width)
				}
				for _, field := range strings.Fields(ansi.Strip(line)) {
					seen[field]++
				}
			}
			for _, core := range cores {
				if seen[core.CPU] != 1 {
					t.Fatalf("%s rendered %d times at width %d", core.CPU, seen[core.CPU], width)
				}
			}
			if count > 1 && width >= 24 && (seen["12.50GHz"] == 0 || seen["--"] == 0) {
				t.Fatalf("known/unknown frequencies lost at width %d: %v", width, lines)
			}
		}
	}
}
