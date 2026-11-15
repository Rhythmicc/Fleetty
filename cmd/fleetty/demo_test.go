//go:build demo

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestDemoFramesUseFictionalData(t *testing.T) {
	forbidden := []string{"SSSLab", "ssslab", "NAS01", "4090", "5090", "A100"}
	for _, screen := range []string{"hub", "hub-links", "hub-queue", "gpu", "gpu-compute", "nas"} {
		for _, theme := range []string{"dark", "light"} {
			frame, err := renderDemoFrame(screen, theme, 0, 0)
			if err != nil {
				t.Fatalf("%s %s: %v", screen, theme, err)
			}
			plain := ansi.Strip(frame)
			if strings.TrimSpace(plain) == "" {
				t.Fatalf("%s %s rendered an empty frame", screen, theme)
			}
			if !strings.Contains(frame, "\x1b[") {
				t.Fatalf("%s %s rendered without color", screen, theme)
			}
			for _, word := range forbidden {
				if strings.Contains(plain, word) {
					t.Fatalf("%s %s contains %q", screen, theme, word)
				}
			}
		}
	}

	hub, err := renderDemoFrame("hub", "dark", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	hubPlain := ansi.Strip(hub)
	for _, text := range []string{
		"Acme Research Cluster", "gpu-a01", "gpu-c01", "cpu-01", "storage-01", "OFFLINE", "14/16",
		"i/o timeout", "alice +1", "DEVICES", "NODE ·",
		"↓86M ↑14M", "VRAM", "59/96G", "71°C", "HIST", "NET", "▲ storage-01",
		"SLURM QUEUE", "RUN 3", "NEXT 1", "PENDING 13", "18421", "protein-fold", "batch-encode",
	} {
		if !strings.Contains(hubPlain, text) {
			t.Fatalf("hub frame missing %q", text)
		}
	}
	if strings.Contains(hubPlain, "PAGE ") {
		t.Fatal("140x48 hub spilled onto a second page")
	}
	if strings.Contains(hubPlain, "HTTP 1/2") {
		t.Fatal("NAS services should not take the memory column")
	}
	nasSelected := demoHub(colorModeDark, 140, 30)
	nasSelected.cursor = 14
	for _, text := range []string{"HTTP 1/2", "CTR 2/3", "PM2 1/2"} {
		if !strings.Contains(ansi.Strip(nasSelected.hubView()), text) {
			t.Fatalf("NAS sidebar missing %q", text)
		}
	}
	wide, err := renderDemoFrame("hub", "dark", 220, 48)
	if err != nil {
		t.Fatal(err)
	}
	widePlain := ansi.Strip(wide)
	for _, text := range []string{"storage-01", "cpu-01", "gpu-a01", "VRAM", "59/96G"} {
		if !strings.Contains(widePlain, text) {
			t.Fatalf("220-col hub frame missing %q", text)
		}
	}
	if strings.Contains(widePlain, "PAGE ") {
		t.Fatal("220-col hub spilled onto a second page")
	}
	links, err := renderDemoFrame("hub-links", "dark", 140, 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"CLUSTER LINKS", "12/14 observed", "HIST ", "gpu-a01 ↔ NAS", "gpu-a01 ↔ login-01", "82M      6M", "11M      2M", "1 unmeasured", "SLURM QUEUE"} {
		if !strings.Contains(ansi.Strip(links), text) {
			t.Fatalf("links frame missing %q", text)
		}
	}

	gpu, err := renderDemoFrame("gpu", "dark", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	gpuPlain := ansi.Strip(gpu)
	for _, text := range []string{"alice", "bob", "train.py", "protein-fold", "L40S"} {
		if !strings.Contains(gpuPlain, text) {
			t.Fatalf("gpu frame missing %q", text)
		}
	}

	nas, err := renderDemoFrame("nas", "dark", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	nasPlain := ansi.Strip(nas)
	for _, text := range []string{"/data/shared", "acme-sync", "Console"} {
		if !strings.Contains(nasPlain, text) {
			t.Fatalf("nas frame missing %q\n%s", text, nasPlain)
		}
	}

	light, err := renderDemoFrame("hub", "light", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if light == hub {
		t.Fatal("light theme rendered the same bytes as dark")
	}
}

func TestDemoProgressAnimatesAcrossProcessTables(t *testing.T) {
	m := demoGPUPreview(colorModeDark, 160, 42, false)
	backend := m.backend.(*demoProgressBackend)
	initial := backend.sample(backend.started)
	later := backend.sample(backend.started.Add(5 * time.Second))
	if initial.Processes[0].Progress.Done != 62 || later.Processes[0].Progress.Done != 67 ||
		initial.Processes[2].Progress != nil || later.Processes[2].Progress != nil {
		t.Fatalf("demo should advance reported processes without mutating the fixture")
	}
	m.snapshot = later
	for _, page := range []monitorPage{monitorPageOverview, monitorPageCompute} {
		m.monitorPage = page
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "train.py · epoch 4/6") || !strings.Contains(view, "67%") ||
			!strings.Contains(view, "━") {
			t.Fatalf("page %v lost the animated meter", page)
		}
	}
}
