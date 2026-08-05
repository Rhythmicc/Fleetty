package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	historyRetention    = 7 * 24 * time.Hour
	historySamplePeriod = time.Minute
	historyPrunePeriod  = time.Hour
)

// historySample is one minute-aligned record of the host and GPU metrics.
// The JSONL file is append-only; the in-memory copy is a bounded ring used
// for queries and for rewriting the file during pruning.
type historySample struct {
	Timestamp   time.Time          `json:"t"`
	CPUPercent  float64            `json:"cpu"`
	MemoryUsed  uint64             `json:"mu"`
	MemoryTotal uint64             `json:"mt"`
	DiskUsed    uint64             `json:"du"`
	DiskTotal   uint64             `json:"dt"`
	NetworkRX   uint64             `json:"nr"`
	NetworkTX   uint64             `json:"nt"`
	GPUs        []historyGPUSample `json:"gpus,omitempty"`
}

type historyGPUSample struct {
	Index       int     `json:"i"`
	Utilization float64 `json:"u"`
	MemoryUsed  uint64  `json:"mu"`
	Temperature int     `json:"t"`
}

// historyStore persists minute-aligned samples as JSONL and keeps a bounded
// in-memory copy. A missing or empty path disables persistence.
type historyStore struct {
	mu         sync.Mutex
	path       string
	samples    []historySample
	lastBucket time.Time
	lastPrune  time.Time
}

// defaultHistoryPath resolves the node-local history file. Operators can pin
// an explicit path with FLEETTY_HISTORY_FILE; otherwise the file lives next to
// machine.json when configured, falling back to ~/.config/fleetty.
func defaultHistoryPath() string {
	if path := strings.TrimSpace(os.Getenv("FLEETTY_HISTORY_FILE")); path != "" {
		return path
	}
	if machineConfigFile := strings.TrimSpace(os.Getenv("MACHINE_CONFIG_FILE")); machineConfigFile != "" {
		return filepath.Join(filepath.Dir(machineConfigFile), "history.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "fleetty", "history.jsonl")
	}
	return ""
}

func newHistoryStore(path string) *historyStore {
	store := &historyStore{path: strings.TrimSpace(path)}
	if store.path != "" {
		store.load()
	}
	return store
}

func (h *historyStore) enabled() bool {
	return h != nil && h.path != ""
}

// Record appends one sample per wall-clock minute. Multiple collection cycles
// within the same minute (shared cache, Hub RPC, warmup) collapse into one
// record, so the file grows by at most 1440 lines per day.
func (h *historyStore) Record(snapshot monitorSnapshot) {
	if !h.enabled() {
		return
	}
	now := time.Now()
	bucket := now.Truncate(historySamplePeriod)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.lastBucket.IsZero() && !bucket.After(h.lastBucket) {
		return
	}
	sample := historySample{
		Timestamp:   bucket,
		CPUPercent:  snapshot.CPUPercent,
		MemoryUsed:  snapshot.MemoryUsed,
		MemoryTotal: snapshot.MemoryTotal,
		DiskUsed:    snapshot.DiskUsed,
		DiskTotal:   snapshot.DiskTotal,
		NetworkRX:   snapshot.NetworkRX,
		NetworkTX:   snapshot.NetworkTX,
	}
	for _, gpu := range snapshot.GPUs {
		sample.GPUs = append(sample.GPUs, historyGPUSample{
			Index: gpu.Index, Utilization: gpu.Utilization,
			MemoryUsed: gpu.MemoryUsed, Temperature: gpu.Temperature,
		})
	}
	h.samples = append(h.samples, sample)
	h.lastBucket = bucket
	if err := h.appendFile(sample); err == nil &&
		(now.Sub(h.lastPrune) >= historyPrunePeriod || h.lastPrune.IsZero()) {
		h.lastPrune = now
		h.pruneFile()
	}
	h.trimMemory(now)
}

// Recent returns a copy of the samples from the last minutes (all when
// minutes <= 0), ordered oldest to newest.
func (h *historyStore) Recent(minutes int) []historySample {
	if !h.enabled() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if minutes <= 0 || len(h.samples) == 0 {
		return append([]historySample(nil), h.samples...)
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	index := sort.Search(len(h.samples), func(i int) bool {
		return h.samples[i].Timestamp.After(cutoff)
	})
	return append([]historySample(nil), h.samples[index:]...)
}

func (h *historyStore) appendFile(sample historySample) error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func (h *historyStore) load() {
	file, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer file.Close()
	cutoff := time.Now().Add(-historyRetention)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var samples []historySample
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample historySample
		if json.Unmarshal([]byte(line), &sample) != nil || sample.Timestamp.IsZero() {
			continue
		}
		if sample.Timestamp.Before(cutoff) {
			continue
		}
		if len(samples) > 0 && !sample.Timestamp.After(samples[len(samples)-1].Timestamp) {
			continue
		}
		samples = append(samples, sample)
	}
	h.samples = samples
	if len(samples) > 0 {
		h.lastBucket = samples[len(samples)-1].Timestamp
	}
	h.lastPrune = time.Now()
}

func (h *historyStore) pruneFile() {
	if len(h.samples) == 0 {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), ".fleetty-history-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	ok := true
	for _, sample := range h.samples {
		encoded, encodeErr := json.Marshal(sample)
		if encodeErr != nil {
			ok = false
			break
		}
		if _, writeErr := tmp.Write(append(encoded, '\n')); writeErr != nil {
			ok = false
			break
		}
	}
	_ = tmp.Close()
	if !ok {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, h.path); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func (h *historyStore) trimMemory(now time.Time) {
	cutoff := now.Add(-historyRetention)
	index := sort.Search(len(h.samples), func(i int) bool {
		return h.samples[i].Timestamp.After(cutoff)
	})
	if index > 0 {
		h.samples = append([]historySample(nil), h.samples[index:]...)
	}
}
