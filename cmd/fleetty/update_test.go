package main

import (
	byteutil "bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestUpdateExecutable(t *testing.T) {
	for _, test := range []struct {
		name                                                               string
		check, same, corrupt, missing, probeFail, symlink, changed, locked bool
		wantErr                                                            string
	}{
		{name: "install"},
		{name: "check", check: true},
		{name: "current", same: true},
		{name: "corrupt", corrupt: true, wantErr: "checksum mismatch"},
		{name: "missing", missing: true, wantErr: "do not contain"},
		{name: "probe", probeFail: true, wantErr: "probe failed"},
		{name: "symlink", symlink: true},
		{name: "concurrent replacement", changed: true, wantErr: "changed during update"},
		{name: "locked", locked: true, wantErr: "another update"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fleetty")
			old, payload := []byte("previous executable"), []byte("new executable")
			if test.same {
				old = payload
			}
			if err := os.WriteFile(path, old, 0o751); err != nil {
				t.Fatal(err)
			}
			input := path
			if test.symlink {
				input = filepath.Join(root, "link")
				if err := os.Symlink(path, input); err != nil {
					t.Fatal(err)
				}
			}
			if test.locked {
				lock, err := os.OpenFile(path+".update.lock", os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			asset := "fleetty_" + runtime.GOOS + "_" + runtime.GOARCH
			downloads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/checksums.txt":
					name := asset
					if test.missing {
						name = "other"
					}
					fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(payload), name)
				case "/" + asset:
					downloads++
					if test.corrupt {
						_, _ = w.Write([]byte("broken"))
					} else {
						_, _ = w.Write(payload)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			probes := 0
			probe := func(_ context.Context, candidate, version string) error {
				probes++
				got, err := os.ReadFile(candidate)
				if err != nil || !byteutil.Equal(got, payload) || version != "v0.1.4" {
					t.Fatal("invalid candidate")
				}
				installed, _ := os.ReadFile(path)
				if !byteutil.Equal(installed, old) {
					t.Fatal("replaced before preflight")
				}
				if test.probeFail {
					return errors.New("probe failed")
				}
				if test.changed {
					return os.WriteFile(path, []byte("concurrent update"), 0o751)
				}
				return nil
			}
			var output byteutil.Buffer
			err := updateExecutable(context.Background(), server.Client(), server.URL, input, "v0.1.4", test.check, &output, probe)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("got %v, want %s", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := old
			installed := !test.check && !test.same && test.wantErr == ""
			if installed {
				want = payload
			}
			if test.changed {
				want = []byte("concurrent update")
			}
			got, err := os.ReadFile(path)
			if err != nil || !byteutil.Equal(got, want) {
				t.Fatalf("installed = %q, want %q (%v)", got, want, err)
			}
			backups, _ := filepath.Glob(filepath.Join(root, ".fleetty-update-*", "fleetty.previous"))
			if installed {
				if len(backups) != 1 {
					t.Fatal("missing backup")
				}
				backup, _ := os.ReadFile(backups[0])
				if !byteutil.Equal(backup, old) {
					t.Fatal("wrong backup")
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0o751 {
					t.Fatal("permissions changed")
				}
				if probes != 1 || downloads != 1 {
					t.Fatal("missing download/probe")
				}
			} else if len(backups) != 0 {
				t.Fatal("unexpected backup")
			}
			if test.check || test.same {
				if downloads != 0 || probes != 0 {
					t.Fatal("check downloaded/executed a binary")
				}
				files, _ := os.ReadDir(root)
				if len(files) != 1 {
					t.Fatal("read-only check wrote files")
				}
			}
			if test.corrupt && probes != 0 {
				t.Fatal("executed unverified binary")
			}
			if test.symlink {
				info, _ := os.Lstat(input)
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("replaced symlink")
				}
			}
		})
	}
}

func TestUpdateCommandArguments(t *testing.T) {
	for _, args := range [][]string{{"--version", "../bad"}, {"--version", ""}, {"extra"}, {"--unknown"}} {
		if err := runUpdateCommand(args, &byteutil.Buffer{}, &byteutil.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := runUpdateCommand([]string{"--help"}, &byteutil.Buffer{}, &byteutil.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var help byteutil.Buffer
	writeOperationsUsage(&help)
	if !strings.Contains(help.String(), "fleetty update [--check]") {
		t.Fatal("missing update help")
	}
}

func TestProbeUpdateBinary(t *testing.T) {
	for _, test := range []struct {
		name, response string
		bad            bool
	}{
		{"valid", fmt.Sprintf(`{"version":"v0.1.4","os":%q,"arch":%q}`, runtime.GOOS, runtime.GOARCH), false},
		{"wrong version", fmt.Sprintf(`{"version":"v0.0.0","os":%q,"arch":%q}`, runtime.GOOS, runtime.GOARCH), true},
		{"wrong platform", `{"version":"v0.1.4","os":"other","arch":"other"}`, true},
		{"invalid JSON", "invalid", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleetty")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+test.response+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			err := probeUpdateBinary(context.Background(), path, "v0.1.4")
			if (err != nil) != test.bad {
				t.Fatalf("probe = %v", err)
			}
		})
	}
}
