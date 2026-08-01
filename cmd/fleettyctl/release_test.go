package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadUpdateManifestAllowsReleaseWithoutLocalBinary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fleet-update.json")
	manifest := `{
  "version": 1,
  "release": {"cache_dir":"cache"},
  "targets": [
    {"name":"gpu-1","ssh":"gpu-1-admin","role":"node","arch":"x86_64"}
  ]
}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, targets, err := loadUpdateManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Arch != "amd64" || targets[0].Binary != "" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if parsed.Release.BaseURL != defaultReleaseBaseURL ||
		parsed.Release.CacheDir != filepath.Join(root, "cache") {
		t.Fatalf("unexpected release config: %#v", parsed.Release)
	}
	if _, _, err := loadManifest(path); err == nil || !strings.Contains(err.Error(), "has no binary") {
		t.Fatalf("normal deployment manifest should still require a local binary: %v", err)
	}
}

func TestDownloadReleaseAssetsVerifiesAndReusesHubCache(t *testing.T) {
	payload := []byte("verified fleetty release")
	hash := sha256.Sum256(payload)
	expected := hex.EncodeToString(hash[:])
	checksums := []byte(expected + "  fleetty_linux_amd64\n")
	var mu sync.Mutex
	assetDownloads := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			_, _ = writer.Write(checksums)
		case "/fleetty_linux_amd64":
			mu.Lock()
			assetDownloads++
			mu.Unlock()
			_, _ = writer.Write(payload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	release := releaseConfig{BaseURL: server.URL, CacheDir: t.TempDir()}
	for attempt := 0; attempt < 2; attempt++ {
		assets, releaseID, err := downloadReleaseAssets(
			context.Background(), release, []string{"amd64"}, server.Client(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if releaseID == "" || assets["amd64"] == "" {
			t.Fatalf("unexpected release result: id=%q assets=%#v", releaseID, assets)
		}
		actual, err := os.ReadFile(assets["amd64"])
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("cached asset = %q, error=%v", actual, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if assetDownloads != 1 {
		t.Fatalf("release asset downloads = %d, want one cached download", assetDownloads)
	}
}

func TestDownloadReleaseAssetsRejectsChecksumMismatch(t *testing.T) {
	badHash := strings.Repeat("0", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/checksums.txt" {
			_, _ = fmt.Fprintf(writer, "%s  fleetty_linux_amd64\n", badHash)
			return
		}
		_, _ = writer.Write([]byte("tampered"))
	}))
	defer server.Close()
	_, _, err := downloadReleaseAssets(context.Background(), releaseConfig{
		BaseURL: server.URL, CacheDir: t.TempDir(),
	}, []string{"amd64"}, server.Client())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("downloadReleaseAssets() error = %v", err)
	}
}

func TestReleasePreparationBuildsVerifiedRelaySubtree(t *testing.T) {
	fleettyPayload := []byte("fleetty release")
	controllerPayload := []byte("fleettyctl release")
	fleettyDigest := sha256.Sum256(fleettyPayload)
	controllerDigest := sha256.Sum256(controllerPayload)
	checksums := fmt.Sprintf("%s  fleetty_linux_amd64\n%s  fleettyctl_linux_amd64\n",
		hex.EncodeToString(fleettyDigest[:]), hex.EncodeToString(controllerDigest[:]))
	var requestMu sync.Mutex
	checksumsRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			requestMu.Lock()
			checksumsRequests++
			requestMu.Unlock()
			_, _ = writer.Write([]byte(checksums))
		case "/fleetty_linux_amd64":
			_, _ = writer.Write(fleettyPayload)
		case "/fleettyctl_linux_amd64":
			_, _ = writer.Write(controllerPayload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	manifest := fleetManifest{
		Parallel: 1,
		Release:  &releaseConfig{BaseURL: server.URL, CacheDir: t.TempDir()},
	}
	targets := []resolvedTarget{{
		Name: "a100-login", SSH: "a100", Role: "relay", Arch: "amd64", TimeoutSeconds: 5,
		Children: []resolvedTarget{{
			Name: "a100-compute", SSH: "a100", Role: "node", Scope: "system",
			Become: "none", Arch: "amd64", TimeoutSeconds: 5,
		}},
	}}
	preparation, err := prepareReleaseTargets(context.Background(), manifest, targets, &scriptedRemoteRunner{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if preparation.ReleaseID == "" || preparation.Targets[0].Binary == "" ||
		len(preparation.Targets[0].Children) != 1 || preparation.Targets[0].Children[0].Binary == "" {
		t.Fatalf("relay preparation = %#v", preparation)
	}
	controller, err := os.ReadFile(preparation.Targets[0].Binary)
	if err != nil || !bytes.Equal(controller, controllerPayload) {
		t.Fatalf("relay controller = %q, error=%v", controller, err)
	}
	leaf, err := os.ReadFile(preparation.Targets[0].Children[0].Binary)
	if err != nil || !bytes.Equal(leaf, fleettyPayload) {
		t.Fatalf("leaf asset = %q, error=%v", leaf, err)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if checksumsRequests != 1 {
		t.Fatalf("checksums requests = %d, want exactly one Hub download", checksumsRequests)
	}
}

func TestReleasePreparationDefersOfflineArchitectureProbe(t *testing.T) {
	manifest := fleetManifest{Parallel: 1, Release: &releaseConfig{}}
	targets := []resolvedTarget{{
		Name: "offline", SSH: "offline", Role: "node", Become: "sudo", TimeoutSeconds: 1,
	}}
	preparation, err := prepareReleaseTargets(
		context.Background(), manifest, targets, &scriptedRemoteRunner{}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Deferred[0] == "" || preparation.ReleaseID != "" {
		t.Fatalf("offline preparation = %#v", preparation)
	}
	plans := releasePlans(context.Background(), preparation, 1, &scriptedRemoteRunner{})
	if plans[0].Action != "deferred" {
		t.Fatalf("offline plan = %#v", plans[0])
	}
}

func TestUpdateCommandDownloadsOnHubAndBuildsPlan(t *testing.T) {
	payload := []byte("fleetty release for update command")
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			_, _ = fmt.Fprintf(writer, "%s  fleetty_linux_amd64\n", hash)
		case "/fleetty_linux_amd64":
			_, _ = writer.Write(payload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previousFactory := releaseHTTPClientFactory
	releaseHTTPClientFactory = server.Client
	defer func() { releaseHTTPClientFactory = previousFactory }()

	root := t.TempDir()
	manifestPath := filepath.Join(root, "fleet-update.json")
	manifest := fmt.Sprintf(`{
  "version":1,
  "release":{"base_url":%q,"cache_dir":%q},
  "targets":[{"name":"gpu-1","ssh":"gpu-1-admin","role":"node","arch":"amd64"}]
}`, server.URL, filepath.Join(root, "cache"))
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRemoteRunner{binaryHash: hash, state: "active", enabled: "enabled"}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"update", "--file", manifestPath, "--json"}, &stdout, &stderr, runner); err != nil {
		t.Fatal(err)
	}
	var document struct {
		ReleaseID string       `json:"release_id"`
		Plan      []targetPlan `json:"plan"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("invalid update JSON: %v\n%s", err, stdout.String())
	}
	if document.ReleaseID == "" || len(document.Plan) != 1 || document.Plan[0].Action != "noop" {
		t.Fatalf("unexpected update plan: %#v", document)
	}

	runner = &scriptedRemoteRunner{
		binaryHash: strings.Repeat("0", 64), state: "active", enabled: "enabled",
		staging:       "/tmp/fleettyctl.Update123",
		installResult: `{"role":"node","scope":"system","service":"fleetty.service","changed":true,"state":"active"}`,
	}
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"update", "--file", manifestPath, "--yes", "--json"}, &stdout, &stderr, runner); err != nil {
		t.Fatal(err)
	}
	var applied struct {
		Plan    []targetPlan  `json:"plan"`
		Results []targetApply `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &applied); err != nil {
		t.Fatalf("invalid update apply JSON: %v\n%s", err, stdout.String())
	}
	if len(applied.Plan) != 1 || applied.Plan[0].Action != "update" ||
		len(applied.Results) != 1 || applied.Results[0].Action != "applied" {
		t.Fatalf("unexpected update apply: %#v", applied)
	}
}
