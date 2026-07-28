package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidConfigName(t *testing.T) {
	for _, name := range []string{"machine.env", "nodes.json", "node_rpc_ed25519", "authorized-keys"} {
		if !validConfigName(name) {
			t.Fatalf("validConfigName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", ".secret", "../admin.env", "nested/file", "-admin.env", "-bad name"} {
		if validConfigName(name) {
			t.Fatalf("validConfigName(%q) = true", name)
		}
	}
}

func TestEffectiveServiceEnvironmentFallsBackToUnitValues(t *testing.T) {
	systemctl := func(arguments ...string) ([]byte, error) {
		command := strings.Join(arguments, " ")
		switch {
		case strings.Contains(command, "Environment"):
			return []byte("SSH_HOST=127.0.0.1 SSH_PORT=24234\n"), nil
		case strings.Contains(command, "MainPID"):
			return []byte("0\n"), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	environment := effectiveServiceEnvironment(systemctl, "fleetty.service")
	if environment["SSH_HOST"] != "127.0.0.1" || environment["SSH_PORT"] != "24234" {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestReadStagedConfigRejectsSymlinkAndSortsFiles(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "z.env"), []byte("Z=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "a.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := readStagedConfig(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0].Name != "a.json" || configs[1].Name != "z.env" {
		t.Fatalf("unexpected config order: %#v", configs)
	}
	if err := os.Symlink(filepath.Join(configDir, "a.json"), filepath.Join(configDir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readStagedConfig(configDir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestFileTransactionCommitAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleetty")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction := &fileTransaction{}
	changed, err := transaction.Replace(path, []byte("new"), 0o600, -1, -1)
	if err != nil || !changed {
		t.Fatalf("Replace() = %v, %v", changed, err)
	}
	transaction.Rollback()
	assertFileContentAndMode(t, path, "old", 0o644)

	transaction = &fileTransaction{}
	changed, err = transaction.Replace(path, []byte("new"), 0o600, -1, -1)
	if err != nil || !changed {
		t.Fatalf("Replace() = %v, %v", changed, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	assertFileContentAndMode(t, path, "new", 0o600)
}

func TestInstallFleettyIsIdempotent(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "source-fleetty")
	if err := os.WriteFile(executable, []byte("fleetty-v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	configSource := filepath.Join(root, "staged")
	if err := os.Mkdir(configSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configSource, "machine.env"), []byte("DEFAULT_THEME=dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeInstallRunner{}
	options := installOptions{
		Role: "node", ConfigSource: configSource, ExecutableSource: executable,
		BinaryPath: filepath.Join(root, "opt", "fleetty"),
		ConfigPath: filepath.Join(root, "etc"),
		UnitPath:   filepath.Join(root, "systemd"),
		Run:        runner.Run,
	}
	first, err := installFleetty(options)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || !runner.active || !containsCommand(runner.commands, "systemctl start fleetty.service") {
		t.Fatalf("unexpected first install: result=%#v commands=%v", first, runner.commands)
	}
	assertFileContentAndMode(t, options.BinaryPath, "fleetty-v1", 0o755)
	assertFileContentAndMode(t, filepath.Join(options.ConfigPath, "machine.env"), "DEFAULT_THEME=dark\n", 0o600)

	runner.commands = nil
	second, err := installFleetty(options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || containsCommand(runner.commands, "systemctl restart fleetty.service") ||
		containsCommand(runner.commands, "systemctl start fleetty.service") {
		t.Fatalf("idempotent install changed service: result=%#v commands=%v", second, runner.commands)
	}
}

func TestInstallFleettyUserScope(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "source-fleetty")
	if err := os.WriteFile(executable, []byte("fleetty-user"), 0o755); err != nil {
		t.Fatal(err)
	}
	configSource := filepath.Join(root, "staged")
	if err := os.Mkdir(configSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configSource, "authorized_keys"), []byte("key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeInstallRunner{}
	options := installOptions{
		Role: "node", Scope: "user", ConfigSource: configSource,
		ExecutableSource: executable,
		BinaryPath:       filepath.Join(root, ".local", "bin", "fleetty"),
		ConfigPath:       filepath.Join(root, ".config", "fleetty"),
		UnitPath:         filepath.Join(root, ".config", "systemd", "user"),
		SystemctlArgs:    []string{"--user"},
		OwnerUID:         os.Geteuid(), OwnerGID: os.Getegid(),
		Run: runner.Run,
	}
	result, err := installFleetty(options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scope != "user" || !result.Changed {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !containsCommand(runner.commands, "systemctl --user start fleetty.service") {
		t.Fatalf("user systemd was not used: %v", runner.commands)
	}
	unit, err := os.ReadFile(filepath.Join(options.UnitPath, "fleetty.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart=%h/.local/bin/fleetty") ||
		!strings.Contains(string(unit), "FLEETTY_INSTALL_SCOPE=user") {
		t.Fatalf("unexpected user unit:\n%s", unit)
	}
}

func TestInstallFleettyRollsBackFailedStart(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "source-fleetty")
	if err := os.WriteFile(executable, []byte("broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "opt", "fleetty")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("working"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeInstallRunner{failStart: true}
	_, err := installFleetty(installOptions{
		Role: "hub", ExecutableSource: executable,
		BinaryPath: binaryPath, ConfigPath: filepath.Join(root, "etc"),
		UnitPath: filepath.Join(root, "systemd"), Run: runner.Run,
	})
	if err == nil || !strings.Contains(err.Error(), "installation rolled back") &&
		!strings.Contains(err.Error(), "start fleetty-hub.service") {
		t.Fatalf("install error = %v", err)
	}
	if runner.enabled || runner.active {
		t.Fatalf("service state was not restored: active=%v enabled=%v", runner.active, runner.enabled)
	}
	assertFileContentAndMode(t, binaryPath, "working", 0o755)
}

type fakeInstallRunner struct {
	active    bool
	enabled   bool
	failStart bool
	commands  []string
}

func (runner *fakeInstallRunner) Run(name string, args ...string) ([]byte, error) {
	command := strings.Join(append([]string{name}, args...), " ")
	runner.commands = append(runner.commands, command)
	if name != "systemctl" || len(args) == 0 {
		return nil, nil
	}
	if args[0] == "--user" {
		args = args[1:]
		if len(args) == 0 {
			return nil, errors.New("missing systemctl command")
		}
	}
	switch args[0] {
	case "is-active":
		if runner.active {
			return nil, nil
		}
		return nil, errors.New("inactive")
	case "is-enabled":
		if runner.enabled {
			return nil, nil
		}
		return nil, errors.New("disabled")
	case "enable":
		runner.enabled = true
	case "start", "restart":
		if runner.failStart {
			return []byte("failed"), errors.New("exit status 1")
		}
		runner.active = true
	case "disable":
		runner.enabled = false
	case "stop":
		runner.active = false
	}
	return nil, nil
}

func containsCommand(commands []string, expected string) bool {
	for _, command := range commands {
		if command == expected {
			return true
		}
	}
	return false
}

func assertFileContentAndMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content || info.Mode().Perm() != mode.Perm() {
		t.Fatalf("%s = %q mode %04o, want %q mode %04o", path, data, info.Mode().Perm(), content, mode)
	}
}
