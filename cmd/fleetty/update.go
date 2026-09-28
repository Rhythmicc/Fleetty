package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	"github.com/Rhythmicc/fleetty/internal/buildinfo"
	"github.com/Rhythmicc/fleetty/internal/releaseasset"
)

const fleettyReleasesURL = "https://github.com/Rhythmicc/Fleetty/releases/download/"

var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func runUpdateCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.String("version", "latest", "release tag to install (default: latest stable release)")
	check := flags.Bool("check", false, "check for an update without changing files")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("update does not accept positional arguments")
	}
	if *version != "latest" && !releaseVersionPattern.MatchString(*version) {
		return errors.New("version must be latest or a release tag such as v0.1.4")
	}
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("self-update is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	client := releaseasset.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if *version == "latest" {
		data, err := releaseasset.Bytes(ctx, client, "https://api.github.com/repos/Rhythmicc/Fleetty/releases/latest", releaseasset.MaxChecksumsSize)
		if err != nil {
			return fmt.Errorf("resolve latest release: %w", err)
		}
		var release struct {
			Tag string `json:"tag_name"`
		}
		if err := json.Unmarshal(data, &release); err != nil {
			return fmt.Errorf("decode latest release: %w", err)
		}
		if !releaseVersionPattern.MatchString(release.Tag) {
			return errors.New("GitHub returned an invalid release tag")
		}
		*version = release.Tag
	}
	return updateExecutable(ctx, client, fleettyReleasesURL+*version, path, *version, *check, stdout, probeUpdateBinary)
}

// probeUpdateBinary runs only after the official release checksum is verified.
func probeUpdateBinary(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("new binary failed its version check: %w", err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("invalid new binary version output: %w", err)
	}
	if info.Version != version || info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		return fmt.Errorf("new binary reports %s %s/%s; expected %s %s/%s", info.Version, info.OS, info.Arch, version, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

func updateExecutable(ctx context.Context, client *http.Client, base, executable, version string, check bool, stdout io.Writer, probe func(context.Context, string, string) error) error {
	if err := releaseasset.ValidateBaseURL(base); err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve installed binary: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return errors.New("refusing to update a non-regular or set-ID executable")
	}
	currentHash, err := releaseasset.FileSHA256(path)
	if err != nil {
		return err
	}
	asset := "fleetty_" + runtime.GOOS + "_" + runtime.GOARCH
	fmt.Fprintf(stdout, "Checking Fleetty %s for %s/%s...\n", version, runtime.GOOS, runtime.GOARCH)
	data, err := releaseasset.Bytes(ctx, client, base+"/checksums.txt", releaseasset.MaxChecksumsSize)
	if err != nil {
		return fmt.Errorf("download release checksums: %w", err)
	}
	checksums, err := releaseasset.Checksums(data)
	if err != nil {
		return err
	}
	expected := checksums[asset]
	if expected == "" {
		return fmt.Errorf("release checksums do not contain %s", asset)
	}
	if currentHash == expected {
		fmt.Fprintf(stdout, "Already up to date: %s (%s).\n", version, path)
		return nil
	}
	if check {
		fmt.Fprintf(stdout, "Update available: %s → %s (%s). Run fleetty update --version %s to install.\n", buildinfo.Current().Version, version, path, version)
		return nil
	}
	// A stable lock next to the executable also serializes updates after rename.
	lock, err := os.OpenFile(path+".update.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("cannot update %s: %w; run as the installation owner (sudo for a root-owned installation)", path, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another update is in progress: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	stage, err := os.MkdirTemp(filepath.Dir(path), ".fleetty-update-*")
	if err != nil {
		return fmt.Errorf("create update staging directory: %w", err)
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(stage)
		}
	}()
	fmt.Fprintf(stdout, "Downloading and verifying %s...\n", asset)
	candidate, err := releaseasset.Asset(ctx, client, base+"/"+asset, stage, asset, expected)
	if err != nil {
		return err
	}
	if err := probe(ctx, candidate, version); err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		if err := os.Chown(candidate, int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	if err := os.Chmod(candidate, info.Mode().Perm()); err != nil {
		return err
	}
	latestInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, latestInfo) {
		return errors.New("installed binary changed during update; retry")
	}
	latestHash, err := releaseasset.FileSHA256(path)
	if err != nil || latestHash != currentHash {
		return errors.New("installed binary contents changed during update; retry")
	}
	backup := filepath.Join(stage, "fleetty.previous")
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep the existing executable in place until the single atomic rename.
	if err := os.Link(path, backup); err != nil {
		return fmt.Errorf("back up installed binary: %w", err)
	}
	if err := os.Rename(candidate, path); err != nil {
		return fmt.Errorf("replace installed binary: %w", err)
	}
	keepBackup = true
	fmt.Fprintf(stdout, "Updated to %s: %s\nBackup: %s\nRunning sessions and services are unchanged. Restart them to use the new version.\n", version, path, backup)
	return nil
}
