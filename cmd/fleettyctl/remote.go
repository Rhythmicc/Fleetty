package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type commandRunner interface {
	Run(context.Context, string, []string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.CombinedOutput()
}

type targetPlan struct {
	Index       int      `json:"-"`
	Name        string   `json:"name"`
	SSH         string   `json:"ssh"`
	Role        string   `json:"role"`
	Scope       string   `json:"scope"`
	Action      string   `json:"action"`
	Service     string   `json:"service"`
	DesiredHash string   `json:"desired_hash"`
	CurrentHash string   `json:"current_hash,omitempty"`
	State       string   `json:"state"`
	Enabled     string   `json:"enabled"`
	Reasons     []string `json:"reasons,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type targetApply struct {
	Index  int             `json:"-"`
	Name   string          `json:"name"`
	Action string          `json:"action"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type remoteDeploymentLayout struct {
	BinaryPath    string
	ConfigPath    string
	SystemctlArgs []string
}

func planTargets(ctx context.Context, targets []resolvedTarget, parallel int, runner commandRunner) []targetPlan {
	results := make([]targetPlan, len(targets))
	runParallel(len(targets), parallel, func(index int) {
		results[index] = planTarget(ctx, targets[index], runner)
	})
	return results
}

func planTarget(parent context.Context, target resolvedTarget, runner commandRunner) targetPlan {
	plan := targetPlan{
		Index: target.Index, Name: target.Name, SSH: target.SSH, Role: target.Role,
		Scope:   targetScope(target),
		Service: serviceForRole(target.Role), State: "unknown", Enabled: "unknown",
	}
	desiredHash, err := fileSHA256(target.Binary)
	if err != nil {
		plan.Action, plan.Error = "error", err.Error()
		return plan
	}
	plan.DesiredHash = desiredHash
	operationTimeout := time.Duration(max(target.TimeoutSeconds*6, 30)) * time.Second
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	if output, connectErr := runSSH(ctx, runner, target, "true"); connectErr != nil {
		plan.Action = "error"
		plan.Error = remoteFailure("connect to target", output, connectErr).Error()
		return plan
	}
	if target.Become == "sudo" {
		if output, privilegeErr := runSSH(ctx, runner, target, "sudo", "-n", "true"); privilegeErr != nil {
			plan.Action = "error"
			plan.Error = remoteFailure("verify passwordless administrative sudo", output, privilegeErr).Error()
			return plan
		}
	}
	layout, layoutErr := resolveRemoteLayout(ctx, runner, target)
	if layoutErr != nil {
		plan.Action, plan.Error = "error", layoutErr.Error()
		return plan
	}
	if targetScope(target) == "user" {
		arguments := append([]string{"systemctl"}, layout.SystemctlArgs...)
		arguments = append(arguments, "show", "--property=Version", "--value")
		if output, systemdErr := runSSH(ctx, runner, target, arguments...); systemdErr != nil {
			plan.Action = "error"
			plan.Error = remoteFailure("connect to the remote systemd user manager", output, systemdErr).Error()
			return plan
		}
	}
	output, hashErr := runSSH(ctx, runner, target, "sha256sum", layout.BinaryPath)
	if hashErr == nil {
		fields := strings.Fields(string(output))
		if len(fields) == 0 || !validSHA256(fields[0]) {
			plan.Action = "error"
			plan.Error = "remote sha256sum returned an invalid digest"
			return plan
		} else {
			plan.CurrentHash = strings.ToLower(fields[0])
		}
	}
	systemctlArgs := append(append([]string{"systemctl"}, layout.SystemctlArgs...), "is-active", plan.Service)
	stateOutput, stateErr := runSSH(ctx, runner, target, systemctlArgs...)
	if stateErr == nil || strings.TrimSpace(string(stateOutput)) != "" {
		plan.State = safeRemoteText(string(stateOutput), 80)
	}
	systemctlArgs = append(append([]string{"systemctl"}, layout.SystemctlArgs...), "is-enabled", plan.Service)
	enabledOutput, enabledErr := runSSH(ctx, runner, target, systemctlArgs...)
	if enabledErr == nil || strings.TrimSpace(string(enabledOutput)) != "" {
		plan.Enabled = safeRemoteText(string(enabledOutput), 80)
	}
	switch {
	case hashErr != nil:
		plan.Reasons = append(plan.Reasons, "binary is not installed")
	case plan.CurrentHash != desiredHash:
		plan.Reasons = append(plan.Reasons, "binary hash differs")
	}
	if plan.State != "active" {
		plan.Reasons = append(plan.Reasons, "service is "+plan.State)
	}
	if plan.Enabled != "enabled" {
		plan.Reasons = append(plan.Reasons, "service is "+plan.Enabled)
	}
	configReasons, configErr := compareRemoteConfigs(ctx, runner, target, layout)
	if configErr != nil {
		plan.Action, plan.Error = "error", configErr.Error()
		return plan
	}
	plan.Reasons = append(plan.Reasons, configReasons...)
	if len(plan.Reasons) == 0 {
		plan.Action = "noop"
	} else if hashErr != nil {
		plan.Action = "install"
	} else {
		plan.Action = "update"
	}
	return plan
}

func compareRemoteConfigs(
	ctx context.Context,
	runner commandRunner,
	target resolvedTarget,
	layout remoteDeploymentLayout,
) ([]string, error) {
	if target.ConfigDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(target.ConfigDir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	arguments := []string{"sha256sum"}
	local := make(map[string]string, len(entries))
	for _, entry := range entries {
		hash, hashErr := fileSHA256(filepath.Join(target.ConfigDir, entry.Name()))
		if hashErr != nil {
			return nil, hashErr
		}
		local[entry.Name()] = hash
		arguments = append(arguments, filepath.Join(layout.ConfigPath, entry.Name()))
	}
	if target.Become == "sudo" {
		arguments = append([]string{"sudo", "-n"}, arguments...)
	}
	output, remoteErr := runSSH(ctx, runner, target, arguments...)
	remote := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			remote[filepath.Base(fields[1])] = fields[0]
		}
	}
	var reasons []string
	for name, hash := range local {
		if remote[name] != hash {
			reasons = append(reasons, "config differs: "+name)
		}
	}
	sort.Strings(reasons)
	if remoteErr != nil && len(reasons) == 0 {
		return nil, fmt.Errorf("check remote config: %w: %s", remoteErr, strings.TrimSpace(string(output)))
	}
	return reasons, nil
}

func applyTargets(
	ctx context.Context,
	targets []resolvedTarget,
	plans []targetPlan,
	parallel int,
	runner commandRunner,
) []targetApply {
	results := make([]targetApply, len(targets))
	runParallel(len(targets), parallel, func(index int) {
		if plans[index].Action == "noop" {
			results[index] = targetApply{Index: index, Name: targets[index].Name, Action: "noop"}
			return
		}
		if plans[index].Action == "error" {
			results[index] = targetApply{
				Index: index, Name: targets[index].Name, Action: "error", Error: plans[index].Error,
			}
			return
		}
		results[index] = applyTarget(ctx, targets[index], runner)
	})
	return results
}

func applyTarget(parent context.Context, target resolvedTarget, runner commandRunner) targetApply {
	result := targetApply{Index: target.Index, Name: target.Name, Action: "apply"}
	timeout := time.Duration(max(target.TimeoutSeconds*4, 60)) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, err := runSSH(ctx, runner, target, "mktemp", "-d", "/tmp/fleettyctl.XXXXXXXXXX")
	if err != nil {
		result.Error = remoteFailure("create staging directory", output, err).Error()
		return result
	}
	staging := strings.TrimSpace(string(output))
	if !validRemoteStagingPath(staging) {
		result.Error = fmt.Sprintf("remote returned unsafe staging path %q", staging)
		return result
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = runSSH(cleanupCtx, runner, target, "find", staging, "-xdev", "-depth", "-delete")
	}()
	if output, err = runSCP(ctx, runner, target, target.Binary, staging+"/fleetty"); err != nil {
		result.Error = remoteFailure("upload binary", output, err).Error()
		return result
	}
	if output, err = runSSH(ctx, runner, target, "chmod", "0700", staging+"/fleetty"); err != nil {
		result.Error = remoteFailure("make staged binary executable", output, err).Error()
		return result
	}
	installArgs := []string{
		staging + "/fleetty", "install",
		"--role", target.Role, "--scope", targetScope(target), "--json",
	}
	if target.ConfigDir != "" {
		if output, err = runSSH(ctx, runner, target, "mkdir", "-m", "0700", staging+"/config"); err != nil {
			result.Error = remoteFailure("create config staging directory", output, err).Error()
			return result
		}
		entries, readErr := os.ReadDir(target.ConfigDir)
		if readErr != nil {
			result.Error = readErr.Error()
			return result
		}
		for _, entry := range entries {
			source := filepath.Join(target.ConfigDir, entry.Name())
			if output, err = runSCP(ctx, runner, target, source, staging+"/config/"+entry.Name()); err != nil {
				result.Error = remoteFailure("upload "+entry.Name(), output, err).Error()
				return result
			}
		}
		installArgs = append(installArgs, "--config-dir", staging+"/config")
	}
	if target.Become == "sudo" {
		installArgs = append([]string{"sudo", "-n"}, installArgs...)
	}
	output, err = runSSH(ctx, runner, target, installArgs...)
	if err != nil {
		result.Error = remoteFailure("install Fleetty", output, err).Error()
		return result
	}
	if !json.Valid(output) {
		result.Error = "installer returned an invalid JSON response: " + safeRemoteText(string(output), 500)
		return result
	}
	var raw json.RawMessage
	raw = append(raw, output...)
	result.Result = raw
	result.Action = "applied"
	return result
}

func statusTargets(ctx context.Context, targets []resolvedTarget, parallel int, runner commandRunner) []targetApply {
	results := make([]targetApply, len(targets))
	runParallel(len(targets), parallel, func(index int) {
		target := targets[index]
		result := targetApply{Index: index, Name: target.Name, Action: "status"}
		operationTimeout := time.Duration(max(target.TimeoutSeconds*3, 30)) * time.Second
		targetCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		layout, layoutErr := resolveRemoteLayout(targetCtx, runner, target)
		if layoutErr != nil {
			result.Action = "error"
			result.Error = layoutErr.Error()
			results[index] = result
			return
		}
		arguments := []string{
			layout.BinaryPath, "doctor",
			"--role", target.Role, "--scope", targetScope(target), "--json",
		}
		if target.Become == "sudo" {
			arguments = append([]string{"sudo", "-n"}, arguments...)
		}
		output, err := runSSH(targetCtx, runner, target, arguments...)
		if json.Valid(output) {
			result.Result = append(result.Result, output...)
		}
		if err != nil {
			result.Action = "error"
			result.Error = remoteFailure("run Fleetty doctor", output, err).Error()
		}
		results[index] = result
	})
	return results
}

func runParallel(count, parallel int, operation func(int)) {
	semaphore := make(chan struct{}, parallel)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			operation(index)
		}(index)
	}
	waitGroup.Wait()
}

func runSSH(
	ctx context.Context,
	runner commandRunner,
	target resolvedTarget,
	remoteArguments ...string,
) ([]byte, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(target.TimeoutSeconds),
		target.SSH,
		shellJoin(remoteArguments),
	}
	return runner.Run(ctx, "ssh", args)
}

func runSCP(
	ctx context.Context,
	runner commandRunner,
	target resolvedTarget,
	source, destination string,
) ([]byte, error) {
	args := []string{
		"-q",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(target.TimeoutSeconds),
		source,
		target.SSH + ":" + destination,
	}
	return runner.Run(ctx, "scp", args)
}

func shellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func resolveRemoteLayout(
	ctx context.Context,
	runner commandRunner,
	target resolvedTarget,
) (remoteDeploymentLayout, error) {
	if targetScope(target) == "system" {
		return remoteDeploymentLayout{
			BinaryPath: "/opt/fleetty/fleetty",
			ConfigPath: "/etc/fleetty",
		}, nil
	}
	output, err := runSSH(ctx, runner, target, "printenv", "HOME")
	if err != nil {
		return remoteDeploymentLayout{}, remoteFailure("resolve remote user home", output, err)
	}
	home := strings.TrimSpace(string(output))
	if !safeRemoteHome(home) {
		return remoteDeploymentLayout{}, fmt.Errorf("remote returned unsafe user home %q", safeRemoteText(home, 120))
	}
	return remoteDeploymentLayout{
		BinaryPath:    filepath.Join(home, ".local", "bin", "fleetty"),
		ConfigPath:    filepath.Join(home, ".config", "fleetty"),
		SystemctlArgs: []string{"--user"},
	}, nil
}

func targetScope(target resolvedTarget) string {
	if target.Scope == "" {
		return "system"
	}
	return target.Scope
}

func safeRemoteHome(path string) bool {
	if !filepath.IsAbs(path) || path == "/" || filepath.Clean(path) != path {
		return false
	}
	for _, character := range path {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '/' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validRemoteStagingPath(path string) bool {
	const prefix = "/tmp/fleettyctl."
	if !strings.HasPrefix(path, prefix) || len(path) <= len(prefix) {
		return false
	}
	for _, character := range path[len(prefix):] {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func serviceForRole(role string) string {
	if role == "hub" {
		return "fleetty-hub.service"
	}
	return "fleetty.service"
}

func remoteFailure(action string, output []byte, err error) error {
	message := safeRemoteText(string(output), 500)
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

func safeRemoteText(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f || character >= 0x80 && character <= 0x9f {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if maximum > 0 && len(runes) > maximum {
		return string(runes[:maximum-1]) + "…"
	}
	return value
}

func plansHealthy(plans []targetPlan) bool {
	for _, plan := range plans {
		if plan.Action == "error" {
			return false
		}
	}
	return true
}

func appliesHealthy(results []targetApply) bool {
	for _, result := range results {
		if result.Error != "" {
			return false
		}
	}
	return true
}

var errOperationFailed = errors.New("one or more targets failed")
