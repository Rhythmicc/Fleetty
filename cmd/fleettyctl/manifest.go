package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestVersion     = 1
	defaultParallelism  = 4
	maxParallelism      = 16
	defaultTimeout      = 15
	maxTimeout          = 120
	maxManifestTargets  = 512
	maxManifestFileSize = 4 << 20
	maxConfigEntrySize  = 16 << 20
)

type fleetManifest struct {
	Version        int              `json:"version"`
	Binary         string           `json:"binary"`
	Parallel       int              `json:"parallel,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
	Targets        []manifestTarget `json:"targets"`
	baseDir        string
}

type manifestTarget struct {
	Name      string `json:"name"`
	SSH       string `json:"ssh"`
	Role      string `json:"role"`
	Scope     string `json:"scope,omitempty"`
	Binary    string `json:"binary,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"`
	Become    string `json:"become,omitempty"`
}

type resolvedTarget struct {
	Index          int
	Name           string
	SSH            string
	Role           string
	Scope          string
	Binary         string
	ConfigDir      string
	Become         string
	TimeoutSeconds int
}

func loadManifest(path string) (fleetManifest, []resolvedTarget, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return fleetManifest{}, nil, errors.New("manifest path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fleetManifest{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return fleetManifest{}, nil, errors.New("manifest is not a regular file")
	}
	if info.Size() > maxManifestFileSize {
		return fleetManifest{}, nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestFileSize)
	}
	file, err := os.Open(path)
	if err != nil {
		return fleetManifest{}, nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestFileSize+1))
	decoder.DisallowUnknownFields()
	var manifest fleetManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fleetManifest{}, nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fleetManifest{}, nil, err
	}
	manifest.baseDir, err = filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fleetManifest{}, nil, err
	}
	targets, err := manifest.validateAndResolve()
	return manifest, targets, err
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("manifest contains multiple JSON values")
}

func (manifest *fleetManifest) validateAndResolve() ([]resolvedTarget, error) {
	if manifest.Version != manifestVersion {
		return nil, fmt.Errorf("unsupported manifest version %d; expected %d", manifest.Version, manifestVersion)
	}
	if len(manifest.Targets) == 0 {
		return nil, errors.New("manifest has no targets")
	}
	if len(manifest.Targets) > maxManifestTargets {
		return nil, fmt.Errorf("manifest has more than %d targets", maxManifestTargets)
	}
	if manifest.Parallel == 0 {
		manifest.Parallel = defaultParallelism
	}
	if manifest.Parallel < 1 || manifest.Parallel > maxParallelism {
		return nil, fmt.Errorf("parallel must be between 1 and %d", maxParallelism)
	}
	if manifest.TimeoutSeconds == 0 {
		manifest.TimeoutSeconds = defaultTimeout
	}
	if manifest.TimeoutSeconds < 1 || manifest.TimeoutSeconds > maxTimeout {
		return nil, fmt.Errorf("timeout_seconds must be between 1 and %d", maxTimeout)
	}
	globalBinary, err := resolveRegularFile(manifest.baseDir, manifest.Binary, "binary")
	if err != nil && strings.TrimSpace(manifest.Binary) != "" {
		return nil, err
	}
	seen := make(map[string]struct{}, len(manifest.Targets))
	targets := make([]resolvedTarget, 0, len(manifest.Targets))
	for index, target := range manifest.Targets {
		target.Name = strings.TrimSpace(target.Name)
		target.SSH = strings.TrimSpace(target.SSH)
		target.Role = strings.ToLower(strings.TrimSpace(target.Role))
		target.Scope = strings.ToLower(strings.TrimSpace(target.Scope))
		target.Become = strings.ToLower(strings.TrimSpace(target.Become))
		if target.Name == "" || !safeLabel(target.Name) {
			return nil, fmt.Errorf("target %d has invalid name %q", index+1, target.Name)
		}
		if _, exists := seen[target.Name]; exists {
			return nil, fmt.Errorf("duplicate target name %q", target.Name)
		}
		seen[target.Name] = struct{}{}
		if !safeSSHTarget(target.SSH) {
			return nil, fmt.Errorf("target %q has invalid ssh destination %q", target.Name, target.SSH)
		}
		if target.Role != "node" && target.Role != "hub" {
			return nil, fmt.Errorf("target %q role must be node or hub", target.Name)
		}
		if target.Scope == "" {
			target.Scope = "system"
		}
		if target.Scope != "system" && target.Scope != "user" {
			return nil, fmt.Errorf("target %q scope must be system or user", target.Name)
		}
		if target.Become == "" {
			if target.Scope == "user" {
				target.Become = "none"
			} else {
				target.Become = "sudo"
			}
		}
		if target.Become != "sudo" && target.Become != "none" {
			return nil, fmt.Errorf("target %q become must be sudo or none", target.Name)
		}
		if target.Scope == "user" && target.Become != "none" {
			return nil, fmt.Errorf("target %q user scope must use become none", target.Name)
		}
		binary := globalBinary
		if strings.TrimSpace(target.Binary) != "" {
			binary, err = resolveRegularFile(manifest.baseDir, target.Binary, "binary for "+target.Name)
			if err != nil {
				return nil, err
			}
		}
		if binary == "" {
			return nil, fmt.Errorf("target %q has no binary", target.Name)
		}
		configDir := ""
		if strings.TrimSpace(target.ConfigDir) != "" {
			configDir, err = resolveConfigDirectory(manifest.baseDir, target.ConfigDir)
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", target.Name, err)
			}
		}
		targets = append(targets, resolvedTarget{
			Index: index, Name: target.Name, SSH: target.SSH, Role: target.Role, Scope: target.Scope,
			Binary: binary, ConfigDir: configDir, Become: target.Become,
			TimeoutSeconds: manifest.TimeoutSeconds,
		})
	}
	return targets, nil
}

func resolveRegularFile(base, value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", label)
	}
	return path, nil
}

func resolveConfigDirectory(base, value string) (string, error) {
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("config_dir: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("config_dir is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("config_dir must not be accessible by group or others")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	if len(entries) > 128 {
		return "", errors.New("config_dir has more than 128 entries")
	}
	for _, entry := range entries {
		if !safeConfigName(entry.Name()) {
			return "", fmt.Errorf("invalid config filename %q", entry.Name())
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return "", infoErr
		}
		if !entryInfo.Mode().IsRegular() {
			return "", fmt.Errorf("config entry %q is not a regular file", entry.Name())
		}
		if entryInfo.Size() > maxConfigEntrySize {
			return "", fmt.Errorf("config entry %q exceeds %d bytes", entry.Name(), maxConfigEntrySize)
		}
		if entryInfo.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("config entry %q must not be accessible by group or others", entry.Name())
		}
	}
	return path, nil
}

func safeLabel(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
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

func safeSSHTarget(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n:") {
		return false
	}
	parts := strings.Split(value, "@")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !safeLabel(part) {
			return false
		}
	}
	return true
}

func safeConfigName(value string) bool {
	return safeLabel(value) && value != "." && value != ".." && !strings.HasPrefix(value, ".")
}
