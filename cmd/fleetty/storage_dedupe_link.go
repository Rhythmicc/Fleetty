package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type storageDedupeLinkRequest struct {
	KeepPath    string
	ReplacePath string
	Hash        string
}

func runStorageDedupeLinkCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dedupe-link", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keep := flags.String("keep", "", "canonical file to retain")
	replace := flags.String("replace", "", "duplicate file to replace with a symbolic link")
	hash := flags.String("sha256", "", "expected SHA-256 content hash")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("dedupe-link does not accept positional arguments")
	}
	request, err := normalizeStorageDedupeLinkRequest(storageDedupeLinkRequest{
		KeepPath: *keep, ReplacePath: *replace, Hash: *hash,
	})
	if err != nil {
		return err
	}
	if err := replaceStorageDuplicateWithSymlink(context.Background(), request); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Linked %s -> %s\n", request.ReplacePath, request.KeepPath)
	return err
}

func normalizeStorageDedupeLinkRequest(
	request storageDedupeLinkRequest,
) (storageDedupeLinkRequest, error) {
	var err error
	keepPath := strings.TrimSpace(request.KeepPath)
	replacePath := strings.TrimSpace(request.ReplacePath)
	if keepPath == "" || replacePath == "" {
		return storageDedupeLinkRequest{}, errors.New("keep and replacement paths are required")
	}
	request.KeepPath, err = filepath.Abs(keepPath)
	if err != nil {
		return storageDedupeLinkRequest{}, fmt.Errorf("resolve keep path: %w", err)
	}
	request.ReplacePath, err = filepath.Abs(replacePath)
	if err != nil {
		return storageDedupeLinkRequest{}, fmt.Errorf("resolve replacement path: %w", err)
	}
	request.KeepPath = filepath.Clean(request.KeepPath)
	request.ReplacePath = filepath.Clean(request.ReplacePath)
	if request.KeepPath == request.ReplacePath {
		return storageDedupeLinkRequest{}, errors.New("keep and replacement paths must differ")
	}
	request.Hash = strings.ToLower(strings.TrimSpace(request.Hash))
	decoded, err := hex.DecodeString(request.Hash)
	if err != nil || len(decoded) != 32 {
		return storageDedupeLinkRequest{}, errors.New("sha256 must be a 64-character hexadecimal digest")
	}
	return request, nil
}

func replaceStorageDuplicateWithSymlink(
	ctx context.Context,
	request storageDedupeLinkRequest,
) error {
	request, err := normalizeStorageDedupeLinkRequest(request)
	if err != nil {
		return err
	}
	keepInfo, err := inspectRegularDedupeFile(request.KeepPath, "keep")
	if err != nil {
		return err
	}
	replaceInfo, err := inspectRegularDedupeFile(request.ReplacePath, "replacement")
	if err != nil {
		return err
	}
	if sameStorageIdentity(keepInfo, replaceInfo) {
		return errors.New("keep and replacement paths already reference the same physical file")
	}
	if keepInfo.Size() != replaceInfo.Size() {
		return errors.New("files no longer have the same size")
	}
	if err := validateStorageDedupeMetadata(
		request.KeepPath, keepInfo, request.ReplacePath, replaceInfo,
	); err != nil {
		return err
	}
	if err := verifyStorageDedupeHash(ctx, request.KeepPath, request.Hash, "keep"); err != nil {
		return err
	}
	if err := verifyStorageDedupeHash(ctx, request.ReplacePath, request.Hash, "replacement"); err != nil {
		return err
	}
	currentKeep, err := inspectRegularDedupeFile(request.KeepPath, "keep")
	if err != nil {
		return err
	}
	currentReplace, err := inspectRegularDedupeFile(request.ReplacePath, "replacement")
	if err != nil {
		return err
	}
	if !sameStorageIdentity(keepInfo, currentKeep) ||
		!sameStorageIdentity(replaceInfo, currentReplace) {
		return errors.New("a file changed while its content was being verified")
	}

	parent := filepath.Dir(request.ReplacePath)
	workspace, err := os.MkdirTemp(parent, ".fleetty-dedupe-")
	if err != nil {
		return fmt.Errorf("create dedupe workspace: %w", err)
	}
	payload := filepath.Join(workspace, "duplicate")
	link := filepath.Join(workspace, "replacement-link")
	if err := os.Rename(request.ReplacePath, payload); err != nil {
		_ = os.Remove(workspace)
		return fmt.Errorf("isolate duplicate file: %w", err)
	}
	restore := func(cause error) error {
		_ = os.Remove(link)
		if linkInfo, linkErr := os.Lstat(request.ReplacePath); linkErr == nil &&
			linkInfo.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(request.ReplacePath)
		}
		if _, originalErr := os.Lstat(request.ReplacePath); errors.Is(originalErr, os.ErrNotExist) {
			if restoreErr := os.Rename(payload, request.ReplacePath); restoreErr == nil {
				_ = os.Remove(workspace)
				return fmt.Errorf("%w; original duplicate restored", cause)
			}
		}
		return fmt.Errorf("%w; recover the duplicate from %s", cause, workspace)
	}
	payloadInfo, err := inspectRegularDedupeFile(payload, "isolated replacement")
	if err != nil {
		return restore(err)
	}
	if !sameStorageIdentity(replaceInfo, payloadInfo) {
		return restore(errors.New("replacement identity changed while dedupe started"))
	}
	if err := verifyStorageDedupeHash(ctx, request.KeepPath, request.Hash, "keep"); err != nil {
		return restore(err)
	}
	if err := verifyStorageDedupeHash(ctx, payload, request.Hash, "isolated replacement"); err != nil {
		return restore(err)
	}
	finalKeepInfo, err := inspectRegularDedupeFile(request.KeepPath, "keep")
	if err != nil {
		return restore(err)
	}
	finalPayloadInfo, err := inspectRegularDedupeFile(payload, "isolated replacement")
	if err != nil {
		return restore(err)
	}
	if !sameStorageIdentity(currentKeep, finalKeepInfo) ||
		!sameStorageIdentity(payloadInfo, finalPayloadInfo) {
		return restore(errors.New("a file changed during final dedupe verification"))
	}
	if err := validateStorageDedupeMetadata(
		request.KeepPath, finalKeepInfo, payload, finalPayloadInfo,
	); err != nil {
		return restore(err)
	}
	relativeTarget, err := filepath.Rel(parent, request.KeepPath)
	if err != nil {
		return restore(fmt.Errorf("build relative symbolic link: %w", err))
	}
	if relativeTarget == "." || strings.TrimSpace(relativeTarget) == "" {
		return restore(errors.New("relative symbolic link target is invalid"))
	}
	if err := os.Symlink(relativeTarget, link); err != nil {
		return restore(fmt.Errorf("create replacement symbolic link: %w", err))
	}
	if err := os.Rename(link, request.ReplacePath); err != nil {
		return restore(fmt.Errorf("publish replacement symbolic link: %w", err))
	}
	publishedInfo, err := os.Stat(request.ReplacePath)
	if err != nil || !sameStorageIdentity(finalKeepInfo, publishedInfo) {
		if err == nil {
			err = errors.New("published link resolves to an unexpected file")
		}
		return restore(fmt.Errorf("verify replacement symbolic link: %w", err))
	}
	if err := os.Remove(payload); err != nil {
		return restore(fmt.Errorf("remove verified duplicate payload: %w", err))
	}
	if err := os.Remove(workspace); err != nil {
		return fmt.Errorf("remove empty dedupe workspace %s: %w", workspace, err)
	}
	return nil
}

func validateStorageDedupeMetadata(
	firstPath string,
	firstInfo os.FileInfo,
	secondPath string,
	secondInfo os.FileInfo,
) error {
	if firstInfo.Mode().Perm() != secondInfo.Mode().Perm() {
		return errors.New("files have different permission modes; refusing to change access semantics")
	}
	firstUID, firstGID, firstOwnerOK := storageFileOwner(firstInfo)
	secondUID, secondGID, secondOwnerOK := storageFileOwner(secondInfo)
	if firstOwnerOK != secondOwnerOK ||
		firstOwnerOK && (firstUID != secondUID || firstGID != secondGID) {
		return errors.New("files have different owners; refusing to change access semantics")
	}
	metadataEqual, err := storageExtendedMetadataEqual(firstPath, secondPath)
	if err != nil {
		return err
	}
	if !metadataEqual {
		return errors.New("files have different extended attributes or ACL metadata")
	}
	return nil
}

func inspectRegularDedupeFile(path, label string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s file: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s path is not a regular non-symlink file", label)
	}
	return info, nil
}

func verifyStorageDedupeHash(
	ctx context.Context,
	path, expected, label string,
) error {
	actual, err := hashStorageFile(ctx, path)
	if err != nil {
		return fmt.Errorf("hash %s file: %w", label, err)
	}
	if actual != expected {
		return fmt.Errorf("%s file content no longer matches the planned SHA-256", label)
	}
	return nil
}
