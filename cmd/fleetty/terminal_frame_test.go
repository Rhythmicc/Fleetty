package main

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func requirePinnedFooter(t *testing.T, rendered string, height int, marker string) {
	t.Helper()
	if got := lipgloss.Height(rendered); got != height {
		t.Fatalf("frame height = %d, want %d\n%s", got, height, rendered)
	}
	lines := strings.Split(ansi.Strip(rendered), "\n")
	if got := lines[len(lines)-1]; !strings.Contains(got, marker) {
		t.Fatalf("last terminal row %q does not contain %q\n%s", got, marker, rendered)
	}
}

func TestTerminalFramePinsAndPreservesFooter(t *testing.T) {
	rendered := terminalFrame("one\ntwo", "[q] quit", 40, 8)
	requirePinnedFooter(t, rendered, 8, "[q] quit")
	lines := strings.Split(rendered, "\n")
	if lines[1] != "two" || lines[6] != "" {
		t.Fatalf("body was not padded above the footer: %#v", lines)
	}

	clipped := terminalFrame("1\n2\n3\n4", "footer", 20, 3)
	if clipped != "1\n2\nfooter" {
		t.Fatalf("overflow did not preserve the footer: %q", clipped)
	}
}

func TestMonitorScreensPinOperationBarToLastRow(t *testing.T) {
	now := time.Now()
	base := monitorModel{
		width: 100, height: 32, colorMode: colorModeDark,
		snapshot: monitorSnapshot{
			CollectedAt: now, Profile: machineProfileGeneral,
			MemoryTotal: 64 << 30, DiskTotal: 1 << 40,
			Processes: []processInfo{{PID: 42, User: "alice", Command: "worker"}},
		},
		admin:   &adminController{},
		storage: &storageMapState{Root: "/tmp", Path: "/tmp", Scope: "USER", Scanning: true},
	}

	for _, page := range []monitorPage{
		monitorPageOverview, monitorPageCompute, monitorPageNetwork,
		monitorPageStorage, monitorPageCustom,
	} {
		model := base
		model.screen = screenMonitor
		model.monitorPage = page
		marker := "pages"
		if page == monitorPageCustom {
			marker = "management"
		}
		requirePinnedFooter(t, model.View().Content, model.height, marker)
	}

	nas := base
	nas.snapshot.Profile = machineProfileNAS
	nas.profile = machineProfileNAS
	requirePinnedFooter(t, nas.View().Content, nas.height, "management")

	password := base
	password.screen = screenPassword
	requirePinnedFooter(t, password.View().Content, password.height, "continue")

	admin := base
	admin.screen = screenAdmin
	requirePinnedFooter(t, admin.View().Content, admin.height, "sort/reverse")

	confirm := base
	confirm.screen = screenConfirm
	confirm.selectedAction = &adminAction{label: "Restart Fleetty"}
	requirePinnedFooter(t, confirm.View().Content, confirm.height, "confirm")

	detail := base
	detail.screen = screenProcessDetail
	detail.processReadOnly = true
	detail.selectedProcess = &detail.snapshot.Processes[0]
	detail.processDetail = &processDetail{PID: 42, Name: "worker"}
	requirePinnedFooter(t, detail.View().Content, detail.height, "back")

	terminate := base
	terminate.screen = screenProcessTerminateConfirm
	terminate.selectedProcess = &terminate.snapshot.Processes[0]
	requirePinnedFooter(t, terminate.View().Content, terminate.height, "confirm")

	storage := base
	storage.screen = screenStorageConfirm
	storage.status = "Type DELETE to confirm."
	storage.storageAction = &storageActionRequest{
		Kind: storageActionDelete, Path: "/tmp/example", Name: "example",
	}
	requirePinnedFooter(t, storage.View().Content, storage.height, "execute")

	layout := base
	layout.screen = screenLayout
	requirePinnedFooter(t, layout.View().Content, layout.height, "apply")
}

func TestHubAndLauncherPinOperationBarToLastRow(t *testing.T) {
	hub := newTestHubModel(100, 36, 0)
	requirePinnedFooter(t, hub.View().Content, hub.height, "select")

	hub.config.SlurmClusters = []slurmClusterConfig{{Name: "GPU Cluster"}}
	hub.slurmView = true
	hub.slurmFilter = -1
	requirePinnedFooter(t, hub.View().Content, hub.height, "hub")

	launcher := newLauncherModel()
	launcher.width, launcher.height = 72, 30
	for _, page := range []launcherPage{
		launcherPageHome, launcherPageSSH, launcherPageConfig, launcherPageHelp,
	} {
		launcher.page = page
		requirePinnedFooter(t, launcher.View().Content, launcher.height, "quit")
	}
}
