package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestResolvesTargets(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "node-config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "machine.env"), []byte("DEFAULT_THEME=dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "fleet.json")
	manifest := `{
  "version": 1,
  "binary": "fleetty",
  "targets": [
    {"name":"node-1","ssh":"node-1-admin","role":"node","config_dir":"node-config"},
    {"name":"hub","ssh":"hub","role":"hub","scope":"user"},
    {"name":"node-1-helper","ssh":"node-1-admin","role":"privileged-helper"}
  ]
}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, targets, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Parallel != defaultParallelism || parsed.TimeoutSeconds != defaultTimeout ||
		len(targets) != 3 || targets[0].Binary != binary ||
		targets[0].ConfigDir != configDir || targets[0].Become != "sudo" ||
		targets[0].Scope != "system" ||
		targets[1].Become != "none" || targets[1].Scope != "user" ||
		targets[2].Role != "privileged-helper" || targets[2].Scope != "system" {
		t.Fatalf("unexpected manifest: parsed=%#v targets=%#v", parsed, targets)
	}
}

func TestLoadManifestRejectsUnsafeOrUnknownValues(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fleetty")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		manifest string
		contains string
	}{
		{
			name: "ssh option injection",
			manifest: `{"version":1,"binary":"fleetty","targets":[
				{"name":"node","ssh":"-oProxyCommand=bad","role":"node"}]}`,
			contains: "invalid ssh",
		},
		{
			name: "unknown field",
			manifest: `{"version":1,"binary":"fleetty","unexpected":true,"targets":[
				{"name":"node","ssh":"node","role":"node"}]}`,
			contains: "unknown field",
		},
		{
			name: "unsupported privilege",
			manifest: `{"version":1,"binary":"fleetty","targets":[
				{"name":"node","ssh":"node","role":"node","become":"password"}]}`,
			contains: "become",
		},
		{
			name: "user scope sudo",
			manifest: `{"version":1,"binary":"fleetty","targets":[
				{"name":"node","ssh":"node","role":"node","scope":"user","become":"sudo"}]}`,
			contains: "user scope",
		},
		{
			name: "user helper",
			manifest: `{"version":1,"binary":"fleetty","targets":[
				{"name":"helper","ssh":"node","role":"privileged-helper","scope":"user"}]}`,
			contains: "system scope",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(test.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadManifest(path); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("loadManifest() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestResolveConfigDirectoryRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(configDir, "admin.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfigDirectory(root, "config"); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("resolveConfigDirectory() error = %v", err)
	}
}

func TestSafeSSHTarget(t *testing.T) {
	for _, value := range []string{"a100", "n1-admin", "root@hub.example.com"} {
		if !safeSSHTarget(value) {
			t.Fatalf("safeSSHTarget(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-V", "host command", "host:22", "a@b@c", "user@-host"} {
		if safeSSHTarget(value) {
			t.Fatalf("safeSSHTarget(%q) = true", value)
		}
	}
}
