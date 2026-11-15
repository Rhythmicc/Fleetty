package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/x/ansi"
	gossh "golang.org/x/crypto/ssh"
)

func TestHubLinksMatchOnlyUnambiguousObservedPeers(t *testing.T) {
	nodes := []hubNodeConfig{
		{Name: "selected", Address: "192.0.2.1"},
		{Name: "storage", Address: "192.0.2.2:23234"},
		{Name: "worker", Address: "[2001:db8::3]:23234"},
		{Name: "alias", Address: "worker.example"},
		{Name: "shared-a", Address: "192.0.2.4"},
		{Name: "shared-b", Address: "192.0.2.4:1234"},
		{Name: "local", Address: "127.0.0.1:23234"},
	}
	socket := func(local, remote, proto, state string, known bool, rx, tx uint64) networkConnectionInfo {
		return networkConnectionInfo{Local: local, Remote: remote, Protocol: proto, State: state, RateAvailable: known, RX: rx, TX: tx}
	}
	const local = "192.0.2.1:123"
	peers := hubPeerLinks(nodes, 0, []networkConnectionInfo{
		socket(local, "192.0.2.2:2049", "TCP", "ESTABLISHED", true, 200, 10),
		socket(local, "[::ffff:192.0.2.2]:43", "TCP6", "ESTABLISHED", true, 100, 20),
		socket(local, "192.0.2.2:1", "UDP", "UNCONN", false, 9, 9),
		socket(local, "[2001:db8::3]:450", "TCP6", "ESTABLISHED", false, 0, 0),
		socket(local, "192.0.2.1:12", "TCP", "ESTABLISHED", true, 99, 99),
		socket(local, "192.0.2.4:12", "TCP", "ESTABLISHED", true, 99, 99),
		socket(local, "192.0.2.99:12", "TCP", "ESTABLISHED", true, 99, 99),
		socket(local, "192.0.2.2:12", "TCP", "TIME_WAIT", true, 99, 99),
		socket(local, "192.0.2.2:23234", "TCP", "ESTABLISHED", true, 99, 99),
		socket("192.0.2.1:23234", "192.0.2.2:12", "TCP", "ESTABLISHED", true, 99, 99),
		socket(local, "127.0.0.1:12", "TCP", "ESTABLISHED", true, 99, 99),
		socket("127.0.0.1:22", "192.0.2.2:12", "TCP", "ESTABLISHED", true, 99, 99),
	})
	if len(peers) != 2 || peers[0] != (hubPeerLink{node: 1, sockets: 3, sampled: 2, rx: 300, tx: 30}) ||
		peers[1] != (hubPeerLink{node: 2, sockets: 1}) {
		t.Fatalf("wrong peer links: %#v", peers)
	}
}

func TestHubLinksCombineReportsWithoutCountingBothEnds(t *testing.T) {
	now := time.Now()
	graph := mergeHubLinks([]hubLinkObservation{
		{node: 0, available: true, sampledAt: now, peers: []hubPeerLink{
			{node: 14, sockets: 2, sampled: 2, rx: 100, tx: 200},
		}},
		{node: 14, available: true, sampledAt: now.Add(time.Second), peers: []hubPeerLink{
			{node: 0, sockets: 2, sampled: 1, rx: 202, tx: 101},
			{node: 1, sockets: 1},
		}},
		{node: 15, available: true, sampledAt: now, peers: []hubPeerLink{
			{node: 2, sockets: 3, sampled: 2, rx: 500, tx: 900},
		}},
		{node: 13, available: true},
		{node: 9},
	})
	want := []hubLinkEdge{
		{a: 2, b: 15, ab: 500, ba: 900, known: true, partial: true},
		{a: 0, b: 14, ab: 200, ba: 100, known: true},
	}
	if graph.sampled != 4 || len(graph.edges) != len(want) || graph.unmeasured != 1 {
		t.Fatalf("wrong graph coverage or edge count: %+v", graph)
	}
	for i, edge := range want {
		if graph.edges[i] != edge {
			t.Fatalf("edge %d: got %+v want %+v", i, graph.edges[i], edge)
		}
	}
}

func TestHubLinksKeepOnlyTrafficAndCountUnmeasured(t *testing.T) {
	graph := mergeHubLinks([]hubLinkObservation{{
		node: 0, available: true, peers: []hubPeerLink{
			{node: 1, sockets: 1, sampled: 1},
			{node: 2, sockets: 2, sampled: 1}, // partial report, but both measured rates are zero
			{node: 3, sockets: 1, sampled: 1, tx: 1},
			{node: 4, sockets: 1, sampled: 1, rx: 1},
			{node: 5, sockets: 1}, // unknown rates are not zero
		},
	}})
	if len(graph.edges) != 2 || graph.edges[0].b != 3 || graph.edges[1].b != 4 || graph.unmeasured != 1 {
		t.Fatalf("unexpected visible pairs: %+v", graph.edges)
	}
}

func TestHubLinksShowAllNodePairsRegardlessOfSelection(t *testing.T) {
	m := compactTestHub(140, 30)
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !m.linksView {
		t.Fatal("n did not switch to peer links")
	}
	m.links = hubLinkGraph{sampledAt: time.Now(), eligible: 15, sampled: 14, unmeasured: 1, edges: []hubLinkEdge{
		{a: 0, b: 14, ab: 1 << 20, ba: 1, known: true, partial: true},
		{a: 1, b: 15, ab: 12000, ba: 16, known: true},
		{a: 13, b: 14, ab: 1, known: true},
	}}
	for _, cursor := range []int{0, 1, 12} {
		m.cursor = cursor
		view := ansi.Strip(m.hubView())
		for _, want := range []string{"CLUSTER LINKS", "14/15 observed", "A100 ↔ NAS", "4090 ↔ ssslab-login-1", "≥1M      ≥1", "1 unmeasured"} {
			if !strings.Contains(view, want) {
				t.Fatalf("cursor %d: missing %q: %s", cursor, want, view)
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 8})
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if view := ansi.Strip(m.hubView()); !strings.Contains(view, "intel9462 ↔ NAS") || !strings.Contains(view, "3–3 / 3") {
		t.Fatalf("cannot page to the final link: %s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 72, Height: 30})
	if !strings.Contains(ansi.Strip(m.hubView()), "CLUSTER LINKS") {
		t.Fatal("an explicitly opened link list should stay open on resize")
	}
	m.Update(testKey("n"))
	if strings.Contains(ansi.Strip(m.hubView()), "CLUSTER LINKS") {
		t.Fatal("n should return from narrow links to the rack")
	}
}

func TestHubLinkRatesStayInFixedColumns(t *testing.T) {
	m := compactTestHub(140, 30)
	m.links = hubLinkGraph{sampledAt: time.Now(), eligible: 1, sampled: 1}
	cases := []struct {
		link hubLinkEdge
		want string
	}{
		{hubLinkEdge{known: true, partial: true, ab: 1 << 20, ba: 778 << 10}, "   ≥1M   ≥778K"},
		{hubLinkEdge{known: true, ab: 9 << 10, ba: 1 << 10}, "    9K      1K"},
		{hubLinkEdge{known: true, ab: 824, ba: 261}, "   824     261"},
		{hubLinkEdge{known: true, ab: 1}, "     1       0"},
		{hubLinkEdge{known: true, partial: true, ab: (1 << 20) - 1, ba: ^uint64(0)}, "≥1024K    ≥16E"},
	}
	for i, test := range cases {
		test.link.a, test.link.b = i, 14
		m.links.edges = append(m.links.edges, test.link)
	}
	for _, width := range []int{32, 48} {
		rows := m.renderHubLinks(width)
		for i, test := range cases {
			row := rows[i+2]
			if lipgloss.Width(row) != width || !strings.HasSuffix(ansi.Strip(row), test.want) {
				t.Errorf("width %d, row %d: got %q, want suffix %q", width, i, ansi.Strip(row), test.want)
			}
		}
	}
}

func TestHubLinksStackLabelsInsteadOfHidingEndpoints(t *testing.T) {
	m := compactTestHub(30, 14)
	m.linksView, m.linksExplicit = true, true
	m.links = hubLinkGraph{sampledAt: time.Now(), eligible: 2, sampled: 2, edges: []hubLinkEdge{
		{a: 1, b: 15, ab: 12000, ba: 16, known: true},
	}}
	view := ansi.Strip(m.hubView())
	if !strings.Contains(view, "4090 ↔ ssslab-login-1") ||
		!strings.Contains(view, "A → B") || !strings.Contains(view, "B → A") ||
		!strings.Contains(view, "12K      16") || lipgloss.Width(view) > m.width {
		t.Fatalf("narrow layout should keep the full pair and both rates: %q", view)
	}
}

func TestHubLinksViewsShareDemandAndCache(t *testing.T) {
	a, b := compactTestHub(140, 30), compactTestHub(140, 30)
	service := newHubService(a.config)
	a.service, b.service = service, service
	a.refreshHubLinks()
	if len(service.linksWake) != 0 {
		t.Fatal("closed links panel started sampling")
	}
	for _, m := range []*hubModel{a, b} {
		m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	}
	if len(service.linksWake) != 1 {
		t.Fatal("multiple views should wake one shared sampler")
	}
	<-service.linksWake
	service.links = hubLinkGraph{sampledAt: time.Now(), eligible: 1, sampled: 1}
	for _, m := range []*hubModel{a, b} {
		m.refreshHubLinks()
		if m.links.sampled != 1 {
			t.Fatal("open view did not receive the shared graph")
		}
	}
	if len(service.linksWake) != 0 {
		t.Fatal("another viewer should not restart active sampling")
	}
}

func TestHubLinksFollowConfiguredCadenceDespiteBlockedCompute(t *testing.T) {
	for _, seconds := range []int{1, 4} {
		t.Run(fmt.Sprintf("%ds", seconds), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				nodes := make([]hubNodeConfig, hubLinkComputeSlots)
				nodes = append(nodes, hubNodeConfig{Name: "NAS", Profile: "nas"}, hubNodeConfig{Name: "login", Address: "127.0.0.1"})
				service := newHubService(hubConfig{Nodes: nodes, RefreshSeconds: seconds})
				for i := range service.states {
					service.states[i].Snapshot.CollectedAt = time.Now()
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				interval := service.config.refreshInterval()
				start := time.Now()
				calls := make(chan int, 32)
				service.linksForView()
				go service.runLinkSampler(ctx, func(ctx context.Context, index int) hubLinkObservation {
					calls <- index
					if index < hubLinkComputeSlots {
						// Fill every compute slot and keep them blocked across ticks.
						<-ctx.Done()
						return hubLinkObservation{}
					}
					observation := hubLinkObservation{node: index, available: true}
					if index == hubLinkComputeSlots {
						observation.peers = []hubPeerLink{{
							node: index + 1, sockets: 1, sampled: 1,
							tx: uint64(1 + time.Since(start)/interval),
						}}
					}
					return observation
				}, func() {})
				for tick := 0; tick < 4; tick++ {
					synctest.Wait()
					seen := make(map[int]int)
					for len(calls) > 0 {
						seen[<-calls]++
					}
					if seen[hubLinkComputeSlots] != 1 || seen[hubLinkComputeSlots+1] != 1 ||
						(tick > 0 && len(seen) != 2) || (tick == 0 && len(seen) != hubLinkTotalSlots) {
						t.Fatalf("tick %d: expected one call per service node, got %v", tick, seen)
					}
					graph := service.linksForView()
					if graph.sampled != 2 || len(graph.edges) != 1 || graph.edges[0].ab != uint64(tick+1) {
						t.Fatalf("tick %d: newest service-node rate missing: %+v", tick, graph)
					}
					if tick < 3 {
						time.Sleep(interval - time.Millisecond)
						synctest.Wait()
						if len(calls) != 0 {
							t.Fatal("sample started before its configured deadline")
						}
						time.Sleep(time.Millisecond)
					}
				}
			})
		})
	}
}

func TestHubLinksStopSamplingWhenViewsClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newHubService(hubConfig{Nodes: []hubNodeConfig{{Name: "node"}}})
		service.states[0].Snapshot.CollectedAt = time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := make(chan struct{}, 10)
		go service.runLinkSampler(ctx, func(_ context.Context, index int) hubLinkObservation {
			calls <- struct{}{}
			return hubLinkObservation{node: index, available: true}
		}, func() {})
		synctest.Wait()
		if len(calls) != 0 {
			t.Fatal("no viewers should mean no requests")
		}
		service.linksForView()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if len(calls) != 2 {
			t.Fatalf("got %d calls without view renewals; want two before the lease expires", len(calls))
		}
	})
}

func TestHubLinksReadsExistingNodeRPC(t *testing.T) {
	host := localAccessTestSigner(t)
	// Generate an on-disk test identity for the actual RPC dialer.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := gossh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(t.TempDir(), "rpc_key")
	if err := os.WriteFile(identity, pem.EncodeToMemory(private), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := monitorSnapshot{
		CollectedAt: time.Now(), NetworkConnectionMode: true,
		NetworkConnections: []networkConnectionInfo{{
			Local: "192.0.2.11:49152", Remote: "192.0.2.12:2049", State: "ESTABLISHED",
			Protocol: "TCP", RateAvailable: true, RX: 2 << 20, TX: 300,
		}},
	}
	cache := &snapshotCache{fullFn: func() (monitorSnapshot, error) { return snapshot, nil }, warmupFn: func() bool { return false }}
	rpc := newNodeRPCService(&adminController{}, cache, nil)
	server := &ssh.Server{HostSigners: []ssh.Signer{host}, Handler: nodeRPCMiddleware(rpc)(func(ssh.Session) {})}
	access := sshAccessConfig{rpcKeys: map[string]struct{}{gossh.FingerprintSHA256(signer.PublicKey()): {}}, connectionLimit: 4}
	for _, option := range access.options() {
		if err := option(server); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	service := newHubService(hubConfig{Nodes: []hubNodeConfig{
		{Name: "local", Address: listener.Addr().String(), IdentityFile: identity, HostKey: gossh.FingerprintSHA256(host.PublicKey())},
		{Name: "peer", Address: "192.0.2.12:23234"},
	}})
	pool := newRPCPool(service.config.Nodes[0])
	first := fetchHubLinkObservation(context.Background(), service.config.Nodes, 0, pool)
	client := pool.client
	second := fetchHubLinkObservation(context.Background(), service.config.Nodes, 0, pool)
	if client != nil {
		defer client.Close()
	}
	graph := mergeHubLinks([]hubLinkObservation{second})
	if !first.available || client == nil || pool.client != client ||
		graph.sampled != 1 || len(graph.edges) != 1 ||
		graph.edges[0] != (hubLinkEdge{a: 0, b: 1, ab: 300, ba: 2 << 20, known: true}) {
		t.Fatalf("unexpected peer graph: %+v", graph)
	}
}
