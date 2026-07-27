package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/log/v2"
	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const (
	nodeRPCUser                  = "fleetty-hub"
	defaultSSHConnectionLimit    = 64
	defaultSSHIdleTimeout        = 30 * time.Minute
	defaultSSHMaxTimeout         = 24 * time.Hour
	maxManagementAuthFailures    = 5
	managementAuthLockout        = 30 * time.Second
	redactedProcessDetailMessage = "[hidden in read-only mode]"
)

type sshAccessConfig struct {
	interactiveKeys map[string]struct{}
	rpcKeys         map[string]struct{}
	allowAnonymous  bool
	connectionLimit int
	idleTimeout     time.Duration
	maxTimeout      time.Duration
}

func loadSSHAccessConfig() (sshAccessConfig, error) {
	config := sshAccessConfig{
		allowAnonymous:  envBool("SSH_ALLOW_ANONYMOUS", false),
		connectionLimit: envInt("SSH_MAX_CONNECTIONS", defaultSSHConnectionLimit, 1, 10_000),
		idleTimeout:     envDuration("SSH_IDLE_TIMEOUT", defaultSSHIdleTimeout),
		maxTimeout:      envDuration("SSH_MAX_TIMEOUT", defaultSSHMaxTimeout),
	}
	interactivePath := strings.TrimSpace(os.Getenv("SSH_AUTHORIZED_KEYS_FILE"))
	rpcPath := strings.TrimSpace(os.Getenv("NODE_RPC_AUTHORIZED_KEYS_FILE"))
	if config.allowAnonymous && (interactivePath != "" || rpcPath != "") {
		return sshAccessConfig{}, errors.New("SSH_ALLOW_ANONYMOUS cannot be combined with authorized-key files")
	}
	var err error
	if interactivePath != "" {
		config.interactiveKeys, err = loadAuthorizedKeySet(interactivePath)
		if err != nil {
			return sshAccessConfig{}, fmt.Errorf("load SSH authorized keys: %w", err)
		}
	}
	if rpcPath != "" {
		config.rpcKeys, err = loadAuthorizedKeySet(rpcPath)
		if err != nil {
			return sshAccessConfig{}, fmt.Errorf("load Hub RPC authorized keys: %w", err)
		}
	}
	if !config.allowAnonymous && len(config.interactiveKeys) == 0 {
		return sshAccessConfig{}, errors.New(
			"SSH client authentication is required: configure SSH_AUTHORIZED_KEYS_FILE " +
				"or explicitly set SSH_ALLOW_ANONYMOUS=true for an isolated migration",
		)
	}
	return config, nil
}

func loadAuthorizedKeySet(path string) (map[string]struct{}, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("authorized keys path is not a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("authorized keys file must not be group- or world-writable (mode %04o)", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{})
	for len(strings.TrimSpace(string(data))) > 0 {
		key, _, _, rest, parseErr := gossh.ParseAuthorizedKey(data)
		if parseErr != nil {
			return nil, parseErr
		}
		keys[gossh.FingerprintSHA256(key)] = struct{}{}
		if len(rest) >= len(data) {
			return nil, errors.New("authorized keys parser made no progress")
		}
		data = rest
	}
	if len(keys) == 0 {
		return nil, errors.New("authorized keys file contains no public keys")
	}
	return keys, nil
}

func loadPrivateKeySigner(path string) (gossh.Signer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("identity file is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("identity file must not be accessible by group or others (mode %04o)", info.Mode().Perm())
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	return signer, nil
}

func (c sshAccessConfig) options() []ssh.Option {
	options := []ssh.Option{
		ssh.WrapConn(newConnectionLimiter(c.connectionLimit).wrap),
	}
	if c.idleTimeout > 0 {
		options = append(options, func(server *ssh.Server) error {
			server.IdleTimeout = c.idleTimeout
			return nil
		})
	}
	if c.maxTimeout > 0 {
		options = append(options, func(server *ssh.Server) error {
			server.MaxTimeout = c.maxTimeout
			return nil
		})
	}
	if c.allowAnonymous {
		return options
	}
	options = append(options, ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
		allowed := c.interactiveKeys
		if ctx.User() == nodeRPCUser {
			allowed = c.rpcKeys
		}
		fingerprint := gossh.FingerprintSHA256(key)
		_, ok := allowed[fingerprint]
		if ok {
			if ctx.Permissions().Extensions == nil {
				ctx.Permissions().Extensions = make(map[string]string)
			}
			ctx.Permissions().Extensions["key_fingerprint"] = fingerprint
		}
		return ok
	}))
	return options
}

type connectionLimiter struct {
	max    int64
	active atomic.Int64
}

func newConnectionLimiter(maximum int) *connectionLimiter {
	return &connectionLimiter{max: int64(maximum)}
}

func (l *connectionLimiter) wrap(_ ssh.Context, connection net.Conn) net.Conn {
	if l.active.Add(1) > l.max {
		l.active.Add(-1)
		log.Warn("SSH connection limit reached", "limit", l.max, "remote", connection.RemoteAddr())
		return nil
	}
	return &limitedConnection{
		Conn: connection,
		release: func() {
			l.active.Add(-1)
		},
	}
}

type limitedConnection struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConnection) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return min(max(value, minimum), maximum)
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return fallback
	}
	return duration
}

func redactProcessCommandLine(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return value
	}
	redacted := make([]string, 0, len(fields))
	hideNext := false
	for _, field := range fields {
		if hideNext {
			redacted = append(redacted, "[REDACTED]")
			hideNext = false
			continue
		}
		name, fieldValue, hasValue := strings.Cut(field, "=")
		if isSensitiveArgumentName(name) {
			if hasValue {
				redacted = append(redacted, name+"=[REDACTED]")
				hideNext = fieldValue == ""
			} else {
				redacted = append(redacted, field)
				hideNext = true
			}
			continue
		}
		if hasValue {
			redacted = append(redacted, name+"="+redactURLSecrets(fieldValue))
		} else {
			redacted = append(redacted, redactURLSecrets(field))
		}
	}
	return strings.Join(redacted, " ")
}

func redactURLSecrets(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	if parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if isSensitiveArgumentName(key) {
			query.Set(key, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func isSensitiveArgumentName(value string) bool {
	value = strings.ToLower(strings.TrimLeft(value, "-"))
	replacer := strings.NewReplacer("_", "", "-", "", ".", "")
	value = replacer.Replace(value)
	for _, marker := range []string{
		"password", "passwd", "secret", "token", "apikey", "accesskey",
		"privatekey", "credential", "authorization",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
