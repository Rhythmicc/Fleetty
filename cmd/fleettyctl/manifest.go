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
	maxManifestDepth    = 8
	maxManifestFileSize = 4 << 20
	maxConfigEntrySize  = 16 << 20
)

type fleetManifest struct {
	Version        int              `json:"version"`
	Binary         string           `json:"binary"`
	Release        *releaseConfig   `json:"release,omitempty"`
	Parallel       int              `json:"parallel,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
	Targets        []manifestTarget `json:"targets"`
	baseDir        string
}

type releaseConfig struct {
	BaseURL  string `json:"base_url,omitempty"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type manifestTarget struct {
	Name      string           `json:"name"`
	SSH       string           `json:"ssh"`
	Role      string           `json:"role"`
	Scope     string           `json:"scope,omitempty"`
	Binary    string           `json:"binary,omitempty"`
	ConfigDir string           `json:"config_dir,omitempty"`
	Become    string           `json:"become,omitempty"`
	Arch      string           `json:"arch,omitempty"`
	SHA256    string           `json:"sha256,omitempty"`
	Children  []manifestTarget `json:"children,omitempty"`
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
	Arch           string
	SHA256         string
	TimeoutSeconds int
	Children       []resolvedTarget
}

func loadManifest(path string) (fleetManifest, []resolvedTarget, error) {
	return loadManifestMode(path, true, false, false)
}

func loadUpdateManifest(path string) (fleetManifest, []resolvedTarget, error) {
	return loadManifestMode(path, false, true, true)
}

func loadCascadeManifest(path string) (fleetManifest, []resolvedTarget, error) {
	manifest, targets, err := loadManifestMode(path, true, false, true)
	if err != nil {
		return fleetManifest{}, nil, err
	}
	if err := requireCascadeChecksums(targets); err != nil {
		return fleetManifest{}, nil, err
	}
	return manifest, targets, nil
}

func requireCascadeChecksums(targets []resolvedTarget) error {
	for _, target := range targets {
		if target.SHA256 == "" {
			return fmt.Errorf("cascade target %q requires sha256", target.Name)
		}
		if err := requireCascadeChecksums(target.Children); err != nil {
			return err
		}
	}
	return nil
}

func loadManifestMode(path string, requireBinary, requireRelease, allowRelay bool) (fleetManifest, []resolvedTarget, error) {
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
	targets, err := manifest.validateAndResolve(requireBinary, requireRelease, allowRelay)
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

func (manifest *fleetManifest) validateAndResolve(requireBinary, requireRelease, allowRelay bool) ([]resolvedTarget, error) {
	if manifest.Version != manifestVersion {
		return nil, fmt.Errorf("unsupported manifest version %d; expected %d", manifest.Version, manifestVersion)
	}
	if len(manifest.Targets) == 0 {
		return nil, errors.New("manifest has no targets")
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
	if requireRelease && manifest.Release == nil {
		return nil, errors.New("update requires a release configuration")
	}
	if manifest.Release != nil {
		if err := manifest.resolveReleaseConfig(); err != nil {
			return nil, err
		}
	}
	globalBinary, err := resolveRegularFile(manifest.baseDir, manifest.Binary, "binary")
	if err != nil && strings.TrimSpace(manifest.Binary) != "" {
		return nil, err
	}
	seen := make(map[string]struct{}, len(manifest.Targets))
	index := 0
	var resolveTargets func([]manifestTarget, int, bool) ([]resolvedTarget, error)
	resolveTargets = func(entries []manifestTarget, depth int, nested bool) ([]resolvedTarget, error) {
		if depth > maxManifestDepth {
			return nil, fmt.Errorf("manifest target depth exceeds %d", maxManifestDepth)
		}
		targets := make([]resolvedTarget, 0, len(entries))
		for _, target := range entries {
			index++
			currentIndex := index - 1
			if index > maxManifestTargets {
				return nil, fmt.Errorf("manifest has more than %d targets", maxManifestTargets)
			}
			target.Name = strings.TrimSpace(target.Name)
			target.SSH = strings.TrimSpace(target.SSH)
			target.Role = strings.ToLower(strings.TrimSpace(target.Role))
			target.Scope = strings.ToLower(strings.TrimSpace(target.Scope))
			target.Become = strings.ToLower(strings.TrimSpace(target.Become))
			rawArch := strings.TrimSpace(target.Arch)
			target.Arch = normalizeReleaseArchitecture(rawArch)
			target.SHA256 = strings.ToLower(strings.TrimSpace(target.SHA256))
			if target.Name == "" || !safeLabel(target.Name) {
				return nil, fmt.Errorf("target %d has invalid name %q", index, target.Name)
			}
			if _, exists := seen[target.Name]; exists {
				return nil, fmt.Errorf("duplicate target name %q", target.Name)
			}
			seen[target.Name] = struct{}{}
			if !safeSSHTarget(target.SSH) {
				return nil, fmt.Errorf("target %q has invalid ssh destination %q", target.Name, target.SSH)
			}
			if target.Role != "node" && target.Role != "hub" && target.Role != "privileged-helper" && target.Role != "relay" {
				return nil, fmt.Errorf("target %q role must be node, hub, privileged-helper, or relay", target.Name)
			}
			if target.Role == "relay" && !allowRelay {
				return nil, fmt.Errorf("target %q relay role is only supported by update or cascade", target.Name)
			}
			if target.Role == "relay" && len(target.Children) == 0 {
				return nil, fmt.Errorf("target %q relay has no children", target.Name)
			}
			if target.Role != "relay" && len(target.Children) != 0 {
				return nil, fmt.Errorf("target %q has children but is not a relay", target.Name)
			}
			if nested && target.Arch == "" {
				return nil, fmt.Errorf("nested target %q must declare arch", target.Name)
			}
			if target.Scope == "" {
				target.Scope = "system"
			}
			if target.Scope != "system" && target.Scope != "user" {
				return nil, fmt.Errorf("target %q scope must be system or user", target.Name)
			}
			if target.Role == "relay" {
				target.Scope = "user"
				target.Become = "none"
			} else if target.Role == "privileged-helper" && target.Scope != "system" {
				return nil, fmt.Errorf("target %q privileged-helper requires system scope", target.Name)
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
			if rawArch != "" && target.Arch == "" {
				return nil, fmt.Errorf("target %q arch must be amd64 or arm64", target.Name)
			}
			binary := globalBinary
			if strings.TrimSpace(target.Binary) != "" {
				binary, err = resolveRegularFile(manifest.baseDir, target.Binary, "binary for "+target.Name)
				if err != nil {
					return nil, err
				}
			}
			if binary == "" && requireBinary {
				return nil, fmt.Errorf("target %q has no binary", target.Name)
			}
			if target.SHA256 != "" && !validSHA256(target.SHA256) {
				return nil, fmt.Errorf("target %q has invalid sha256", target.Name)
			}
			if target.SHA256 != "" && binary != "" {
				actual, hashErr := fileSHA256(binary)
				if hashErr != nil {
					return nil, fmt.Errorf("target %q hash binary: %w", target.Name, hashErr)
				}
				if actual != target.SHA256 {
					return nil, fmt.Errorf("target %q binary checksum mismatch", target.Name)
				}
			}
			configDir := ""
			if strings.TrimSpace(target.ConfigDir) != "" {
				configDir, err = resolveConfigDirectory(manifest.baseDir, target.ConfigDir)
				if err != nil {
					return nil, fmt.Errorf("target %q: %w", target.Name, err)
				}
			}
			children, childErr := resolveTargets(target.Children, depth+1, true)
			if childErr != nil {
				return nil, childErr
			}
			targets = append(targets, resolvedTarget{
				Index: currentIndex, Name: target.Name, SSH: target.SSH, Role: target.Role, Scope: target.Scope,
				Binary: binary, ConfigDir: configDir, Become: target.Become, Arch: target.Arch,
				SHA256: target.SHA256, TimeoutSeconds: manifest.TimeoutSeconds, Children: children,
			})
		}
		return targets, nil
	}
	return resolveTargets(manifest.Targets, 1, false)
}

func (manifest *fleetManifest) resolveReleaseConfig() error {
	release := manifest.Release
	release.BaseURL = strings.TrimRight(strings.TrimSpace(release.BaseURL), "/")
	if release.BaseURL == "" {
		release.BaseURL = defaultReleaseBaseURL
	}
	if err := validateReleaseBaseURL(release.BaseURL); err != nil {
		return err
	}
	release.CacheDir = strings.TrimSpace(release.CacheDir)
	if release.CacheDir == "" {
		cacheRoot, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("resolve release cache directory: %w", err)
		}
		release.CacheDir = filepath.Join(cacheRoot, "fleetty", "releases")
	} else if !filepath.IsAbs(release.CacheDir) {
		release.CacheDir = filepath.Join(manifest.baseDir, release.CacheDir)
	}
	absolute, err := filepath.Abs(release.CacheDir)
	if err != nil {
		return fmt.Errorf("resolve release cache directory: %w", err)
	}
	release.CacheDir = absolute
	return nil
}

func normalizeReleaseArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "amd64", "x86_64", "x86-64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
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
