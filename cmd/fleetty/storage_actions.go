package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type storageActionKind int

const (
	storageActionDelete storageActionKind = iota + 1
	storageActionArchiveDelete
)

type storageActionRequest struct {
	Kind       storageActionKind
	Path       string
	Parent     string
	Root       string
	Name       string
	Size       uint64
	IsDir      bool
	SevenZip   string
	SourceMode os.FileMode
	SourceKey  string
}

type storageCommandSpec struct {
	Directory  string
	Executable string
	Arguments  []string
}

type storageCommandRunner func(context.Context, storageCommandSpec) error

func (request storageActionRequest) label() string {
	if request.Kind == storageActionArchiveDelete {
		return "archive with 7z, then delete"
	}
	return "permanently delete"
}

func (request storageActionRequest) confirmation() string {
	if request.Kind == storageActionArchiveDelete {
		return "ARCHIVE"
	}
	return "DELETE"
}

func (m *monitorModel) prepareStorageAction(kind storageActionKind) {
	if m.storage == nil || len(m.storage.Rects) == 0 {
		m.status = "Select a storage item first."
		return
	}
	m.storage.Cursor = min(max(0, m.storage.Cursor), len(m.storage.Rects)-1)
	entry := m.storage.Rects[m.storage.Cursor].Entry
	request, err := newStorageActionRequest(
		kind, entry, m.storage.Root, m.storage.Path,
	)
	if err != nil {
		m.status = "Storage action unavailable: " + err.Error()
		return
	}
	if m.storage.Scanning {
		m.storage.cacheCurrentResult()
		m.cancelStorageScan()
		m.storage.Generation++
		m.storage.Scanning = false
	}
	m.storageAction = &request
	m.storageConfirm = ""
	m.screen = screenStorageConfirm
	m.status = "Type " + request.confirmation() + " to confirm."
}

func newStorageActionRequest(
	kind storageActionKind,
	entry storageEntry,
	root, parent string,
) (storageActionRequest, error) {
	if kind != storageActionDelete && kind != storageActionArchiveDelete {
		return storageActionRequest{}, errors.New("unknown storage action")
	}
	if entry.Synthetic || strings.TrimSpace(entry.Path) == "" {
		return storageActionRequest{}, errors.New("the selected block is not a real filesystem item")
	}
	info, err := validateStorageActionTarget(entry.Path, root, parent)
	if err != nil {
		return storageActionRequest{}, err
	}
	request := storageActionRequest{
		Kind: kind, Path: filepath.Clean(entry.Path), Parent: filepath.Clean(parent),
		Root: filepath.Clean(root), Name: info.Name(), Size: entry.Size,
		IsDir: info.IsDir(), SourceMode: info.Mode(),
	}
	_, request.SourceKey = storageAllocatedSize(info)
	if kind == storageActionArchiveDelete {
		request.SevenZip, err = findSevenZip()
		if err != nil {
			return storageActionRequest{}, err
		}
	}
	return request, nil
}

func validateStorageActionTarget(path, root, parent string) (os.FileInfo, error) {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	parent = filepath.Clean(parent)
	if path == root {
		return nil, errors.New("the storage scan root cannot be deleted")
	}
	if !storagePathWithinRoot(path, root) {
		return nil, errors.New("the target is outside the storage scan root")
	}
	if filepath.Dir(path) != parent {
		return nil, errors.New("the target is no longer a direct child of the displayed directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links cannot be modified from the storage map")
	}
	if err := ensureStorageTargetHasNoMounts(path); err != nil {
		return nil, err
	}
	return info, nil
}

func ensureStorageTargetHasNoMounts(path string) error {
	policy, err := localStorageMountPolicy()
	if err != nil {
		return fmt.Errorf("cannot safely inspect the mount table: %w", err)
	}
	path = filepath.Clean(path)
	if mountPoint, fileSystem, ok := storageMountWithinTarget(path, policy.mounts); ok {
		return fmt.Errorf("target contains mount point %s (%s)", mountPoint, fileSystem)
	}
	return nil
}

func storageMountWithinTarget(
	path string,
	mounts map[string]string,
) (string, string, bool) {
	path = filepath.Clean(path)
	for mountPoint, fileSystem := range mounts {
		mountPoint = filepath.Clean(mountPoint)
		if mountPoint == path ||
			mountPoint != "/" && storagePathWithinRoot(mountPoint, path) {
			return mountPoint, fileSystem, true
		}
	}
	return "", "", false
}

func findSevenZip() (string, error) {
	for _, name := range []string{"7z", "7zz", "7za"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("7z is not installed; install 7-Zip, 7zz, or p7zip first")
}

func (m *monitorModel) appendStorageConfirmation(text string) {
	if m.storageAction == nil || text == "" {
		return
	}
	for _, character := range strings.ToUpper(text) {
		if character >= 'A' && character <= 'Z' &&
			len([]rune(m.storageConfirm)) < 16 {
			m.storageConfirm += string(character)
		}
	}
}

func (m *monitorModel) cancelStorageAction(status string) {
	m.cancelStorageActionProcess()
	m.screen = screenMonitor
	m.storageAction = nil
	m.storageConfirm = ""
	m.busy = false
	m.status = status
}

func (m *monitorModel) cancelStorageActionProcess() {
	if m.storageActionStop != nil {
		m.storageActionStop()
		m.storageActionStop = nil
	}
}

func (m *monitorModel) runStorageAction(request storageActionRequest) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.storageActionStop = cancel
	return func() tea.Msg {
		archivePath, err := performStorageAction(
			ctx, request, runStorageCommand,
		)
		return storageActionResultMsg{
			request: request, archivePath: archivePath, err: err,
		}
	}
}

func (m *monitorModel) applyStorageActionResult(msg storageActionResultMsg) tea.Cmd {
	m.cancelStorageActionProcess()
	m.busy = false
	m.screen = screenMonitor
	m.storageAction = nil
	m.storageConfirm = ""
	if m.storage == nil {
		return nil
	}
	m.storage.invalidateMutation(msg.request.Path)
	resultStatus := ""
	if msg.err != nil {
		if msg.archivePath != "" {
			resultStatus = fmt.Sprintf(
				"Archive created at %s, but the source was not fully deleted: %v",
				msg.archivePath, msg.err,
			)
		} else {
			resultStatus = "Storage action failed: " + msg.err.Error()
		}
	} else if msg.request.Kind == storageActionArchiveDelete {
		resultStatus = "Created " + msg.archivePath + " and deleted the original."
	} else {
		resultStatus = "Deleted " + msg.request.Path + "."
	}
	refresh := m.refreshStorageScan(msg.request.Parent)
	m.status = resultStatus
	return tea.Sequence(
		forceFullScreenRedraw(),
		refresh,
	)
}

func (s *storageMapState) invalidateMutation(path string) {
	if s == nil || len(s.Cache) == 0 {
		return
	}
	path = filepath.Clean(path)
	for cachedPath := range s.Cache {
		if storagePathWithinRoot(path, cachedPath) ||
			storagePathWithinRoot(cachedPath, path) {
			delete(s.Cache, cachedPath)
		}
	}
	order := s.CacheOrder[:0]
	for _, cachedPath := range s.CacheOrder {
		if _, ok := s.Cache[cachedPath]; ok {
			order = append(order, cachedPath)
		}
	}
	s.CacheOrder = order
}

func performStorageAction(
	ctx context.Context,
	request storageActionRequest,
	run storageCommandRunner,
) (string, error) {
	info, err := validateStorageActionRequest(request)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch request.Kind {
	case storageActionDelete:
		return "", removeStorageTarget(request.Path, info)
	case storageActionArchiveDelete:
		if strings.TrimSpace(request.SevenZip) == "" {
			return "", errors.New("7z executable is unavailable")
		}
		return archiveAndDeleteStorageTarget(ctx, request, run)
	default:
		return "", errors.New("unknown storage action")
	}
}

func validateStorageActionRequest(request storageActionRequest) (os.FileInfo, error) {
	info, err := validateStorageActionTarget(
		request.Path, request.Root, request.Parent,
	)
	if err != nil {
		return nil, err
	}
	if info.Mode() != request.SourceMode || info.IsDir() != request.IsDir {
		return nil, errors.New("target type changed after confirmation")
	}
	if _, key := storageAllocatedSize(info); request.SourceKey != "" && key != request.SourceKey {
		return nil, errors.New("target identity changed after confirmation")
	}
	return info, nil
}

func removeStorageTarget(path string, info os.FileInfo) error {
	parent := filepath.Dir(path)
	quarantine, err := os.MkdirTemp(parent, ".fleetty-delete-")
	if err != nil {
		return fmt.Errorf("create deletion quarantine: %w", err)
	}
	payload := filepath.Join(quarantine, "payload")
	if err := os.Rename(path, payload); err != nil {
		_ = os.Remove(quarantine)
		return fmt.Errorf("move target into deletion quarantine: %w", err)
	}
	movedInfo, err := os.Lstat(payload)
	if err != nil {
		return restoreQuarantinedStorageTarget(payload, path, quarantine,
			fmt.Errorf("inspect quarantined target: %w", err))
	}
	if !sameStorageIdentity(info, movedInfo) {
		return restoreQuarantinedStorageTarget(payload, path, quarantine,
			errors.New("target identity changed while deletion started"))
	}
	if info.IsDir() {
		err = os.RemoveAll(payload)
	} else {
		err = os.Remove(payload)
	}
	if err != nil {
		return restoreQuarantinedStorageTarget(payload, path, quarantine,
			fmt.Errorf("delete quarantined target: %w", err))
	}
	if err := os.Remove(quarantine); err != nil {
		return fmt.Errorf("remove empty deletion quarantine %s: %w", quarantine, err)
	}
	return nil
}

func sameStorageIdentity(first, second os.FileInfo) bool {
	_, firstKey := storageAllocatedSize(first)
	_, secondKey := storageAllocatedSize(second)
	return firstKey != "" && firstKey == secondKey
}

func restoreQuarantinedStorageTarget(
	payload, original, quarantine string,
	cause error,
) error {
	if _, err := os.Lstat(original); errors.Is(err, os.ErrNotExist) {
		if restoreErr := os.Rename(payload, original); restoreErr == nil {
			_ = os.Remove(quarantine)
			return fmt.Errorf("%w; original path restored", cause)
		}
	}
	return fmt.Errorf("%w; recover remaining data from %s", cause, quarantine)
}

func archiveAndDeleteStorageTarget(
	ctx context.Context,
	request storageActionRequest,
	run storageCommandRunner,
) (string, error) {
	tempDirectory, err := os.MkdirTemp(request.Parent, ".fleetty-archive-")
	if err != nil {
		return "", fmt.Errorf("create archive workspace: %w", err)
	}
	inputDirectory := filepath.Join(tempDirectory, "input")
	if err := os.Mkdir(inputDirectory, 0o700); err != nil {
		_ = os.Remove(tempDirectory)
		return "", fmt.Errorf("create archive input workspace: %w", err)
	}
	stagedSource := filepath.Join(inputDirectory, filepath.Base(request.Path))
	if err := os.Rename(request.Path, stagedSource); err != nil {
		_ = os.RemoveAll(tempDirectory)
		return "", fmt.Errorf("isolate source for archiving: %w", err)
	}
	stagedInfo, err := os.Lstat(stagedSource)
	if err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("inspect isolated source: %w", err),
		)
	}
	_, stagedKey := storageAllocatedSize(stagedInfo)
	if request.SourceKey == "" || stagedKey != request.SourceKey {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			errors.New("source identity changed while archiving started"),
		)
	}
	tempArchive := filepath.Join(tempDirectory, "payload.7z")
	sourceArgument := "." + string(filepath.Separator) + filepath.Base(request.Path)
	create := storageCommandSpec{
		Directory: inputDirectory, Executable: request.SevenZip,
		Arguments: []string{
			"a", "-t7z", "-mx=5", "-snl", "-bd", "-y", tempArchive, "--", sourceArgument,
		},
	}
	if err := run(ctx, create); err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("create 7z archive: %w", err),
		)
	}
	archiveInfo, err := os.Stat(tempArchive)
	if err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("inspect created archive: %w", err),
		)
	}
	if !archiveInfo.Mode().IsRegular() || archiveInfo.Size() == 0 {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			errors.New("7z produced an empty or non-regular archive"),
		)
	}
	test := storageCommandSpec{
		Directory: inputDirectory, Executable: request.SevenZip,
		Arguments: []string{"t", "-bd", "-y", tempArchive},
	}
	if err := run(ctx, test); err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("verify 7z archive: %w", err),
		)
	}
	if err := ctx.Err(); err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory, err,
		)
	}
	if err := syncStorageArchive(tempArchive); err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("sync 7z archive: %w", err),
		)
	}
	finalArchive, err := nextStorageArchivePath(request.Path)
	if err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory, err,
		)
	}
	if err := os.Link(tempArchive, finalArchive); err != nil {
		return "", restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("publish archive without overwriting existing data: %w", err),
		)
	}
	if err := os.Remove(tempArchive); err != nil {
		return finalArchive, restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("remove temporary archive link: %w", err),
		)
	}
	if stagedInfo.IsDir() {
		err = os.RemoveAll(stagedSource)
	} else {
		err = os.Remove(stagedSource)
	}
	if err != nil {
		return finalArchive, restoreStagedArchiveSource(
			stagedSource, request.Path, tempDirectory,
			fmt.Errorf("delete archived source: %w", err),
		)
	}
	_ = os.Remove(inputDirectory)
	_ = os.Remove(tempDirectory)
	return finalArchive, nil
}

func restoreStagedArchiveSource(
	staged, original, workspace string,
	cause error,
) error {
	if _, err := os.Lstat(original); errors.Is(err, os.ErrNotExist) {
		if restoreErr := os.Rename(staged, original); restoreErr == nil {
			_ = os.RemoveAll(workspace)
			return fmt.Errorf("%w; original path restored", cause)
		}
	}
	return fmt.Errorf("%w; recover original data from %s", cause, workspace)
}

func nextStorageArchivePath(source string) (string, error) {
	candidate := source + ".7z"
	if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	} else if err != nil {
		return "", fmt.Errorf("inspect archive destination: %w", err)
	}
	for suffix := 1; suffix <= 10_000; suffix++ {
		candidate = fmt.Sprintf("%s-%d.7z", source, suffix)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect archive destination: %w", err)
		}
	}
	return "", errors.New("could not find an unused archive filename")
}

func syncStorageArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func runStorageCommand(ctx context.Context, spec storageCommandSpec) error {
	command := exec.CommandContext(ctx, spec.Executable, spec.Arguments...)
	command.Dir = spec.Directory
	command.Stdout = io.Discard
	stderr := &storageLimitedWriter{Limit: 16 << 10}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(string(stderr.Data))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

type storageLimitedWriter struct {
	Data  []byte
	Limit int
}

func (writer *storageLimitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	remaining := writer.Limit - len(writer.Data)
	if remaining > 0 {
		writer.Data = append(writer.Data, data[:min(remaining, len(data))]...)
	}
	return original, nil
}

func (m *monitorModel) storageConfirmView() string {
	width := usableWidth(m.width)
	if m.storageAction == nil {
		m.cancelStorageAction("Storage action is no longer available.")
		return m.monitorView()
	}
	request := m.storageAction
	kind := "FILE"
	if request.IsDir {
		kind = "DIRECTORY"
	}
	explanation := "The selected item will be permanently removed."
	if request.Kind == storageActionArchiveDelete {
		explanation = "Fleetty will create and verify a new .7z archive beside the source. " +
			"Only then will the original be permanently removed."
	}
	confirmation := request.confirmation()
	input := m.storageConfirm
	if input == "" {
		input = dimStyle.Render(confirmation)
	}
	progress := ""
	if m.busy {
		progress = warningStyle.Render("Working… Do not close Fleetty until this operation finishes.")
	}
	content := strings.Join([]string{
		titleStyle.Render("CONFIRM STORAGE ACTION"),
		dangerStyle.Render(strings.ToUpper(request.label())),
		"",
		panelStyle(width).Render(strings.Join([]string{
			sectionStyle.Render(kind + "  " + request.Name),
			dimStyle.Render("PATH") + "  " + truncate(request.Path, max(12, width-12)),
			dimStyle.Render("SIZE") + "  " + bytes(request.Size),
			"",
			warningStyle.Render(explanation),
			dimStyle.Render("The operation uses the current Unix user's filesystem permissions."),
		}, "\n")),
		"",
		fmt.Sprintf("Type %s to continue: %s",
			accentStyle.Render(confirmation), inputStyle.Render(input+"█")),
		progress,
		"",
		helpStyle.Render("[enter] execute  [esc] cancel") + "  " + dimStyle.Render(m.status),
	}, "\n")
	return centeredPanel(width, content)
}
