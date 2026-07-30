package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const storageDuplicateMinimumSize = 1 << 20

type storageDuplicateState struct {
	Path       string
	Result     storageDuplicateResult
	Err        error
	Scanning   bool
	Generation uint64
	Cancel     context.CancelFunc
	Cursor     int
	Rows       []storageHitRect
	RowStart   int
	ScriptPath string
}

type storageDuplicateResult struct {
	Path           string
	Groups         []storageDuplicateGroup
	FilesScanned   uint64
	CandidateFiles uint64
	HashedFiles    uint64
	BytesHashed    uint64
	Skipped        uint64
	Reclaimable    uint64
	ExcludedMounts uint64
	Workers        int
	Duration       time.Duration
	FinishedAt     time.Time
}

type storageDuplicateGroup struct {
	Hash        string
	Size        uint64
	Reclaimable uint64
	Files       []storageDuplicateFile
	Keep        int
}

type storageDuplicateFile struct {
	Path      string
	Identity  string
	Allocated uint64
}

type storageDuplicateUpdate struct {
	result storageDuplicateResult
	err    error
	done   bool
}

type storageDuplicateCandidate struct {
	Path      string
	Size      uint64
	Allocated uint64
	Identity  string
	Inode     string
	Mode      os.FileMode
	UID       uint32
	GID       uint32
	HasOwner  bool
}

type storageDuplicateHashResult struct {
	Candidate storageDuplicateCandidate
	Hash      string
	Err       error
}

type storageDuplicateContentKey struct {
	Size     uint64
	Hash     string
	Mode     os.FileMode
	UID      uint32
	GID      uint32
	HasOwner bool
}

func (m *monitorModel) beginStorageDuplicateScan(path string) tea.Cmd {
	if m.storage == nil {
		return nil
	}
	path = filepath.Clean(path)
	if !storagePathWithinRoot(path, m.storage.Root) {
		m.status = "Duplicate scanning is restricted to " + m.storage.Root + "."
		return nil
	}
	m.cancelStorageScan()
	m.storage.Generation++
	m.storage.Scanning = false
	m.cancelStorageDuplicateScan()
	if m.storage.Duplicates == nil {
		m.storage.Duplicates = &storageDuplicateState{}
	}
	state := m.storage.Duplicates
	state.Generation++
	generation := state.Generation
	ctx, cancel := context.WithCancel(context.Background())
	state.Path = path
	state.Result = storageDuplicateResult{Path: path}
	state.Err = nil
	state.Scanning = true
	state.Cancel = cancel
	state.Cursor = 0
	state.Rows = nil
	state.ScriptPath = ""
	m.storage.DuplicateMode = true
	m.status = fmt.Sprintf("Finding duplicate files of at least %s under %s…",
		bytes(storageDuplicateMinimumSize), path)
	return func() tea.Msg {
		updates := make(chan storageDuplicateUpdate, 1)
		go func() {
			result, err := scanStorageDuplicates(ctx, path, func(result storageDuplicateResult) {
				publishStorageDuplicateUpdate(updates, storageDuplicateUpdate{result: result})
			})
			publishStorageDuplicateUpdate(updates, storageDuplicateUpdate{
				result: result, err: err, done: true,
			})
			close(updates)
		}()
		return waitForStorageDuplicateUpdate(generation, updates)()
	}
}

func (m *monitorModel) cancelStorageDuplicateScan() {
	if m.storage == nil || m.storage.Duplicates == nil {
		return
	}
	if m.storage.Duplicates.Cancel != nil {
		m.storage.Duplicates.Cancel()
		m.storage.Duplicates.Cancel = nil
	}
}

func (m *monitorModel) leaveStorageDuplicates() tea.Cmd {
	if m.storage == nil || !m.storage.DuplicateMode {
		return nil
	}
	m.cancelStorageDuplicateScan()
	if m.storage.Duplicates != nil {
		m.storage.Duplicates.Generation++
		m.storage.Duplicates.Scanning = false
	}
	m.storage.DuplicateMode = false
	m.status = "Returned to the storage map."
	return forceFullScreenRedraw()
}

func (m *monitorModel) applyStorageDuplicateProgress(msg storageDuplicateProgressMsg) bool {
	state := m.storageDuplicateState()
	if state == nil || msg.generation != state.Generation || !state.Scanning {
		return false
	}
	state.Result = msg.result
	state.Err = nil
	m.status = fmt.Sprintf("Duplicate scan: %d files inspected · %d/%d candidates hashed…",
		msg.result.FilesScanned, msg.result.HashedFiles, msg.result.CandidateFiles)
	return true
}

func (m *monitorModel) applyStorageDuplicateResult(msg storageDuplicateResultMsg) {
	state := m.storageDuplicateState()
	if state == nil || msg.generation != state.Generation {
		return
	}
	state.Cancel = nil
	state.Scanning = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return
		}
		state.Err = msg.err
		m.status = "Duplicate scan failed: " + msg.err.Error()
		return
	}
	state.Result = msg.result
	state.Err = nil
	state.Cursor = min(max(0, state.Cursor), max(0, len(msg.result.Groups)-1))
	m.status = fmt.Sprintf("Found %d verified duplicate groups · %s reclaimable.",
		len(msg.result.Groups), bytes(msg.result.Reclaimable))
}

func (m *monitorModel) storageDuplicateState() *storageDuplicateState {
	if m.storage == nil || !m.storage.DuplicateMode {
		return nil
	}
	return m.storage.Duplicates
}

func (m *monitorModel) moveStorageDuplicateSelection(delta int) {
	state := m.storageDuplicateState()
	if state == nil || len(state.Result.Groups) == 0 {
		return
	}
	state.Cursor = min(max(0, state.Cursor+delta), len(state.Result.Groups)-1)
	group := state.Result.Groups[state.Cursor]
	m.status = fmt.Sprintf("%d copies · %s each · %s reclaimable.",
		len(group.Files), bytes(group.Size), bytes(group.Reclaimable))
}

func (m *monitorModel) cycleStorageDuplicateKeeper() {
	state := m.storageDuplicateState()
	if state == nil || len(state.Result.Groups) == 0 {
		return
	}
	group := &state.Result.Groups[state.Cursor]
	if len(group.Files) < 2 {
		return
	}
	previous := group.Reclaimable
	group.Keep = (group.Keep + 1) % len(group.Files)
	group.Reclaimable = storageDuplicateReclaimable(*group)
	if state.Result.Reclaimable >= previous {
		state.Result.Reclaimable -= previous
	} else {
		state.Result.Reclaimable = 0
	}
	state.Result.Reclaimable += group.Reclaimable
	m.status = "Keeping " + group.Files[group.Keep].Path + "."
}

func (m *monitorModel) handleStorageDuplicateClick(x, y int) tea.Cmd {
	state := m.storageDuplicateState()
	if state == nil {
		return nil
	}
	for index, row := range state.Rows {
		if storageHit(&row, x, y) {
			groupIndex := state.RowStart + index
			if groupIndex < 0 || groupIndex >= len(state.Result.Groups) {
				return nil
			}
			state.Cursor = groupIndex
			group := state.Result.Groups[groupIndex]
			m.status = fmt.Sprintf("%d copies · %s reclaimable.",
				len(group.Files), bytes(group.Reclaimable))
			return nil
		}
	}
	return nil
}

func publishStorageDuplicateUpdate(
	updates chan storageDuplicateUpdate,
	update storageDuplicateUpdate,
) {
	select {
	case updates <- update:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	updates <- update
}

func waitForStorageDuplicateUpdate(
	generation uint64,
	updates <-chan storageDuplicateUpdate,
) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return storageDuplicateResultMsg{generation: generation, err: context.Canceled}
		}
		if update.done {
			return storageDuplicateResultMsg{
				generation: generation, result: update.result, err: update.err,
			}
		}
		return storageDuplicateProgressMsg{
			generation: generation, result: update.result, updates: updates,
		}
	}
}

func scanStorageDuplicates(
	ctx context.Context,
	root string,
	publish func(storageDuplicateResult),
) (storageDuplicateResult, error) {
	policy, err := localStorageMountPolicy()
	if err != nil {
		return storageDuplicateResult{}, fmt.Errorf("inspect local mount table: %w", err)
	}
	return scanStorageDuplicatesWithPolicy(ctx, root, policy, publish)
}

func scanStorageDuplicatesWithPolicy(
	ctx context.Context,
	root string,
	policy storageMountPolicy,
	publish func(storageDuplicateResult),
) (storageDuplicateResult, error) {
	started := time.Now()
	root = filepath.Clean(root)
	result := storageDuplicateResult{Path: root, Workers: 1}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return result, err
	}
	if !rootInfo.IsDir() {
		return result, errors.New("duplicate scan target is not a directory")
	}
	candidatesBySize := make(map[uint64][]storageDuplicateCandidate)
	seen := make(map[string]struct{})
	lastPublished := time.Time{}
	publishProgress := func(force bool) {
		if publish == nil {
			return
		}
		now := time.Now()
		if !force && !lastPublished.IsZero() &&
			now.Sub(lastPublished) < storageScanPublishInterval {
			return
		}
		snapshot := result
		snapshot.Duration = now.Sub(started)
		publish(snapshot)
		lastPublished = now
	}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path = filepath.Clean(path)
		if walkErr != nil {
			result.Skipped++
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != root {
			if _, excluded := policy.excluded[path]; excluded {
				result.ExcludedMounts++
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.Skipped++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			result.Skipped++
			return nil
		}
		result.FilesScanned++
		if info.Size() < storageDuplicateMinimumSize {
			publishProgress(false)
			return nil
		}
		allocated, inode := storageAllocatedSize(info)
		identity := storageFileIdentity(info)
		if inode == "" {
			inode = identity
		}
		if inode != "" {
			if _, duplicateInode := seen[inode]; duplicateInode {
				publishProgress(false)
				return nil
			}
			seen[inode] = struct{}{}
		}
		size := uint64(info.Size())
		uid, gid, hasOwner := storageFileOwner(info)
		candidatesBySize[size] = append(candidatesBySize[size], storageDuplicateCandidate{
			Path: path, Size: size, Allocated: allocated, Identity: identity, Inode: inode,
			Mode: info.Mode().Perm(), UID: uid, GID: gid, HasOwner: hasOwner,
		})
		publishProgress(false)
		return nil
	})
	if err != nil {
		return result, err
	}

	var candidates []storageDuplicateCandidate
	for _, sized := range candidatesBySize {
		if len(sized) > 1 {
			candidates = append(candidates, sized...)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	result.CandidateFiles = uint64(len(candidates))
	workers := storageScanWorkerCount(len(candidates))
	result.Workers = max(1, workers)
	publishProgress(true)
	if len(candidates) == 0 {
		result.FinishedAt = time.Now()
		result.Duration = time.Since(started)
		return result, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan storageDuplicateCandidate)
	hashes := make(chan storageDuplicateHashResult, workers)
	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			for candidate := range jobs {
				hash, hashErr := hashStorageCandidate(ctx, candidate)
				select {
				case hashes <- storageDuplicateHashResult{
					Candidate: candidate, Hash: hash, Err: hashErr,
				}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case jobs <- candidate:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workerGroup.Wait()
		close(hashes)
	}()

	grouped := make(map[storageDuplicateContentKey][]storageDuplicateFile)
	for hashed := range hashes {
		if hashed.Err != nil {
			if errors.Is(hashed.Err, context.Canceled) {
				return result, hashed.Err
			}
			result.Skipped++
			continue
		}
		result.HashedFiles++
		result.BytesHashed += hashed.Candidate.Size
		key := storageDuplicateContentKey{
			Size: hashed.Candidate.Size, Hash: hashed.Hash,
			Mode: hashed.Candidate.Mode, UID: hashed.Candidate.UID,
			GID: hashed.Candidate.GID, HasOwner: hashed.Candidate.HasOwner,
		}
		grouped[key] = append(grouped[key], storageDuplicateFile{
			Path: hashed.Candidate.Path, Identity: hashed.Candidate.Identity,
			Allocated: hashed.Candidate.Allocated,
		})
		publishProgress(false)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	for key, files := range grouped {
		if len(files) < 2 {
			continue
		}
		sort.Slice(files, func(i, j int) bool {
			leftDepth := strings.Count(filepath.Clean(files[i].Path), string(filepath.Separator))
			rightDepth := strings.Count(filepath.Clean(files[j].Path), string(filepath.Separator))
			if leftDepth != rightDepth {
				return leftDepth < rightDepth
			}
			if len(files[i].Path) != len(files[j].Path) {
				return len(files[i].Path) < len(files[j].Path)
			}
			return files[i].Path < files[j].Path
		})
		group := storageDuplicateGroup{Hash: key.Hash, Size: key.Size, Files: files}
		group.Reclaimable = storageDuplicateReclaimable(group)
		result.Groups = append(result.Groups, group)
		result.Reclaimable += group.Reclaimable
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Reclaimable == result.Groups[j].Reclaimable {
			return result.Groups[i].Files[0].Path < result.Groups[j].Files[0].Path
		}
		return result.Groups[i].Reclaimable > result.Groups[j].Reclaimable
	})
	result.FinishedAt = time.Now()
	result.Duration = time.Since(started)
	publishProgress(true)
	return result, nil
}

func storageDuplicateReclaimable(group storageDuplicateGroup) uint64 {
	reclaimable := uint64(0)
	for index, file := range group.Files {
		if index != group.Keep {
			reclaimable += file.Allocated
		}
	}
	return reclaimable
}

func hashStorageFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashStorageCandidate(
	ctx context.Context,
	candidate storageDuplicateCandidate,
) (string, error) {
	file, err := os.Open(candidate.Path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || uint64(before.Size()) != candidate.Size ||
		candidate.Identity != "" && storageFileIdentity(before) != candidate.Identity {
		return "", errors.New("candidate changed before hashing")
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !sameStorageIdentity(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() {
		return "", errors.New("candidate changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *monitorModel) generateStorageDedupeScript(all bool) tea.Cmd {
	state := m.storageDuplicateState()
	if state == nil || len(state.Result.Groups) == 0 {
		m.status = "No verified duplicate group is selected."
		return nil
	}
	groups := []storageDuplicateGroup{state.Result.Groups[state.Cursor]}
	if all {
		groups = append([]storageDuplicateGroup(nil), state.Result.Groups...)
	}
	m.status = "Generating a reviewable dedupe script…"
	return func() tea.Msg {
		path, err := writeStorageDedupeScript(groups)
		return storageDedupeScriptResultMsg{path: path, groups: len(groups), err: err}
	}
}

func writeStorageDedupeScript(groups []storageDuplicateGroup) (string, error) {
	if len(groups) == 0 {
		return "", errors.New("dedupe plan is empty")
	}
	configDirectory, err := storageDedupeConfigDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	planDirectory := filepath.Join(configDirectory, "fleetty", "dedupe-plans")
	if err := os.MkdirAll(planDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create dedupe plan directory: %w", err)
	}
	if err := os.Chmod(planDirectory, 0o700); err != nil {
		return "", fmt.Errorf("secure dedupe plan directory: %w", err)
	}
	planInfo, err := os.Lstat(planDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect dedupe plan directory: %w", err)
	}
	if !planInfo.IsDir() || planInfo.Mode()&os.ModeSymlink != 0 ||
		!ownedBy(planInfo, os.Geteuid(), os.Getegid()) {
		return "", errors.New("dedupe plan directory must be a real directory owned by the current user")
	}
	resolvedConfig, configErr := filepath.EvalSymlinks(configDirectory)
	resolvedPlan, planErr := filepath.EvalSymlinks(planDirectory)
	if configErr != nil || planErr != nil ||
		!storagePathWithinRoot(resolvedPlan, resolvedConfig) {
		return "", errors.New("dedupe plan directory resolves outside the user configuration directory")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve Fleetty executable: %w", err)
	}
	name := "dedupe-" + time.Now().Format("20060102-150405.000000000") + ".sh"
	path := filepath.Join(planDirectory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", fmt.Errorf("create dedupe plan: %w", err)
	}
	writer := bufio.NewWriter(file)
	fail := func(cause error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", cause
	}
	fmt.Fprintln(writer, "#!/bin/sh")
	fmt.Fprintln(writer, "set -eu")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "FLEETTY_BIN=${FLEETTY_BIN:-%s}\n", shellQuote(executable))
	fmt.Fprintln(writer, `if [ ! -x "$FLEETTY_BIN" ]; then`)
	fmt.Fprintln(writer, `  echo "Fleetty executable is unavailable: $FLEETTY_BIN" >&2`)
	fmt.Fprintln(writer, "  exit 1")
	fmt.Fprintln(writer, "fi")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "# Review every KEEP/LINK pair before running this file.")
	fmt.Fprintln(writer, "# Each command re-hashes both files and atomically replaces only the LINK path.")
	commands := 0
	for groupIndex, group := range groups {
		if len(group.Files) < 2 || group.Keep < 0 || group.Keep >= len(group.Files) ||
			len(group.Hash) != sha256.Size*2 {
			return fail(fmt.Errorf("duplicate group %d is invalid", groupIndex+1))
		}
		if decoded, decodeErr := hex.DecodeString(group.Hash); decodeErr != nil ||
			len(decoded) != sha256.Size {
			return fail(fmt.Errorf("duplicate group %d has an invalid SHA-256", groupIndex+1))
		}
		keep := group.Files[group.Keep].Path
		fmt.Fprintln(writer)
		fmt.Fprintf(writer, "# GROUP %d · %s · %d copies · %s reclaimable\n",
			groupIndex+1, group.Hash[:12], len(group.Files), bytes(group.Reclaimable))
		fmt.Fprintf(writer, "# KEEP %s\n", storageScriptCommentPath(keep))
		for index, duplicate := range group.Files {
			if index == group.Keep {
				continue
			}
			fmt.Fprintf(writer, "# LINK %s\n", storageScriptCommentPath(duplicate.Path))
			fmt.Fprintf(writer,
				`"$FLEETTY_BIN" dedupe-link --keep %s --replace %s --sha256 %s`+"\n",
				shellQuote(keep), shellQuote(duplicate.Path), shellQuote(group.Hash))
			commands++
		}
	}
	if commands == 0 {
		return fail(errors.New("dedupe plan contains no replacements"))
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "echo %s\n", shellQuote(fmt.Sprintf(
		"Fleetty dedupe plan complete: %d paths replaced.", commands)))
	if err := writer.Flush(); err != nil {
		return fail(fmt.Errorf("write dedupe plan: %w", err))
	}
	if err := file.Sync(); err != nil {
		return fail(fmt.Errorf("sync dedupe plan: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close dedupe plan: %w", err)
	}
	return path, nil
}

func storageDedupeConfigDirectory() (string, error) {
	if os.Geteuid() != 0 {
		return os.UserConfigDir()
	}
	account, err := user.Current()
	if err != nil || account == nil || strings.TrimSpace(account.HomeDir) == "" {
		return "", errors.New("could not resolve the root account home directory")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(account.HomeDir, "Library", "Application Support"), nil
	}
	return filepath.Join(account.HomeDir, ".config"), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func storageScriptCommentPath(path string) string {
	path = strings.ReplaceAll(path, "\r", `\r`)
	return strings.ReplaceAll(path, "\n", `\n`)
}

func renderStorageDuplicates(m *monitorModel, width int) (string, []widgetPlacement) {
	bodyHeight := max(3, m.height-lipgloss.Height(m.renderMonitorPageHeader(width))-
		lipgloss.Height(m.renderMonitorPageFooter(width)))
	contentHeight := max(1, bodyHeight-2)
	contentWidth := max(1, width-4)
	state := m.storageDuplicateState()
	if state == nil {
		return btopPanel(width, "DUPLICATE FILES", "UNAVAILABLE",
			"Duplicate view is unavailable.", diskTitleStyle, colorDiskBorder), nil
	}
	state.Rows = nil
	state.RowStart = 0
	result := state.Result
	meta := fmt.Sprintf("SHA-256 · ≥%s · ×%d", bytes(storageDuplicateMinimumSize),
		max(1, result.Workers))
	if state.Scanning {
		meta = "SCANNING · " + meta
	} else if state.Err != nil {
		meta = "ERROR · " + meta
	}
	summary := fmt.Sprintf("%d GROUPS  ·  %s RECLAIMABLE  ·  %d FILES  ·  %d/%d HASHED",
		len(result.Groups), bytes(result.Reclaimable), result.FilesScanned,
		result.HashedFiles, result.CandidateFiles)
	lines := []string{
		accentStyle.Render(ansi.Truncate(storageBreadcrumb(m.storage.Root, state.Path, contentWidth), contentWidth, "…")),
		dimStyle.Render(ansi.Truncate(summary, contentWidth, "…")),
	}
	if state.ScriptPath != "" {
		lines = append(lines, processRunningStyle.Render("PLAN  ")+
			ansi.Truncate(state.ScriptPath, max(8, contentWidth-6), "…"))
	}
	if state.Err != nil {
		lines = append(lines, dangerStyle.Render(ansi.Truncate(state.Err.Error(), contentWidth, "…")))
	} else if state.Scanning && len(result.Groups) == 0 {
		lines = append(lines,
			diskTitleStyle.Render("Hashing same-size candidates in parallel…"),
			dimStyle.Render("The list will appear after strong hashes have been verified."),
		)
	} else if len(result.Groups) == 0 {
		lines = append(lines,
			valueStyle.Render("No exact duplicates found at the current minimum size."),
			dimStyle.Render("Press r to scan again or u to return to the storage map."),
		)
	} else {
		groupSpace := max(4, (contentHeight-4)/2)
		groupRows := min(len(result.Groups), max(1, groupSpace-1))
		start := min(max(0, state.Cursor-groupRows+1), max(0, len(result.Groups)-groupRows))
		state.RowStart = start
		lines = append(lines, processHeaderStyle.Render(fixedCell("#", 4, false)+
			fixedCell("SIZE", 12, true)+fixedCell("COPIES", 8, true)+
			fixedCell("RECLAIM", 13, true)+"  HASH"))
		headerOffset := len(lines)
		for index := start; index < start+groupRows; index++ {
			group := result.Groups[index]
			row := fixedCell(fmt.Sprint(index+1), 4, false) +
				fixedCell(bytes(group.Size), 12, true) +
				fixedCell(fmt.Sprint(len(group.Files)), 8, true) +
				fixedCell(bytes(group.Reclaimable), 13, true) + "  " +
				ansi.Truncate(group.Hash, max(8, contentWidth-39), "…")
			if index == state.Cursor {
				row = selectedStorageRow(row, contentWidth, m.colorMode)
			} else {
				row = dimStyle.Render(ansi.Truncate(row, contentWidth, ""))
			}
			lines = append(lines, row)
			state.Rows = append(state.Rows, storageHitRect{
				X: 2,
				Y: lipgloss.Height(m.renderMonitorPageHeader(width)) + 1 +
					headerOffset + (index - start),
				Width: contentWidth, Height: 1,
			})
		}
		group := result.Groups[state.Cursor]
		lines = append(lines, pageRule(contentWidth))
		detailRows := max(1, contentHeight-len(lines))
		for index, file := range group.Files {
			if index >= detailRows {
				remaining := len(group.Files) - index
				lines = append(lines, dimStyle.Render(fmt.Sprintf("… %d more paths", remaining)))
				break
			}
			role := dangerStyle.Render("LINK")
			if index == group.Keep {
				role = processRunningStyle.Render("KEEP")
			}
			lines = append(lines, role+"  "+ansi.Truncate(file.Path, max(8, contentWidth-7), "…"))
		}
	}
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	lines = lines[:contentHeight]
	return btopPanel(width, "DUPLICATE FILES", meta, strings.Join(lines, "\n"),
		diskTitleStyle, colorDiskBorder), nil
}

func selectedStorageRow(row string, width int, mode colorMode) string {
	return selectedProcessStyle(mode).Width(width).
		Render(ansi.Truncate(row, width, ""))
}
