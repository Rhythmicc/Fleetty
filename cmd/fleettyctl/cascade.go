package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// relayCascadeDocument is the machine-readable contract exchanged only
// between adjacent fleettyctl processes. No SSH agent or private key is
// forwarded: every relay uses its own local SSH configuration for children.
type relayCascadeDocument struct {
	Plan    []targetPlan  `json:"plan"`
	Results []targetApply `json:"results,omitempty"`
}

func cascadePlans(
	ctx context.Context,
	targets []resolvedTarget,
	parallel int,
	runner commandRunner,
) []targetPlan {
	plans := make([]targetPlan, len(targets))
	runParallel(len(targets), parallel, func(index int) {
		target := targets[index]
		if target.Role != "relay" {
			plans[index] = planTarget(ctx, target, runner)
			return
		}
		plans[index] = planRelayTarget(ctx, target, runner)
	})
	return plans
}

func planRelayTarget(ctx context.Context, target resolvedTarget, runner commandRunner) targetPlan {
	plan := targetPlan{
		Index: target.Index, Name: target.Name, SSH: target.SSH, Role: target.Role,
		Scope: "relay", Arch: target.Arch, Service: "adjacent update relay",
		State: "unknown", Enabled: "n/a",
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(max(target.TimeoutSeconds*2, 15))*time.Second)
	defer cancel()
	if output, err := runSSH(probeCtx, runner, target, "true"); err != nil {
		plan.Action = "deferred"
		plan.State = "offline"
		plan.Reasons = []string{remoteFailure("connect to relay", output, err).Error()}
		return plan
	}
	hash, err := fileSHA256(target.Binary)
	if err != nil {
		plan.Action, plan.Error = "error", err.Error()
		return plan
	}
	plan.DesiredHash = hash
	document, err := runRelayCascade(ctx, target, false, runner)
	if err != nil {
		plan.Action = "error"
		plan.Error = err.Error()
		return plan
	}
	if !plansHealthy(document.Plan) {
		plan.Action = "error"
		plan.Error = "one or more adjacent targets failed validation"
		return plan
	}
	plan.Action = "relay"
	plan.State = "reachable"
	plan.Reasons = []string{fmt.Sprintf("%d adjacent target(s) planned on relay", len(document.Plan))}
	return plan
}

func applyCascadeTargets(
	ctx context.Context,
	targets []resolvedTarget,
	plans []targetPlan,
	parallel int,
	runner commandRunner,
) []targetApply {
	results := make([]targetApply, len(targets))
	runParallel(len(targets), parallel, func(index int) {
		target := targets[index]
		plan := plans[index]
		switch plan.Action {
		case "noop":
			results[index] = targetApply{Index: target.Index, Name: target.Name, Action: "noop", Message: "already current"}
		case "deferred":
			results[index] = targetApply{Index: target.Index, Name: target.Name, Action: "deferred", Message: strings.Join(plan.Reasons, "; ")}
		case "error":
			results[index] = targetApply{Index: target.Index, Name: target.Name, Action: "error", Error: plan.Error}
		default:
			if target.Role != "relay" {
				results[index] = applyTarget(ctx, target, runner)
				return
			}
			document, err := runRelayCascade(ctx, target, true, runner)
			if err != nil {
				results[index] = targetApply{Index: target.Index, Name: target.Name, Action: "error", Error: err.Error()}
				return
			}
			if !releaseResultsHealthy(document.Results) {
				results[index] = targetApply{Index: target.Index, Name: target.Name, Action: "error", Error: "one or more adjacent targets failed to update"}
				return
			}
			results[index] = targetApply{
				Index: target.Index, Name: target.Name, Action: "relayed",
				Message: fmt.Sprintf("%d adjacent target(s) processed", len(document.Results)),
			}
		}
	})
	return results
}

func runRelayCascade(
	parent context.Context,
	target resolvedTarget,
	apply bool,
	runner commandRunner,
) (relayCascadeDocument, error) {
	var document relayCascadeDocument
	if target.Role != "relay" || len(target.Children) == 0 {
		return document, errors.New("relay target must have children")
	}
	if target.Binary == "" {
		return document, errors.New("relay fleettyctl binary is missing")
	}
	operationTimeout := time.Duration(max(target.TimeoutSeconds*20, 120)) * time.Second
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	output, err := runSSH(ctx, runner, target, "mktemp", "-d", "/tmp/fleettyctl.XXXXXXXXXX")
	if err != nil {
		return document, remoteFailure("create relay staging directory", output, err)
	}
	staging := strings.TrimSpace(string(output))
	if !validRemoteStagingPath(staging) {
		return document, fmt.Errorf("relay returned unsafe staging path %q", staging)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = runSSH(cleanupCtx, runner, target, "find", staging, "-xdev", "-depth", "-delete")
	}()

	files, manifestTargets, err := buildRelayManifest(target.Children)
	if err != nil {
		return document, err
	}
	files["fleettyctl"] = target.Binary
	for name, source := range files {
		if output, err = runSCP(ctx, runner, target, source, filepath.Join(staging, name)); err != nil {
			return document, remoteFailure("upload relay bundle asset "+name, output, err)
		}
	}
	if output, err = runSSH(ctx, runner, target, "chmod", "0700", filepath.Join(staging, "fleettyctl")); err != nil {
		return document, remoteFailure("make relay executable", output, err)
	}
	expectedHash, err := fileSHA256(target.Binary)
	if err != nil {
		return document, err
	}
	output, err = runSSH(ctx, runner, target, "sha256sum", filepath.Join(staging, "fleettyctl"))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(output)), expectedHash+" ") {
		return document, remoteFailure("verify relay executable checksum", output, errOrMismatch(err))
	}

	manifest := fleetManifest{
		Version: manifestVersion, Parallel: defaultParallelism,
		TimeoutSeconds: target.TimeoutSeconds, Targets: manifestTargets,
	}
	manifestFile, err := os.CreateTemp("", "fleetty-cascade-*.json")
	if err != nil {
		return document, err
	}
	manifestPath := manifestFile.Name()
	defer os.Remove(manifestPath)
	encoder := json.NewEncoder(manifestFile)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		_ = manifestFile.Close()
		return document, err
	}
	if err = manifestFile.Chmod(0o600); err != nil {
		_ = manifestFile.Close()
		return document, err
	}
	if err = manifestFile.Close(); err != nil {
		return document, err
	}
	remoteManifest := filepath.Join(staging, "cascade.json")
	if output, err = runSCP(ctx, runner, target, manifestPath, remoteManifest); err != nil {
		return document, remoteFailure("upload relay manifest", output, err)
	}
	arguments := []string{"cascade", "--file", remoteManifest, "--json"}
	if apply {
		arguments = append(arguments, "--yes")
	}
	remoteCommand := append([]string{filepath.Join(staging, "fleettyctl")}, arguments...)
	output, err = runSSH(ctx, runner, target, remoteCommand...)
	if err != nil {
		return document, remoteFailure("run adjacent relay update", output, err)
	}
	if err = json.Unmarshal(output, &document); err != nil {
		return document, fmt.Errorf("relay returned invalid JSON: %w", err)
	}
	return document, nil
}

func buildRelayManifest(children []resolvedTarget) (map[string]string, []manifestTarget, error) {
	files := make(map[string]string)
	targets := make([]manifestTarget, 0, len(children))
	for _, child := range children {
		if child.ConfigDir != "" {
			return nil, nil, fmt.Errorf("relay child %q cannot use config_dir; configuration belongs on its adjacent relay", child.Name)
		}
		if child.Binary == "" {
			return nil, nil, fmt.Errorf("relay child %q has no bundle binary", child.Name)
		}
		name := filepath.Base(child.Binary)
		if !safeConfigName(name) || name == "fleettyctl" {
			return nil, nil, fmt.Errorf("relay child %q has unsafe bundle asset name", child.Name)
		}
		hash, err := fileSHA256(child.Binary)
		if err != nil {
			return nil, nil, err
		}
		if existing, ok := files[name]; ok {
			existingHash, hashErr := fileSHA256(existing)
			if hashErr != nil || existingHash != hash {
				return nil, nil, fmt.Errorf("relay bundle asset name collision for %s", name)
			}
		} else {
			files[name] = child.Binary
		}
		grandchildrenFiles, grandchildren, err := buildRelayManifest(child.Children)
		if err != nil {
			return nil, nil, err
		}
		for grandchildName, source := range grandchildrenFiles {
			if existing, ok := files[grandchildName]; ok && existing != source {
				existingHash, firstErr := fileSHA256(existing)
				sourceHash, secondErr := fileSHA256(source)
				if firstErr != nil || secondErr != nil || existingHash != sourceHash {
					return nil, nil, fmt.Errorf("relay bundle asset name collision for %s", grandchildName)
				}
			} else {
				files[grandchildName] = source
			}
		}
		targets = append(targets, manifestTarget{
			Name: child.Name, SSH: child.SSH, Role: child.Role, Scope: child.Scope,
			Binary: name, Become: child.Become, Arch: child.Arch, SHA256: hash,
			Children: grandchildren,
		})
	}
	return files, targets, nil
}

func errOrMismatch(err error) error {
	if err != nil {
		return err
	}
	return errors.New("checksum mismatch")
}
