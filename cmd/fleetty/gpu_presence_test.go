package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestGPUHardwarePresenceAndCollectionErrors(t *testing.T) {
	for _, test := range []struct {
		name, class, vendor string
		present, unknown    bool
	}{
		{name: "empty inventory"},
		{name: "server display", class: "0x030000\n", vendor: "0x102b\n"},
		{name: "NVIDIA VGA", class: "0x030000\n", vendor: "0x10de\n", present: true},
		{name: "NVIDIA 3D", class: "0x030200\n", vendor: "0x10de\n", present: true},
		{name: "NVIDIA audio is not a GPU", class: "0x040300\n", vendor: "0x10de\n"},
		{name: "invalid class", class: "invalid", vendor: "0x10de", unknown: true},
		{name: "missing vendor", class: "0x030000", unknown: true},
		{name: "invalid vendor", class: "0x030000", vendor: "invalid", unknown: true},
		{name: "missing inventory", unknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.name == "missing inventory" {
				root = filepath.Join(root, "missing")
			} else if test.class != "" {
				device := t.TempDir()
				if err := os.WriteFile(filepath.Join(device, "class"), []byte(test.class), 0600); err != nil {
					t.Fatal(err)
				}
				if test.vendor != "" {
					if err := os.WriteFile(filepath.Join(device, "vendor"), []byte(test.vendor), 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Real PCI inventory entries are symlinks, not directories.
				if err := os.Symlink(device, filepath.Join(root, "0000:63:00.0")); err != nil {
					t.Fatal(err)
				}
			}
			present, err := hasNVIDIAGPU(root)
			if present != test.present || (err != nil) != test.unknown {
				t.Fatalf("presence = %v, %v", present, err)
			}
			for _, commandErr := range []error{exec.ErrNotFound, errors.New("driver unavailable")} {
				want := ""
				if test.present || test.unknown {
					want = compactCommandError(commandErr)
				}
				if got := gpuMetricsError(commandErr, root); got != want {
					t.Fatalf("GPU error = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestGPUVisibilityUsesMetricsInsteadOfProfile(t *testing.T) {
	for _, profile := range []string{machineProfileGPU, machineProfileGeneral} {
		for _, width := range []int{60, 140} {
			for _, state := range []string{"absent", "healthy", "failed"} {
				t.Run(fmt.Sprintf("%s/%d/%s", profile, width, state), func(t *testing.T) {
					snapshot := monitorSnapshot{
						CollectedAt: time.Now(), Profile: profile,
						MemoryTotal: 8 << 30, DiskTotal: 100 << 30,
					}
					switch state {
					case "healthy":
						snapshot.GPUs = []gpuInfo{{Name: "NVIDIA GPU", MemoryTotal: 8 << 30}}
					case "failed":
						snapshot.GPUError = "driver unavailable"
					}
					local := &monitorModel{profile: profile, width: width, height: 40, snapshot: snapshot}
					remote := newRemoteMonitorModel(hubNodeConfig{Name: "node", Profile: profile}, width, 40, colorModeDark)
					remote.snapshot = snapshot
					for _, m := range []*monitorModel{local, remote} {
						wantGPU := state != "absent"
						if m.capabilities().GPU != wantGPU {
							t.Fatalf("GPU capability = %v, want %v", m.capabilities().GPU, wantGPU)
						}
						overview, _ := m.renderOverviewPage(width)
						compute, _ := m.renderComputePage(width)
						custom, placements := m.renderWidgetGrid(width)
						views := []string{overview, compute, custom}
						for _, size := range []widgetSize{widgetSizeSmall, widgetSizeMedium, widgetSizeLarge} {
							views = append(views, m.renderGPUWidget(width, size))
						}
						for _, view := range views {
							plain := ansi.Strip(view)
							if strings.Contains(plain, "GPU") != wantGPU {
								t.Fatalf("unexpected GPU visibility:\n%s", plain)
							}
							if state == "failed" && !strings.Contains(plain, snapshot.GPUError) {
								t.Fatalf("GPU failure was hidden:\n%s", plain)
							}
						}
						if !wantGPU {
							if strings.Contains(m.renderMonitorPageHeader(width), "Caps:") {
								t.Fatal("GPU-less host still advertises GPU capabilities")
							}
							for _, placement := range placements {
								if placement.ID == dashboardPanelGPU {
									t.Fatal("hidden GPU card still reserves grid space")
								}
							}
						}
					}
				})
			}
		}
	}
}
