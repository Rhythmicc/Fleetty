package main

import (
	bytebuf "bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"charm.land/wish/v2"
	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const (
	nodeRPCCommand = "_gpu_monitor_rpc"
	nodeRPCVersion = 1
)

type nodeRPCOperation string

const (
	rpcSnapshot         nodeRPCOperation = "snapshot"
	rpcAuthenticate     nodeRPCOperation = "authenticate"
	rpcProcessDetail    nodeRPCOperation = "process_detail"
	rpcTerminateProcess nodeRPCOperation = "terminate_process"
	rpcRunAction        nodeRPCOperation = "run_action"
)

type nodeRPCRequest struct {
	Version          int              `json:"version"`
	Operation        nodeRPCOperation `json:"operation"`
	Password         string           `json:"password,omitempty"`
	PID              int              `json:"pid,omitempty"`
	StartTimeTicks   uint64           `json:"start_time_ticks,omitempty"`
	ActionID         int              `json:"action_id,omitempty"`
	IncludeProcesses bool             `json:"include_processes,omitempty"`
}

type nodeRPCResponse struct {
	Version       int             `json:"version"`
	Snapshot      monitorSnapshot `json:"snapshot,omitempty"`
	ProcessDetail processDetail   `json:"process_detail,omitempty"`
	Authorized    bool            `json:"authorized,omitempty"`
	Output        string          `json:"output,omitempty"`
	Warning       string          `json:"warning,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type nodeRPCService struct {
	admin     *adminController
	backend   *localMonitorBackend
	collectMu sync.Mutex
}

func newNodeRPCService(admin *adminController) *nodeRPCService {
	return &nodeRPCService{
		admin:   admin,
		backend: newLocalMonitorBackend(admin, "hub", "node-rpc"),
	}
}

func (s *nodeRPCService) Handle(request nodeRPCRequest) nodeRPCResponse {
	if request.Version != nodeRPCVersion {
		return nodeRPCResponse{Error: fmt.Sprintf("unsupported RPC version %d", request.Version)}
	}
	switch request.Operation {
	case rpcSnapshot:
		s.collectMu.Lock()
		needsWarmup := !s.backend.collector.haveCPU || !s.backend.collector.haveNet
		collect := s.backend.collector.collectSummary
		if request.IncludeProcesses {
			collect = s.backend.collector.collect
		}
		snapshot, err := collect()
		// The first CPU/network sample establishes counters. Take a second
		// sample so the first Hub card does not misleadingly report zero.
		if needsWarmup {
			time.Sleep(120 * time.Millisecond)
			snapshot, err = collect()
		}
		s.collectMu.Unlock()
		response := nodeRPCResponse{Snapshot: snapshot}
		if err != nil {
			response.Warning = err.Error()
		}
		return response
	case rpcAuthenticate:
		return nodeRPCResponse{Authorized: s.admin.authenticate(request.Password)}
	case rpcProcessDetail:
		if !s.admin.authenticate(request.Password) {
			return nodeRPCResponse{Error: "management authentication failed"}
		}
		detail, err := s.backend.ProcessDetail(request.PID, "")
		return responseWithProcessDetail(detail, err)
	case rpcTerminateProcess:
		if !s.admin.authenticate(request.Password) {
			return nodeRPCResponse{Error: "management authentication failed"}
		}
		err := s.backend.TerminateProcess(request.PID, request.StartTimeTicks, "")
		return responseWithError(err)
	case rpcRunAction:
		if !s.admin.authenticate(request.Password) {
			return nodeRPCResponse{Error: "management authentication failed"}
		}
		output, err := s.backend.RunAction(request.ActionID, "")
		response := responseWithError(err)
		response.Output = output
		return response
	default:
		return nodeRPCResponse{Error: "unknown RPC operation"}
	}
}

func responseWithProcessDetail(detail processDetail, err error) nodeRPCResponse {
	response := nodeRPCResponse{ProcessDetail: detail}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

func responseWithError(err error) nodeRPCResponse {
	if err == nil {
		return nodeRPCResponse{}
	}
	return nodeRPCResponse{Error: err.Error()}
}

func nodeRPCMiddleware(service *nodeRPCService) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			command := sess.Command()
			if len(command) == 0 || command[0] != nodeRPCCommand {
				next(sess)
				return
			}
			var request nodeRPCRequest
			decoder := json.NewDecoder(io.LimitReader(sess, 64*1024))
			if err := decoder.Decode(&request); err != nil {
				_ = json.NewEncoder(sess).Encode(nodeRPCResponse{Error: "invalid RPC request"})
				_ = sess.Exit(1)
				return
			}
			response := service.Handle(request)
			response.Version = nodeRPCVersion
			_ = json.NewEncoder(sess).Encode(response)
		}
	}
}

type nodeRPCClient struct {
	node hubNodeConfig
}

func newNodeRPCClient(node hubNodeConfig) *nodeRPCClient {
	return &nodeRPCClient{node: node}
}

func (c *nodeRPCClient) Call(request nodeRPCRequest) (nodeRPCResponse, error) {
	request.Version = nodeRPCVersion
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.call(ctx, request)
}

func (c *nodeRPCClient) call(ctx context.Context, request nodeRPCRequest) (nodeRPCResponse, error) {
	address := normalizeNodeAddress(c.node.Address)
	hostKeyCallback, err := c.hostKeyCallback()
	if err != nil {
		return nodeRPCResponse{}, err
	}
	config := &gossh.ClientConfig{
		User:            "gpu-monitor-hub",
		HostKeyCallback: hostKeyCallback,
		Timeout:         5 * time.Second,
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nodeRPCResponse{}, fmt.Errorf("connect %s: %w", c.node.Name, err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(20 * time.Second))

	clientConnection, channels, requests, err := gossh.NewClientConn(connection, address, config)
	if err != nil {
		return nodeRPCResponse{}, fmt.Errorf("SSH handshake %s: %w", c.node.Name, err)
	}
	client := gossh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return nodeRPCResponse{}, fmt.Errorf("open RPC session %s: %w", c.node.Name, err)
	}
	defer session.Close()

	payload, err := json.Marshal(request)
	if err != nil {
		return nodeRPCResponse{}, err
	}
	session.Stdin = bytebuf.NewReader(payload)
	output, err := session.Output(nodeRPCCommand)
	if err != nil {
		return nodeRPCResponse{}, fmt.Errorf("RPC %s: %w", c.node.Name, err)
	}
	var response nodeRPCResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nodeRPCResponse{}, fmt.Errorf("decode RPC response from %s: %w", c.node.Name, err)
	}
	if response.Version != nodeRPCVersion {
		return nodeRPCResponse{}, fmt.Errorf("node %s uses incompatible RPC version %d", c.node.Name, response.Version)
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (c *nodeRPCClient) hostKeyCallback() (gossh.HostKeyCallback, error) {
	expected := strings.TrimSpace(c.node.HostKey)
	if expected == "" {
		if c.node.InsecureSkipHostKey {
			return gossh.InsecureIgnoreHostKey(), nil //nolint:gosec
		}
		return nil, fmt.Errorf("node %s has no host_key fingerprint", c.node.Name)
	}
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		actual := gossh.FingerprintSHA256(key)
		if actual != expected {
			return fmt.Errorf("host key mismatch for %s: got %s", c.node.Name, actual)
		}
		return nil
	}, nil
}

func normalizeNodeAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return net.JoinHostPort("127.0.0.1", "23234")
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "23234")
}
