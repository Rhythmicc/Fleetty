package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
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
	rpcHistory          nodeRPCOperation = "history"
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
	HistoryMinutes   int              `json:"history_minutes,omitempty"`
}

type nodeRPCResponse struct {
	Version       int             `json:"version"`
	Snapshot      monitorSnapshot `json:"snapshot,omitempty"`
	ProcessDetail processDetail   `json:"process_detail,omitempty"`
	Authorized    bool            `json:"authorized,omitempty"`
	Output        string          `json:"output,omitempty"`
	Warning       string          `json:"warning,omitempty"`
	Error         string          `json:"error,omitempty"`
	History       []historySample `json:"history,omitempty"`
}

type nodeRPCService struct {
	admin   *adminController
	backend *localMonitorBackend
	cache   *snapshotCache
	history *historyStore
}

func newNodeRPCService(admin *adminController, cache *snapshotCache, history *historyStore) *nodeRPCService {
	return &nodeRPCService{
		admin:   admin,
		backend: newLocalMonitorBackend(admin, cache, history, "hub", "node-rpc"),
		cache:   cache,
		history: history,
	}
}

func (s *nodeRPCService) Handle(request nodeRPCRequest) nodeRPCResponse {
	if request.Version != nodeRPCVersion {
		return nodeRPCResponse{Error: fmt.Sprintf("unsupported RPC version %d", request.Version)}
	}
	switch request.Operation {
	case rpcSnapshot:
		snapshot, err := s.cache.Get(request.IncludeProcesses)
		snapshot.ManagementActions = s.admin.actionInfo()
		response := nodeRPCResponse{Snapshot: snapshot}
		if err != nil {
			response.Warning = err.Error()
		}
		return response
	case rpcHistory:
		if s.history == nil {
			return nodeRPCResponse{Error: "history persistence is disabled"}
		}
		return nodeRPCResponse{History: s.history.Recent(request.HistoryMinutes)}
	case rpcAuthenticate:
		return nodeRPCResponse{Authorized: s.admin.authenticate(request.Password)}
	case rpcProcessDetail:
		detail, err := s.backend.ProcessDetail(request.PID, request.Password)
		return responseWithProcessDetail(detail, err)
	case rpcTerminateProcess:
		if !s.admin.authenticate(request.Password) {
			return nodeRPCResponse{Error: "management authentication failed"}
		}
		err := s.backend.TerminateProcess(request.PID, request.StartTimeTicks, request.Password)
		return responseWithError(err)
	case rpcRunAction:
		if !s.admin.authenticate(request.Password) {
			return nodeRPCResponse{Error: "management authentication failed"}
		}
		output, err := s.backend.RunAction(request.ActionID, request.Password)
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
	pool *rpcClientPool
}

func newNodeRPCClient(node hubNodeConfig) *nodeRPCClient {
	return &nodeRPCClient{node: node, pool: newRPCPool(node)}
}

func (c *nodeRPCClient) Call(request nodeRPCRequest) (nodeRPCResponse, error) {
	return c.CallWithTimeout(request, 20*time.Second)
}

func (c *nodeRPCClient) CallWithTimeout(request nodeRPCRequest, timeout time.Duration) (nodeRPCResponse, error) {
	request.Version = nodeRPCVersion
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.pool.call(ctx, request)
}

func fixedHostKeyCallback(name, fingerprint string, insecure bool) (gossh.HostKeyCallback, error) {
	return fixedHostKeysCallback(name, []string{fingerprint}, insecure)
}

func fixedHostKeysCallback(name string, fingerprints []string, insecure bool) (gossh.HostKeyCallback, error) {
	expected := stringSet(fingerprints)
	if len(expected) == 0 {
		if insecure {
			return gossh.InsecureIgnoreHostKey(), nil //nolint:gosec
		}
		return nil, fmt.Errorf("%s has no host_key fingerprint", name)
	}
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		actual := gossh.FingerprintSHA256(key)
		if _, ok := expected[actual]; !ok {
			return fmt.Errorf("host key mismatch for %s: got %s", name, actual)
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
