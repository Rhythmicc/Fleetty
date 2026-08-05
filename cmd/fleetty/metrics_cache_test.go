package main

import (
	"testing"
	"time"
)

func TestSnapshotCacheReusesFreshFullSnapshots(t *testing.T) {
	calls := 0
	cache := &snapshotCache{
		fullFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		summaryFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		warmupFn: func() bool { return false },
	}

	first, err := cache.Get(true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Get(true)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fresh full snapshot recollected: calls=%d", calls)
	}
	if first.CPUPercent != second.CPUPercent {
		t.Fatalf("cached snapshot changed: %v -> %v", first.CPUPercent, second.CPUPercent)
	}
}

func TestSnapshotCacheKeepsSummaryAndFullIndependent(t *testing.T) {
	fullCalls := 0
	summaryCalls := 0
	cache := &snapshotCache{
		fullFn: func() (monitorSnapshot, error) {
			fullCalls++
			return monitorSnapshot{CollectedAt: time.Now()}, nil
		},
		summaryFn: func() (monitorSnapshot, error) {
			summaryCalls++
			return monitorSnapshot{CollectedAt: time.Now()}, nil
		},
		warmupFn: func() bool { return false },
	}

	if _, err := cache.Get(true); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(false); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(true); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(false); err != nil {
		t.Fatal(err)
	}
	if fullCalls != 1 || summaryCalls != 1 {
		t.Fatalf("cache depths should be independent: full=%d summary=%d", fullCalls, summaryCalls)
	}
}

func TestSnapshotCacheWarmsUpFirstSample(t *testing.T) {
	calls := 0
	cache := &snapshotCache{
		fullFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		summaryFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		warmupFn: func() bool { return true },
	}

	snapshot, err := cache.Get(true)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("warmup should collect twice, calls=%d", calls)
	}
	if snapshot.CPUPercent != 2 {
		t.Fatalf("warmup should return the second sample, got %v", snapshot.CPUPercent)
	}
}

func TestSnapshotCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	cache := &snapshotCache{
		fullFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		summaryFn: func() (monitorSnapshot, error) {
			calls++
			return monitorSnapshot{CollectedAt: time.Now(), CPUPercent: float64(calls)}, nil
		},
		warmupFn: func() bool { return false },
	}
	if _, err := cache.Get(true); err != nil {
		t.Fatal(err)
	}
	cache.fullAt = time.Now().Add(-snapshotCacheTTL - time.Millisecond)
	if _, err := cache.Get(true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expired snapshot should be recollected, calls=%d", calls)
	}
}
