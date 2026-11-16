package main

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/x/ansi"
)

func TestHubModelReadsSharedServiceSnapshot(t *testing.T) {
	service := newHubService(hubConfig{Nodes: []hubNodeConfig{{
		Name: "unreachable", Address: "192.0.2.1:23234",
	}}})
	service.states[0] = hubNodeState{Snapshot: monitorSnapshot{
		CollectedAt: time.Now(), CPUPercent: 42,
	}}

	model := newHubModel(service, 100, 30)
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

func TestHubLoginDefaultsToNodeInspector(t *testing.T) {
	service := newHubService(hubConfig{
		Nodes: []hubNodeConfig{{Name: "worker"}},
	})
	model := newHubModel(service, 120, 30)
	model.Init()
	if !strings.Contains(ansi.Strip(model.hubView()), "NODE · worker") {
		t.Fatal("wide login should show the selected node's inspector immediately")
	}
	queueOnly := newHubModel(newHubService(hubConfig{SlurmClusters: []slurmClusterConfig{{Name: "CPU"}}}), 120, 30)
	if !queueOnly.queueExpanded {
		t.Fatal("a queue-only Hub should still open the queue")
	}
}

func TestHubViewerCountTracksConcurrentPanels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newHubService(hubConfig{
			Name:          "Test Fleet",
			Nodes:         []hubNodeConfig{{Name: "worker"}},
			SlurmClusters: []slurmClusterConfig{{Name: "GPU"}},
		})
		model := newHubModel(service, 120, 30)
		startPanel := func() (closePanel func()) {
			started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			handle := service.trackPanel(func(ssh.Session) {
				close(started)
				<-stop
			})
			go func() {
				defer close(done)
				handle(nil)
			}()
			<-started
			return func() {
				close(stop)
				<-done
			}
		}
		check := func(want string) {
			t.Helper()
			for _, expanded := range []bool{false, true} {
				model.queueExpanded = expanded
				view := model.hubView()
				line := strings.SplitN(ansi.Strip(view), "\n", 2)[0]
				if !strings.Contains(line, " "+want) || lipgloss.Width(line) > model.width {
					t.Errorf("header = %q, want %s within %d columns", line, want, model.width)
				}
			}
		}
		closeFirst := startPanel()
		check("1 VIEWER")
		closeSecond := startPanel()
		check("2 VIEWERS")
		closeFirst()
		check("1 VIEWER")
		closeSecond()
		check("0 VIEWERS")
	})
}

func TestHubRetryWakesSharedCollector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		service := newHubService(hubConfig{Nodes: []hubNodeConfig{{Name: "offline"}}})
		calls := 0
		go service.runNode(ctx, 0, func(context.Context, hubNodeConfig) (nodeRPCResponse, error) {
			calls++
			if calls <= 3 {
				return nodeRPCResponse{}, context.DeadlineExceeded
			}
			return nodeRPCResponse{Snapshot: monitorSnapshot{CollectedAt: time.Now()}}, nil
		})
		time.Sleep(2*time.Second + time.Millisecond)
		synctest.Wait()
		states, _ := service.snapshot()
		if state := states[0]; state.Error == "" || !state.NextRetry.After(time.Now()) {
			t.Fatalf("node did not enter backoff after three failed attempts: %#v", state)
		}
		service.retryOfflineNow()
		synctest.Wait()
		states, _ = service.snapshot()
		if state := states[0]; state.Error != "" || state.Delay != "" || !state.NextRetry.IsZero() || state.Snapshot.CollectedAt.IsZero() || calls != 4 {
			t.Fatalf("manual retry did not recover the node immediately after %d attempts: %#v", calls, state)
		}
		select {
		case <-service.slurmWake:
		default:
			t.Fatal("manual retry should also wake the Slurm collector")
		}
	})
}

func TestHubSlowNodeDoesNotBlockPeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		service := newHubService(hubConfig{Nodes: []hubNodeConfig{{Name: "fast"}, {Name: "slow"}}})
		fastCalls, slowCalls := 0, 0
		started := time.Now()
		go service.runNode(ctx, 0, func(ctx context.Context, _ hubNodeConfig) (nodeRPCResponse, error) {
			fastCalls++
			select {
			case <-time.After(900 * time.Millisecond):
				return nodeRPCResponse{Snapshot: monitorSnapshot{CollectedAt: time.Now()}}, nil
			case <-ctx.Done():
				return nodeRPCResponse{}, ctx.Err()
			}
		})
		go service.runNode(ctx, 1, func(ctx context.Context, _ hubNodeConfig) (nodeRPCResponse, error) {
			slowCalls++
			if slowCalls == 1 {
				select {
				case <-time.After(1400 * time.Millisecond):
					return nodeRPCResponse{Snapshot: monitorSnapshot{CollectedAt: time.Now(), CPUPercent: 42}}, nil
				case <-ctx.Done():
					return nodeRPCResponse{}, ctx.Err()
				}
			}
			<-ctx.Done()
			return nodeRPCResponse{}, ctx.Err()
		})
		time.Sleep(time.Second)
		synctest.Wait()
		states, _ := service.snapshot()
		if states[0].Checked.IsZero() || !states[1].Checked.IsZero() {
			t.Fatal("fast result must publish while the slow request is pending")
		}
		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()
		states, _ = service.snapshot()
		if got := states[0].Checked.Sub(started); got != 3900*time.Millisecond || fastCalls != 5 {
			t.Fatalf("fast node lost its one-second cadence: last response %s, %d calls", got, fastCalls)
		}
		slow := states[1]
		if slow.Error != "" || slow.Delay != context.DeadlineExceeded.Error() || slow.Snapshot.CPUPercent != 42 ||
			slow.Latency != hubOverviewRPCTimeout || slowCalls != 3 || len(slow.Spark) != 1 {
			t.Fatalf("slow node should keep its 1.4s sample after one timeout: %#v", slow)
		}
	})
}

func TestHubNodeBackoffAndRecovery(t *testing.T) {
	tick := time.Unix(1000, 0)
	snapshot := nodeRPCResponse{Snapshot: monitorSnapshot{CollectedAt: tick, CPUPercent: 32}}
	state := updateHubNodeState(hubNodeState{}, snapshot, nil, tick, tick, time.Second)
	for failures := 1; failures <= 4; failures++ {
		at := tick.Add(time.Duration(failures) * time.Second)
		state = updateHubNodeState(state, nodeRPCResponse{}, context.DeadlineExceeded, at, at, time.Second)
		if state.Snapshot.CPUPercent != 32 || !state.LastSeen.Equal(tick) || len(state.Spark) != 1 {
			t.Fatal("a failed attempt overwrote the last successful sample")
		}
		if failures < 3 {
			if state.Error != "" || state.Delay == "" || !state.NextRetry.IsZero() {
				t.Fatalf("failure %d should show stale data and keep polling", failures)
			}
		} else if want := 5 * time.Second * time.Duration(failures-2); state.Error == "" || state.Delay != "" || state.NextRetry.Sub(at) != want {
			t.Fatalf("failure %d should back off for %s", failures, want)
		}
	}
	recovered := tick.Add(15 * time.Second)
	snapshot.Snapshot.CPUPercent = 64
	state = updateHubNodeState(state, snapshot, nil, recovered, recovered, time.Second)
	if state.Error != "" || state.Delay != "" || !state.NextRetry.IsZero() || state.ConsecutiveFailures != 0 ||
		state.Snapshot.CPUPercent != 64 || !state.LastSeen.Equal(recovered) || len(state.Spark) != 2 {
		t.Fatalf("successful response did not clear the failure state: %#v", state)
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
