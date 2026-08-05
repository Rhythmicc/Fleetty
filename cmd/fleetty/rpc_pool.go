package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	bytebuf "bytes"

	gossh "golang.org/x/crypto/ssh"
)

const (
	rpcDialTimeout  = 5 * time.Second
	rpcPoolIdleTime = 2 * time.Minute
)

// rpcSSHClient is the small surface of an SSH client that the RPC pool needs;
// *ssh.Client implements it and tests can substitute a fake.
type rpcSSHClient interface {
	NewSession() (*gossh.Session, error)
	Close() error
}

// rpcClientPool keeps one long-lived SSH connection per Hub node. RPC calls
// are serialized per node so request deadlines can interrupt session reads
// without racing concurrent use of the same connection; across nodes calls
// still run in parallel. A dead connection is dropped and redialed once.
type rpcClientPool struct {
	node    hubNodeConfig
	address string
	dial    func(context.Context) (rpcSSHClient, error)

	mu       sync.Mutex
	client   rpcSSHClient
	lastUsed time.Time
}

func newRPCPool(node hubNodeConfig) *rpcClientPool {
	pool := &rpcClientPool{
		node:    node,
		address: normalizeNodeAddress(node.Address),
	}
	pool.dial = pool.dialLocked
	return pool
}

func (p *rpcClientPool) call(ctx context.Context, request nodeRPCRequest) (nodeRPCResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil && time.Since(p.lastUsed) > rpcPoolIdleTime {
		_ = p.client.Close()
		p.client = nil
	}
	p.lastUsed = time.Now()

	client, err := p.ensureClient(ctx)
	if err != nil {
		return nodeRPCResponse{}, err
	}
	response, err, drop := p.callWithClient(ctx, client, request)
	if !drop {
		return response, err
	}
	// The connection may have died between polls. Drop it and retry once with
	// a fresh dial so transient node restarts do not surface to every session.
	_ = client.Close()
	p.client = nil
	if ctx.Err() != nil {
		return response, err
	}
	fresh, dialErr := p.ensureClient(ctx)
	if dialErr != nil {
		return nodeRPCResponse{}, dialErr
	}
	response, err, drop = p.callWithClient(ctx, fresh, request)
	if drop {
		_ = fresh.Close()
		p.client = nil
	}
	return response, err
}

func (p *rpcClientPool) ensureClient(ctx context.Context) (rpcSSHClient, error) {
	if p.client != nil {
		return p.client, nil
	}
	client, err := p.dial(ctx)
	if err != nil {
		return nil, err
	}
	p.client = client
	return client, nil
}

// callWithClient runs one RPC round trip. The third return value reports
// whether the failure is transport-level (session/connection) and the pooled
// connection should be discarded; RPC-level errors keep the connection alive.
func (p *rpcClientPool) callWithClient(
	ctx context.Context,
	client rpcSSHClient,
	request nodeRPCRequest,
) (nodeRPCResponse, error, bool) {
	session, err := client.NewSession()
	if err != nil {
		return nodeRPCResponse{}, err, true
	}
	defer session.Close()

	payload, err := json.Marshal(request)
	if err != nil {
		return nodeRPCResponse{}, err, false
	}
	session.Stdin = bytebuf.NewReader(payload)

	type outputResult struct {
		output []byte
		err    error
	}
	done := make(chan outputResult, 1)
	go func() {
		output, outputErr := session.Output(nodeRPCCommand)
		done <- outputResult{output: output, err: outputErr}
	}()
	var output []byte
	select {
	case <-ctx.Done():
		// Closing the session is not guaranteed to interrupt a stalled remote
		// read; closing the whole connection is, and the pool will redial.
		_ = session.Close()
		_ = client.Close()
		return nodeRPCResponse{}, ctx.Err(), true
	case result := <-done:
		if result.err != nil {
			return nodeRPCResponse{}, result.err, true
		}
		output = result.output
	}

	var response nodeRPCResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nodeRPCResponse{}, fmt.Errorf("decode RPC response from %s: %w", p.node.Name, err), true
	}
	if response.Version != nodeRPCVersion {
		return response, fmt.Errorf(
			"node %s uses incompatible RPC version %d", p.node.Name, response.Version,
		), false
	}
	if response.Error != "" {
		return response, errors.New(response.Error), false
	}
	return response, nil, false
}

func (p *rpcClientPool) dialLocked(ctx context.Context) (rpcSSHClient, error) {
	hostKeyCallback, err := fixedHostKeyCallback(
		p.node.Name, p.node.HostKey, p.node.InsecureSkipHostKey,
	)
	if err != nil {
		return nil, err
	}
	auth, err := p.authMethods()
	if err != nil {
		return nil, err
	}
	config := &gossh.ClientConfig{
		User:            nodeRPCUser,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         rpcDialTimeout,
	}
	dialer := net.Dialer{Timeout: rpcDialTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", p.address)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", p.node.Name, err)
	}
	// The handshake must not outlive the request context. Once established,
	// the connection is long-lived, so the deadline is cleared.
	deadline := time.Now().Add(rpcDialTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	clientConnection, channels, requests, err := gossh.NewClientConn(connection, p.address, config)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("SSH handshake %s: %w", p.node.Name, err)
	}
	_ = connection.SetDeadline(time.Time{})
	return gossh.NewClient(clientConnection, channels, requests), nil
}

func (p *rpcClientPool) authMethods() ([]gossh.AuthMethod, error) {
	if p.node.IdentityFile == "" {
		if p.node.AllowUnauthenticated {
			return nil, nil
		}
		return nil, fmt.Errorf("node %s has no RPC identity_file", p.node.Name)
	}
	signer, err := loadPrivateKeySigner(p.node.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("load RPC identity for %s: %w", p.node.Name, err)
	}
	return []gossh.AuthMethod{gossh.PublicKeys(signer)}, nil
}

// rpcClientRegistry shares one connection pool per node identity across the
// Hub overview and every open detail session.
type rpcClientRegistry struct {
	mu      sync.Mutex
	clients map[string]*nodeRPCClient
}

func newRPCClientRegistry() *rpcClientRegistry {
	return &rpcClientRegistry{clients: make(map[string]*nodeRPCClient)}
}

func (r *rpcClientRegistry) clientFor(node hubNodeConfig) *nodeRPCClient {
	key := node.rpcPoolKey()
	r.mu.Lock()
	defer r.mu.Unlock()
	if client, ok := r.clients[key]; ok {
		return client
	}
	client := newNodeRPCClient(node)
	r.clients[key] = client
	return client
}

func (n hubNodeConfig) rpcPoolKey() string {
	return strings.Join([]string{
		normalizeNodeAddress(n.Address),
		strings.TrimSpace(n.IdentityFile),
		strings.TrimSpace(n.HostKey),
		strconv.FormatBool(n.InsecureSkipHostKey),
		strconv.FormatBool(n.AllowUnauthenticated),
	}, "\x00")
}

var sharedRPCClientRegistry = newRPCClientRegistry()
