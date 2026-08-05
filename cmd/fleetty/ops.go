package main

import (
	byteutil "bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	deployassets "github.com/Rhythmicc/fleetty/deploy"
	"github.com/Rhythmicc/fleetty/internal/buildinfo"
)

const (
	systemFleettyBinaryPath = "/opt/fleetty/fleetty"
	systemFleettyConfigDir  = "/etc/fleetty"
	systemdUnitDir          = "/etc/systemd/system"
	maxConfigFiles          = 128
	maxConfigFileSize       = 16 << 20
)

type commandResult struct {
	Role         string   `json:"role"`
	Scope        string   `json:"scope"`
	Service      string   `json:"service"`
	Changed      bool     `json:"changed"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	State        string   `json:"state"`
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorResult struct {
	Role    string         `json:"role"`
	Scope   string         `json:"scope"`
	Healthy bool           `json:"healthy"`
	Build   buildinfo.Info `json:"build"`
	Checks  []doctorCheck  `json:"checks"`
}

type systemCommandRunner func(name string, args ...string) ([]byte, error)

type deploymentLayout struct {
	Scope         string
	BinaryPath    string
	ConfigPath    string
	UnitPath      string
	SystemctlArgs []string
	OwnerUID      int
	OwnerGID      int
}

func resolveDeploymentLayout(scope string) (deploymentLayout, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" || scope == "auto" {
		if os.Geteuid() == 0 {
			scope = "system"
		} else {
			scope = "user"
		}
	}
	switch scope {
	case "system":
		if os.Geteuid() != 0 {
			return deploymentLayout{}, errors.New("system installation requires root; use --scope user")
		}
		return deploymentLayout{
			Scope: "system", BinaryPath: systemFleettyBinaryPath,
			ConfigPath: systemFleettyConfigDir, UnitPath: systemdUnitDir,
			OwnerUID: 0, OwnerGID: 0,
		}, nil
	case "user":
		if os.Geteuid() == 0 {
			return deploymentLayout{}, errors.New("user installation must run as the intended service user, without sudo")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return deploymentLayout{}, fmt.Errorf("resolve user home: %w", err)
		}
		if strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
			return deploymentLayout{}, errors.New("user home must be an absolute path")
		}
		return deploymentLayout{
			Scope:         "user",
			BinaryPath:    filepath.Join(home, ".local", "bin", "fleetty"),
			ConfigPath:    filepath.Join(home, ".config", "fleetty"),
			UnitPath:      filepath.Join(home, ".config", "systemd", "user"),
			SystemctlArgs: []string{"--user"},
			OwnerUID:      os.Geteuid(), OwnerGID: os.Getegid(),
		}, nil
	default:
		return deploymentLayout{}, fmt.Errorf("scope must be auto, user, or system")
	}
}

func runOperations(args []string, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 || args[0] == "serve" {
		if len(args) > 1 {
			return true, errors.New("serve does not accept arguments")
		}
		return false, nil
	}
	switch args[0] {
	case "top":
		return true, runTopCommand(args[1:], os.Stdin, stdout, stderr)
	case "dedupe-link":
		return true, runStorageDedupeLinkCommand(args[1:], stdout, stderr)
	case "privileged-helper":
		return true, runPrivilegedHelperCommand(args[1:], stdout, stderr)
	case "version":
		flags := flag.NewFlagSet("version", flag.ContinueOnError)
		flags.SetOutput(stderr)
		asJSON := flags.Bool("json", false, "write machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return true, err
		}
		if flags.NArg() != 0 {
			return true, errors.New("version does not accept positional arguments")
		}
		return true, buildinfo.Write(stdout, *asJSON)
	case "install":
		return true, runInstallCommand(args[1:], stdout, stderr)
	case "doctor":
		return true, runDoctorCommand(args[1:], stdout, stderr)
	case "snapshot":
		return true, runSnapshotCommand(args[1:], stdout, stderr)
	case "metrics":
		return true, runMetricsCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		writeOperationsUsage(stdout)
		return true, nil
	default:
		writeOperationsUsage(stderr)
		return true, fmt.Errorf("unknown command %q", args[0])
	}
}

func writeOperationsUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Fleetty

Usage:
  fleetty top [--config PATH] [--theme dark|light] [--layout PATH]
  fleetty dedupe-link --keep PATH --replace PATH --sha256 HEX
  fleetty serve
  fleetty privileged-helper [--socket PATH] [--group NAME] [--service fleetty.service]
  fleetty install --role node|hub|privileged-helper [--scope auto|user|system] [--config-dir PATH] [--json]
  fleetty doctor --role node|hub|privileged-helper [--scope auto|user|system] [--json]
  fleetty snapshot [--config PATH] [--processes]
  fleetty metrics [--config PATH]
  fleetty version [--json]`)
}

func runInstallCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	role := flags.String("role", "", "installation role: node, hub, or privileged-helper")
	scope := flags.String("scope", "auto", "installation scope: auto, user, or system")
	configDir := flags.String("config-dir", "", "staged flat configuration directory")
	asJSON := flags.Bool("json", false, "write machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("install does not accept positional arguments")
	}
	layout, err := resolveDeploymentLayout(*scope)
	if err != nil {
		return err
	}
	result, err := installFleetty(installOptions{
		Role: *role, ConfigSource: *configDir,
		ExecutableSource: executablePath(),
		Scope:            layout.Scope, BinaryPath: layout.BinaryPath,
		ConfigPath: layout.ConfigPath, UnitPath: layout.UnitPath,
		SystemctlArgs: layout.SystemctlArgs,
		OwnerUID:      layout.OwnerUID, OwnerGID: layout.OwnerGID,
		EnforcePlatform: true,
		Run:             runSystemCommand,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	change := "unchanged"
	if result.Changed {
		change = "service state updated"
		if len(result.ChangedFiles) > 0 {
			change = "updated: " + strings.Join(result.ChangedFiles, ", ")
		}
	}
	_, err = fmt.Fprintf(stdout, "Fleetty %s installed for %s scope (%s, %s, %s)\n",
		result.Role, result.Scope, result.Service, result.State, change)
	return err
}

type installOptions struct {
	Role             string
	Scope            string
	ConfigSource     string
	ExecutableSource string
	BinaryPath       string
	ConfigPath       string
	UnitPath         string
	SystemctlArgs    []string
	OwnerUID         int
	OwnerGID         int
	EnforcePlatform  bool
	Run              systemCommandRunner
}

func installFleetty(options installOptions) (commandResult, error) {
	if runtime.GOOS != "linux" && options.EnforcePlatform {
		return commandResult{}, errors.New("installation is supported only on Linux")
	}
	options.Role = strings.TrimSpace(options.Role)
	if options.Scope == "" {
		options.Scope = "system"
		options.OwnerUID, options.OwnerGID = -1, -1
	}
	unit, service, err := deployassets.ServiceUnit(options.Role, options.Scope)
	if err != nil {
		return commandResult{}, err
	}
	if options.Run == nil {
		options.Run = runSystemCommand
	}
	if options.ExecutableSource == "" {
		return commandResult{}, errors.New("executable source is empty")
	}
	configFiles, err := readStagedConfig(options.ConfigSource)
	if err != nil {
		return commandResult{}, err
	}
	if err := ensureManagedDirectory(filepath.Dir(options.BinaryPath), 0o755, options.OwnerUID, options.OwnerGID); err != nil {
		return commandResult{}, err
	}
	if err := ensureManagedDirectory(options.ConfigPath, 0o700, options.OwnerUID, options.OwnerGID); err != nil {
		return commandResult{}, err
	}
	if err := ensureManagedDirectory(options.UnitPath, 0o755, options.OwnerUID, options.OwnerGID); err != nil {
		return commandResult{}, err
	}

	executable, err := os.ReadFile(options.ExecutableSource)
	if err != nil {
		return commandResult{}, fmt.Errorf("read executable: %w", err)
	}
	transaction := &fileTransaction{}
	replace := func(path string, data []byte, mode os.FileMode, label string) error {
		changed, replaceErr := transaction.Replace(path, data, mode, options.OwnerUID, options.OwnerGID)
		if changed {
			transaction.changedFiles = append(transaction.changedFiles, label)
		}
		return replaceErr
	}
	if err := replace(options.BinaryPath, executable, 0o755, options.BinaryPath); err != nil {
		transaction.Rollback()
		return commandResult{}, err
	}
	unitDestination := filepath.Join(options.UnitPath, service)
	if err := replace(unitDestination, unit, 0o644, unitDestination); err != nil {
		transaction.Rollback()
		return commandResult{}, err
	}
	for _, config := range configFiles {
		destination := filepath.Join(options.ConfigPath, config.Name)
		if err := replace(destination, config.Data, 0o600, destination); err != nil {
			transaction.Rollback()
			return commandResult{}, err
		}
	}

	systemctl := func(arguments ...string) ([]byte, error) {
		return options.Run("systemctl", append(append([]string(nil), options.SystemctlArgs...), arguments...)...)
	}
	systemctlSucceeds := func(arguments ...string) bool {
		_, commandErr := systemctl(arguments...)
		return commandErr == nil
	}
	wasActive := systemctlSucceeds("is-active", "--quiet", service)
	wasEnabled := systemctlSucceeds("is-enabled", "--quiet", service)
	restore := func() {
		transaction.Rollback()
		_, _ = systemctl("daemon-reload")
		if wasEnabled {
			_, _ = systemctl("enable", service)
		} else {
			_, _ = systemctl("disable", service)
		}
		if wasActive {
			_, _ = systemctl("restart", service)
		} else {
			_, _ = systemctl("stop", service)
		}
	}
	if output, runErr := systemctl("daemon-reload"); runErr != nil {
		restore()
		return commandResult{}, commandFailure("systemctl daemon-reload", output, runErr)
	}
	if output, runErr := systemctl("enable", service); runErr != nil {
		restore()
		return commandResult{}, commandFailure("enable "+service, output, runErr)
	}
	filesChanged := len(transaction.changedFiles) > 0
	var actionOutput []byte
	var actionErr error
	action := ""
	switch {
	case !wasActive:
		action = "start"
		actionOutput, actionErr = systemctl("start", service)
	case filesChanged:
		action = "restart"
		actionOutput, actionErr = systemctl("restart", service)
	}
	if actionErr != nil {
		restore()
		return commandResult{}, commandFailure(action+" "+service, actionOutput, actionErr)
	}
	if !systemctlSucceeds("is-active", "--quiet", service) {
		restore()
		return commandResult{}, fmt.Errorf("%s did not become active; installation rolled back", service)
	}
	if err := transaction.Commit(); err != nil {
		return commandResult{}, err
	}
	return commandResult{
		Role: options.Role, Scope: options.Scope, Service: service,
		Changed:      filesChanged || !wasActive || !wasEnabled,
		ChangedFiles: transaction.changedFiles, State: "active",
	}, nil
}

type stagedConfig struct {
	Name string
	Data []byte
}

func readStagedConfig(path string) ([]stagedConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("open config directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("config source is not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxConfigFiles {
		return nil, fmt.Errorf("config directory contains more than %d entries", maxConfigFiles)
	}
	result := make([]stagedConfig, 0, len(entries))
	for _, entry := range entries {
		if !validConfigName(entry.Name()) {
			return nil, fmt.Errorf("invalid config filename %q", entry.Name())
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if !entryInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("config entry %q is not a regular file", entry.Name())
		}
		if entryInfo.Size() > maxConfigFileSize {
			return nil, fmt.Errorf("config entry %q exceeds %d bytes", entry.Name(), maxConfigFileSize)
		}
		data, readErr := os.ReadFile(filepath.Join(path, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, stagedConfig{Name: entry.Name(), Data: data})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func validConfigName(name string) bool {
	if name == "" || name == "." || name == ".." ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

type fileReplacement struct {
	Path      string
	Backup    string
	HadBackup bool
}

type fileTransaction struct {
	replacements []fileReplacement
	changedFiles []string
}

func (transaction *fileTransaction) Replace(
	path string,
	data []byte,
	mode os.FileMode,
	ownerUID, ownerGID int,
) (bool, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("refusing to replace non-regular file %s", path)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return false, readErr
		}
		ownershipMatches := ownerUID < 0 || ownedBy(info, ownerUID, ownerGID)
		if byteutil.Equal(existing, data) && info.Mode().Perm() == mode.Perm() && ownershipMatches {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fleetty-install-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		cleanup()
		return false, err
	}
	if ownerUID >= 0 && (ownerUID != os.Geteuid() || ownerGID != os.Getegid()) {
		if err = temporary.Chown(ownerUID, ownerGID); err != nil {
			_ = temporary.Close()
			cleanup()
			return false, err
		}
	}
	if err = temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		cleanup()
		return false, err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return false, err
	}
	if err = temporary.Close(); err != nil {
		cleanup()
		return false, err
	}
	replacement := fileReplacement{Path: path}
	if _, err = os.Lstat(path); err == nil {
		replacement.Backup = fmt.Sprintf("%s.fleetty-backup-%d", path, os.Getpid())
		if _, backupErr := os.Lstat(replacement.Backup); backupErr == nil {
			cleanup()
			return false, fmt.Errorf("backup path already exists: %s", replacement.Backup)
		} else if !errors.Is(backupErr, os.ErrNotExist) {
			cleanup()
			return false, backupErr
		}
		if err = os.Rename(path, replacement.Backup); err != nil {
			cleanup()
			return false, err
		}
		replacement.HadBackup = true
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return false, err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		if replacement.HadBackup {
			_ = os.Rename(replacement.Backup, path)
		}
		cleanup()
		return false, err
	}
	transaction.replacements = append(transaction.replacements, replacement)
	return true, nil
}

func ensureManagedDirectory(path string, mode os.FileMode, ownerUID, ownerGID int) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if ownerUID >= 0 && !ownedBy(info, ownerUID, ownerGID) {
		if os.Geteuid() != 0 {
			return fmt.Errorf("%s is not owned by the current user", path)
		}
		if err := os.Chown(path, ownerUID, ownerGID); err != nil {
			return err
		}
	}
	return nil
}

func ownedBy(info os.FileInfo, uid, gid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid && int(stat.Gid) == gid
}

func (transaction *fileTransaction) Rollback() {
	for index := len(transaction.replacements) - 1; index >= 0; index-- {
		replacement := transaction.replacements[index]
		_ = os.Remove(replacement.Path)
		if replacement.HadBackup {
			_ = os.Rename(replacement.Backup, replacement.Path)
		}
	}
}

func (transaction *fileTransaction) Commit() error {
	var failures []string
	for _, replacement := range transaction.replacements {
		if replacement.HadBackup {
			if err := os.Remove(replacement.Backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err.Error())
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func runDoctorCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	role := flags.String("role", "", "installation role: node, hub, or privileged-helper")
	scope := flags.String("scope", "auto", "installation scope: auto, user, or system")
	asJSON := flags.Bool("json", false, "write machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
	}
	layout, err := resolveDeploymentLayout(*scope)
	if err != nil {
		return err
	}
	result, err := doctorFleetty(strings.TrimSpace(*role), layout, runSystemCommand)
	if *asJSON {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
	} else {
		for _, check := range result.Checks {
			fmt.Fprintf(stdout, "%-5s %-22s %s\n", strings.ToUpper(check.Status), check.Name, check.Message)
		}
	}
	return err
}

func doctorFleetty(role string, layout deploymentLayout, runner systemCommandRunner) (doctorResult, error) {
	_, service, err := deployassets.ServiceUnit(role, layout.Scope)
	if err != nil {
		return doctorResult{}, err
	}
	result := doctorResult{Role: role, Scope: layout.Scope, Healthy: true, Build: buildinfo.Current()}
	add := func(name, status, message string) {
		result.Checks = append(result.Checks, doctorCheck{Name: name, Status: status, Message: message})
		if status == "fail" {
			result.Healthy = false
		}
	}
	if runtime.GOOS == "linux" {
		add("operating-system", "pass", runtime.GOOS+"/"+runtime.GOARCH)
	} else {
		add("operating-system", "fail", runtime.GOOS+"/"+runtime.GOARCH+" is not supported for deployment")
	}
	checkRegularFile := func(name, path string, executable bool) {
		info, statErr := os.Lstat(path)
		switch {
		case statErr != nil:
			add(name, "fail", statErr.Error())
		case !info.Mode().IsRegular():
			add(name, "fail", path+" is not a regular file")
		case executable && info.Mode().Perm()&0o111 == 0:
			add(name, "fail", path+" is not executable")
		case !ownedBy(info, layout.OwnerUID, layout.OwnerGID):
			add(name, "fail", path+" has an unexpected owner")
		default:
			add(name, "pass", path)
		}
	}
	checkRegularFile("binary", layout.BinaryPath, true)
	checkRegularFile("systemd-unit", filepath.Join(layout.UnitPath, service), false)
	if info, statErr := os.Lstat(layout.ConfigPath); statErr != nil {
		add("config-directory", "fail", statErr.Error())
	} else if !info.IsDir() || info.Mode().Perm()&0o077 != 0 ||
		!ownedBy(info, layout.OwnerUID, layout.OwnerGID) {
		add("config-directory", "fail", "must be owner-only and owned by the service account")
	} else {
		add("config-directory", "pass", layout.ConfigPath)
	}
	systemctl := func(arguments ...string) ([]byte, error) {
		return runner("systemctl", append(append([]string(nil), layout.SystemctlArgs...), arguments...)...)
	}
	systemctlSucceeds := func(arguments ...string) bool {
		_, commandErr := systemctl(arguments...)
		return commandErr == nil
	}
	if systemctlSucceeds("is-active", "--quiet", service) {
		add("service-active", "pass", service)
	} else {
		add("service-active", "fail", service+" is not active")
	}
	if systemctlSucceeds("is-enabled", "--quiet", service) {
		add("service-enabled", "pass", service)
	} else {
		add("service-enabled", "fail", service+" is not enabled")
	}
	if references, scanErr := legacyConfigReferences(layout.ConfigPath); scanErr != nil {
		add("configuration", "fail", scanErr.Error())
	} else if len(references) > 0 {
		add("configuration", "fail", "legacy references: "+strings.Join(references, ", "))
	} else {
		add("configuration", "pass", "no legacy deployment paths")
	}
	if role == "privileged-helper" {
		socketPath := "/run/fleetty/privileged.sock"
		info, statErr := os.Lstat(socketPath)
		switch {
		case statErr != nil:
			add("privileged-socket", "fail", statErr.Error())
		case info.Mode()&os.ModeSocket == 0:
			add("privileged-socket", "fail", socketPath+" is not a Unix socket")
		default:
			connection, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
			if dialErr != nil {
				add("privileged-socket", "fail", dialErr.Error())
			} else {
				_ = connection.Close()
				add("privileged-socket", "pass", socketPath)
			}
		}
		if !result.Healthy {
			return result, errors.New("Fleetty doctor found failed checks")
		}
		return result, nil
	}
	switch role {
	case "node":
		machinePath := filepath.Join(layout.ConfigPath, "machine.json")
		if _, statErr := os.Stat(machinePath); errors.Is(statErr, os.ErrNotExist) {
			add("config-schema", "pass", "default GPU node profile")
		} else if statErr != nil {
			add("config-schema", "fail", statErr.Error())
		} else if _, loadErr := loadMachineConfig(machinePath); loadErr != nil {
			add("config-schema", "fail", loadErr.Error())
		} else {
			add("config-schema", "pass", machinePath)
		}
	case "hub":
		hubPath := filepath.Join(layout.ConfigPath, "nodes.json")
		if _, loadErr := loadHubConfig(hubPath); loadErr != nil {
			add("config-schema", "fail", loadErr.Error())
		} else {
			add("config-schema", "pass", hubPath)
		}
	}
	port := "23234"
	host := "127.0.0.1"
	if role == "hub" {
		port = "23235"
	}
	hostKey := filepath.Join(layout.ConfigPath, "ssh_host_ed25519")
	if role == "hub" {
		hostKey = filepath.Join(layout.ConfigPath, "hub_host_ed25519")
	}
	environment := effectiveServiceEnvironment(systemctl, service)
	if value := strings.TrimSpace(environment["SSH_PORT"]); value != "" {
		port = value
	}
	if value := strings.TrimSpace(environment["SSH_HOST"]); value != "" &&
		value != "0.0.0.0" && value != "::" {
		host = value
	}
	if value := strings.TrimSpace(environment["SSH_HOST_KEY_PATH"]); value != "" {
		hostKey = value
	}
	address := net.JoinHostPort(host, port)
	var dialErr error
	for attempt := 0; attempt < 20; attempt++ {
		var connection net.Conn
		connection, dialErr = net.DialTimeout("tcp", address, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dialErr != nil {
		add("ssh-port", "fail", address+": "+dialErr.Error())
	} else {
		add("ssh-port", "pass", address)
	}
	checkRegularFile("ssh-host-key", hostKey, false)
	if role == "node" {
		if _, lookupErr := exec.LookPath("nvidia-smi"); lookupErr != nil {
			add("nvidia-smi", "warn", "not installed; GPU metrics will be unavailable")
		} else {
			add("nvidia-smi", "pass", "available")
		}
	}
	if layout.Scope == "user" {
		output, lingerErr := runner(
			"loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property", "Linger", "--value",
		)
		if lingerErr != nil {
			add("user-linger", "warn", "could not determine whether the user service survives logout")
		} else if strings.TrimSpace(string(output)) == "yes" {
			add("user-linger", "pass", "enabled")
		} else {
			add("user-linger", "warn", "disabled; the service may stop after the last login session ends")
		}
	}
	if !result.Healthy {
		return result, errors.New("Fleetty doctor found failed checks")
	}
	return result, nil
}

func effectiveServiceEnvironment(
	systemctl func(...string) ([]byte, error),
	service string,
) map[string]string {
	result := make(map[string]string)
	if output, err := systemctl("show", service, "--property", "Environment", "--value"); err == nil {
		for _, field := range strings.Fields(string(output)) {
			name, value, found := strings.Cut(field, "=")
			if found && name != "" {
				result[name] = strings.Trim(value, `"'`)
			}
		}
	}
	output, err := systemctl("show", service, "--property", "MainPID", "--value")
	if err != nil {
		return result
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		return result
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return result
	}
	for _, field := range strings.Split(string(data), "\x00") {
		name, value, found := strings.Cut(field, "=")
		if found && name != "" {
			result[name] = value
		}
	}
	return result
}

func legacyConfigReferences(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".env") && !strings.HasSuffix(name, ".json") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(path, name))
		if readErr != nil {
			return nil, readErr
		}
		value := string(data)
		if strings.Contains(value, "/etc/gpu-ssh-monitor") ||
			strings.Contains(value, "gpu-ssh-monitor.service") ||
			strings.Contains(value, "ADMIN_RESTART_MONITOR_CMD") {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result, nil
}

func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return path
}

func runSystemCommand(name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	return command.CombinedOutput()
}

func commandSucceeds(runner systemCommandRunner, name string, args ...string) bool {
	_, err := runner(name, args...)
	return err == nil
}

func commandFailure(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}
