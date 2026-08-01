package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPlanTargetDetectsNoopAndUpdate(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(binary)
	if err != nil {
		t.Fatal(err)
	}
	target := resolvedTarget{
		Name: "node", SSH: "node-admin", Role: "node", Binary: binary,
		Become: "sudo", TimeoutSeconds: 5,
	}
	runner := &scriptedRemoteRunner{binaryHash: hash, state: "active", enabled: "enabled"}
	plan := planTarget(context.Background(), target, runner)
	if plan.Action != "noop" || len(plan.Reasons) != 0 {
		t.Fatalf("noop plan = %#v", plan)
	}
	runner.binaryHash = strings.Repeat("0", 64)
	plan = planTarget(context.Background(), target, runner)
	if plan.Action != "update" || !containsReason(plan.Reasons, "binary hash differs") {
		t.Fatalf("update plan = %#v", plan)
	}
}

func TestPlanTargetRejectsUnavailableSudoAndInvalidDigest(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := resolvedTarget{
		Name: "node", SSH: "node-admin", Role: "node", Binary: binary,
		Become: "sudo", TimeoutSeconds: 5,
	}
	runner := &scriptedRemoteRunner{failSudo: true}
	plan := planTarget(context.Background(), target, runner)
	if plan.Action != "error" || !strings.Contains(plan.Error, "passwordless administrative sudo") {
		t.Fatalf("sudo plan = %#v", plan)
	}

	runner = &scriptedRemoteRunner{binaryHash: "not-a-digest", state: "active", enabled: "enabled"}
	plan = planTarget(context.Background(), target, runner)
	if plan.Action != "error" || !strings.Contains(plan.Error, "invalid digest") {
		t.Fatalf("invalid digest plan = %#v", plan)
	}
}

func TestApplyTargetUsesBatchSSHAndPasswordlessSudo(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "machine.env"), []byte("A=B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRemoteRunner{
		staging:       "/tmp/fleettyctl.Abc123",
		installResult: `{"role":"node","service":"fleetty.service","changed":true,"state":"active"}`,
	}
	result := applyTarget(context.Background(), resolvedTarget{
		Name: "node", SSH: "node-admin", Role: "node", Binary: binary,
		ConfigDir: configDir, Become: "sudo", TimeoutSeconds: 5,
	}, runner)
	if result.Error != "" || result.Action != "applied" || !json.Valid(result.Result) {
		t.Fatalf("applyTarget() = %#v", result)
	}
	commands := strings.Join(runner.Commands(), "\n")
	for _, expected := range []string{
		"ssh -o BatchMode=yes",
		"scp -q -o BatchMode=yes",
		"'sudo' '-n' '/tmp/fleettyctl.Abc123/fleetty' 'install'",
		"'find' '/tmp/fleettyctl.Abc123' '-xdev' '-depth' '-delete'",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("commands missing %q:\n%s", expected, commands)
		}
	}
	if strings.Contains(commands, "'sudo' '-S'") || strings.Contains(commands, "--password") {
		t.Fatalf("commands contain password-based sudo:\n%s", commands)
	}
}

func TestUserScopePlanAndApplyNeverUsesSudo(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty-user"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(binary)
	if err != nil {
		t.Fatal(err)
	}
	target := resolvedTarget{
		Name: "user-node", SSH: "user-node", Role: "node", Scope: "user",
		Binary: binary, Become: "none", TimeoutSeconds: 5,
	}
	runner := &scriptedRemoteRunner{
		home: "/home/alice", binaryHash: hash, state: "active", enabled: "enabled",
	}
	plan := planTarget(context.Background(), target, runner)
	if plan.Action != "noop" || plan.Scope != "user" {
		t.Fatalf("user plan = %#v", plan)
	}

	runner.binaryHash = strings.Repeat("0", 64)
	runner.staging = "/tmp/fleettyctl.User123"
	runner.installResult = `{"role":"node","scope":"user","service":"fleetty.service","changed":true,"state":"active"}`
	plan = planTarget(context.Background(), target, runner)
	result := applyTarget(context.Background(), target, runner)
	if plan.Action != "update" || result.Error != "" || result.Action != "applied" {
		t.Fatalf("plan=%#v result=%#v", plan, result)
	}
	commands := strings.Join(runner.Commands(), "\n")
	for _, expected := range []string{
		"'printenv' 'HOME'",
		"'sha256sum' '/home/alice/.local/bin/fleetty'",
		"'systemctl' '--user' 'is-active'",
		"'--scope' 'user'",
	} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("commands missing %q:\n%s", expected, commands)
		}
	}
	if strings.Contains(commands, "'sudo'") {
		t.Fatalf("user deployment invoked sudo:\n%s", commands)
	}
}

func TestApplyRejectsUnsafeRemoteStagingPath(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRemoteRunner{staging: "/tmp/fleettyctl.ok;touch-pwned"}
	result := applyTarget(context.Background(), resolvedTarget{
		Name: "node", SSH: "node", Role: "node", Binary: binary,
		Become: "sudo", TimeoutSeconds: 5,
	}, runner)
	if result.Error == "" || !strings.Contains(result.Error, "unsafe staging path") {
		t.Fatalf("applyTarget() = %#v", result)
	}
}

func TestApplyJSONIsSingleDocument(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty")
	if err := os.WriteFile(binary, []byte("fleetty"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(binary)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"binary":"fleetty","targets":[
		{"name":"node","ssh":"node","role":"node"}]}`
	manifestPath := filepath.Join(root, "fleet.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRemoteRunner{binaryHash: hash, state: "active", enabled: "enabled"}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"apply", "--file", manifestPath, "--yes", "--json"}, &stdout, &stderr, runner); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("apply JSON is invalid: %v\n%s", err, stdout.String())
	}
	if _, ok := document["plan"]; !ok {
		t.Fatalf("apply JSON missing plan: %v", document)
	}
	if _, ok := document["results"]; !ok {
		t.Fatalf("apply JSON missing results: %v", document)
	}
}

func TestSafeRemoteTextRemovesControlSequencesAndBoundsOutput(t *testing.T) {
	input := "\x1b[31mFAILED\x1b[0m\n" + strings.Repeat("x", 600)
	output := safeRemoteText(input, 40)
	if strings.ContainsRune(output, '\x1b') || len([]rune(output)) != 40 ||
		!strings.HasSuffix(output, "…") {
		t.Fatalf("safeRemoteText() = %q", output)
	}
}

type scriptedRemoteRunner struct {
	mu            sync.Mutex
	binaryHash    string
	state         string
	enabled       string
	staging       string
	installResult string
	failSudo      bool
	home          string
	architecture  string
	relayOutput   string
	commands      []string
}

func (runner *scriptedRemoteRunner) Run(_ context.Context, name string, args []string) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	command := strings.Join(append([]string{name}, args...), " ")
	runner.commands = append(runner.commands, command)
	if name == "scp" {
		return nil, nil
	}
	if name != "ssh" || len(args) == 0 {
		return nil, errors.New("unexpected command")
	}
	remote := args[len(args)-1]
	switch {
	case remote == "'true'":
		return nil, nil
	case remote == "'sudo' '-n' 'true'":
		if runner.failSudo {
			return []byte("sudo: a password is required"), errors.New("exit status 1")
		}
		return nil, nil
	case remote == "'printenv' 'HOME'":
		home := runner.home
		if home == "" {
			home = "/home/test"
		}
		return []byte(home + "\n"), nil
	case remote == "'uname' '-m'":
		if runner.architecture == "" {
			return nil, errors.New("target offline")
		}
		return []byte(runner.architecture + "\n"), nil
	case strings.Contains(remote, "'systemctl'") && strings.Contains(remote, "'--property=Version'"):
		return []byte("249\n"), nil
	case strings.Contains(remote, "'mktemp'"):
		return []byte(runner.staging + "\n"), nil
	case strings.Contains(remote, "'sha256sum'") && !strings.Contains(remote, "/etc/fleetty/"):
		if runner.binaryHash == "" {
			return []byte("missing"), errors.New("exit status 1")
		}
		return []byte(runner.binaryHash + "  fleetty\n"), nil
	case strings.Contains(remote, "'systemctl'") && strings.Contains(remote, "'is-active'"):
		if runner.state == "active" {
			return []byte("active\n"), nil
		}
		return []byte(runner.state + "\n"), errors.New("exit status 3")
	case strings.Contains(remote, "'systemctl'") && strings.Contains(remote, "'is-enabled'"):
		if runner.enabled == "enabled" {
			return []byte("enabled\n"), nil
		}
		return []byte(runner.enabled + "\n"), errors.New("exit status 1")
	case strings.Contains(remote, "'install' '--role'"):
		return []byte(runner.installResult + "\n"), nil
	case strings.Contains(remote, "/fleettyctl'") && strings.Contains(remote, "'cascade'"):
		if runner.relayOutput == "" {
			return nil, errors.New("relay output is not configured")
		}
		return []byte(runner.relayOutput + "\n"), nil
	case strings.Contains(remote, "'mkdir'"), strings.Contains(remote, "'chmod'"),
		strings.Contains(remote, "'find'"):
		return nil, nil
	default:
		return nil, errors.New("unexpected remote command: " + remote)
	}
}

func (runner *scriptedRemoteRunner) Commands() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]string(nil), runner.commands...)
}

func containsReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
