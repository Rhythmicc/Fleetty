package main

import (
	"context"
	"errors"
	"os"
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
	cache         *snapshotCache
	admin         *adminController
	privileged    *privilegedClient
	runManagement func(context.Context, privilegedRequest) (string, error)
	user          string
	remote        string
}

func newLocalMonitorBackend(admin *adminController, cache *snapshotCache, user, remote string) *localMonitorBackend {
	return &localMonitorBackend{
		cache:      cache,
		admin:      admin,
		privileged: newPrivilegedClient(privilegedSocketPath()),
		user:       user,
		remote:     remote,
	}
}

func (b *localMonitorBackend) Collect() (monitorSnapshot, error) {
	return b.cache.Get(true)
}

func (b *localMonitorBackend) Authenticate(password string) (bool, error) {
	return b.admin.authenticate(password), nil
}

func (b *localMonitorBackend) ProcessDetail(pid int, password string) (processDetail, error) {
	includeSensitive := password != "" && b.admin.authenticate(password)
	detail, err := readProcessDetailWithSensitive(pid, includeSensitive)
	if err == nil {
		detail.CanTerminate = os.Geteuid() == 0 || detail.UID == os.Geteuid() || b.privileged != nil
	}
	return detail, err
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
	if b.privileged != nil && os.Geteuid() != 0 {
		_, err = b.privileged.Execute(context.Background(), privilegedRequest{
			Operation: managementTerminateProcess,
			PID:       pid, StartTimeTicks: expectedStartTicks,
		})
	} else {
		var process *os.Process
		process, err = os.FindProcess(pid)
		if err == nil {
			err = process.Signal(syscall.SIGTERM)
		}
	}
	log.Info("Process termination requested", "pid", pid, "signal", "SIGTERM", "user", b.user, "remote", b.remote, "error", err)
	return err
}

func (b *localMonitorBackend) RunAction(actionID int, password string) (string, error) {
	if !b.admin.authenticate(password) {
		return "", errors.New("management authentication failed")
	}
	var action *adminAction
	for index := range b.admin.actions {
		if b.admin.actions[index].ID == actionID {
			action = &b.admin.actions[index]
			break
		}
	}
	if action == nil {
		return "", errors.New("unknown management action")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request := privilegedRequest{
		Operation: action.operation,
		Target:    action.target,
	}
	var output string
	var err error
	if b.runManagement != nil {
		output, err = b.runManagement(ctx, request)
	} else if action.privileged {
		if b.privileged == nil {
			return "", errors.New("privileged helper is unavailable")
		}
		output, err = b.privileged.Execute(ctx, request)
	} else {
		output, err = executeLocalManagement(ctx, request)
	}
	log.Info("Management action requested", "action", action.label, "user", b.user, "remote", b.remote, "error", err)
	return string(output), err
}

type remoteMonitorBackend struct {
	client *nodeRPCClient
}

func newRemoteMonitorBackend(node hubNodeConfig) *remoteMonitorBackend {
	return &remoteMonitorBackend{client: sharedRPCClientRegistry.clientFor(node)}
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
