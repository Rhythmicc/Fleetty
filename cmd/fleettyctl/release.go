package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rhythmicc/fleetty/internal/releaseasset"
)

const defaultReleaseBaseURL = "https://github.com/Rhythmicc/fleetty/releases/latest/download"

var releaseHTTPClientFactory = releaseasset.NewClient

type releasePreparation struct {
	Targets     []resolvedTarget
	Deferred    []string
	ReleaseID   string
	Assets      map[string]string
	RelayAssets map[string]string
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
		client = releaseasset.NewClient()
	}
	checksums, err := releaseasset.Bytes(ctx, client, release.BaseURL+"/checksums.txt", releaseasset.MaxChecksumsSize)
	if err != nil {
		return nil, "", fmt.Errorf("download release checksums: %w", err)
	}
	expected, err := releaseasset.Checksums(checksums)
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
		path, err := releaseasset.Asset(ctx, client, release.BaseURL+"/"+asset, cacheDir, asset, expectedHash)
		if err != nil {
			return nil, "", err
		}
		assets[key] = path
	}
	return assets, releaseID, nil
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
