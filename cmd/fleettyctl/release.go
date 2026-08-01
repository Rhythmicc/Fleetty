package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultReleaseBaseURL = "https://github.com/Rhythmicc/fleetty/releases/latest/download"
	maxChecksumsSize      = 2 << 20
	maxReleaseAssetSize   = 256 << 20
	maxReleaseRedirects   = 5
)

var releaseHTTPClientFactory = defaultReleaseHTTPClient

type releasePreparation struct {
	Targets     []resolvedTarget
	Deferred    []string
	ReleaseID   string
	Assets      map[string]string
	RelayAssets map[string]string
}

func validateReleaseBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse release base_url: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("release base_url must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("release base_url must not contain credentials, a query, or a fragment")
	}
	return nil
}

func defaultReleaseHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxReleaseRedirects {
				return errors.New("too many release download redirects")
			}
			if request.URL.Scheme != "https" {
				return errors.New("release download redirected away from HTTPS")
			}
			return nil
		},
	}
}

func prepareReleaseTargets(
	ctx context.Context,
	manifest fleetManifest,
	targets []resolvedTarget,
	runner commandRunner,
	client *http.Client,
) (releasePreparation, error) {
	preparation := releasePreparation{
		Targets:     append([]resolvedTarget(nil), targets...),
		Deferred:    make([]string, len(targets)),
		Assets:      make(map[string]string),
		RelayAssets: make(map[string]string),
	}
	architectures := make([]string, len(targets))
	runParallel(len(targets), manifest.Parallel, func(index int) {
		target := targets[index]
		if target.Arch != "" {
			architectures[index] = target.Arch
			return
		}
		timeout := time.Duration(max(target.TimeoutSeconds*2, 15)) * time.Second
		targetCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		output, err := runSSH(targetCtx, runner, target, "uname", "-m")
		if err != nil {
			preparation.Deferred[index] = remoteFailure("detect target architecture", output, err).Error()
			return
		}
		architecture := normalizeReleaseArchitecture(string(output))
		if architecture == "" {
			preparation.Deferred[index] = fmt.Sprintf(
				"target reported unsupported architecture %q", safeRemoteText(string(output), 80),
			)
			return
		}
		architectures[index] = architecture
	})

	for index, architecture := range architectures {
		if architecture != "" {
			preparation.Targets[index].Arch = architecture
		}
	}
	leafArchitectures := make(map[string]struct{})
	relayArchitectures := make(map[string]struct{})
	for _, target := range preparation.Targets {
		collectReleaseArchitectures(target, leafArchitectures, relayArchitectures)
	}
	if len(leafArchitectures) == 0 && len(relayArchitectures) == 0 {
		return preparation, nil
	}
	names := make(map[string]string, len(leafArchitectures)+len(relayArchitectures))
	for _, architecture := range sortedArchitectureSet(leafArchitectures) {
		names["fleetty:"+architecture] = "fleetty_linux_" + architecture
	}
	for _, architecture := range sortedArchitectureSet(relayArchitectures) {
		names["relay:"+architecture] = "fleettyctl_linux_" + architecture
	}
	bundleAssets, releaseID, err := downloadReleaseNamedAssets(ctx, *manifest.Release, names, client)
	if err != nil {
		return releasePreparation{}, err
	}
	for key, path := range bundleAssets {
		kind, architecture, ok := strings.Cut(key, ":")
		if !ok {
			return releasePreparation{}, fmt.Errorf("invalid prepared release asset key %q", key)
		}
		if kind == "relay" {
			preparation.RelayAssets[architecture] = path
		} else {
			preparation.Assets[architecture] = path
		}
	}
	preparation.ReleaseID = releaseID
	for index := range preparation.Targets {
		assignReleaseBinaries(&preparation.Targets[index], preparation.Assets, preparation.RelayAssets)
	}
	return preparation, nil
}

func sortedArchitectureSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func collectReleaseArchitectures(
	target resolvedTarget,
	leaves, relays map[string]struct{},
) {
	if target.Arch != "" {
		if target.Role == "relay" {
			relays[target.Arch] = struct{}{}
		} else {
			leaves[target.Arch] = struct{}{}
		}
	}
	for _, child := range target.Children {
		collectReleaseArchitectures(child, leaves, relays)
	}
}

func assignReleaseBinaries(target *resolvedTarget, leaves, relays map[string]string) {
	if target.Role == "relay" {
		target.Binary = relays[target.Arch]
	} else {
		target.Binary = leaves[target.Arch]
	}
	for index := range target.Children {
		assignReleaseBinaries(&target.Children[index], leaves, relays)
	}
}

func downloadReleaseAssets(
	ctx context.Context,
	release releaseConfig,
	architectures []string,
	client *http.Client,
) (map[string]string, string, error) {
	names := make(map[string]string, len(architectures))
	for _, architecture := range architectures {
		architecture = normalizeReleaseArchitecture(architecture)
		if architecture == "" {
			return nil, "", errors.New("release architecture must be amd64 or arm64")
		}
		names[architecture] = "fleetty_linux_" + architecture
	}
	return downloadReleaseNamedAssets(ctx, release, names, client)
}

func downloadReleaseRelayAssets(
	ctx context.Context,
	release releaseConfig,
	architectures []string,
	client *http.Client,
) (map[string]string, string, error) {
	names := make(map[string]string, len(architectures))
	for _, architecture := range architectures {
		architecture = normalizeReleaseArchitecture(architecture)
		if architecture == "" {
			return nil, "", errors.New("release architecture must be amd64 or arm64")
		}
		names[architecture] = "fleettyctl_linux_" + architecture
	}
	return downloadReleaseNamedAssets(ctx, release, names, client)
}

func downloadReleaseNamedAssets(
	ctx context.Context,
	release releaseConfig,
	names map[string]string,
	client *http.Client,
) (map[string]string, string, error) {
	if client == nil {
		client = defaultReleaseHTTPClient()
	}
	checksums, err := downloadReleaseBytes(ctx, client, release.BaseURL+"/checksums.txt", maxChecksumsSize)
	if err != nil {
		return nil, "", fmt.Errorf("download release checksums: %w", err)
	}
	expected, err := parseReleaseChecksums(checksums)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(checksums)
	releaseID := hex.EncodeToString(digest[:])
	cacheDir := filepath.Join(release.CacheDir, releaseID)
	if err := ensurePrivateReleaseDirectory(cacheDir); err != nil {
		return nil, "", err
	}
	assets := make(map[string]string, len(names))
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		asset := names[key]
		expectedHash := expected[asset]
		if expectedHash == "" {
			return nil, "", fmt.Errorf("release checksums do not contain %s", asset)
		}
		path, err := ensureReleaseAsset(ctx, client, release.BaseURL+"/"+asset, cacheDir, asset, expectedHash)
		if err != nil {
			return nil, "", err
		}
		assets[key] = path
	}
	return assets, releaseID, nil
}

func downloadReleaseBytes(ctx context.Context, client *http.Client, address string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "fleettyctl-release-updater")
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("download exceeds %d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("download exceeds %d bytes", maximum)
	}
	return data, nil
}

func parseReleaseChecksums(data []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !validSHA256(fields[0]) {
			return nil, fmt.Errorf("invalid release checksum line %q", safeRemoteText(scanner.Text(), 160))
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("invalid release asset name %q", name)
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("duplicate checksum for %s", name)
		}
		checksums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(checksums) == 0 {
		return nil, errors.New("release checksums are empty")
	}
	return checksums, nil
}

func ensurePrivateReleaseDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("unsafe release cache directory %q", path)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create release cache directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release cache path is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func ensureReleaseAsset(
	ctx context.Context,
	client *http.Client,
	address, cacheDir, asset, expectedHash string,
) (string, error) {
	destination := filepath.Join(cacheDir, asset)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode().IsRegular() {
			actual, hashErr := fileSHA256(destination)
			if hashErr == nil && actual == expectedHash {
				if chmodErr := os.Chmod(destination, 0o700); chmodErr != nil {
					return "", chmodErr
				}
				return destination, nil
			}
		}
		if removeErr := os.Remove(destination); removeErr != nil {
			return "", fmt.Errorf("replace invalid cached release asset: %w", removeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "fleettyctl-release-updater")
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: unexpected HTTP status %s", asset, response.Status)
	}
	if response.ContentLength > maxReleaseAssetSize {
		return "", fmt.Errorf("download %s exceeds %d bytes", asset, maxReleaseAssetSize)
	}
	temporary, err := os.CreateTemp(cacheDir, "."+asset+"-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o700); err != nil {
		return "", err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, maxReleaseAssetSize+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if written > maxReleaseAssetSize {
		return "", fmt.Errorf("download %s exceeds %d bytes", asset, maxReleaseAssetSize)
	}
	actualHash := hex.EncodeToString(digest.Sum(nil))
	if actualHash != expectedHash {
		return "", fmt.Errorf("download %s checksum mismatch", asset)
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	committed = true
	return destination, nil
}

func releasePlans(
	ctx context.Context,
	preparation releasePreparation,
	parallel int,
	runner commandRunner,
) []targetPlan {
	plans := make([]targetPlan, len(preparation.Targets))
	runParallel(len(preparation.Targets), parallel, func(index int) {
		target := preparation.Targets[index]
		if preparation.Deferred[index] != "" {
			plans[index] = deferredTargetPlan(target, preparation.Deferred[index])
			return
		}
		plan := planTarget(ctx, target, runner)
		if target.Role == "relay" {
			plan = planRelayTarget(ctx, target, runner)
		}
		if plan.Action == "error" && strings.Contains(plan.Error, "connect to target") {
			plan.Action = "deferred"
			plan.Reasons = []string{plan.Error}
			plan.Error = ""
		}
		plans[index] = plan
	})
	return plans
}

func deferredTargetPlan(target resolvedTarget, reason string) targetPlan {
	return targetPlan{
		Index: target.Index, Name: target.Name, SSH: target.SSH, Role: target.Role,
		Scope: targetScope(target), Arch: target.Arch, Service: serviceForRole(target.Role),
		Action: "deferred", State: "offline", Enabled: "unknown", Reasons: []string{reason},
	}
}

func applyReleaseTargets(
	ctx context.Context,
	targets []resolvedTarget,
	plans []targetPlan,
	parallel int,
	runner commandRunner,
) []targetApply {
	return applyCascadeTargets(ctx, targets, plans, parallel, runner)
}

func releaseResultsHealthy(results []targetApply) bool {
	for _, result := range results {
		if result.Action == "error" || result.Error != "" {
			return false
		}
	}
	return true
}
