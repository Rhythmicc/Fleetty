package main

import (
	stdbytes "bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRenderPrometheusMetricsCoversHostGPUAndServices(t *testing.T) {
	snapshot := monitorSnapshot{
		CollectedAt:    time.Now(),
		NodeName:       "gpu-1",
		CPUPercent:     42.5,
		MemoryUsed:     1 << 30,
		MemoryTotal:    4 << 30,
		DiskUsed:       2 << 30,
		DiskTotal:      8 << 30,
		NetworkRX:      1024,
		NetworkTX:      512,
		NetworkRXTotal: 1 << 40,
		NetworkTXTotal: 2 << 40,
		GPUs: []gpuInfo{{
			Index: 0, Name: "NVIDIA A100", Utilization: 88,
			MemoryUsed: 10 << 30, MemoryTotal: 80 << 30,
			Temperature: 65, ClockMHz: 1410, Power: 250, PowerLimit: 300,
		}},
		Filesystems: []filesystemInfo{{Mount: "/mnt/data", Used: 1 << 30, Total: 2 << 30}},
		Services:    []serviceHealth{{Name: "web", Kind: "http", Healthy: true}},
		Containers: []containerInfo{
			{Name: "app", Running: true},
			{Name: "worker", Running: false},
		},
		PM2Processes: []pm2ProcessInfo{{Name: "api", Status: "online"}},
	}
	output := renderPrometheusMetrics(snapshot)
	for _, expected := range []string{
		`node="gpu-1"`,
		"fleetty_cpu_percent{node=\"gpu-1\"} 42.5",
		"fleetty_memory_used_bytes{node=\"gpu-1\"} 1073741824",
		`fleetty_gpu_utilization_percent{node="gpu-1",gpu="0",name="NVIDIA A100"} 88`,
		`fleetty_gpu_temperature_celsius{node="gpu-1",gpu="0",name="NVIDIA A100"} 65`,
		`fleetty_filesystem_used_bytes{node="gpu-1",mount="/mnt/data"} 1073741824`,
		`fleetty_service_healthy{node="gpu-1",service="web",kind="http"} 1`,
		"fleetty_containers_running{node=\"gpu-1\"} 1",
		"fleetty_pm2_running{node=\"gpu-1\"} 1",
		"# TYPE fleetty_network_rx_total_bytes counter",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics output missing %q:\n%s", expected, output)
		}
	}
}

func TestRenderPrometheusMetricsEscapesLabels(t *testing.T) {
	snapshot := monitorSnapshot{
		NodeName: `node"with\newline` + "\n",
		Services: []serviceHealth{{Name: `svc"x` + "\n", Kind: "http", Healthy: false}},
	}
	output := renderPrometheusMetrics(snapshot)
	if !strings.Contains(output, `node="node\"with\\newline\n"`) {
		t.Fatalf("node label was not escaped:\n%s", output)
	}
	if !strings.Contains(output, `service="svc\"x\n"`) {
		t.Fatalf("service label was not escaped:\n%s", output)
	}
	if !strings.Contains(output, "fleetty_service_healthy{node=\"node\\\"with\\\\newline\\n\",service=\"svc\\\"x\\n\",kind=\"http\"} 0") {
		t.Fatalf("unhealthy service sample is wrong:\n%s", output)
	}
}

func TestExportSnapshotConvertsStructuredFields(t *testing.T) {
	export := exportSnapshot(monitorSnapshot{
		CollectedAt: time.Unix(1700000000, 0),
		NodeName:    "nas-1",
		Profile:     machineProfileNAS,
		Battery:     &batteryInfo{Percent: 80, Status: "charging"},
		GPUs: []gpuInfo{{
			Index: 0, UUID: "gpu-uuid", Name: "A100",
			Workloads: []gpuWorkloadInfo{{PID: 42, User: "alice", Name: "train", MemoryUsed: 1 << 30}},
		}},
		Processes: []processInfo{{PID: 7, User: "root", Command: "systemd", CPU: 0.5}},
	})
	encoded, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"node_name":"nas-1"`,
		`"profile":"nas"`,
		`"percent":80`,
		`"workloads":[{"pid":42,"user":"alice","name":"train","memory_used_bytes":1073741824}]`,
		`"command":"systemd"`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("export missing %q:\n%s", expected, encoded)
		}
	}
}

func TestRunSnapshotCommandWritesMachineReadableJSON(t *testing.T) {
	configPath := t.TempDir() + "/machine.json"
	config := `{"name":"test-node","profile":"general"}`
	if err := writeTestFile(configPath, config); err != nil {
		t.Fatal(err)
	}
	var stdout stdbytes.Buffer
	if err := runSnapshotCommand(
		[]string{"--config", configPath, "--processes"},
		&stdout, &stdbytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("snapshot output is not JSON: %v\n%s", err, stdout.String())
	}
	if document["node_name"] != "test-node" {
		t.Fatalf("unexpected node_name: %#v", document["node_name"])
	}
	if _, ok := document["cpu_percent"]; !ok {
		t.Fatalf("snapshot missing cpu_percent:\n%s", stdout.String())
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
