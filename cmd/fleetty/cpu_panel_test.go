package main

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCPUCoreHeaderCompactAndResponsive(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		for _, count := range []int{18, 128} {
			for _, state := range []string{"full", "partial", "unavailable", "warmup"} {
				m := &monitorModel{colorMode: mode, snapshot: monitorSnapshot{
					CPUCores: count, CPUPercent: 18.4, LoadAverage: "load 23.76 · 23.57 · 23.50",
				}}
				frequencies := make(map[string]float64)
				for i := 0; i < count; i++ {
					id := fmt.Sprintf("cpu%d", i)
					m.snapshot.CPUCoreUsage = append(m.snapshot.CPUCoreUsage, cpuCoreUsage{
						CPU: id, Percent: 18.4, Available: state != "warmup",
					})
					if state != "unavailable" && (state != "partial" || i%2 == 0) {
						frequencies[id] = 2000 + float64(i%2)*1340
					}
				}
				applyCPUFrequencies(&m.snapshot, frequencies, "/proc/cpuinfo")
				card, _ := m.metricWidgetCard(dashboardPanelCPU)
				card.primaryHistory = []float64{10, 20, 60, 18.4}
				for _, width := range []int{32, 54, 60, 80, 120, 160, 240} {
					contentWidth := width - 4
					lines := m.cpuMetricLines(card, width)
					headerRows := len(m.cpuMetricHeaderLines(card, width))
					wantRows := 2
					if lipgloss.Width(card.value)+2+lipgloss.Width(card.detail) > contentWidth {
						wantRows = 3
					}
					if headerRows != wantRows {
						t.Fatalf("%s %d CPUs width=%d: %d header rows, want %d", state, count, width, headerRows, wantRows)
					}
					header := ansi.Strip(strings.Join(lines[:headerRows], "\n"))
					for _, want := range []string{"18.4%", "load 23.76 · 23.57 · 23.50", "CPUs"} {
						if !strings.Contains(header, want) {
							t.Fatalf("header lost %q at width %d:\n%s", want, width, header)
						}
					}
					if state == "partial" && !strings.Contains(header, fmt.Sprintf("%d/%d CPUs", (count+1)/2, count)) {
						t.Fatalf("partial frequency coverage lost: %s", header)
					}
					if state == "unavailable" {
						if !strings.Contains(header, "CLK --") || !strings.Contains(header, "-- unavailable") {
							t.Fatalf("unavailable frequency ambiguous: %s", header)
						}
					} else if !strings.Contains(header, "GHz") || !strings.Contains(header, "AVG ") {
						t.Fatalf("frequency summary lost: %s", header)
					}
					if contentWidth >= 76 && !strings.Contains(strings.Join(lines, "\n"), "HIST · CPU %") {
						t.Fatalf("wide CPU panel lost side history: %s", strings.Join(lines, "\n"))
					}
					for _, line := range lines[:headerRows] {
						if strings.TrimSpace(ansi.Strip(line)) == "" || lipgloss.Width(line) > contentWidth {
							t.Fatalf("blank/overflowed header at width %d: %q", width, line)
						}
					}
					if !strings.HasPrefix(ansi.Strip(lines[headerRows]), "cpu0 ") {
						t.Fatal("core grid must immediately follow the compact header")
					}
				}
			}
		}
	}
}

func TestOverviewCPUInformationGroups(t *testing.T) {
	for _, mode := range []colorMode{colorModeDark, colorModeLight} {
		for _, state := range []string{"full", "partial", "unavailable"} {
			m := &monitorModel{colorMode: mode, snapshot: monitorSnapshot{
				CPUModel: "AMD EPYC 7702 64-Core Processor", CPUCores: 128, CPUPercent: 37.8,
				LoadAverage: "load 76.57 · 80.42 · 74.61", MemoryTotal: 1, DiskTotal: 1,
			}, cpuHistory: []float64{20, 30, 37.8}, hourCPUHistory: []float64{15, 25, 35}}
			if state != "unavailable" {
				m.snapshot.CPUFrequency = &cpuFrequencyInfo{
					AverageMHz: 2490, MinMHz: 2000, MaxMHz: 3340, SampledCores: 128, Source: "/proc/cpuinfo",
				}
				if state == "partial" {
					m.snapshot.CPUFrequency.SampledCores = 64
				}
			}
			for _, width := range []int{32, 46, 60, 80, 90, 120, 180, 240} {
				spec := m.hostHealthPanelSpec(width, true)
				if len(spec.lines) != 8 {
					t.Fatalf("host summary should use 8 rows: %d", len(spec.lines))
				}
				rows := make([]string, 3)
				for i := range rows {
					rows[i] = ansi.Strip(spec.lines[i])
					if lipgloss.Width(spec.lines[i]) > width-4 {
						t.Fatalf("CPU overview overflow width=%d: %q", width, rows[i])
					}
				}
				joined := strings.Join(rows, "\n")
				for _, unwanted := range []string{"HEALTHY", "WATCH", "HIGH", "CRITICAL", "vCPU", "/proc/", "128/128", "64/128"} {
					if strings.Contains(joined, unwanted) {
						t.Fatalf("overview retains misleading/diagnostic text %q: %s", unwanted, joined)
					}
				}
				if !strings.HasPrefix(rows[0], "CPU ") || !strings.Contains(rows[0], "128 logical") {
					t.Fatalf("hardware row lost logical count: %s", rows[0])
				}
				clock := "AVG 2.49GHz"
				if state == "unavailable" {
					clock = "CLK --"
					if strings.Contains(joined, "GHz") {
						t.Fatal("missing frequency rendered as a measurement")
					}
				}
				if !strings.HasPrefix(rows[1], "USAGE 37.8%") || !strings.Contains(rows[1], clock) {
					t.Fatalf("primary readings truncated: %s", rows[1])
				}
				if !strings.HasPrefix(rows[2], "LOAD 76.57 · 80.42 · 74.61") {
					t.Fatalf("load values moved or truncated: %s", rows[2])
				}
				if width >= 80 {
					if !strings.Contains(strings.Join(spec.lines, "\n"), "HIST · CPU %") || !strings.Contains(rows[2], "(1m 5m 15m)") {
						t.Fatalf("wide overview lost model or trend labels: %s", joined)
					}
					if state != "unavailable" && !strings.Contains(rows[1], "RANGE 2.00–3.34GHz") {
						t.Fatal("wide overview lost frequency range")
					}
				}
				if width >= 90 && !strings.Contains(rows[0], m.snapshot.CPUModel) {
					t.Fatal("wide hardware row lost CPU model")
				}
				if strings.Contains(strings.Join(spec.lines, "\n"), "1H") {
					t.Fatal("overview retains duplicate CPU history")
				}
				compact := m.hostHealthPanelSpec(width, false)
				if len(compact.lines) != 11 || strings.Contains(ansi.Strip(compact.lines[0]), "HEALTHY") {
					t.Fatal("compact overview miscounted history or retained CPU health diagnosis")
				}
			}
		}
	}
}
