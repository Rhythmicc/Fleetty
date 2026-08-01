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
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	request.SourceKey = storageFileIdentity(info)
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
	if key := storageFileIdentity(info); request.SourceKey != "" && key != request.SourceKey {
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
	firstKey := storageFileIdentity(first)
	secondKey := storageFileIdentity(second)
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
	stagedKey := storageFileIdentity(stagedInfo)
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
	if m.storageAction == nil {
		m.cancelStorageAction("Storage action is no longer available.")
		return m.monitorView()
	}
	request := m.storageAction
	screenWidth := usableWidth(m.width)
	dialogWidth := min(86, max(44, screenWidth-8))
	if screenWidth < 52 {
		dialogWidth = screenWidth
	}
	contentWidth := max(12, dialogWidth-4)

	kind := "FILE"
	if request.IsDir {
		kind = "DIRECTORY"
	}

	actionTitle := "DELETE PERMANENTLY"
	actionStyle := dangerStyle
	meta := "CONFIRM"
	if request.Kind == storageActionArchiveDelete {
		actionTitle = "ARCHIVE + DELETE"
		actionStyle = diskTitleStyle
	}
	if m.busy {
		meta = "IN PROGRESS"
	}

	targetNameWidth := max(4, contentWidth-lipgloss.Width(kind)-
		lipgloss.Width(bytes(request.Size))-4)
	target := storageActionAlignedLine(
		dimStyle.Render(kind)+"  "+
			valueStyle.Render(ansi.Truncate(request.Name, targetNameWidth, "…")),
		diskTitleStyle.Render(bytes(request.Size)),
		contentWidth,
	)
	path := dimStyle.Render("PATH  ") +
		valueStyle.Render(ansi.Truncate(
			request.Path, max(6, contentWidth-lipgloss.Width("PATH  ")), "…",
		))

	lines := []string{
		target,
		path,
		dimStyle.Render(strings.Repeat("─", contentWidth)),
	}

	if request.Kind == storageActionArchiveDelete {
		lines = append(lines,
			storageActionStep("1", "CREATE", "new .7z beside the source"),
			storageActionStep("2", "VERIFY", "test archive integrity"),
			storageActionStep("3", "REMOVE", "original only after verification"),
		)
	} else {
		lines = append(lines,
			dangerStyle.Render("IRREVERSIBLE")+
				"  "+warningStyle.Render("No Trash or recovery step."),
			dimStyle.Render("Safety checks run again immediately before deletion."),
		)
	}

	if m.busy {
		working := "REMOVING SELECTED ITEM"
		reassurance := "Keep Fleetty open until this operation finishes."
		if request.Kind == storageActionArchiveDelete {
			working = "CREATING + VERIFYING ARCHIVE"
			reassurance = "The original is removed only after archive verification succeeds."
		}
		lines = append(lines,
			"",
			warningStyle.Copy().Bold(true).Render("● WORKING")+
				"  "+valueStyle.Render(working),
			dimStyle.Render(reassurance),
		)
		if request.Kind == storageActionArchiveDelete {
			lines = append(lines, "",
				compactButton("esc", "request cancellation", false))
		} else {
			lines = append(lines, "",
				dimStyle.Render("Deletion cannot be interrupted after it starts."))
		}
		lines = append(lines,
			dimStyle.Render(ansi.Truncate(m.status, contentWidth, "…")))
	} else {
		confirmation := request.confirmation()
		inputWidth := min(20, max(12, contentWidth/3))
		input := inputStyle.Copy().
			Width(inputWidth).
			MaxWidth(inputWidth).
			Render(m.storageConfirm + "█")

		lines = append(lines,
			"",
			dimStyle.Render("TYPE ")+accentStyle.Render(confirmation)+
				dimStyle.Render(" TO CONFIRM"),
			input,
			"",
			compactButton("enter", "execute", true)+"  "+
				compactButton("esc", "cancel", false),
		)
		if m.status != "" &&
			!strings.HasPrefix(m.status, "Type "+confirmation) {
			lines = append(lines,
				dangerStyle.Render(ansi.Truncate(m.status, contentWidth, "…")))
		}
	}

	dialog := btopPanel(
		dialogWidth, actionTitle, meta, strings.Join(lines, "\n"),
		actionStyle, colorDiskBorder,
	)
	if m.width <= 0 || m.height <= 0 {
		return "\n" + dialog
	}
	return lipgloss.Place(
		screenWidth, max(1, m.height),
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

func storageActionAlignedLine(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return ansi.Truncate(left+"  "+right, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

func storageActionStep(number, action, detail string) string {
	return accentStyle.Render(number) + "  " +
		dimStyle.Copy().Bold(true).Render(fmt.Sprintf("%-7s", action)) +
		valueStyle.Render(detail)
}
