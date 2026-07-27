package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"charm.land/log/v2"
)

// monitorBackend keeps the TUI independent from where metrics and management
// actions run. A node uses localMonitorBackend; Hub detail pages use the same
// model with remoteMonitorBackend.
type monitorBackend interface {
	Collect() (monitorSnapshot, error)
	Authenticate(password string) (bool, error)
	ProcessDetail(pid int, password string) (processDetail, error)
	TerminateProcess(pid int, expectedStartTicks uint64, password string) error
	RunAction(actionID int, password string) (string, error)
}

type localMonitorBackend struct {
	collector *metricsCollector
	admin     *adminController
	user      string
	remote    string
}

func newLocalMonitorBackend(admin *adminController, machine machineConfig, user, remote string) *localMonitorBackend {
	return &localMonitorBackend{
		collector: newMetricsCollector(machine),
		admin:     admin,
		user:      user,
		remote:    remote,
	}
}

func (b *localMonitorBackend) Collect() (monitorSnapshot, error) {
	return b.collector.collect()
}

func (b *localMonitorBackend) Authenticate(password string) (bool, error) {
	return b.admin.authenticate(password), nil
}

func (b *localMonitorBackend) ProcessDetail(pid int, password string) (processDetail, error) {
	includeSensitive := password != "" && b.admin.authenticate(password)
	return readProcessDetailWithSensitive(pid, includeSensitive)
}

func (b *localMonitorBackend) TerminateProcess(pid int, expectedStartTicks uint64, password string) error {
	if !b.admin.authenticate(password) {
		return errors.New("management authentication failed")
	}
	if !canTerminatePID(pid) {
		return errors.New("protected process")
	}
	currentStartTicks, err := readProcessStartTicks(pid)
	if err != nil {
		return errors.New("process no longer exists")
	}
	if expectedStartTicks == 0 || currentStartTicks != expectedStartTicks {
		return errors.New("process identity changed; reload details")
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		err = process.Signal(syscall.SIGTERM)
	}
	log.Info("Process termination requested", "pid", pid, "signal", "SIGTERM", "user", b.user, "remote", b.remote, "error", err)
	return err
}

func (b *localMonitorBackend) RunAction(actionID int, password string) (string, error) {
	if !b.admin.authenticate(password) {
		return "", errors.New("management authentication failed")
	}
	if actionID < 0 || actionID >= len(b.admin.actions) {
		return "", errors.New("unknown management action")
	}
	action := b.admin.actions[actionID]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "sh", "-c", action.command).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out")
	}
	log.Info("Management action requested", "action", action.label, "user", b.user, "remote", b.remote, "error", err)
	return string(output), err
}

type remoteMonitorBackend struct {
	client *nodeRPCClient
}

func newRemoteMonitorBackend(node hubNodeConfig) *remoteMonitorBackend {
	return &remoteMonitorBackend{client: newNodeRPCClient(node)}
}

func (b *remoteMonitorBackend) Collect() (monitorSnapshot, error) {
	response, err := b.client.Call(nodeRPCRequest{Operation: rpcSnapshot, IncludeProcesses: true})
	if err != nil {
		return monitorSnapshot{}, err
	}
	return response.Snapshot, nil
}

func (b *remoteMonitorBackend) Authenticate(password string) (bool, error) {
	response, err := b.client.Call(nodeRPCRequest{Operation: rpcAuthenticate, Password: password})
	if err != nil {
		return false, err
	}
	return response.Authorized, nil
}

func (b *remoteMonitorBackend) ProcessDetail(pid int, password string) (processDetail, error) {
	response, err := b.client.Call(nodeRPCRequest{Operation: rpcProcessDetail, Password: password, PID: pid})
	if err != nil {
		return processDetail{}, err
	}
	return response.ProcessDetail, nil
}

func (b *remoteMonitorBackend) TerminateProcess(pid int, expectedStartTicks uint64, password string) error {
	_, err := b.client.Call(nodeRPCRequest{
		Operation:      rpcTerminateProcess,
		Password:       password,
		PID:            pid,
		StartTimeTicks: expectedStartTicks,
	})
	return err
}

func (b *remoteMonitorBackend) RunAction(actionID int, password string) (string, error) {
	response, err := b.client.Call(nodeRPCRequest{
		Operation: rpcRunAction,
		Password:  password,
		ActionID:  actionID,
	})
	return response.Output, err
}
