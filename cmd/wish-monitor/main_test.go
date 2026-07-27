package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	gossh "golang.org/x/crypto/ssh"
)

type scriptedSlurmRunner struct {
	calls  int
	closed int
}

func (r *scriptedSlurmRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	r.calls++
	arguments := strings.Join(args, " ")
	switch {
	case command == "sinfo" && strings.Contains(arguments, "--version"):
		return []byte("slurm-wlm 25.11.2\n"), nil
	case command == "sinfo" && strings.Contains(arguments, "%N"):
		return []byte("gpu01\tgpu*\n"), nil
	case command == "sinfo":
		return []byte("gpu*\tup\t20:00\t1\tidle\t0/16/0/16\tgpu:test:1\n"), nil
	case command == "squeue":
		return []byte("101\tgpu\ttrain\talice\tPENDING\t0:00\t20:00\t1\t\t(Priority)\n"), nil
	default:
		return nil, fmt.Errorf("unexpected command %s %s", command, arguments)
	}
}

func (r *scriptedSlurmRunner) Close() error {
	r.closed++
	return nil
}

func TestAdminAuthenticationAndSelection(t *testing.T) {
	admin := &adminController{
		password: "correct horse battery staple",
		actions: []adminAction{
			{label: "Restart monitor service", command: "true"},
			{label: "Reboot machine", command: "true"},
		},
	}
	m := &monitorModel{admin: admin, screen: screenMonitor}
	m.handleKey(testKey("m"))
	if m.screen != screenPassword {
		t.Fatalf("screen after m = %v, want password", m.screen)
	}
	for _, r := range "correct horse battery staple" {
		m.handleKey(testKey(string(r)))
	}
	command := m.handleKey(testKey("enter"))
	if command == nil {
		t.Fatal("password submission did not start authentication")
	}
	_, _ = m.Update(command())
	if m.screen != screenAdmin {
		t.Fatalf("screen after password = %v, want admin", m.screen)
	}
	m.handleKey(testKey("2"))
	if m.screen != screenConfirm || m.selectedAction == nil || m.selectedAction.label != "Reboot machine" {
		t.Fatalf("selection = screen %v action %#v", m.screen, m.selectedAction)
	}
}

func TestPasswordPasteAndLegacyKeyInput(t *testing.T) {
	m := &monitorModel{admin: &adminController{password: "p@ss word"}, screen: screenPassword}
	_, _ = m.Update(tea.PasteMsg{Content: "p@ss word\n"})
	if m.password != "p@ss word" {
		t.Fatalf("pasted password = %q", m.password)
	}
	m.password = ""
	m.handleKey(tea.KeyPressMsg{Code: 'x'}) // Legacy terminals can omit Key.Text.
	if m.password != "x" {
		t.Fatalf("legacy key input = %q", m.password)
	}
}

func TestThemeSelectionIsSessionLocal(t *testing.T) {
	first := &monitorModel{screen: screenMonitor, colorMode: colorModeDark}
	second := &monitorModel{screen: screenMonitor, colorMode: colorModeDark}

	first.handleKey(testKey("t"))
	if first.colorMode != colorModeLight {
		t.Fatalf("first session theme = %s, want light", first.colorMode)
	}
	if second.colorMode != colorModeDark {
		t.Fatalf("second session theme changed to %s", second.colorMode)
	}

	lightView := first.View()
	darkView := second.View()
	lightR, lightG, lightB, _ := lightView.BackgroundColor.RGBA()
	darkR, darkG, darkB, _ := darkView.BackgroundColor.RGBA()
	if lightR == darkR && lightG == darkG && lightB == darkB {
		t.Fatal("light and dark sessions should use different terminal backgrounds")
	}
	if lightView.Content == darkView.Content {
		t.Fatal("light theme should remap the session's rendered palette")
	}

	filtering := &monitorModel{screen: screenAdmin, filtering: true, colorMode: colorModeDark}
	filtering.handleKey(testKey("T"))
	if filtering.colorMode != colorModeDark || filtering.filter != "T" {
		t.Fatalf("filter input should receive T without changing theme: mode=%s filter=%q", filtering.colorMode, filtering.filter)
	}
}

func TestProcessHeaderHasHighContrastInBothThemes(t *testing.T) {
	dark := processTableHeader("PID  USER  CPU  COMMAND", 50)
	if !strings.Contains(dark, "38;2;16;19;26") || !strings.Contains(dark, "48;2;159;195;255") {
		t.Fatalf("dark process header should use dark text on a bright background: %q", dark)
	}
	light := applyLightTheme(dark)
	if !strings.Contains(light, "38;2;248;250;252") || !strings.Contains(light, "48;2;49;93;145") {
		t.Fatalf("light process header should use light text on a dark background: %q", light)
	}
}

func TestDisabledManagementMode(t *testing.T) {
	m := &monitorModel{admin: &adminController{}, screen: screenMonitor}
	m.handleKey(testKey("m"))
	if m.screen != screenMonitor || m.status == "" {
		t.Fatalf("disabled management should remain on monitor, got screen=%v status=%q", m.screen, m.status)
	}
}

func TestNodeRPCAuthenticatesEveryManagementRequest(t *testing.T) {
	admin := &adminController{
		password: "node-secret",
		actions: []adminAction{
			{ID: 0, label: "Safe test action", command: "true"},
		},
	}
	service := newNodeRPCService(admin, machineConfig{Profile: machineProfileGPU})

	denied := service.Handle(nodeRPCRequest{
		Version:   nodeRPCVersion,
		Operation: rpcRunAction,
		ActionID:  0,
		Password:  "wrong",
	})
	if denied.Error == "" {
		t.Fatal("management RPC should reject an incorrect password")
	}
	allowed := service.Handle(nodeRPCRequest{
		Version:   nodeRPCVersion,
		Operation: rpcRunAction,
		ActionID:  0,
		Password:  "node-secret",
	})
	if allowed.Error != "" {
		t.Fatalf("management RPC rejected the correct password: %s", allowed.Error)
	}
	incompatible := service.Handle(nodeRPCRequest{Version: nodeRPCVersion + 1, Operation: rpcAuthenticate})
	if incompatible.Error == "" {
		t.Fatal("RPC should reject incompatible protocol versions")
	}
}

func TestHubConfigAndResponsiveOverview(t *testing.T) {
	if got := (hubConfig{}).refreshInterval(); got != time.Second {
		t.Fatalf("default hub refresh interval = %s, want 1s", got)
	}
	for failures, want := range map[int]time.Duration{
		1: 5 * time.Second,
		2: 10 * time.Second,
		3: 20 * time.Second,
		4: 30 * time.Second,
		8: 30 * time.Second,
	} {
		if got := hubOfflineRetryDelay(failures); got != want {
			t.Fatalf("offline retry delay after %d failures = %s, want %s", failures, got, want)
		}
	}

	configPath := t.TempDir() + "/nodes.json"
	configJSON := `{
		"name": "Test Machine Hub",
		"refresh_seconds": 3,
		"nodes": [
			{"name":"node-1","address":"192.0.2.1:23234","host_key":"SHA256:first","identity_file":"/etc/hub_key"},
			{"name":"node-2","address":"192.0.2.2:23234","profile":"nas","host_key":"SHA256:second","identity_file":"/etc/hub_key"},
			{"name":"node-3","address":"192.0.2.3:23234","host_key":"SHA256:third","identity_file":"/etc/hub_key"},
			{"name":"node-4","address":"192.0.2.4:23234","profile":"nas","host_key":"SHA256:fourth","identity_file":"/etc/hub_key"}
		]
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadHubConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.refreshInterval() != 3*time.Second || len(config.Nodes) != 4 {
		t.Fatalf("unexpected hub config: %#v", config)
	}
	if config.displayName() != "Test Machine Hub" {
		t.Fatalf("hub display name = %q", config.displayName())
	}

	model := &hubModel{
		config: *config,
		states: []hubNodeState{
			{
				Snapshot: monitorSnapshot{
					CollectedAt: time.Now(),
					CPUPercent:  25,
					MemoryUsed:  16 * 1024 * 1024 * 1024,
					MemoryTotal: 64 * 1024 * 1024 * 1024,
					DiskUsed:    100 * 1024 * 1024 * 1024,
					DiskTotal:   500 * 1024 * 1024 * 1024,
					GPUs: []gpuInfo{{
						Name: "GPU", Utilization: 75,
						MemoryUsed: 8 * 1024 * 1024 * 1024, MemoryTotal: 16 * 1024 * 1024 * 1024,
						Temperature: 65,
					}},
				},
				Latency: 20 * time.Millisecond,
			},
			{Error: "connection refused"},
			{},
			{},
		},
		status: "test",
	}
	for _, size := range []struct{ width, height int }{{140, 30}, {80, 24}, {60, 24}} {
		model.width, model.height = size.width, size.height
		model.clampCursor()
		rendered := model.hubView()
		if size.width == 140 {
			for _, section := range []string{"GPU COMPUTE", "NAS & STORAGE"} {
				if !strings.Contains(rendered, section) {
					t.Fatalf("%dx%d Hub missing section %q\n%s", size.width, size.height, section, rendered)
				}
			}
			if index, ok := model.nodeAt(0, 2); !ok || index != 0 {
				t.Fatalf("GPU card click resolved to index=%d ok=%t", index, ok)
			}
			if index, ok := model.nodeAt(0, 10); !ok || index != 1 {
				t.Fatalf("NAS card click resolved to index=%d ok=%t", index, ok)
			}
			if _, ok := model.nodeAt(0, 9); ok {
				t.Fatal("NAS section heading should not be clickable")
			}
		}
		if got := lipgloss.Height(rendered); got > size.height {
			t.Fatalf("%dx%d Hub height = %d\n%s", size.width, size.height, got, rendered)
		}
		for index, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d Hub line %d width = %d\n%q", size.width, size.height, index, got, line)
			}
		}
	}

	model.width, model.height, model.cursor = 140, 30, 0
	model.moveCursor(1)
	if model.cursor != 2 {
		t.Fatalf("grouped right navigation selected config index %d, want 2", model.cursor)
	}
	model.moveCursor(1)
	if model.cursor != 1 {
		t.Fatalf("grouped navigation should enter NAS section at config index 1, got %d", model.cursor)
	}

	model.cursor = 1
	if command := model.openSelected(); command != nil || model.detail != nil {
		t.Fatal("offline node should not open a detail dashboard")
	}
	if !strings.Contains(model.status, "offline") {
		t.Fatalf("offline selection status = %q", model.status)
	}
}

func TestHubConfigRequiresNodeRPCIdentity(t *testing.T) {
	path := t.TempDir() + "/nodes.json"
	withoutIdentity := `{
		"name":"Secure Hub",
		"nodes":[{"name":"node-1","address":"192.0.2.1:23234","host_key":"SHA256:test"}]
	}`
	if err := os.WriteFile(path, []byte(withoutIdentity), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHubConfig(path); err == nil || !strings.Contains(err.Error(), "identity_file") {
		t.Fatalf("missing node RPC identity should be rejected, got %v", err)
	}
	migration := `{
		"name":"Migration Hub",
		"insecure_allow_unauthenticated_nodes":true,
		"nodes":[{"name":"node-1","address":"192.0.2.1:23234","host_key":"SHA256:test"}]
	}`
	if err := os.WriteFile(path, []byte(migration), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadHubConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Nodes[0].AllowUnauthenticated {
		t.Fatal("migration flag was not propagated to the node")
	}
}

func TestSlurmConfigParsersAndResponsiveQueueView(t *testing.T) {
	configPath := t.TempDir() + "/nodes.json"
	configJSON := `{
		"name": "Test Lab Hub",
		"nodes": [],
		"slurm_clusters": [
			{"name":"Local GPU","transport":"local","partitions":["gpu","gpu"]},
			{
				"name":"Remote A100",
				"transport":"ssh",
				"address":"login.example.test:2222",
				"user":"slurm-monitor",
				"identity_file":"/etc/gpu-ssh-monitor/slurm_ed25519",
				"host_key":"SHA256:test"
			}
		]
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadHubConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Nodes) != 0 || len(config.SlurmClusters) != 2 {
		t.Fatalf("unexpected Slurm-only Hub config: %#v", config)
	}
	if config.SlurmClusters[0].Transport != "local" || len(config.SlurmClusters[0].Partitions) != 1 {
		t.Fatalf("local Slurm config was not normalized: %#v", config.SlurmClusters[0])
	}
	if config.SlurmClusters[1].refreshInterval() != 2*time.Second ||
		config.SlurmClusters[1].timeout() != 4*time.Second {
		t.Fatalf("unexpected Slurm defaults: %#v", config.SlurmClusters[1])
	}

	partitions, err := parseSlurmPartitions(strings.Join([]string{
		"gpu*\tup\t20:00\t1\tmix\t2/14/0/16\tgpu:rtx_4090:1",
		"gpu*\tup\t20:00\t1\talloc\t8/8/0/16\tgpu:rtx_5090:1",
		"cpu\tup\tinfinite\t2\tidle\t0/64/0/64\t(null)",
	}, "\n"), []string{"gpu"})
	if err != nil {
		t.Fatal(err)
	}
	if len(partitions) != 1 || partitions[0].Nodes != 2 ||
		partitions[0].CPUsAlloc != 10 || partitions[0].CPUsTotal != 32 ||
		len(partitions[0].States) != 2 || len(partitions[0].GRES) != 2 {
		t.Fatalf("unexpected aggregated Slurm partition: %#v", partitions)
	}
	jobs, err := parseSlurmJobs(strings.Join([]string{
		"101\tgpu\ttrain\talice\tRUNNING\t1:20\t20:00\t1\tgpu01\tgpu01",
		"102_[1-4]\tgpu\tsweep\tbob\tPENDING\t0:00\t20:00\t1\t\t(Priority)",
		"103\tcpu\tcompile\tcarol\tRUNNING\t0:10\t10:00\t1\tcpu01\tcpu01",
	}, "\n"), []string{"gpu"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].NodeList != "gpu01" ||
		jobs[1].Reason != "(Priority)" || jobs[1].Nodes != 1 {
		t.Fatalf("unexpected parsed Slurm jobs: %#v", jobs)
	}
	nodes, err := parseSlurmNodes("gpu01\tgpu*\ngpu01\tmixed\ngpu02\tcpu\n", []string{"gpu", "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].Partitions) != 2 || nodes[0].Partitions[1] != "mixed" {
		t.Fatalf("unexpected parsed Slurm nodes: %#v", nodes)
	}
	if got := shellJoin([]string{"squeue", "-o", "a'b"}); got != "'squeue' '-o' 'a'\"'\"'b'" {
		t.Fatalf("shell quoting = %q", got)
	}

	now := time.Now()
	model := &hubModel{
		config: hubConfig{
			Name: "Test Lab Hub",
			SlurmClusters: []slurmClusterConfig{
				{Name: "A100 Cluster", Transport: "ssh"},
				{Name: "General GPU", Transport: "local"},
			},
		},
		slurmView:   true,
		slurmFilter: -1,
		slurmStates: []slurmClusterState{
			{Snapshot: slurmSnapshot{
				Name: "A100 Cluster", CollectedAt: now, Version: "slurm-wlm 23.11.4",
				Partitions: []slurmPartition{{
					Name: "gpu-part", Default: true, Available: "up", Nodes: 1,
					States: []string{"alloc"}, CPUsAlloc: 20, CPUsTotal: 20,
				}},
				Jobs: []slurmJob{
					{ID: "57612", Partition: "gpu-part", Name: "bash", User: "yida", State: "RUNNING", Elapsed: "4:00", TimeLimit: "20:00", Nodes: 1, Reason: "a100"},
					{ID: "57615", Partition: "gpu-part", Name: "laplace", User: "lwx", State: "PENDING", Elapsed: "0:00", TimeLimit: "20:00", Nodes: 1, Reason: "(Resources)"},
				},
			}, Latency: 20 * time.Millisecond},
			{Snapshot: slurmSnapshot{
				Name: "General GPU", CollectedAt: now, Version: "slurm-wlm 25.11.2",
				Partitions: []slurmPartition{{
					Name: "mixed-gpu", Default: true, Available: "up", Nodes: 2,
					States: []string{"mix"}, CPUsAlloc: 6, CPUsTotal: 24,
				}},
				Jobs: []slurmJob{
					{ID: "600_15", Partition: "mixed-gpu", Name: "ctx5090", User: "chl", State: "RUNNING", Elapsed: "1:41", TimeLimit: "20:00", Nodes: 1, Reason: "gpu5090"},
					{ID: "613_[0-7%1]", Partition: "mixed-gpu", Name: "sc-spmv-full", User: "lhc", State: "PENDING", Elapsed: "0:00", TimeLimit: "20:00", Nodes: 1, Reason: "(Priority)"},
				},
			}, Latency: 12 * time.Millisecond},
		},
		colorMode: colorModeDark,
		status:    "test",
	}
	for _, size := range []struct{ width, height int }{{160, 40}, {100, 30}, {60, 24}} {
		model.width, model.height, model.slurmOffset = size.width, size.height, 0
		rendered := model.slurmQueueView()
		if size.width == 160 {
			for _, expected := range []string{"SLURM QUEUES", "A100 Cluster", "General GPU", "57612", "NEXT", "613_[0-7%1]", "(Priority)"} {
				if !strings.Contains(rendered, expected) {
					t.Fatalf("%dx%d Slurm view missing %q\n%s", size.width, size.height, expected, rendered)
				}
			}
		}
		if got := lipgloss.Height(rendered); got > size.height {
			t.Fatalf("%dx%d Slurm height = %d\n%s", size.width, size.height, got, rendered)
		}
		for index, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d Slurm line %d width = %d\n%q", size.width, size.height, index, got, line)
			}
		}
	}
	if index, ok := model.slurmClusterAt(0, 1); !ok || index != 0 {
		t.Fatalf("Slurm cluster click resolved to index=%d ok=%t", index, ok)
	}
	model.moveSlurmFilter(1)
	if model.slurmFilter != 0 {
		t.Fatalf("Slurm filter = %d, want first cluster", model.slurmFilter)
	}
	updated, command := model.Update(testKey("q"))
	if updated != model || command != nil || model.slurmView {
		t.Fatalf("q from Slurm queue should return to Hub: view=%t command=%v", model.slurmView, command)
	}
	_, command = model.Update(testKey("q"))
	if command == nil {
		t.Fatal("q from the Hub overview should quit")
	}
}

func TestNodeSlurmQueueSelectsRunningNextAndEligibleJobs(t *testing.T) {
	now := time.Now()
	model := &hubModel{
		config: hubConfig{
			Nodes: []hubNodeConfig{{
				Name: "n2", SlurmCluster: "General GPU", SlurmNode: "node2",
			}},
			SlurmClusters: []slurmClusterConfig{{Name: "General GPU"}},
		},
		slurmStates: []slurmClusterState{{Snapshot: slurmSnapshot{
			Name: "General GPU", CollectedAt: now,
			Nodes: []slurmNode{{Name: "node2", Partitions: []string{"rtx5060"}}},
			Jobs: []slurmJob{
				{ID: "90", Partition: "mixed-gpu", State: "PENDING", Reason: "(Priority)"},
				{ID: "12", Partition: "rtx5060", State: "PENDING", Name: "first", Reason: "(Priority)"},
				{ID: "101", Partition: "rtx5060", State: "PENDING", Name: "later", Reason: "(Resources)"},
				{ID: "7", Partition: "rtx5060", State: "RUNNING", Name: "active", NodeList: "node[1-3]"},
			},
		}}},
	}
	queue := model.nodeSlurmQueue(0)
	if queue == nil || len(queue.Jobs) != 3 {
		t.Fatalf("unexpected node queue: %#v", queue)
	}
	if queue.Jobs[0].Job.ID != "7" || !queue.Jobs[1].Next ||
		queue.Jobs[1].Job.ID != "12" || queue.Jobs[2].Job.ID != "101" {
		t.Fatalf("node queue order/classification = %#v", queue.Jobs)
	}
	detail := &monitorModel{width: 120, height: 42, slurmQueue: queue}
	panel := detail.slurmNodePanel(120, detail.slurmQueueRows())
	for _, expected := range []string{"NODE QUEUE", "RUNNING", "NEXT", "QUEUED", "active", "first", "later"} {
		if !strings.Contains(panel, expected) {
			t.Fatalf("node queue panel missing %q\n%s", expected, panel)
		}
	}
	if !slurmNodeListContains("gpu[01-04],node7", "gpu03") ||
		slurmNodeListContains("gpu[01-04]", "gpu05") {
		t.Fatal("Slurm node-list range matching failed")
	}
}

func TestSlurmCollectorReusesPersistentRunner(t *testing.T) {
	runner := &scriptedSlurmRunner{}
	runners := []slurmCommandRunner{runner}
	configs := []slurmClusterConfig{{
		Name: "remote", Transport: "ssh", RefreshSeconds: 2, TimeoutSeconds: 4,
	}}
	now := time.Now()
	first := collectSlurmStates(configs, nil, now, runners)
	second := collectSlurmStates(configs, first, now.Add(3*time.Second), runners)
	if first[0].Error != "" || second[0].Error != "" {
		t.Fatalf("persistent collection failed: first=%#v second=%#v", first[0], second[0])
	}
	if runner.calls != 8 {
		t.Fatalf("runner calls = %d, want two four-command snapshots", runner.calls)
	}
	if runner.closed != 0 || runners[0] != runner {
		t.Fatalf("healthy runner was not retained: closed=%d runner=%#v", runner.closed, runners[0])
	}
}

func TestHubRefreshLifecycleSurvivesDetailDashboard(t *testing.T) {
	model := &hubModel{
		config: hubConfig{
			RefreshSeconds: 1,
			Nodes: []hubNodeConfig{{
				Name:    "nas",
				Address: "192.0.2.1:23234",
			}},
		},
		states:     make([]hubNodeState, 1),
		collecting: true,
		detail: &monitorModel{
			screen:    screenMonitor,
			colorMode: colorModeDark,
		},
	}

	snapshot := monitorSnapshot{Profile: machineProfileNAS, CPUPercent: 12.5}
	updated, command := model.Update(hubSnapshotsMsg{
		States: []hubNodeState{{Snapshot: snapshot}},
	})
	if updated != model || command != nil {
		t.Fatal("Hub snapshot should be consumed by the parent model")
	}
	if model.collecting {
		t.Fatal("Hub snapshot received in a detail dashboard left collecting stuck")
	}
	if got := model.states[0].Snapshot.CPUPercent; got != snapshot.CPUPercent {
		t.Fatalf("Hub snapshot CPU = %.1f, want %.1f", got, snapshot.CPUPercent)
	}

	updated, command = model.Update(hubTickMsg{})
	if updated != model || command == nil {
		t.Fatal("Hub tick received in a detail dashboard should continue the refresh chain")
	}
	if !model.collecting {
		t.Fatal("Hub tick received in a detail dashboard did not start collection")
	}

	_, _ = model.Update(hubSnapshotsMsg{States: []hubNodeState{{Snapshot: snapshot}}})
	updated, command = model.Update(testKey("esc"))
	if updated != model || model.detail != nil {
		t.Fatal("Esc should return from the node dashboard to the Hub")
	}
	if command == nil || !model.collecting {
		t.Fatal("returning to the Hub should request an immediate overview refresh")
	}
}

func TestNASConfigCollectionAndResponsiveView(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	configPath := t.TempDir() + "/machine.json"
	configJSON := fmt.Sprintf(`{
		"name": "Test NAS",
		"profile": "nas",
		"network_interfaces": ["eth0", "eth0"],
		"mounts": ["/", "/"],
		"docker": true,
		"pm2_user": "ssslab",
		"http_checks": [{"name": "API", "url": %q}]
	}`, server.URL)
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadMachineConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Profile != machineProfileNAS || config.Name != "Test NAS" {
		t.Fatalf("unexpected machine config: %#v", config)
	}
	if len(config.NetworkInterfaces) != 1 || len(config.Mounts) != 1 {
		t.Fatalf("machine config should remove duplicates: %#v", config)
	}
	if config.PM2User != "ssslab" {
		t.Fatalf("PM2 user = %q, want ssslab", config.PM2User)
	}
	services := checkHTTPServices(config.HTTPChecks)
	if len(services) != 1 || !services[0].Healthy || services[0].Detail != "204" {
		t.Fatalf("unexpected HTTP health result: %#v", services)
	}

	now := time.Now()
	model := &monitorModel{
		profile:  machineProfileNAS,
		nodeName: "Test NAS",
		snapshot: monitorSnapshot{
			CollectedAt: now, Profile: machineProfileNAS, NodeName: "Test NAS",
			CPUPercent: 12, LoadAverage: "load 0.1 · 0.2 · 0.3",
			MemoryUsed: 4 << 30, MemoryTotal: 8 << 30,
			NetworkRX: 10 << 20, NetworkTX: 20 << 20,
			NetworkRXTotal: 20 << 40, NetworkTXTotal: 80 << 40,
			NetworkInterfaces: []networkInterfaceInfo{{
				Name: "eth0", RX: 10 << 20, TX: 20 << 20,
				RXTotal: 20 << 40, TXTotal: 80 << 40, RXDrops: 2,
			}},
			Filesystems: []filesystemInfo{
				{Mount: "/", Used: 100 << 30, Total: 1 << 40},
				{Mount: "/mnt/NAS01", Used: 99 << 30, Total: 100 << 30},
				{Mount: "/mnt/NAS02", Used: 90 << 30, Total: 100 << 30},
				{Mount: "/mnt/NAS03", Used: 50 << 30, Total: 100 << 30},
				{Mount: "/mnt/NAS04", Used: 25 << 30, Total: 100 << 30},
			},
			Services: []serviceHealth{{Name: "API", Healthy: true, Detail: "200", Latency: 12 * time.Millisecond}},
			Containers: []containerInfo{{
				Name: "web", Image: "nginx:latest", Running: true, State: "running", Health: "healthy",
				CPU: 1.2, MemoryUsed: 64 << 20, NetworkRX: 2 << 20, NetworkTX: 1 << 20,
				PIDs: 8, Restarts: 1, Uptime: 3600, Ports: "80→80/tcp",
			}},
			PM2Processes: []pm2ProcessInfo{{
				ID: 0, PID: 1234, Name: "web-console", Status: "online", Mode: "fork",
				CPU: 0.3, Memory: 48 << 20, Uptime: 7200, Restarts: 2,
			}},
		},
		cpuHistory:       []float64{10, 12},
		networkRXHistory: []float64{1 << 20, 10 << 20},
		networkTXHistory: []float64{2 << 20, 20 << 20},
		colorMode:        colorModeDark,
	}
	for _, size := range []struct{ width, height int }{{140, 30}, {80, 24}, {50, 24}} {
		model.width, model.height = size.width, size.height
		rendered := model.monitorView()
		if size.width == 140 {
			for _, expected := range []string{"DOCKER", "nginx:latest", "PM2", "web-console"} {
				if !strings.Contains(rendered, expected) {
					t.Fatalf("%dx%d NAS view missing %q\n%s", size.width, size.height, expected, rendered)
				}
			}
		}
		if got := lipgloss.Height(rendered); got > size.height {
			t.Fatalf("%dx%d NAS height = %d\n%s", size.width, size.height, got, rendered)
		}
		for index, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d NAS line %d width = %d\n%q", size.width, size.height, index, got, line)
			}
		}
	}

	hubCard := strings.Join(renderNASHubCard(model.snapshot), "\n")
	for _, expected := range []string{"TOTAL", "DISK", "HTTP", "CTR", "PM2"} {
		if !strings.Contains(hubCard, expected) {
			t.Fatalf("NAS Hub card missing %q: %s", expected, hubCard)
		}
	}
}

func TestDockerAPICollectionIncludesRuntimeDetails(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/containers/json":
			_, _ = response.Write([]byte(`[
				{"Id":"abc","Names":["/web"],"Image":"nginx:latest","State":"running","Status":"Up 2 hours (healthy)","Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]},
				{"Id":"def","Names":["/old"],"Image":"busybox","State":"exited","Status":"Exited (0) 1 day ago","Ports":[]}
			]`))
		case "/containers/abc/json":
			_, _ = fmt.Fprintf(response, `{"Name":"/web","RestartCount":3,"State":{"Status":"running","Running":true,"StartedAt":%q,"Health":{"Status":"healthy"}}}`, started)
		case "/containers/abc/stats":
			_, _ = response.Write([]byte(`{
				"cpu_stats":{"cpu_usage":{"total_usage":300,"percpu_usage":[1,1]},"system_cpu_usage":1000,"online_cpus":2},
				"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":500},
				"memory_stats":{"usage":104857600,"limit":1073741824,"stats":{"inactive_file":10485760}},
				"networks":{"eth0":{"rx_bytes":2048,"tx_bytes":4096}},
				"blkio_stats":{"io_service_bytes_recursive":[{"op":"read","value":8192},{"op":"write","value":16384}]},
				"pids_stats":{"current":7}
			}`))
		case "/containers/def/json":
			_, _ = response.Write([]byte(`{"Name":"/old","RestartCount":1,"State":{"Status":"exited","Running":false,"StartedAt":"0001-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	containers, err := readDockerContainersFrom(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 || containers[0].Name != "web" || !containers[0].Running {
		t.Fatalf("unexpected Docker containers: %#v", containers)
	}
	web := containers[0]
	if web.Health != "healthy" || web.Restarts != 3 || web.PIDs != 7 || web.Ports != "8080→80/tcp" {
		t.Fatalf("missing Docker metadata: %#v", web)
	}
	if web.CPU != 80 || web.MemoryUsed != 90<<20 || web.NetworkRX != 2048 || web.BlockWrite != 16384 {
		t.Fatalf("missing Docker runtime stats: %#v", web)
	}
	if containers[1].Name != "old" || containers[1].Running {
		t.Fatalf("stopped container ordering/state = %#v", containers[1])
	}
}

func TestPM2ProcessParser(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	data := []byte(`[
		{"pid":1234,"name":"web","pm_id":2,"monit":{"cpu":1.5,"memory":50331648},"pm2_env":{"status":"online","namespace":"default","exec_mode":"fork_mode","pm_uptime":1785060000000,"restart_time":4,"unstable_restarts":1}},
		{"pid":0,"name":"worker","pm_id":1,"monit":{"cpu":0,"memory":0},"pm2_env":{"status":"stopped","namespace":"jobs","exec_mode":"cluster_mode","pm_uptime":0,"restart_time":2,"unstable_restarts":0}}
	]`)
	processes, err := parsePM2Processes(data, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 2 || processes[0].ID != 1 || processes[1].ID != 2 {
		t.Fatalf("PM2 processes should be sorted by id: %#v", processes)
	}
	web := processes[1]
	if web.Status != "online" || web.Mode != "fork" || web.CPU != 1.5 || web.Memory != 48<<20 || web.Restarts != 4 {
		t.Fatalf("missing PM2 fields: %#v", web)
	}
	if web.Uptime != 7200 {
		t.Fatalf("PM2 uptime = %d, want 7200", web.Uptime)
	}
}

func TestNASRichServiceTablesFitLargeTerminal(t *testing.T) {
	model := &monitorModel{
		profile:  machineProfileNAS,
		nodeName: "Service NAS",
		width:    160,
		height:   50,
		snapshot: monitorSnapshot{
			CollectedAt: time.Now(), Profile: machineProfileNAS,
			MemoryTotal: 8 << 30, DiskTotal: 1 << 40,
			Filesystems: []filesystemInfo{
				{Mount: "/", Total: 1 << 40},
				{Mount: "/mnt/one", Total: 1 << 40},
				{Mount: "/mnt/two", Total: 1 << 40},
			},
		},
	}
	for index := 1; index <= 7; index++ {
		model.snapshot.Containers = append(model.snapshot.Containers, containerInfo{
			Name: fmt.Sprintf("container-%d", index), Image: "example/image:latest",
			State: "running", Running: true, CPU: float64(index), MemoryUsed: uint64(index) << 20,
		})
	}
	for index := 1; index <= 4; index++ {
		model.snapshot.PM2Processes = append(model.snapshot.PM2Processes, pm2ProcessInfo{
			ID: index, PID: 1000 + index, Name: fmt.Sprintf("pm2-%d", index),
			Status: "online", CPU: float64(index), Memory: uint64(index) << 20,
		})
	}
	for index := 1; index <= 8; index++ {
		model.snapshot.Services = append(model.snapshot.Services, serviceHealth{
			Name: fmt.Sprintf("http-%d", index), Healthy: true, Detail: "200",
		})
	}

	rendered := model.monitorView()
	for _, expected := range []string{"DOCKER", "container-7", "PM2", "pm2-4", "HTTP CHECKS", "http-8", "ATTENTION"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("large NAS view missing final service row %q\n%s", expected, rendered)
		}
	}
	if got := lipgloss.Height(rendered); got > model.height {
		t.Fatalf("large NAS view height = %d, terminal height = %d", got, model.height)
	}
	for index, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("large NAS line %d width = %d\n%q", index, got, line)
		}
	}
}

func TestNodeRPCTimeoutCoversSSHHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		<-stop
	}()

	client := newNodeRPCClient(hubNodeConfig{
		Name:                 "sleeping-node",
		Address:              listener.Addr().String(),
		HostKey:              "SHA256:not-reached",
		AllowUnauthenticated: true,
	})
	started := time.Now()
	_, err = client.CallWithTimeout(nodeRPCRequest{Operation: rpcSnapshot}, 50*time.Millisecond)
	if err == nil {
		t.Fatal("RPC call should time out while waiting for the SSH handshake")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("RPC handshake timeout took %s", elapsed)
	}
}

func TestDefaultRestartCommandMatchesPackagedService(t *testing.T) {
	t.Setenv("ADMIN_RESTART_MONITOR_CMD", "")
	admin := newAdminController()
	if got := admin.actions[0].command; got != "systemctl restart gpu-ssh-monitor.service" {
		t.Fatalf("default restart command = %q", got)
	}
}

func TestSSHAccessConfigRequiresAndLoadsAuthorizedKeys(t *testing.T) {
	t.Setenv("SSH_ALLOW_ANONYMOUS", "")
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", "")
	t.Setenv("NODE_RPC_AUTHORIZED_KEYS_FILE", "")
	if _, err := loadSSHAccessConfig(); err == nil {
		t.Fatal("missing SSH client authentication should be rejected")
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	authorizedKeys := directory + "/authorized_keys"
	if err := os.WriteFile(authorizedKeys, gossh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", authorizedKeys)
	config, err := loadSSHAccessConfig()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := gossh.FingerprintSHA256(signer.PublicKey())
	if _, ok := config.interactiveKeys[fingerprint]; !ok || config.allowAnonymous {
		t.Fatalf("authorized key was not loaded: %#v", config)
	}
}

func TestPrivateKeyPermissionsAndProcessRedaction(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	path := t.TempDir() + "/identity"
	if err := os.WriteFile(path, privatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrivateKeySigner(path); err != nil {
		t.Fatalf("secure private key was rejected: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrivateKeySigner(path); err == nil {
		t.Fatal("group/world-readable private key should be rejected")
	}

	command := "python train.py --password hunter2 API_TOKEN=abc --api-key=xyz " +
		"DATABASE_URL=postgres://alice:dbpass@db.example/app?token=urltoken --epochs 10"
	redacted := redactProcessCommandLine(command)
	for _, secret := range []string{"hunter2", "abc", "xyz", "dbpass", "urltoken"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted command still contains %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "--epochs 10") {
		t.Fatalf("non-sensitive arguments were lost: %s", redacted)
	}
	detail := processDetail{CommandLine: command, CWD: "/srv/private-project"}
	applyProcessDetailPolicy(&detail, false)
	if !detail.Redacted || detail.CWD != redactedProcessDetailMessage ||
		strings.Contains(detail.CommandLine, "hunter2") {
		t.Fatalf("read-only process detail policy did not redact sensitive fields: %#v", detail)
	}
	full := processDetail{CommandLine: command, CWD: "/srv/private-project"}
	applyProcessDetailPolicy(&full, true)
	if full.Redacted || full.CWD != "/srv/private-project" || full.CommandLine != command {
		t.Fatalf("authenticated process detail policy changed full details: %#v", full)
	}
}

func TestSSHConnectionLimiterReleasesClosedConnections(t *testing.T) {
	limiter := newConnectionLimiter(1)
	clientOne, serverOne := net.Pipe()
	defer clientOne.Close()
	limited := limiter.wrap(nil, serverOne)
	if limited == nil {
		t.Fatal("first connection should be accepted")
	}
	clientTwo, serverTwo := net.Pipe()
	defer clientTwo.Close()
	defer serverTwo.Close()
	if got := limiter.wrap(nil, serverTwo); got != nil {
		t.Fatal("connection above the limit should be rejected")
	}
	if err := limited.Close(); err != nil {
		t.Fatal(err)
	}
	clientThree, serverThree := net.Pipe()
	defer clientThree.Close()
	accepted := limiter.wrap(nil, serverThree)
	if accepted == nil {
		t.Fatal("closing a connection should release its slot")
	}
	_ = accepted.Close()
}

func TestManagementCardsCanBeClicked(t *testing.T) {
	m := &monitorModel{
		admin: &adminController{actions: []adminAction{
			{label: "Restart monitor service", command: "true"},
			{label: "Reboot machine", command: "true"},
		}},
		screen: screenAdmin,
	}
	firstWidth := compactButtonWidth("1", "Restart monitor service")
	m.handleClick(firstWidth+2, 2)
	if m.screen != screenConfirm || m.selectedAction == nil || m.selectedAction.label != "Reboot machine" {
		t.Fatalf("click should select reboot, got screen=%v action=%#v", m.screen, m.selectedAction)
	}
}

func TestProcessFilterAndMouseSelection(t *testing.T) {
	m := &monitorModel{
		admin:  &adminController{},
		screen: screenAdmin,
		snapshot: monitorSnapshot{Processes: []processInfo{
			{PID: 100, User: "root", Command: "python trainer.py"},
			{PID: 200, User: "alice", Command: "sshd"},
		}},
	}
	m.handleKey(testKey("/"))
	_, _ = m.Update(tea.PasteMsg{Content: "train"})
	m.handleKey(testKey("enter"))
	filtered := m.filteredProcesses()
	if len(filtered) != 1 || filtered[0].PID != 100 {
		t.Fatalf("filtered processes = %#v", filtered)
	}
	m.handleClick(4, adminProcessRowStart)
	if m.screen != screenProcessDetail || m.selectedProcess == nil || m.selectedProcess.PID != 100 {
		t.Fatalf("process selection = screen %v process %#v", m.screen, m.selectedProcess)
	}
}

func TestReadOnlyDashboardResizesPanelsAndOpensProcessDetails(t *testing.T) {
	processes := make([]processInfo, 16)
	for index := range processes {
		processes[index] = processInfo{
			PID: 200 + index, User: "worker", Command: fmt.Sprintf("trainer-%d", index),
			State: "R", CPU: float64(16 - index),
		}
	}
	jobs := make([]slurmDisplayJob, 10)
	for index := range jobs {
		jobs[index] = slurmDisplayJob{
			Job: slurmJob{
				ID: fmt.Sprintf("%d", 1000+index), User: "alice", State: "PENDING",
				Partition: "gpu", Name: fmt.Sprintf("job-%d", index),
			},
			Next: index == 0,
		}
	}
	m := &monitorModel{
		screen: screenMonitor, width: 120, height: 48, colorMode: colorModeDark,
		snapshot: monitorSnapshot{
			CollectedAt: time.Now(), MemoryTotal: 1, DiskTotal: 1, Processes: processes,
		},
		slurmQueue: &nodeSlurmQueue{
			Cluster: "Test Cluster", Node: "gpu01", CollectedAt: time.Now(), Jobs: jobs,
		},
	}
	initialRows := m.slurmQueueRows()
	if initialRows != 6 {
		t.Fatalf("default node queue rows = %d, want 6", initialRows)
	}
	m.handleKey(testKey("+"))
	if got := m.slurmQueueRows(); got != initialRows+1 {
		t.Fatalf("focused queue did not grow: rows=%d", got)
	}
	m.handleKey(testKey("tab"))
	m.handleKey(testKey("+"))
	if got := m.slurmQueueRows(); got != initialRows {
		t.Fatalf("growing focused process panel should shrink queue: rows=%d", got)
	}

	for _, size := range []struct{ width, height int }{{160, 54}, {120, 48}, {100, 36}, {60, 24}} {
		m.width, m.height = size.width, size.height
		rendered := m.monitorView()
		if got := lipgloss.Height(rendered); got > m.height {
			t.Fatalf("%dx%d resizable dashboard height = %d\n%s", m.width, m.height, got, rendered)
		}
		for lineNumber, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > m.width {
				t.Fatalf("%dx%d line %d width = %d\n%q", m.width, m.height, lineNumber, got, line)
			}
		}
	}
	m.width, m.height = 120, 48
	_ = m.monitorView()
	if m.processRowY <= m.processPanelY || m.monitorRows < 1 {
		t.Fatalf("invalid process hit area: panel=%d row=%d rows=%d", m.processPanelY, m.processRowY, m.monitorRows)
	}
	command := m.handleClick(4, m.processRowY)
	if command == nil || m.screen != screenProcessDetail || !m.processReadOnly ||
		m.selectedProcess == nil || m.selectedProcess.PID != processes[0].PID {
		t.Fatalf("read-only click selection failed: screen=%v readonly=%t process=%#v", m.screen, m.processReadOnly, m.selectedProcess)
	}
	detailView := m.processDetailView()
	if strings.Contains(detailView, "Terminate process") {
		t.Fatalf("read-only process details exposed termination action:\n%s", detailView)
	}
	m.handleKey(testKey("t"))
	if m.screen != screenProcessDetail {
		t.Fatal("read-only details accepted the terminate shortcut")
	}
	m.handleKey(testKey("esc"))
	if m.screen != screenMonitor {
		t.Fatalf("read-only process details returned to screen %v, want monitor", m.screen)
	}
}

func TestRPCProcessDetailsAreReadOnlyWithoutAuthentication(t *testing.T) {
	admin := &adminController{password: "secret"}
	service := newNodeRPCService(admin, machineConfig{})
	detail := service.Handle(nodeRPCRequest{
		Version: nodeRPCVersion, Operation: rpcProcessDetail, PID: -1,
	})
	if detail.Error != "invalid PID" {
		t.Fatalf("unauthenticated read-only detail did not reach the reader: %#v", detail)
	}
	terminate := service.Handle(nodeRPCRequest{
		Version: nodeRPCVersion, Operation: rpcTerminateProcess, PID: os.Getpid(),
	})
	if !strings.Contains(terminate.Error, "authentication failed") {
		t.Fatalf("unauthenticated termination was not rejected: %#v", terminate)
	}
}

func TestManagementAuthenticationLocksAfterRepeatedFailures(t *testing.T) {
	model := &monitorModel{admin: &adminController{password: "correct"}, screen: screenPassword}
	for attempt := 0; attempt < maxManagementAuthFailures; attempt++ {
		_, _ = model.Update(authenticationResultMsg{password: "wrong", ok: false})
	}
	if !time.Now().Before(model.authLockedUntil) {
		t.Fatal("repeated management authentication failures did not start a lockout")
	}
	model.password = "correct"
	if command := model.handleKey(testKey("enter")); command != nil {
		t.Fatal("locked management authentication should not call the backend")
	}
	if !strings.Contains(model.status, "locked") {
		t.Fatalf("lockout status is not visible: %q", model.status)
	}
}

func TestProcessSelectionScrollsWithTerminalHeight(t *testing.T) {
	processes := make([]processInfo, 12)
	for i := range processes {
		processes[i] = processInfo{PID: 100 + i, Command: "worker"}
	}
	m := &monitorModel{height: 12, screen: screenAdmin, snapshot: monitorSnapshot{Processes: processes}}
	for range 6 {
		m.handleKey(testKey("down"))
	}
	if m.cursor != 6 || m.processOffset == 0 {
		t.Fatalf("cursor=%d offset=%d, want a scrolled selection", m.cursor, m.processOffset)
	}
}

func TestProtectedProcessesCannotBeTerminated(t *testing.T) {
	if canTerminatePID(1) {
		t.Fatal("PID 1 must be protected")
	}
	if canTerminatePID(os.Getpid()) {
		t.Fatal("monitor process must be protected")
	}
	if got := sanitizeTerminalText("safe\x1b[2J\ntext"); got != "safe[2Jtext" {
		t.Fatalf("sanitized text = %q", got)
	}
}

func TestProcessStartTicksParserHandlesSpacesInName(t *testing.T) {
	stat := []byte("123 (worker process) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242")
	got, err := parseProcessStartTicks(stat)
	if err != nil {
		t.Fatal(err)
	}
	if got != 424242 {
		t.Fatalf("start ticks = %d", got)
	}
}

func TestFormattingHelpers(t *testing.T) {
	if got := bytes(1024 * 1024 * 1536); got != "1.5 GiB" {
		t.Fatalf("bytes = %q", got)
	}
	if got := elapsed(3661); got != "1h01m" {
		t.Fatalf("elapsed = %q", got)
	}
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := trimLastRune("a界"); got != "a" {
		t.Fatalf("trimLastRune = %q", got)
	}
	if got := counterDelta(150, 100); got != 50 {
		t.Fatalf("counter delta = %d", got)
	}
	if got := counterDelta(10, 100); got != 0 {
		t.Fatalf("reset counter delta = %d, want 0", got)
	}
}

func TestDashboardLayoutUsesTerminalSize(t *testing.T) {
	wide := newDashboardLayout(161, 50, 2, false)
	if wide.width != 161 || wide.metricCols != 4 {
		t.Fatalf("wide layout = %#v, want full width and four metric columns", wide)
	}
	if wide.processRows < 25 {
		t.Fatalf("wide terminal should use its height for process rows, got %d", wide.processRows)
	}
	narrow := newDashboardLayout(70, 24, 1, false)
	if narrow.metricCols != 2 || !narrow.compactGPU {
		t.Fatalf("narrow layout = %#v, want two compact metric columns and compact GPU", narrow)
	}
}

func TestMonitorViewFitsCommonTerminalSizes(t *testing.T) {
	processes := make([]processInfo, 40)
	for i := range processes {
		processes[i] = processInfo{PID: 1000 + i, User: "worker", State: "S", CPU: float64(i), Memory: 0.1, RSS: 12 * 1024 * 1024, Elapsed: 120, Command: "python training-job.py"}
	}
	model := &monitorModel{
		status: "Live data refreshes every second.",
		snapshot: monitorSnapshot{
			CollectedAt: time.Date(2026, 7, 25, 12, 34, 56, 0, time.Local),
			CPUPercent:  42, LoadAverage: "load 0.42 · 0.38 · 0.31",
			MemoryUsed: 24 * 1024 * 1024 * 1024, MemoryTotal: 128 * 1024 * 1024 * 1024,
			DiskUsed: 200 * 1024 * 1024 * 1024, DiskTotal: 1024 * 1024 * 1024 * 1024,
			NetworkRX: 128 * 1024, NetworkTX: 64 * 1024, NetworkRXTotal: 8 * 1024 * 1024 * 1024, NetworkTXTotal: 4 * 1024 * 1024 * 1024,
			GPUs: []gpuInfo{
				{Index: 0, Name: "NVIDIA A100-PCIE-40GB", Utilization: 80, MemoryUsed: 16 * 1024 * 1024 * 1024, MemoryTotal: 40 * 1024 * 1024 * 1024, ClockMHz: 1410, Power: 180, PowerLimit: 250, Temperature: 70},
				{Index: 1, Name: "NVIDIA TITAN RTX", Utilization: 20, MemoryUsed: 2 * 1024 * 1024 * 1024, MemoryTotal: 24 * 1024 * 1024 * 1024, ClockMHz: 900, Power: 80, PowerLimit: 280, Temperature: 45},
			},
			Processes: processes,
		},
		cpuHistory:       []float64{10, 25, 35, 42},
		networkRXHistory: []float64{1024, 4096, 2048, 8192},
		networkTXHistory: []float64{512, 1024, 4096, 2048},
	}
	for _, size := range []struct{ width, height int }{{161, 50}, {100, 40}, {80, 30}, {60, 24}} {
		model.width, model.height = size.width, size.height
		rendered := model.monitorView()
		if got := lipgloss.Height(rendered); got > size.height {
			t.Fatalf("%dx%d view height = %d\n%s", size.width, size.height, got, rendered)
		}
		for index, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d line %d width = %d\n%q", size.width, size.height, index, got, line)
			}
		}
	}
}

func TestGPUClockAndPowerParsing(t *testing.T) {
	gpus := parseGPUs([]byte("0, NVIDIA A100-PCIE-40GB, 82, 1024, 40960, 55, 1410, 70.64, 250.00\n"))
	if len(gpus) != 1 {
		t.Fatalf("parsed GPUs = %#v", gpus)
	}
	gpu := gpus[0]
	if gpu.ClockMHz != 1410 || gpu.Power != 70.64 || gpu.PowerLimit != 250 {
		t.Fatalf("GPU telemetry = %#v", gpu)
	}
	if got := gpuTelemetry(gpu, true); got != "CLK 1410 MHz · PWR  71/250 W ·  55°C" {
		t.Fatalf("GPU telemetry text = %q", got)
	}
}

func TestGPULoadStatusAndWideLayout(t *testing.T) {
	cases := []struct {
		utilization float64
		status      string
	}{
		{0, "IDLE"},
		{20, "LIGHT"},
		{50, "ACTIVE"},
		{75, "BUSY"},
		{90, "HIGH"},
		{99, "MAX"},
	}
	for _, tc := range cases {
		status, _ := gpuLoadStatus(tc.utilization)
		if status != tc.status {
			t.Fatalf("utilization %.0f status = %q, want %q", tc.utilization, status, tc.status)
		}
	}
	if bar(20, 10) == bar(90, 10) {
		t.Fatal("low and high utilization bars should use different colors")
	}

	model := &monitorModel{snapshot: monitorSnapshot{GPUs: []gpuInfo{
		{Index: 0, Name: "NVIDIA A100", MemoryTotal: 40 * 1024 * 1024 * 1024},
		{Index: 1, Name: "NVIDIA TITAN RTX", MemoryTotal: 24 * 1024 * 1024 * 1024},
	}}}
	layout := newDashboardLayout(161, 50, 2, false)
	if height := lipgloss.Height(model.gpuPanel(layout)); height != 6 {
		t.Fatalf("wide GPU panel height = %d, want two lines per GPU plus border", height)
	}
}

func TestGPUMemoryColumnsAreAligned(t *testing.T) {
	gpus := []gpuInfo{
		{MemoryUsed: 425 * 1024 * 1024, MemoryTotal: 40 * 1024 * 1024 * 1024},
		{MemoryUsed: 1 * 1024 * 1024, MemoryTotal: 24 * 1024 * 1024 * 1024},
	}
	usedWidth, totalWidth := gpuMemoryWidths(gpus)
	first := gpuMemoryText(gpus[0], usedWidth, totalWidth)
	second := gpuMemoryText(gpus[1], usedWidth, totalWidth)
	if lipgloss.Width(first) != lipgloss.Width(second) || strings.Index(first, "/") != strings.Index(second, "/") {
		t.Fatalf("GPU memory columns are not aligned: %q / %q", first, second)
	}
}

func TestMetricCardsJoinHorizontally(t *testing.T) {
	cards := []metricCard{
		{title: "CPU", value: "1%", detail: "load 0.1", visual: metricVisualCPU, primaryHistory: []float64{1}, titleStyle: cpuTitleStyle, borderColor: colorCPUBorder},
		{title: "MEMORY", value: "2 GiB", detail: "10% used", visual: metricVisualMeter, usage: 10, titleStyle: memoryTitleStyle, borderColor: colorMemoryBorder},
		{title: "DISK", value: "3 GiB", detail: "20% used", visual: metricVisualMeter, usage: 20, titleStyle: diskTitleStyle, borderColor: colorDiskBorder},
		{title: "NETWORK", value: "1 KiB/s", detail: "2 KiB/s", visual: metricVisualNetwork, primaryHistory: []float64{1}, secondaryHistory: []float64{2}, titleStyle: networkTitleStyle, borderColor: colorNetworkBorder},
	}
	rendered := renderMetricRows(cards, dashboardLayout{width: 161, metricCols: 4})
	if height := lipgloss.Height(rendered); height != 5 {
		t.Fatalf("metric cards height = %d, want one five-line row", height)
	}
}

func TestBtopPanelsAndHistoryVisuals(t *testing.T) {
	panel := btopPanel(80, "CPU", "LIVE", "line one\nline two", cpuTitleStyle, colorCPUBorder)
	for index, line := range strings.Split(panel, "\n") {
		if width := lipgloss.Width(line); width != 80 {
			t.Fatalf("panel line %d width = %d, want 80: %q", index, width, line)
		}
	}
	if !strings.Contains(panel, "CPU") || !strings.Contains(panel, "LIVE") {
		t.Fatalf("panel title metadata missing: %q", panel)
	}

	history := []float64{}
	for i := 0; i < 70; i++ {
		history = appendHistory(history, float64(i), 60)
	}
	if len(history) != 60 || history[0] != 10 || history[len(history)-1] != 69 {
		t.Fatalf("bounded history = %#v", history)
	}
	graph := sparkline([]float64{0, 50, 100}, 8, 100, cpuTitleStyle)
	if lipgloss.Width(graph) != 8 || !strings.Contains(graph, "█") {
		t.Fatalf("sparkline = %q, width %d", graph, lipgloss.Width(graph))
	}
}

func TestProcessFormatRespondsToWidth(t *testing.T) {
	if got := newProcessFormat(140).mode; got != processFull {
		t.Fatalf("wide mode = %d, want full", got)
	}
	if got := newProcessFormat(90).mode; got != processMedium {
		t.Fatalf("medium mode = %d, want medium", got)
	}
	if got := newProcessFormat(60).mode; got != processCompact {
		t.Fatalf("compact mode = %d, want compact", got)
	}
}

func TestProcessColumnsAreAligned(t *testing.T) {
	format := newProcessFormat(140)
	header := format.header()
	row := format.row(processInfo{
		PID: 42, User: "alice", State: "R+", CPU: 12.3, Memory: 4.5,
		RSS: 10 * 1024 * 1024, Elapsed: 65, Command: "trainer",
	})
	if strings.Contains(header, "STAT") || strings.Contains(row, "R+") {
		t.Fatalf("process state should not occupy a table column\nheader: %q\nrow:    %q", header, row)
	}
	for _, field := range []string{"RSS", "ELAPSED", "COMMAND"} {
		if strings.Index(header, field) != strings.Index(row, map[string]string{
			"RSS": "10.0 MiB", "ELAPSED": "1m05s", "COMMAND": "trainer",
		}[field]) {
			t.Fatalf("%s is not aligned\nheader: %q\nrow:    %q", field, header, row)
		}
	}
	if strings.Index(header, "CPU")+len("CPU") != strings.Index(row, "12.3%")+len("12.3%") {
		t.Fatalf("CPU is not right-aligned\nheader: %q\nrow:    %q", header, row)
	}
	if strings.Index(header, "MEM")+len("MEM") != strings.Index(row, "4.5%")+len("4.5%") {
		t.Fatalf("MEM is not right-aligned\nheader: %q\nrow:    %q", header, row)
	}
}

func TestProcessLegendAdaptsToWidth(t *testing.T) {
	wide := processLegend(120)
	for _, label := range []string{"RUNNING", "SLEEPING", "WAITING", "STOPPED", "ZOMBIE", "IDLE"} {
		if !strings.Contains(wide, label) {
			t.Fatalf("wide legend does not contain %q: %q", label, wide)
		}
	}
	compact := processLegend(50)
	if strings.Contains(compact, "RUNNING") {
		t.Fatalf("compact legend should use state letters only: %q", compact)
	}
	for _, state := range []string{"R", "S", "D", "T", "Z", "I"} {
		if !strings.Contains(compact, state) {
			t.Fatalf("compact legend does not contain state %q: %q", state, compact)
		}
	}
}

func TestProcessStateColorsAndRefreshInterval(t *testing.T) {
	if refreshInterval != time.Second {
		t.Fatalf("refresh interval = %s", refreshInterval)
	}
	if processStateStyle("R").Render("row") == processStateStyle("S").Render("row") {
		t.Fatal("running and sleeping processes should use different colors")
	}
	if processStateStyle("D").Render("row") == processStateStyle("Z").Render("row") {
		t.Fatal("waiting and zombie processes should use different colors")
	}
}

func testKey(s string) tea.KeyMsg { return tea.KeyPressMsg{Code: keyCode(s), Text: keyText(s)} }

func keyCode(s string) rune {
	switch s {
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEscape
	case "backspace":
		return tea.KeyBackspace
	}
	return []rune(s)[0]
}

func keyText(s string) string {
	if s == "enter" || s == "esc" || s == "backspace" {
		return ""
	}
	return s
}
