package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageActionValidationProtectsRootsLinksAndMounts(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "data.bin")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := storageEntry{Name: "data.bin", Path: filePath, Size: 4}
	request, err := newStorageActionRequest(storageActionDelete, entry, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if request.SourceKey == "" || request.Path != filePath {
		t.Fatalf("storage request did not bind the source identity: %#v", request)
	}
	if _, err := newStorageActionRequest(storageActionDelete,
		storageEntry{Name: filepath.Base(root), Path: root, IsDir: true},
		root, filepath.Dir(root)); err == nil {
		t.Fatal("storage scan root was accepted for deletion")
	}

	linkPath := filepath.Join(root, "data-link")
	if err := os.Symlink(filePath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := newStorageActionRequest(storageActionDelete,
		storageEntry{Name: "data-link", Path: linkPath}, root, root); err == nil {
		t.Fatal("symbolic link was accepted for deletion")
	}

	mountPoint, fileSystem, ok := storageMountWithinTarget(
		filepath.Join(root, "models"),
		map[string]string{
			"/":                                     "apfs",
			filepath.Join(root, "models", "remote"): "nfs",
		},
	)
	if !ok || mountPoint != filepath.Join(root, "models", "remote") ||
		fileSystem != "nfs" {
		t.Fatalf("nested mount was not detected: %q %q %t", mountPoint, fileSystem, ok)
	}
	if _, _, ok := storageMountWithinTarget(
		filepath.Join(root, "models"), map[string]string{"/": "apfs"},
	); ok {
		t.Fatal("the filesystem root was incorrectly treated as nested in every target")
	}
}

func TestStorageDeleteRejectsChangedIdentityAndRemovesConfirmedTarget(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "old.bin")
	if err := os.WriteFile(filePath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "old.bin", Path: filePath, Size: 3},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := performStorageAction(context.Background(), request, nil); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed target identity was not rejected: %v", err)
	}
	if content, err := os.ReadFile(filePath); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement target was modified: %q %v", content, err)
	}

	confirmed, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "old.bin", Path: filePath, Size: 11},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := performStorageAction(context.Background(), confirmed, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed target still exists: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".fleetty-delete-*")); err != nil ||
		len(matches) != 0 {
		t.Fatalf("successful deletion left quarantines behind: %#v, %v", matches, err)
	}
}

func TestStorageArchiveIsVerifiedBeforeOriginalIsDeleted(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "checkpoint.bin")
	if err := os.WriteFile(filePath, []byte("model weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "checkpoint.bin", Path: filePath, Size: 13},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = storageActionArchiveDelete
	request.SevenZip = "/fake/7z"
	var calls []string
	runner := func(_ context.Context, spec storageCommandSpec) error {
		calls = append(calls, spec.Arguments[0])
		switch spec.Arguments[0] {
		case "a":
			if _, err := os.Lstat(filePath); !errors.Is(err, os.ErrNotExist) {
				return errors.New("source was not isolated before 7z started")
			}
			if _, err := os.Stat(filepath.Join(spec.Directory, filepath.Base(filePath))); err != nil {
				return errors.New("isolated source is missing from archive workspace")
			}
			if got := spec.Arguments[len(spec.Arguments)-1]; got != "./checkpoint.bin" {
				return errors.New("7z source name does not preserve the original basename")
			}
			return os.WriteFile(spec.Arguments[6], []byte("valid archive"), 0o600)
		case "t":
			if _, err := os.Stat(spec.Arguments[3]); err != nil {
				return err
			}
			return nil
		default:
			return errors.New("unexpected 7z operation")
		}
	}
	archivePath, err := performStorageAction(context.Background(), request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "a,t" {
		t.Fatalf("7z operations = %#v, want create then test", calls)
	}
	if archivePath != filePath+".7z" {
		t.Fatalf("archive path = %q", archivePath)
	}
	if content, err := os.ReadFile(archivePath); err != nil ||
		string(content) != "valid archive" {
		t.Fatalf("published archive = %q, %v", content, err)
	}
	if _, err := os.Lstat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists after verified archive: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".fleetty-archive-*")); err != nil ||
		len(matches) != 0 {
		t.Fatalf("successful archive left workspaces behind: %#v, %v", matches, err)
	}
}

func TestStorageArchiveFailureKeepsOriginalAndDoesNotPublishArchive(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "dataset")
	if err := os.Mkdir(filePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filePath, "part.bin"), []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "dataset", Path: filePath, Size: 4, IsDir: true},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = storageActionArchiveDelete
	request.SevenZip = "/fake/7z"
	runner := func(_ context.Context, spec storageCommandSpec) error {
		if spec.Arguments[0] == "a" {
			return os.WriteFile(spec.Arguments[6], []byte("broken archive"), 0o600)
		}
		return errors.New("archive CRC check failed")
	}
	archivePath, err := performStorageAction(context.Background(), request, runner)
	if err == nil || archivePath != "" || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("verification failure = archive %q, err %v", archivePath, err)
	}
	if _, err := os.Stat(filepath.Join(filePath, "part.bin")); err != nil {
		t.Fatalf("original was modified after verification failure: %v", err)
	}
	if _, err := os.Lstat(filePath + ".7z"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed archive was published: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".fleetty-archive-*")); err != nil ||
		len(matches) != 0 {
		t.Fatalf("failed archive was not restored cleanly: %#v, %v", matches, err)
	}
}

func TestStorageArchiveCancellationRestoresOriginal(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "cancel-me.bin")
	if err := os.WriteFile(filePath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "cancel-me.bin", Path: filePath, Size: 7},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = storageActionArchiveDelete
	request.SevenZip = "/fake/7z"
	ctx, cancel := context.WithCancel(context.Background())
	runner := func(_ context.Context, spec storageCommandSpec) error {
		if spec.Arguments[0] == "a" {
			if err := os.WriteFile(spec.Arguments[6], []byte("partial"), 0o600); err != nil {
				return err
			}
			cancel()
			return nil
		}
		return ctx.Err()
	}
	archivePath, err := performStorageAction(ctx, request, runner)
	if err == nil || archivePath != "" {
		t.Fatalf("cancelled archive = %q, %v", archivePath, err)
	}
	if content, readErr := os.ReadFile(filePath); readErr != nil ||
		string(content) != "keep me" {
		t.Fatalf("cancelled archive did not restore source: %q, %v", content, readErr)
	}
	if _, statErr := os.Lstat(filePath + ".7z"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled archive was published: %v", statErr)
	}
}

func TestStorageArchiveWithInstalledSevenZip(t *testing.T) {
	sevenZip, err := findSevenZip()
	if err != nil {
		t.Skip("7z is not installed in the test environment")
	}
	root := t.TempDir()
	source := filepath.Join(root, "real-archive")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "weights.bin"),
		[]byte("verified payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := newStorageActionRequest(
		storageActionDelete,
		storageEntry{Name: "real-archive", Path: source, Size: 16, IsDir: true},
		root, root,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = storageActionArchiveDelete
	request.SevenZip = sevenZip
	archivePath, err := performStorageAction(
		context.Background(), request, runStorageCommand,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("verified archive is missing: %v", err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains after real verified archive: %v", err)
	}
}

func TestStorageArchiveNeverOverwritesExistingFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "models")
	if err := os.WriteFile(source+".7z", []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := nextStorageArchivePath(source); err != nil || got != source+"-1.7z" {
		t.Fatalf("collision-safe archive path = %q, %v", got, err)
	}
}

func TestStorageConfirmationRequiresPhraseAndInvalidatesRelatedCache(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "cache")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	model := &monitorModel{
		screen: screenMonitor,
		storage: &storageMapState{
			Root: root, Path: root, Scope: "HOME",
			Rects: []storageMapRect{{
				Entry: storageEntry{
					Name: "cache", Path: child, Size: 1, IsDir: true,
				},
			}},
			Cache: map[string]storageCachedResult{
				root:  {},
				child: {},
			},
			CacheOrder: []string{root, child},
		},
	}
	model.prepareStorageAction(storageActionDelete)
	if model.screen != screenStorageConfirm || model.storageAction == nil {
		t.Fatalf("delete did not open a confirmation screen: %#v", model)
	}
	if view := model.storageConfirmView(); !strings.Contains(view, "PERMANENTLY DELETE") ||
		!strings.Contains(view, filepath.Base(child)) || !strings.Contains(view, "Type") {
		t.Fatalf("storage confirmation does not identify the destructive target:\n%s", view)
	}
	model.appendStorageConfirmation("wrong")
	if command := model.handleKey(testKey("enter")); command != nil || model.busy {
		t.Fatal("incorrect confirmation phrase started deletion")
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("incorrect confirmation changed the target: %v", err)
	}
	model.storageConfirm = ""
	model.appendStorageConfirmation("delete")
	if model.storageConfirm != "DELETE" {
		t.Fatalf("confirmation input = %q", model.storageConfirm)
	}
	command := model.handleKey(testKey("enter"))
	if command == nil || !model.busy {
		t.Fatal("matching confirmation phrase did not start deletion")
	}
	message := command()
	result, ok := message.(storageActionResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("storage action result = %#v", message)
	}
	refresh := model.applyStorageActionResult(result)
	if refresh == nil || model.screen != screenMonitor || model.busy {
		t.Fatalf("storage action did not return to the monitor: %#v", model)
	}
	if len(model.storage.Cache) != 0 || len(model.storage.CacheOrder) != 0 {
		t.Fatalf("related storage cache was not invalidated: %#v", model.storage.Cache)
	}
	model.cancelStorageScan()
}
