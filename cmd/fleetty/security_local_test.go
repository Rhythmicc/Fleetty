package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func localAccessTestSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestLocalAnonymousAccessConfigStillRequiresRemoteKeys(t *testing.T) {
	t.Setenv("SSH_ALLOW_ANONYMOUS", "false")
	t.Setenv("SSH_ALLOW_LOCAL_ANONYMOUS", "true")
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", "")
	t.Setenv("NODE_RPC_AUTHORIZED_KEYS_FILE", "")
	if _, err := loadSSHAccessConfig(); err == nil {
		t.Fatal("local access must not disable the required remote key configuration")
	}
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, gossh.MarshalAuthorizedKey(localAccessTestSigner(t).PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", path)
	config, err := loadSSHAccessConfig()
	if err != nil || !config.allowLocalAnonymous || config.allowAnonymous {
		t.Fatalf("config = %#v, err = %v", config, err)
	}
	t.Setenv("SSH_ALLOW_ANONYMOUS", "true")
	t.Setenv("SSH_AUTHORIZED_KEYS_FILE", "")
	if _, err := loadSSHAccessConfig(); err == nil {
		t.Fatal("global anonymous and local anonymous modes must be mutually exclusive")
	}
}

func TestLoopbackTCPAddress(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "127.0.1.2", "::1", "::ffff:127.0.0.1"} {
		if !isLoopbackTCPAddress(&net.TCPAddr{IP: net.ParseIP(value)}) {
			t.Errorf("loopback rejected: %s", value)
		}
	}
	for _, addr := range []net.Addr{
		nil, (*net.TCPAddr)(nil), &net.TCPAddr{},
		&net.TCPAddr{IP: net.ParseIP("192.0.2.10")},
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1")},
		&net.UnixAddr{Name: "127.0.0.1", Net: "unix"},
	} {
		if isLoopbackTCPAddress(addr) {
			t.Errorf("non-loopback address accepted: %v", addr)
		}
	}
}

type accessTestConn struct {
	net.Conn
	local, remote net.Addr
}

func (c accessTestConn) LocalAddr() net.Addr  { return c.local }
func (c accessTestConn) RemoteAddr() net.Addr { return c.remote }

func TestLocalAnonymousSSHHandshake(t *testing.T) {
	interactiveKey, rpcKey := localAccessTestSigner(t), localAccessTestSigner(t)
	unknownKey, hostKey := localAccessTestSigner(t), localAccessTestSigner(t)
	for _, tc := range []struct {
		name, local, remote, user string
		enabled, accepted         bool
		key                       gossh.Signer
	}{
		{"local IPv4", "127.0.0.1", "127.0.0.1", "alice", true, true, nil},
		{"local IPv6", "::1", "::1", "bob", true, true, nil},
		{"default stays strict", "127.0.0.1", "127.0.0.1", "alice", false, false, nil},
		{"remote anonymous denied", "192.0.2.20", "192.0.2.10", "alice", true, false, nil},
		{"remote IPv6 denied", "2001:db8::1", "2001:db8::2", "alice", true, false, nil},
		{"loopback source to LAN denied", "192.0.2.20", "127.0.0.1", "alice", true, false, nil},
		{"remote source to loopback denied", "127.0.0.1", "192.0.2.10", "alice", true, false, nil},
		{"remote authorized key", "192.0.2.20", "192.0.2.10", "alice", true, true, interactiveKey},
		{"remote unknown key denied", "192.0.2.20", "192.0.2.10", "alice", true, false, unknownKey},
		{"local RPC anonymous denied", "127.0.0.1", "127.0.0.1", nodeRPCUser, true, false, nil},
		{"local RPC authorized key", "127.0.0.1", "127.0.0.1", nodeRPCUser, true, true, rpcKey},
		{"interactive key cannot be RPC", "127.0.0.1", "127.0.0.1", nodeRPCUser, true, false, interactiveKey},
		{"RPC key cannot be remote interactive", "192.0.2.20", "192.0.2.10", "alice", true, false, rpcKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access := sshAccessConfig{
				interactiveKeys:     map[string]struct{}{gossh.FingerprintSHA256(interactiveKey.PublicKey()): {}},
				rpcKeys:             map[string]struct{}{gossh.FingerprintSHA256(rpcKey.PublicKey()): {}},
				allowLocalAnonymous: tc.enabled, connectionLimit: 8,
			}
			server := &ssh.Server{
				HostSigners: []ssh.Signer{hostKey},
				Handler:     func(s ssh.Session) { _, _ = io.WriteString(s, "ok:"+s.User()) },
			}
			for _, option := range access.options() {
				if err := option(server); err != nil {
					t.Fatal(err)
				}
			}
			wrap := server.ConnCallback
			server.ConnCallback = func(ctx ssh.Context, conn net.Conn) net.Conn {
				return accessTestConn{
					Conn:   wrap(ctx, conn),
					local:  &net.TCPAddr{IP: net.ParseIP(tc.local), Port: 23235},
					remote: &net.TCPAddr{IP: net.ParseIP(tc.remote), Port: 43210},
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close(); <-done })
			config := &gossh.ClientConfig{
				User: tc.user, HostKeyCallback: gossh.FixedHostKey(hostKey.PublicKey()),
				Timeout: 2 * time.Second,
			}
			if tc.key != nil {
				config.Auth = []gossh.AuthMethod{gossh.PublicKeys(tc.key)}
			}
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			sshConn, channels, requests, err := gossh.NewClientConn(conn, listener.Addr().String(), config)
			if !tc.accepted {
				if err == nil {
					_ = sshConn.Close()
					t.Fatal("unauthorized connection accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("authorized connection rejected: %v", err)
			}
			client := gossh.NewClient(sshConn, channels, requests)
			defer client.Close()
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			output, err := session.Output("probe")
			if err != nil || string(output) != "ok:"+tc.user {
				t.Fatalf("session output = %q, err = %v", output, err)
			}
		})
	}
}
