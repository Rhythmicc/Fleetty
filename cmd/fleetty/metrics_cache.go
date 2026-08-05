package main

import (
	"sync"
	"time"
)

// snapshotCacheTTL controls how long a published snapshot is reused before a
// new collection cycle starts. It is deliberately smaller than the TUI refresh
// interval so every one-second tick still observes fresh host metrics, while
// concurrent SSH sessions and Hub RPC requests share the same collection cycle
// instead of each running their own ps/nvidia-smi/nettop pass.
const snapshotCacheTTL = 750 * time.Millisecond

// snapshotCache shares one metrics collection cycle between every local
// terminal session and Hub RPC request on a node. Full and summary snapshots
// are cached independently: the Hub overview asks for the lighter summary so
// process tables are not transferred over RPC for every node card.
type snapshotCache struct {
	fullFn    func() (monitorSnapshot, error)
	summaryFn func() (monitorSnapshot, error)
	warmupFn  func() bool

	mu          sync.Mutex
	full        monitorSnapshot
	fullErr     error
	haveFull    bool
	fullAt      time.Time
	summary     monitorSnapshot
	summaryErr  error
	haveSummary bool
	summaryAt   time.Time
}

func newSnapshotCache(collector *metricsCollector) *snapshotCache {
	return &snapshotCache{
		fullFn:    collector.collect,
		summaryFn: collector.collectSummary,
		warmupFn:  collector.needsWarmup,
	}
}

// Get returns a cached snapshot when one was published recently enough,
// otherwise it runs exactly one collection cycle for the requested depth.
// The returned snapshot is safe for concurrent readers: collection happens
// under the cache mutex and published values are treated as read-only.
func (c *snapshotCache) Get(includeProcesses bool) (monitorSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if includeProcesses {
		if c.haveFull && now.Sub(c.fullAt) < snapshotCacheTTL {
			return c.full, c.fullErr
		}
	} else if c.haveSummary && now.Sub(c.summaryAt) < snapshotCacheTTL {
		return c.summary, c.summaryErr
	}

	collect := c.summaryFn
	if includeProcesses {
		collect = c.fullFn
	}
	// The first sample only establishes CPU and network counters. Take a
	// second sample so the first view does not misleadingly show zero rates.
	warmup := c.warmupFn()
	snapshot, err := collect()
	if warmup {
		time.Sleep(120 * time.Millisecond)
		snapshot, err = collect()
	}
	if includeProcesses {
		c.full, c.fullErr, c.haveFull, c.fullAt = snapshot, err, true, now
	} else {
		c.summary, c.summaryErr, c.haveSummary, c.summaryAt = snapshot, err, true, now
	}
	return snapshot, err
}
