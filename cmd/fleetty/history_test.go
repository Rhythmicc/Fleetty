package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryStoreRecordsOncePerMinute(t *testing.T) {
	store := newHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"))
	store.Record(monitorSnapshot{CPUPercent: 10})
	store.Record(monitorSnapshot{CPUPercent: 20})
	samples := store.Recent(60)
	if len(samples) != 1 || samples[0].CPUPercent != 10 {
		t.Fatalf("records = %#v", samples)
	}
}

func TestHistoryStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	first := newHistoryStore(path)
	first.Record(monitorSnapshot{
		CPUPercent: 33, MemoryUsed: 5, MemoryTotal: 100,
		GPUs: []gpuInfo{{Index: 0, Utilization: 50, MemoryUsed: 7, Temperature: 44}},
	})
	second := newHistoryStore(path)
	samples := second.Recent(60)
	if len(samples) != 1 || samples[0].CPUPercent != 33 ||
		len(samples[0].GPUs) != 1 || samples[0].GPUs[0].Utilization != 50 {
		t.Fatalf("reloaded samples = %#v", samples)
	}
}

func TestHistoryStoreLoadDropsExpiredAndDuplicateBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Minute)
	old := now.Add(-8 * 24 * time.Hour)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, timestamp := range []time.Time{old, now, now} {
		encoded, marshalErr := json.Marshal(historySample{Timestamp: timestamp, CPUPercent: 1})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, writeErr := file.Write(append(encoded, '\n')); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	_ = file.Close()

	store := newHistoryStore(path)
	samples := store.Recent(0)
	if len(samples) != 1 || !samples[0].Timestamp.Equal(now) {
		t.Fatalf("loaded samples = %#v", samples)
	}
}

func TestHistoryStoreDisabled(t *testing.T) {
	store := newHistoryStore("")
	store.Record(monitorSnapshot{CPUPercent: 1})
	if store.Recent(60) != nil {
		t.Fatal("disabled history store should return no samples")
	}
}

func TestRPCHistoryReturnsStoredSamples(t *testing.T) {
	store := newHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"))
	store.Record(monitorSnapshot{CPUPercent: 12})
	service := newNodeRPCService(
		&adminController{},
		newSnapshotCache(newMetricsCollector(machineConfig{Profile: machineProfileGeneral})),
		store,
	)
	response := service.Handle(nodeRPCRequest{
		Version: nodeRPCVersion, Operation: rpcHistory, HistoryMinutes: 60,
	})
	if response.Error != "" || len(response.History) != 1 ||
		response.History[0].CPUPercent != 12 {
		t.Fatalf("RPC history = %#v", response)
	}

	disabled := newNodeRPCService(
		&adminController{},
		newSnapshotCache(newMetricsCollector(machineConfig{Profile: machineProfileGeneral})),
		nil,
	)
	response = disabled.Handle(nodeRPCRequest{
		Version: nodeRPCVersion, Operation: rpcHistory,
	})
	if response.Error == "" {
		t.Fatal("disabled history should return an RPC error")
	}
}

func TestHourHistoryCmdRefreshesOncePerMinute(t *testing.T) {
	model := &monitorModel{backend: &fakeHistoryBackend{
		history: []historySample{
			{Timestamp: time.Now().Add(-2 * time.Minute), CPUPercent: 5},
			{Timestamp: time.Now().Add(-time.Minute), CPUPercent: 15},
		},
	}}
	command := model.hourHistoryCmd()
	if command == nil {
		t.Fatal("hour history should be due on first tick")
	}
	message := command()
	historyMessage, ok := message.(historyMsg)
	if !ok || historyMessage.err != nil || len(historyMessage.history) != 2 {
		t.Fatalf("hour history message = %#v", message)
	}
	updated, _ := model.Update(historyMessage)
	model = updated.(*monitorModel)
	if len(model.hourCPUHistory) != 2 || model.hourCPUHistory[1] != 15 {
		t.Fatalf("hour CPU history = %#v", model.hourCPUHistory)
	}
	if model.hourHistoryCmd() != nil {
		t.Fatal("hour history should not refresh again within the same minute")
	}
}

func TestHubCardRendersHistorySparkline(t *testing.T) {
	model := &hubModel{
		config: hubConfig{Nodes: []hubNodeConfig{{
			Name: "gpu-1", Profile: machineProfileGPU, Description: "Training node",
		}}},
		states: []hubNodeState{{
			Snapshot: monitorSnapshot{
				CollectedAt: time.Now(), Profile: machineProfileGPU,
				CPUPercent: 10, MemoryTotal: 1, DiskTotal: 1,
				GPUs: []gpuInfo{{Utilization: 10, MemoryTotal: 1}},
			},
			History: []historySample{
				{Timestamp: time.Now().Add(-2 * time.Minute), CPUPercent: 10},
				{Timestamp: time.Now().Add(-time.Minute), CPUPercent: 20},
				{Timestamp: time.Now(), CPUPercent: 30},
			},
		}},
		cursor: 0,
		width:  80, height: 30,
	}
	rendered := model.renderNodeCard(0, 40)
	if !strings.Contains(rendered, "▁") && !strings.Contains(rendered, "▂") {
		t.Fatalf("hub card should render a history sparkline:\n%s", rendered)
	}
}

type fakeHistoryBackend struct {
	history []historySample
	err     error
}

func (f *fakeHistoryBackend) Collect() (monitorSnapshot, error) {
	return monitorSnapshot{}, f.err
}

func (f *fakeHistoryBackend) History(_ int) ([]historySample, error) {
	return f.history, f.err
}

func (f *fakeHistoryBackend) Authenticate(string) (bool, error) {
	return false, nil
}

func (f *fakeHistoryBackend) ProcessDetail(int, string) (processDetail, error) {
	return processDetail{}, nil
}

func (f *fakeHistoryBackend) TerminateProcess(int, uint64, string) error {
	return nil
}

func (f *fakeHistoryBackend) RunAction(int, string) (string, error) {
	return "", nil
}
