package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestRPCPoolReusesPersistentConnection(t *testing.T) {
	var accepted atomic.Int32
	address, cleanup := startTestRPCSSHServer(&accepted, false)
	defer cleanup()

	pool := newTestRPCPool(address)
	first, err := pool.call(
		context.Background(),
		nodeRPCRequest{Version: nodeRPCVersion, Operation: rpcSnapshot},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.call(
		context.Background(),
		nodeRPCRequest{Version: nodeRPCVersion, Operation: rpcSnapshot},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.CPUPercent != 42 || second.Snapshot.CPUPercent != 42 {
		t.Fatalf("unexpected snapshots: %#v %#v", first.Snapshot, second.Snapshot)
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("persistent pool dialed %d SSH connections, want 1", got)
	}
}

func TestRPCPoolRedialsAfterDroppedConnection(t *testing.T) {
	var accepted atomic.Int32
	address, cleanup := startTestRPCSSHServer(&accepted, true)
	defer cleanup()

	pool := newTestRPCPool(address)
	first, err := pool.call(
		context.Background(),
		nodeRPCRequest{Version: nodeRPCVersion, Operation: rpcSnapshot},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.CPUPercent != 42 {
		t.Fatalf("unexpected first snapshot: %#v", first.Snapshot)
	}

	// The server drops the connection after the first session; the next call
	// must transparently redial instead of surfacing a dead-connection error.
	second, err := pool.call(
		context.Background(),
		nodeRPCRequest{Version: nodeRPCVersion, Operation: rpcSnapshot},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.Snapshot.CPUPercent != 42 {
		t.Fatalf("unexpected second snapshot: %#v", second.Snapshot)
	}
	if got := accepted.Load(); got != 2 {
		t.Fatalf("dropped connection should redial once, connections=%d", got)
	}
}

func TestRPCClientRegistrySharesPoolsByNodeIdentity(t *testing.T) {
	registry := newRPCClientRegistry()
	node := hubNodeConfig{
		Name:                "gpu-1",
		Address:             "192.0.2.10:23234",
		IdentityFile:        "/etc/fleetty/node_rpc_ed25519",
		HostKey:             "SHA256:same",
		InsecureSkipHostKey: true,
	}
	first := registry.clientFor(node)
	second := registry.clientFor(node)
	if first != second {
		t.Fatal("equivalent node configs should share one client pool")
	}

	other := node
	other.Address = "192.0.2.11:23234"
	if third := registry.clientFor(other); third == first {
		t.Fatal("different node addresses should not share a client pool")
	}

	otherAuth := node
	otherAuth.AllowUnauthenticated = true
	if third := registry.clientFor(otherAuth); third == first {
		t.Fatal("different authentication modes should not share a client pool")
	}
}

func newTestRPCPool(address string) *rpcClientPool {
	_, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	clientSigner, err := gossh.NewSignerFromKey(clientPrivateKey)
	if err != nil {
		panic(err)
	}
	clientConfig := &gossh.ClientConfig{
		User:            nodeRPCUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // test-only server
		Timeout:         2 * time.Second,
	}
	pool := newRPCPool(hubNodeConfig{Name: "test-node", Address: address})
	pool.dial = func(ctx context.Context) (rpcSSHClient, error) {
		return gossh.Dial("tcp", address, clientConfig)
	}
	return pool
}

func startTestRPCSSHServer(accepted *atomic.Int32, dropAfterFirst bool) (string, func()) {
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		panic(err)
	}
	serverConfig := &gossh.ServerConfig{
		PublicKeyCallback: func(gossh.ConnMetadata, gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer connection.Close()
				serverConn, channels, requests, handshakeErr := gossh.NewServerConn(connection, serverConfig)
				if handshakeErr != nil {
					return
				}
				defer serverConn.Close()
				go gossh.DiscardRequests(requests)
				for newChannel := range channels {
					if newChannel.ChannelType() != "session" {
						_ = newChannel.Reject(gossh.UnknownChannelType, "only session channels are supported")
						continue
					}
					channel, channelRequests, channelErr := newChannel.Accept()
					if channelErr != nil {
						continue
					}
					go replyToTestRequests(channelRequests)
					_, _ = io.ReadAll(channel)
					_ = json.NewEncoder(channel).Encode(nodeRPCResponse{
						Version: nodeRPCVersion,
						Snapshot: monitorSnapshot{
							CollectedAt: time.Now(), CPUPercent: 42,
						},
					})
					_, _ = channel.SendRequest(
						"exit-status", false,
						gossh.Marshal(struct{ Status uint32 }{Status: 0}),
					)
					_ = channel.Close()
					if dropAfterFirst {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func replyToTestRequests(requests <-chan *gossh.Request) {
	for request := range requests {
		reply := request.Type == "exec" || request.Type == "shell" || request.Type == "pty-req"
		if request.WantReply {
			_ = request.Reply(reply, nil)
		}
	}
}
