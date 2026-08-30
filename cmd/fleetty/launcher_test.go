package main

import (
	byteutil "bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	gossh "golang.org/x/crypto/ssh"
)

func TestBareCommandRequiresInteractiveLauncherInsteadOfServing(t *testing.T) {
	var stdout, stderr byteutil.Buffer
	handled, err := runOperationsWithInput(nil, &byteutil.Buffer{}, &stdout, &stderr)
	if !handled || err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("bare command = handled %t, error %v", handled, err)
	}

	handled, err = runOperationsWithInput([]string{"serve"}, &byteutil.Buffer{}, &stdout, &stderr)
	if handled || err != nil {
		t.Fatalf("explicit serve = handled %t, error %v", handled, err)
	}
}

func TestLauncherDefaultsToLocalMonitor(t *testing.T) {
	model := newLauncherModel()
	updated, _ := model.activate()
	launcher := updated.(*launcherModel)
	if launcher.result.Action != launcherActionTop {
		t.Fatalf("default launcher action = %v", launcher.result.Action)
	}
}

func TestLauncherCanUseExistingAuthorizedKeysForOneSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", "")
	t.Setenv("NODE_RPC_AUTHORIZED_KEYS_FILE", "")
	t.Setenv("SSH_ALLOW_ANONYMOUS", "")

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authorizedKeys, gossh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}

	model := newLauncherModel()
	if model.ssh.Ready || model.ssh.SuggestedKey != authorizedKeys {
		t.Fatalf("SSH state = %#v", model.ssh)
	}
	model.page = launcherPageSSH
	updated, _ := model.updateDetail("enter")
	launcher := updated.(*launcherModel)
	if launcher.result.Action != launcherActionServe || launcher.result.AuthorizedKeysPath != authorizedKeys {
		t.Fatalf("launcher result = %#v", launcher.result)
	}
}

func TestLauncherViewFitsTerminalAndShowsGuidance(t *testing.T) {
	model := newLauncherModel()
	model.width, model.height = 64, 28
	view := model.View().Content
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > model.width {
			t.Fatalf("launcher line width = %d, terminal = %d\n%s", width, model.width, line)
		}
	}
	if !strings.Contains(view, "LOCAL MONITOR") || !strings.Contains(view, "SSH SERVER") ||
		!strings.Contains(view, "CONFIGURATION") {
		t.Fatalf("launcher guidance is incomplete:\n%s", view)
	}
}
