package main

import (
	"testing"
	"time"
)

func TestHubModelReadsSharedServiceSnapshot(t *testing.T) {
	service := newHubService(hubConfig{Nodes: []hubNodeConfig{{
		Name: "unreachable", Address: "192.0.2.1:23234",
	}}})
	service.states[0] = hubNodeState{Snapshot: monitorSnapshot{
		CollectedAt: time.Now(), CPUPercent: 42,
	}}

	model := newHubModel(service, nil, 100, 30)
	if got := model.states[0].Snapshot.CPUPercent; got != 42 {
		t.Fatalf("initial shared CPU snapshot = %v, want 42", got)
	}
	message, ok := model.startCollect()().(hubSnapshotsMsg)
	if !ok {
		t.Fatalf("shared snapshot command returned %T", message)
	}
	if got := message.States[0].Snapshot.CPUPercent; got != 42 {
		t.Fatalf("refreshed shared CPU snapshot = %v, want 42", got)
	}
}

func TestHubRetryWakesSharedCollector(t *testing.T) {
	service := newHubService(hubConfig{Nodes: []hubNodeConfig{{Name: "offline"}}})
	service.states[0] = hubNodeState{
		Error:     "offline",
		NextRetry: time.Now().Add(time.Minute),
	}

	service.retryOfflineNow()
	if !service.states[0].NextRetry.IsZero() {
		t.Fatal("manual retry should clear the node backoff")
	}
	select {
	case <-service.nodeWake:
	default:
		t.Fatal("manual retry should wake the node collector")
	}
	select {
	case <-service.slurmWake:
	default:
		t.Fatal("manual retry should wake the Slurm collector")
	}
}

func TestHubSlurmCollectorUsesFastestConfiguredInterval(t *testing.T) {
	service := newHubService(hubConfig{SlurmClusters: []slurmClusterConfig{
		{Name: "slow", RefreshSeconds: 5},
		{Name: "fast", RefreshSeconds: 2},
	}})
	if got := service.slurmRefreshInterval(); got != 2*time.Second {
		t.Fatalf("Slurm collector interval = %s, want 2s", got)
	}
}
