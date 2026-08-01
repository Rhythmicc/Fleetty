package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCascadeDryRunJSONUsesRelayDocument(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty_linux_amd64")
	if err := os.WriteFile(binary, []byte("release"), 0o700); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(binary)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"targets":[{
    "name":"compute","ssh":"compute","role":"node","scope":"system",
    "become":"none","arch":"amd64","binary":"fleetty_linux_amd64",
    "sha256":"` + hash + `"}]}`
	path := filepath.Join(root, "cascade.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRemoteRunner{binaryHash: hash, state: "active", enabled: "enabled"}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"cascade", "--file", path, "--json"}, &stdout, &stderr, runner); err != nil {
		t.Fatal(err)
	}
	var document relayCascadeDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("cascade JSON is invalid: %v\n%s", err, stdout.String())
	}
	if len(document.Plan) != 1 || document.Plan[0].Name != "compute" {
		t.Fatalf("cascade JSON plan = %#v", document.Plan)
	}
}

type offlineRelayRunner struct{}

func (offlineRelayRunner) Run(context.Context, string, []string) ([]byte, error) {
	return []byte("connection timed out"), errors.New("exit status 255")
}

func TestRelayBundleTargetsOnlyAdjacentNode(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, "fleettyctl_linux_amd64")
	fleetty := filepath.Join(root, "fleetty_linux_amd64")
	if err := os.WriteFile(controller, []byte("relay controller"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fleetty, []byte("fleetty node"), 0o700); err != nil {
		t.Fatal(err)
	}
	controllerHash, err := fileSHA256(controller)
	if err != nil {
		t.Fatal(err)
	}
	target := resolvedTarget{
		Name: "a100-login", SSH: "a100", Role: "relay", Scope: "user", Become: "none",
		Arch: "amd64", Binary: controller, TimeoutSeconds: 5,
		Children: []resolvedTarget{{
			Name: "a100-compute", SSH: "a100", Role: "node", Scope: "system",
			Become: "none", Arch: "amd64", Binary: fleetty, TimeoutSeconds: 5,
		}},
	}
	runner := &scriptedRemoteRunner{
		staging:    "/tmp/fleettyctl.Relay123",
		binaryHash: controllerHash,
		relayOutput: `{"plan":[{"name":"a100-compute","action":"update",
      "role":"node","scope":"system","state":"active","enabled":"enabled"}]}`,
	}
	plan := planRelayTarget(context.Background(), target, runner)
	if plan.Action != "relay" || plan.State != "reachable" {
		t.Fatalf("relay plan = %#v", plan)
	}
	commands := strings.Join(runner.Commands(), "\n")
	if !strings.Contains(commands, "a100") || !strings.Contains(commands, "cascade") ||
		!strings.Contains(commands, "fleetty_linux_amd64") {
		t.Fatalf("relay commands do not contain the adjacent bundle flow:\n%s", commands)
	}
	if strings.Contains(commands, "a100-compute:") {
		t.Fatalf("Hub attempted to copy directly to the leaf node:\n%s", commands)
	}

	runner.relayOutput = `{"plan":[{"name":"a100-compute","action":"update"}],
    "results":[{"name":"a100-compute","action":"applied"}]}`
	results := applyCascadeTargets(context.Background(), []resolvedTarget{target}, []targetPlan{plan}, 1, runner)
	if len(results) != 1 || results[0].Action != "relayed" || results[0].Error != "" {
		t.Fatalf("relay results = %#v", results)
	}
}

func TestBuildRelayManifestPinsEveryBundleChecksum(t *testing.T) {
	root := t.TempDir()
	fleetty := filepath.Join(root, "fleetty_linux_amd64")
	if err := os.WriteFile(fleetty, []byte("verified release"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, targets, err := buildRelayManifest([]resolvedTarget{{
		Name: "compute", SSH: "compute", Role: "node", Scope: "system",
		Become: "sudo", Arch: "amd64", Binary: fleetty,
	}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("verified release"))
	expected := hex.EncodeToString(digest[:])
	if files["fleetty_linux_amd64"] != fleetty || len(targets) != 1 ||
		targets[0].Binary != "fleetty_linux_amd64" || targets[0].SHA256 != expected {
		t.Fatalf("relay manifest files=%#v targets=%#v", files, targets)
	}
}

func TestOfflineRelayDefersOnlyItsBranch(t *testing.T) {
	target := resolvedTarget{
		Name: "offline-login", SSH: "offline-login", Role: "relay", Arch: "amd64",
		Binary: "/not-reached", TimeoutSeconds: 1,
		Children: []resolvedTarget{{Name: "leaf", SSH: "leaf", Role: "node"}},
	}
	plan := planRelayTarget(context.Background(), target, offlineRelayRunner{})
	if plan.Action != "deferred" || plan.State != "offline" || len(plan.Reasons) != 1 {
		t.Fatalf("offline relay plan = %#v", plan)
	}
}

func TestCascadeManifestRejectsModifiedBundle(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty_linux_amd64")
	if err := os.WriteFile(binary, []byte("modified"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"targets":[{
    "name":"compute","ssh":"compute","role":"node","scope":"system",
    "become":"none","arch":"amd64","binary":"fleetty_linux_amd64",
    "sha256":"` + strings.Repeat("0", 64) + `"}]}`
	path := filepath.Join(root, "cascade.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCascadeManifest(path); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("loadCascadeManifest() error = %v", err)
	}
}

func TestCascadeManifestRequiresPinnedChecksum(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty_linux_amd64")
	if err := os.WriteFile(binary, []byte("release"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "cascade.json")
	manifest := `{"version":1,"targets":[{
    "name":"compute","ssh":"compute","role":"node","scope":"system",
    "become":"none","arch":"amd64","binary":"fleetty_linux_amd64"}]}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCascadeManifest(path); err == nil || !strings.Contains(err.Error(), "requires sha256") {
		t.Fatalf("loadCascadeManifest() error = %v", err)
	}
}
