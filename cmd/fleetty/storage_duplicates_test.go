package main

import (
	byteutil "bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"golang.org/x/sys/unix"
)

func TestScanStorageDuplicatesVerifiesContentAndIgnoresExistingLinks(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	content := make([]byte, storageDuplicateMinimumSize)
	for index := range content {
		content[index] = byte(index % 251)
	}
	first := filepath.Join(root, "first.bin")
	second := filepath.Join(nested, "second.bin")
	different := filepath.Join(root, "different.bin")
	if err := os.WriteFile(first, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, content, 0o600); err != nil {
		t.Fatal(err)
	}
	other := append([]byte(nil), content...)
	other[len(other)-1] ^= 0xff
	if err := os.WriteFile(different, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, filepath.Join(root, "already-hardlinked.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, filepath.Join(root, "already-symlinked.bin")); err != nil {
		t.Fatal(err)
	}

	result, err := scanStorageDuplicatesWithPolicy(
		context.Background(),
		root,
		storageMountPolicy{excluded: map[string]string{}, mounts: map[string]string{}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("groups = %#v, want one verified group", result.Groups)
	}
	group := result.Groups[0]
	if len(group.Files) != 2 || group.Size != uint64(len(content)) {
		t.Fatalf("group = %#v", group)
	}
	if group.Files[0].Identity == group.Files[1].Identity {
		t.Fatalf("hard links were reported as reclaimable duplicates: %#v", group.Files)
	}
	if result.Reclaimable == 0 || result.HashedFiles != 3 {
		t.Fatalf("result = %#v", result)
	}
}

func TestReplaceStorageDuplicateWithRelativeSymlink(t *testing.T) {
	root := t.TempDir()
	keepDirectory := filepath.Join(root, "keep")
	duplicateDirectory := filepath.Join(root, "copies", "nested")
	if err := os.MkdirAll(keepDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(duplicateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(strings.Repeat("verified duplicate\n", 1024))
	keep := filepath.Join(keepDirectory, "dataset.bin")
	duplicate := filepath.Join(duplicateDirectory, "dataset-copy.bin")
	if err := os.WriteFile(keep, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	request := storageDedupeLinkRequest{
		KeepPath: keep, ReplacePath: duplicate, Hash: hex.EncodeToString(digest[:]),
	}
	if err := replaceStorageDuplicateWithSymlink(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement mode = %v, want symbolic link", info.Mode())
	}
	link, err := os.Readlink(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(link) {
		t.Fatalf("link target %q is not relative", link)
	}
	got, err := os.ReadFile(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("replacement link does not expose the canonical content")
	}
	remaining, err := filepath.Glob(filepath.Join(duplicateDirectory, ".fleetty-dedupe-*"))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("dedupe workspace remains: %v, %v", remaining, err)
	}
}

func TestReplaceStorageDuplicateRejectsChangedContentWithoutMutation(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	duplicate := filepath.Join(root, "duplicate")
	if err := os.WriteFile(keep, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("same"))
	err := replaceStorageDuplicateWithSymlink(context.Background(), storageDedupeLinkRequest{
		KeepPath: keep, ReplacePath: duplicate, Hash: hex.EncodeToString(digest[:]),
	})
	if err == nil || !strings.Contains(err.Error(), "same size") &&
		!strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("error = %v", err)
	}
	info, statErr := os.Lstat(duplicate)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("duplicate was mutated after rejection: %v", info.Mode())
	}
}

func TestReplaceStorageDuplicateRejectsDifferentAccessMetadata(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	duplicate := filepath.Join(root, "duplicate")
	content := []byte("matching content")
	if err := os.WriteFile(keep, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, content, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	err := replaceStorageDuplicateWithSymlink(context.Background(), storageDedupeLinkRequest{
		KeepPath: keep, ReplacePath: duplicate, Hash: hex.EncodeToString(digest[:]),
	})
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("permission mismatch error = %v", err)
	}
	info, statErr := os.Lstat(duplicate)
	if statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("duplicate was mutated: mode=%v error=%v", info.Mode(), statErr)
	}
}

func TestStorageExtendedMetadataComparison(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	equal, err := storageExtendedMetadataEqual(first, second)
	if err != nil || !equal {
		t.Fatalf("empty metadata comparison = %t, %v", equal, err)
	}
	if err := unix.Setxattr(second, "user.fleetty-test", []byte("different"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
			t.Skipf("extended attributes are unavailable: %v", err)
		}
		t.Fatal(err)
	}
	equal, err = storageExtendedMetadataEqual(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("different extended metadata was accepted")
	}
}

func TestWriteStorageDedupeScriptQuotesPathsAndCreatesPrivateExecutable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hash := strings.Repeat("a", 64)
	group := storageDuplicateGroup{
		Hash: hash, Size: 1024, Reclaimable: 1024,
		Files: []storageDuplicateFile{
			{Path: "/data/keeper's file"},
			{Path: "/data/copy\nwith-newline"},
		},
	}
	path, err := writeStorageDedupeScript([]storageDuplicateGroup{group})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	if !strings.Contains(script, "dedupe-link --keep") ||
		!strings.Contains(script, `keeper'"'"'s file`) ||
		!strings.Contains(script, `# LINK /data/copy\nwith-newline`) {
		t.Fatalf("unexpected script:\n%s", script)
	}
	if strings.Contains(script, "# LINK /data/copy\nwith-newline\n") {
		t.Fatalf("newline was injected into a script comment:\n%s", script)
	}
}

func TestNormalizeStorageDedupeLinkRequestRejectsMissingAndInvalidHash(t *testing.T) {
	if _, err := normalizeStorageDedupeLinkRequest(storageDedupeLinkRequest{}); err == nil {
		t.Fatal("missing paths were accepted")
	}
	_, err := normalizeStorageDedupeLinkRequest(storageDedupeLinkRequest{
		KeepPath: "/a", ReplacePath: "/b", Hash: "not-a-hash",
	})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("invalid hash error = %v", err)
	}
}

func TestOperationsDispatchesDedupeLinkCommand(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	duplicate := filepath.Join(root, "duplicate")
	content := []byte("command-dispatch")
	if err := os.WriteFile(keep, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	var stdout, stderr byteutil.Buffer
	handled, err := runOperations([]string{
		"dedupe-link", "--keep", keep, "--replace", duplicate,
		"--sha256", hex.EncodeToString(digest[:]),
	}, &stdout, &stderr)
	if err != nil || !handled {
		t.Fatalf("runOperations() = handled %t, error %v, stderr %q",
			handled, err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Linked ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	info, err := os.Lstat(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("duplicate was not linked: mode=%v", info.Mode())
	}
}

func TestStorageDuplicateViewFillsTerminalAndSupportsMouseSelection(t *testing.T) {
	root := t.TempDir()
	groups := []storageDuplicateGroup{
		{
			Hash: strings.Repeat("a", 64), Size: 8 << 30, Reclaimable: 8 << 30,
			Files: []storageDuplicateFile{
				{Path: filepath.Join(root, "datasets", "canonical.bin")},
				{Path: filepath.Join(root, "backup", "copy.bin")},
			},
		},
		{
			Hash: strings.Repeat("b", 64), Size: 2 << 30, Reclaimable: 4 << 30,
			Files: []storageDuplicateFile{
				{Path: filepath.Join(root, "video.mp4")},
				{Path: filepath.Join(root, "copy-1.mp4")},
				{Path: filepath.Join(root, "copy-2.mp4")},
			},
		},
	}
	model := &monitorModel{
		screen: screenMonitor, width: 120, height: 32, monitorPage: monitorPageStorage,
		admin: &adminController{},
		snapshot: monitorSnapshot{
			CollectedAt: time.Now(), MemoryTotal: 1, DiskTotal: 1,
		},
		storage: &storageMapState{
			Root: root, Path: root, Scope: "HOME", DuplicateMode: true,
			Duplicates: &storageDuplicateState{
				Path: root,
				Result: storageDuplicateResult{
					Path: root, Groups: groups, Reclaimable: 12 << 30,
					FilesScanned: 20, CandidateFiles: 5, HashedFiles: 5,
					Workers: 4, FinishedAt: time.Now(),
				},
			},
		},
	}
	rendered := model.monitorView()
	for _, expected := range []string{
		"DUPLICATE FILES", "12.0 GiB RECLAIMABLE", "KEEP", "LINK", "script all",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("duplicate page missing %q:\n%s", expected, rendered)
		}
	}
	if got := lipgloss.Height(rendered); got != model.height {
		t.Fatalf("duplicate page height = %d, want %d", got, model.height)
	}
	if len(model.storage.Duplicates.Rows) != 2 {
		t.Fatalf("duplicate rows = %#v", model.storage.Duplicates.Rows)
	}
	second := model.storage.Duplicates.Rows[1]
	model.handleClick(second.X, second.Y)
	if model.storage.Duplicates.Cursor != 1 {
		t.Fatalf("clicked cursor = %d, want 1", model.storage.Duplicates.Cursor)
	}
}
