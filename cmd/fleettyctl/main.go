package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Rhythmicc/fleetty/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, execRunner{}); err != nil {
		fmt.Fprintln(os.Stderr, "fleettyctl:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, runner commandRunner) error {
	if len(args) == 0 {
		writeUsage(stderr)
		return errors.New("command is required")
	}
	if args[0] == "version" {
		flags := flag.NewFlagSet("version", flag.ContinueOnError)
		flags.SetOutput(stderr)
		asJSON := flags.Bool("json", false, "write machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("version does not accept positional arguments")
		}
		return buildinfo.Write(stdout, *asJSON)
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		writeUsage(stdout)
		return nil
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("file", "fleet.json", "fleet manifest path")
	asJSON := flags.Bool("json", false, "write machine-readable JSON")
	yes := flags.Bool("yes", false, "confirm changes without prompting")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if args[0] == "update" {
		manifest, targets, err := loadUpdateManifest(*manifestPath)
		if err != nil {
			return err
		}
		preparation, err := prepareReleaseTargets(
			context.Background(), manifest, targets, runner, releaseHTTPClientFactory(),
		)
		if err != nil {
			return err
		}
		plans := releasePlans(context.Background(), preparation, manifest.Parallel, runner)
		if !*yes {
			if *asJSON {
				if err := json.NewEncoder(stdout).Encode(map[string]any{
					"release_id": preparation.ReleaseID, "assets": preparation.Assets,
					"relay_assets": preparation.RelayAssets, "plan": plans,
				}); err != nil {
					return err
				}
			} else {
				writeReleaseSummary(stdout, preparation)
				if err := writePlans(stdout, plans, false); err != nil {
					return err
				}
			}
			if !plansHealthy(plans) {
				return errOperationFailed
			}
			return nil
		}
		results := applyReleaseTargets(context.Background(), preparation.Targets, plans, manifest.Parallel, runner)
		if *asJSON {
			if err := json.NewEncoder(stdout).Encode(map[string]any{
				"release_id": preparation.ReleaseID, "assets": preparation.Assets,
				"relay_assets": preparation.RelayAssets, "plan": plans, "results": results,
			}); err != nil {
				return err
			}
		} else {
			writeReleaseSummary(stdout, preparation)
			if err := writePlans(stdout, plans, false); err != nil {
				return err
			}
			if err := writeApplyResults(stdout, results, false); err != nil {
				return err
			}
		}
		if !releaseResultsHealthy(results) {
			return errOperationFailed
		}
		return nil
	}
	if args[0] == "cascade" {
		manifest, targets, err := loadCascadeManifest(*manifestPath)
		if err != nil {
			return err
		}
		plans := cascadePlans(context.Background(), targets, manifest.Parallel, runner)
		if !*yes {
			if *asJSON {
				if err := json.NewEncoder(stdout).Encode(relayCascadeDocument{Plan: plans}); err != nil {
					return err
				}
			} else if err := writePlans(stdout, plans, false); err != nil {
				return err
			}
			if !plansHealthy(plans) {
				return errOperationFailed
			}
			return nil
		}
		results := applyCascadeTargets(context.Background(), targets, plans, manifest.Parallel, runner)
		if *asJSON {
			if err := json.NewEncoder(stdout).Encode(relayCascadeDocument{Plan: plans, Results: results}); err != nil {
				return err
			}
		} else {
			if err := writePlans(stdout, plans, false); err != nil {
				return err
			}
			if err := writeApplyResults(stdout, results, false); err != nil {
				return err
			}
		}
		if !releaseResultsHealthy(results) {
			return errOperationFailed
		}
		return nil
	}
	manifest, targets, err := loadManifest(*manifestPath)
	if err != nil {
		return err
	}
	switch args[0] {
	case "validate":
		if *asJSON {
			return json.NewEncoder(stdout).Encode(map[string]any{
				"valid": true, "version": manifest.Version, "targets": len(targets),
			})
		}
		fmt.Fprintf(stdout, "VALID  manifest v%d  %d targets  parallel=%d\n", manifest.Version, len(targets), manifest.Parallel)
		return nil
	case "plan":
		plans := planTargets(context.Background(), targets, manifest.Parallel, runner)
		if err := writePlans(stdout, plans, *asJSON); err != nil {
			return err
		}
		if !plansHealthy(plans) {
			return errOperationFailed
		}
		return nil
	case "apply":
		plans := planTargets(context.Background(), targets, manifest.Parallel, runner)
		if !*asJSON {
			if err := writePlans(stdout, plans, false); err != nil {
				return err
			}
		}
		if !plansHealthy(plans) {
			if *asJSON {
				if err := json.NewEncoder(stdout).Encode(map[string]any{"plan": plans}); err != nil {
					return err
				}
			}
			return errOperationFailed
		}
		if !*yes {
			if *asJSON {
				if err := json.NewEncoder(stdout).Encode(map[string]any{"plan": plans}); err != nil {
					return err
				}
			}
			return errors.New("apply requires --yes; inspect the plan first")
		}
		results := applyTargets(context.Background(), targets, plans, manifest.Parallel, runner)
		if *asJSON {
			if err := json.NewEncoder(stdout).Encode(map[string]any{
				"plan": plans, "results": results,
			}); err != nil {
				return err
			}
		} else if err := writeApplyResults(stdout, results, false); err != nil {
			return err
		}
		if !appliesHealthy(results) {
			return errOperationFailed
		}
		return nil
	case "status":
		results := statusTargets(context.Background(), targets, manifest.Parallel, runner)
		if err := writeApplyResults(stdout, results, *asJSON); err != nil {
			return err
		}
		if !appliesHealthy(results) {
			return errOperationFailed
		}
		return nil
	default:
		writeUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Fleetty fleet operations

Usage:
  fleettyctl validate --file fleet.json [--json]
  fleettyctl plan     --file fleet.json [--json]
  fleettyctl apply    --file fleet.json --yes [--json]
  fleettyctl status   --file fleet.json [--json]
  fleettyctl update   --file fleet-update.json [--yes] [--json]
  fleettyctl cascade  --file cascade.json [--yes] [--json]
  fleettyctl version [--json]`)
}

func writeReleaseSummary(writer io.Writer, preparation releasePreparation) {
	if preparation.ReleaseID == "" {
		fmt.Fprintln(writer, "RELEASE  no online target architecture was available; deferred targets will retry later")
		return
	}
	fmt.Fprintf(writer, "RELEASE  %s  %d verified asset(s) cached on this Hub\n",
		preparation.ReleaseID[:12], len(preparation.Assets)+len(preparation.RelayAssets))
}

func writePlans(writer io.Writer, plans []targetPlan, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(writer).Encode(plans)
	}
	fmt.Fprintln(writer, "ACTION   TARGET               ROLE  SCOPE   ARCH    STATE      REASON")
	for _, plan := range plans {
		reason := strings.Join(plan.Reasons, "; ")
		if plan.Error != "" {
			reason = plan.Error
		}
		fmt.Fprintf(writer, "%-8s %-20s %-5s %-7s %-7s %-10s %s\n",
			strings.ToUpper(plan.Action), plan.Name, plan.Role, plan.Scope, plan.Arch, plan.State, reason)
	}
	return nil
}

func writeApplyResults(writer io.Writer, results []targetApply, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(writer).Encode(results)
	}
	fmt.Fprintln(writer, "RESULT   TARGET               DETAIL")
	for _, result := range results {
		detail := result.Error
		if detail == "" {
			detail = result.Message
		}
		if detail == "" && len(result.Result) > 0 {
			detail = strings.TrimSpace(string(result.Result))
		}
		fmt.Fprintf(writer, "%-8s %-20s %s\n", strings.ToUpper(result.Action), result.Name, detail)
	}
	return nil
}
