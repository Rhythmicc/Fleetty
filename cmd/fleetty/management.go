package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/log/v2"
)

const (
	privilegedProtocolVersion = 1
	maxPrivilegedMessageSize  = 16 * 1024
)

type managementOperation string

const (
	managementRestartService   managementOperation = "restart_service"
	managementRebootHost       managementOperation = "reboot_host"
	managementTerminateProcess managementOperation = "terminate_process"
)

type privilegedRequest struct {
	Version        int                 `json:"version"`
	Operation      managementOperation `json:"operation"`
	Target         string              `json:"target,omitempty"`
	PID            int                 `json:"pid,omitempty"`
	StartTimeTicks uint64              `json:"start_time_ticks,omitempty"`
}

type privilegedResponse struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type privilegedClient struct {
	socket string
}

func privilegedSocketPath() string {
	return strings.TrimSpace(os.Getenv("FLEETTY_PRIVILEGED_SOCKET"))
}

func newPrivilegedClient(socket string) *privilegedClient {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return nil
	}
	return &privilegedClient{socket: socket}
}

func (c *privilegedClient) Execute(ctx context.Context, request privilegedRequest) (string, error) {
	if c == nil || c.socket == "" {
		return "", errors.New("privileged helper is unavailable")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return "", fmt.Errorf("connect privileged helper: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(15 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	request.Version = privilegedProtocolVersion
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return "", fmt.Errorf("send privileged request: %w", err)
	}
	var response privilegedResponse
	if err := json.NewDecoder(io.LimitReader(connection, maxPrivilegedMessageSize)).Decode(&response); err != nil {
		return "", fmt.Errorf("read privileged response: %w", err)
	}
	if response.Error != "" {
		return response.Output, errors.New(response.Error)
	}
	return response.Output, nil
}

func executeLocalManagement(ctx context.Context, request privilegedRequest) (string, error) {
	switch request.Operation {
	case managementRestartService:
		if request.Target != "fleetty.service" {
			return "", errors.New("service is not allowlisted")
		}
		args := []string{"restart", request.Target}
		scope := strings.ToLower(strings.TrimSpace(os.Getenv("FLEETTY_INSTALL_SCOPE")))
		if scope == "user" || scope == "" && os.Geteuid() != 0 {
			args = append([]string{"--user"}, args...)
		}
		return runStructuredCommand(ctx, "systemctl", args...)
	case managementRebootHost:
		if os.Geteuid() != 0 {
			return "", errors.New("reboot requires the privileged helper")
		}
		return runStructuredCommand(ctx, "systemctl", "reboot")
	default:
		return "", errors.New("management operation is not supported locally")
	}
}

func runStructuredCommand(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return "", errors.New("operation timed out")
	}
	return string(output), err
}

func runPrivilegedHelperCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("privileged-helper", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socketPath := flags.String("socket", "/run/fleetty/privileged.sock", "Unix socket path")
	socketGroup := flags.String("group", "fleetty", "group allowed to connect")
	service := flags.String("service", "fleetty.service", "single service allowed to restart")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("privileged-helper does not accept positional arguments")
	}
	if runtime.GOOS != "linux" {
		return errors.New("privileged-helper is supported only on Linux")
	}
	if os.Geteuid() != 0 {
		return errors.New("privileged-helper must run as root")
	}
	if !filepath.IsAbs(*socketPath) {
		return errors.New("privileged helper socket must be an absolute path")
	}
	if *service != "fleetty.service" {
		return errors.New("only fleetty.service may be allowlisted")
	}
	group, err := user.LookupGroup(*socketGroup)
	if err != nil {
		return fmt.Errorf("lookup socket group: %w", err)
	}
	groupID, err := strconv.Atoi(group.Gid)
	if err != nil {
		return errors.New("socket group has an invalid id")
	}
	return servePrivilegedHelper(*socketPath, groupID, *service, stdout)
}

func servePrivilegedHelper(socketPath string, groupID int, allowedService string, stdout io.Writer) error {
	if err := preparePrivilegedSocketPath(socketPath); err != nil {
		return err
	}
	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chown(socketPath, 0, groupID); err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Fleetty privileged helper listening on %s\n", socketPath)

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(done)
	go func() {
		<-done
		_ = listener.Close()
	}()
	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return acceptErr
		}
		go handlePrivilegedConnection(connection, allowedService)
	}
}

func preparePrivilegedSocketPath(socketPath string) error {
	parent := filepath.Dir(socketPath)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("privileged helper socket directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("privileged helper socket directory must be root-owned and not writable by group or others")
	}
	info, err = os.Lstat(socketPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSocket == 0:
		return errors.New("refusing to replace a non-socket path")
	default:
		return os.Remove(socketPath)
	}
}

func handlePrivilegedConnection(connection *net.UnixConn, allowedService string) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	peer, err := readPeerCredentials(connection)
	if err != nil {
		writePrivilegedResponse(connection, privilegedResponse{Error: "could not authenticate local client"})
		return
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxPrivilegedMessageSize))
	var request privilegedRequest
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		writePrivilegedResponse(connection, privilegedResponse{Error: "invalid request"})
		return
	}
	output, runErr := executePrivilegedRequest(request, allowedService)
	log.Info(
		"Privileged management request",
		"operation", request.Operation, "target", request.Target, "pid", request.PID,
		"peer_uid", peer.UID, "peer_gid", peer.GID, "peer_pid", peer.PID, "error", runErr,
	)
	response := privilegedResponse{Output: output}
	if runErr != nil {
		response.Error = runErr.Error()
	}
	writePrivilegedResponse(connection, response)
}

func executePrivilegedRequest(request privilegedRequest, allowedService string) (string, error) {
	if request.Version != privilegedProtocolVersion {
		return "", errors.New("unsupported privileged protocol version")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch request.Operation {
	case managementRestartService:
		if request.Target != allowedService {
			return "", errors.New("service is not allowlisted")
		}
		return runStructuredCommand(ctx, "systemctl", "restart", allowedService)
	case managementRebootHost:
		if request.Target != "" {
			return "", errors.New("reboot does not accept a target")
		}
		return runStructuredCommand(ctx, "systemctl", "reboot")
	case managementTerminateProcess:
		if !canTerminatePID(request.PID) {
			return "", errors.New("protected process")
		}
		start, err := readProcessStartTicks(request.PID)
		if err != nil {
			return "", errors.New("process no longer exists")
		}
		if request.StartTimeTicks == 0 || start != request.StartTimeTicks {
			return "", errors.New("process identity changed; reload details")
		}
		return "", syscall.Kill(request.PID, syscall.SIGTERM)
	default:
		return "", errors.New("unknown privileged operation")
	}
}

func writePrivilegedResponse(writer io.Writer, response privilegedResponse) {
	_ = json.NewEncoder(writer).Encode(response)
}
