//go:build demo

package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDemoFramesUseFictionalData(t *testing.T) {
	forbidden := []string{"SSSLab", "ssslab", "NAS01", "4090", "5090", "A100"}
	for _, screen := range []string{"hub", "gpu", "nas"} {
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
		"i/o timeout", "alice +1", "carol", "idle", "▁",
		"↓86M ↑14M", "↓12M ↑1M", "r40M w8M", "vram 59/96G", "vram 36/48G",
		"78°C", "87°C",
		"HTTP 1/2", "CTR 2/3", "PM2 1/2",
	} {
		if !strings.Contains(hubPlain, text) {
			t.Fatalf("hub frame missing %q", text)
		}
	}
	if strings.Contains(hubPlain, "PAGE ") {
		t.Fatal("140x48 hub spilled onto a second page")
	}
	wide, err := renderDemoFrame("hub", "dark", 220, 48)
	if err != nil {
		t.Fatal(err)
	}
	widePlain := ansi.Strip(wide)
	for _, text := range []string{"storage-01", "cpu-01", "gpu-a01", "vram 59/96G", "r40M w8M"} {
		if !strings.Contains(widePlain, text) {
			t.Fatalf("220-col hub frame missing %q", text)
		}
	}
	if strings.Contains(widePlain, "PAGE ") {
		t.Fatal("220-col hub spilled onto a second page")
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
