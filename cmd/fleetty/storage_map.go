package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type storageMapState struct {
	Root       string
	Path       string
	Scope      string
	Result     storageScanResult
	Err        error
	Scanning   bool
	Generation uint64
	Cancel     context.CancelFunc
	Cursor     int
	Rects      []storageMapRect
}

type storageScanResult struct {
	Path           string
	Size           uint64
	Files          uint64
	Directories    uint64
	Skipped        uint64
	ExcludedMounts uint64
	Entries        []storageEntry
	FinishedAt     time.Time
	Duration       time.Duration
	PolicyWarning  string
}

type storageEntry struct {
	Name        string
	Path        string
	Size        uint64
	Files       uint64
	Directories uint64
	IsDir       bool
	Synthetic   bool
	ItemCount   int
}

type storageMapRect struct {
	Entry  storageEntry
	X      int
	Y      int
	Width  int
	Height int
}

type storageMountPolicy struct {
	excluded map[string]string
	warning  string
}

type storageScanner struct {
	root                string
	policy              storageMountPolicy
	seen                map[string]struct{}
	result              storageScanResult
	started             time.Time
	lastPublished       time.Time
	progressOperations  uint64
	progressSize        uint64
	progressFiles       uint64
	progressDirectories uint64
	progressEntries     []*storageEntry
	publish             func(storageScanResult)
}

type storageScanUpdate struct {
	result storageScanResult
	err    error
	done   bool
}

const storageScanPublishInterval = 125 * time.Millisecond

func newStorageMapState() *storageMapState {
	root := "/"
	scope := "ROOT"
	if os.Geteuid() != 0 {
		account, err := user.Current()
		if err != nil || account == nil || strings.TrimSpace(account.HomeDir) == "" {
			return &storageMapState{
				Scope: "HOME",
				Err:   errors.New("could not resolve the current user's home directory"),
			}
		}
		root = account.HomeDir
		scope = "HOME"
	}
	root = filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(resolved)
	} else {
		return &storageMapState{
			Root: root, Path: root, Scope: scope,
			Err: fmt.Errorf("could not resolve storage scan root: %w", err),
		}
	}
	return &storageMapState{Root: root, Path: root, Scope: scope}
}

func (m *monitorModel) beginStorageScan(path string) tea.Cmd {
	if m.storage == nil {
		return nil
	}
	path = filepath.Clean(path)
	if !storagePathWithinRoot(path, m.storage.Root) {
		m.status = "Storage navigation is restricted to " + m.storage.Root + "."
		return nil
	}
	m.cancelStorageScan()
	m.storage.Generation++
	generation := m.storage.Generation
	ctx, cancel := context.WithCancel(context.Background())
	m.storage.Cancel = cancel
	m.storage.Path = path
	m.storage.Result = storageScanResult{}
	m.storage.Err = nil
	m.storage.Scanning = true
	m.storage.Cursor = 0
	m.storage.Rects = nil
	m.status = "Scanning " + path + " on local filesystems…"
	return func() tea.Msg {
		updates := make(chan storageScanUpdate, 1)
		go func() {
			result, err := scanStorageDirectoryProgress(ctx, path, func(result storageScanResult) {
				publishStorageScanUpdate(updates, storageScanUpdate{result: result})
			})
			publishStorageScanUpdate(updates, storageScanUpdate{
				result: result,
				err:    err,
				done:   true,
			})
			close(updates)
		}()
		return waitForStorageScanUpdate(generation, updates)()
	}
}

func publishStorageScanUpdate(updates chan storageScanUpdate, update storageScanUpdate) {
	select {
	case updates <- update:
		return
	default:
	}
	// The renderer only needs the newest partial snapshot. Replacing a pending
	// update keeps scanning independent from terminal rendering speed.
	select {
	case <-updates:
	default:
	}
	updates <- update
}

func waitForStorageScanUpdate(generation uint64, updates <-chan storageScanUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return storageScanResultMsg{
				generation: generation,
				err:        context.Canceled,
			}
		}
		if update.done {
			return storageScanResultMsg{
				generation: generation,
				result:     update.result,
				err:        update.err,
			}
		}
		return storageScanProgressMsg{
			generation: generation,
			result:     update.result,
			updates:    updates,
		}
	}
}

func (m *monitorModel) cancelStorageScan() {
	if m.storage != nil && m.storage.Cancel != nil {
		m.storage.Cancel()
		m.storage.Cancel = nil
	}
}

func (m *monitorModel) applyStorageScanResult(msg storageScanResultMsg) {
	if m.storage == nil || msg.generation != m.storage.Generation {
		return
	}
	m.storage.Cancel = nil
	m.storage.Scanning = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return
		}
		m.storage.Err = msg.err
		m.status = "Storage scan failed: " + msg.err.Error()
		return
	}
	m.storage.Result = msg.result
	m.storage.Err = nil
	m.storage.Cursor = 0
	m.status = fmt.Sprintf("Scanned %s in %s · %s across %d items.",
		msg.result.Path, compactStorageDuration(msg.result.Duration), bytes(msg.result.Size),
		len(msg.result.Entries))
}

func (m *monitorModel) applyStorageScanProgress(msg storageScanProgressMsg) bool {
	if m.storage == nil || msg.generation != m.storage.Generation || !m.storage.Scanning {
		return false
	}
	m.storage.Result = msg.result
	m.storage.Err = nil
	m.status = fmt.Sprintf("Scanning %s · %s discovered across %d items…",
		msg.result.Path, bytes(msg.result.Size), len(msg.result.Entries))
	return true
}

func (m *monitorModel) storageNavigateRoot() tea.Cmd {
	if m.storage == nil {
		return nil
	}
	if filepath.Clean(m.storage.Path) == filepath.Clean(m.storage.Root) && !m.storage.Scanning {
		m.status = "Already at the storage scan root."
		return nil
	}
	return m.beginStorageScan(m.storage.Root)
}

func (m *monitorModel) storageNavigateParent() tea.Cmd {
	if m.storage == nil {
		return nil
	}
	current := filepath.Clean(m.storage.Path)
	if current == filepath.Clean(m.storage.Root) {
		m.status = "Already at the storage scan root."
		return nil
	}
	parent := filepath.Dir(current)
	if !storagePathWithinRoot(parent, m.storage.Root) {
		parent = m.storage.Root
	}
	return m.beginStorageScan(parent)
}

func (m *monitorModel) storageOpenSelected() tea.Cmd {
	if m.storage == nil || m.storage.Scanning || len(m.storage.Rects) == 0 {
		return nil
	}
	m.storage.Cursor = min(max(0, m.storage.Cursor), len(m.storage.Rects)-1)
	entry := m.storage.Rects[m.storage.Cursor].Entry
	if entry.Synthetic {
		m.status = fmt.Sprintf("%s groups %d smaller items.", entry.Name, entry.ItemCount)
		return nil
	}
	if !entry.IsDir {
		m.status = entry.Path + " · " + bytes(entry.Size)
		return nil
	}
	return m.beginStorageScan(entry.Path)
}

func (m *monitorModel) moveStorageSelection(delta int) {
	if m.storage == nil || len(m.storage.Rects) == 0 {
		return
	}
	m.storage.Cursor = min(max(0, m.storage.Cursor+delta), len(m.storage.Rects)-1)
	entry := m.storage.Rects[m.storage.Cursor].Entry
	m.status = fmt.Sprintf("%s · %s", entry.Path, bytes(entry.Size))
}

func (m *monitorModel) handleStorageClick(x, y int) tea.Cmd {
	if m.storage == nil || m.storage.Scanning {
		return nil
	}
	for index, rect := range m.storage.Rects {
		if x < rect.X || x >= rect.X+rect.Width || y < rect.Y || y >= rect.Y+rect.Height {
			continue
		}
		m.storage.Cursor = index
		return m.storageOpenSelected()
	}
	return nil
}

func storagePathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func scanStorageDirectory(ctx context.Context, path string) (storageScanResult, error) {
	return scanStorageDirectoryProgress(ctx, path, nil)
}

func scanStorageDirectoryProgress(
	ctx context.Context,
	path string,
	publish func(storageScanResult),
) (storageScanResult, error) {
	policy, err := localStorageMountPolicy()
	if err != nil {
		return storageScanResult{}, fmt.Errorf("inspect local mount table: %w", err)
	}
	return scanStorageDirectoryWithPolicyProgress(ctx, path, policy, publish)
}

func scanStorageDirectoryWithPolicy(
	ctx context.Context,
	path string,
	policy storageMountPolicy,
) (storageScanResult, error) {
	return scanStorageDirectoryWithPolicyProgress(ctx, path, policy, nil)
}

func scanStorageDirectoryWithPolicyProgress(
	ctx context.Context,
	path string,
	policy storageMountPolicy,
	publish func(storageScanResult),
) (storageScanResult, error) {
	started := time.Now()
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return storageScanResult{}, err
	}
	if !info.IsDir() {
		return storageScanResult{}, errors.New("storage scan target is not a directory")
	}
	scanner := storageScanner{
		root:   path,
		policy: policy,
		seen:   make(map[string]struct{}),
		result: storageScanResult{
			Path: path, PolicyWarning: policy.warning,
		},
		started: started,
		publish: publish,
	}
	node, err := scanner.scanEntry(ctx, path, info, true, nil)
	if err != nil {
		return storageScanResult{}, err
	}
	scanner.result.Size = node.Size
	scanner.result.Files = node.Files
	scanner.result.Directories = node.Directories
	scanner.result.Entries = node.children
	sort.SliceStable(scanner.result.Entries, func(i, j int) bool {
		if scanner.result.Entries[i].Size == scanner.result.Entries[j].Size {
			return strings.ToLower(scanner.result.Entries[i].Name) <
				strings.ToLower(scanner.result.Entries[j].Name)
		}
		return scanner.result.Entries[i].Size > scanner.result.Entries[j].Size
	})
	scanner.result.FinishedAt = time.Now()
	scanner.result.Duration = time.Since(started)
	return scanner.result, nil
}

type storageScanNode struct {
	Size        uint64
	Files       uint64
	Directories uint64
	children    []storageEntry
}

func (s *storageScanner) scanEntry(
	ctx context.Context,
	path string,
	info os.FileInfo,
	captureChildren bool,
	progressEntry *storageEntry,
) (storageScanNode, error) {
	if err := ctx.Err(); err != nil {
		return storageScanNode{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		s.result.Skipped++
		return storageScanNode{}, nil
	}
	if path != s.root {
		if _, excluded := s.policy.excluded[filepath.Clean(path)]; excluded {
			s.result.ExcludedMounts++
			return storageScanNode{}, nil
		}
	}
	allocated, key := storageAllocatedSize(info)
	if !info.IsDir() {
		node := storageScanNode{Files: 1}
		if key == "" {
			node.Size = allocated
			s.recordStorageProgress(progressEntry, node.Size, node.Files, 0)
			return node, nil
		}
		if _, duplicate := s.seen[key]; duplicate {
			s.recordStorageProgress(progressEntry, 0, node.Files, 0)
			return node, nil
		}
		s.seen[key] = struct{}{}
		node.Size = allocated
		s.recordStorageProgress(progressEntry, node.Size, node.Files, 0)
		return node, nil
	}

	node := storageScanNode{Size: allocated, Directories: 1}
	s.recordStorageProgress(progressEntry, allocated, 0, 1)
	directory, err := os.Open(path)
	if err != nil {
		s.result.Skipped++
		return node, nil
	}
	openedInfo, statErr := directory.Stat()
	if statErr != nil {
		_ = directory.Close()
		s.result.Skipped++
		return node, nil
	}
	_, openedKey := storageAllocatedSize(openedInfo)
	if !openedInfo.IsDir() || key != "" && openedKey != key {
		_ = directory.Close()
		s.result.Skipped++
		return node, nil
	}
	entries, readErr := directory.ReadDir(-1)
	_ = directory.Close()
	if readErr != nil {
		s.result.Skipped++
		return node, nil
	}
	if captureChildren {
		node.children = make([]storageEntry, 0, len(entries))
		s.progressEntries = make([]*storageEntry, 0, len(entries))
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return storageScanNode{}, err
		}
		childPath := filepath.Join(path, entry.Name())
		childInfo, infoErr := entry.Info()
		if infoErr != nil {
			s.result.Skipped++
			s.maybePublishStorageProgress(false)
			continue
		}
		childProgress := progressEntry
		if captureChildren {
			childProgress = &storageEntry{
				Name:  sanitizeTerminalText(entry.Name()),
				Path:  childPath,
				IsDir: childInfo.IsDir(),
			}
			s.progressEntries = append(s.progressEntries, childProgress)
			s.maybePublishStorageProgress(true)
		}
		child, scanErr := s.scanEntry(ctx, childPath, childInfo, false, childProgress)
		if scanErr != nil {
			return storageScanNode{}, scanErr
		}
		node.Size += child.Size
		node.Files += child.Files
		node.Directories += child.Directories
		if captureChildren && (child.Size > 0 || child.Files > 0 || child.Directories > 0) {
			childProgress.Size = child.Size
			childProgress.Files = child.Files
			childProgress.Directories = child.Directories
			node.children = append(node.children, storageEntry{
				Name: sanitizeTerminalText(entry.Name()), Path: childPath,
				Size: child.Size, Files: child.Files, Directories: child.Directories,
				IsDir: childInfo.IsDir(),
			})
		}
		if captureChildren {
			s.maybePublishStorageProgress(true)
		}
	}
	return node, nil
}

func (s *storageScanner) recordStorageProgress(
	entry *storageEntry,
	size, files, directories uint64,
) {
	s.progressSize += size
	s.progressFiles += files
	s.progressDirectories += directories
	if entry != nil {
		entry.Size += size
		entry.Files += files
		entry.Directories += directories
	}
	s.progressOperations++
	s.maybePublishStorageProgress(false)
}

func (s *storageScanner) maybePublishStorageProgress(force bool) {
	if s.publish == nil {
		return
	}
	now := time.Now()
	if !force && s.progressOperations%256 != 0 {
		return
	}
	if !s.lastPublished.IsZero() && now.Sub(s.lastPublished) < storageScanPublishInterval {
		return
	}
	entries := make([]storageEntry, 0, len(s.progressEntries))
	for _, entry := range s.progressEntries {
		if entry == nil || entry.Size == 0 && entry.Files == 0 && entry.Directories == 0 {
			continue
		}
		entries = append(entries, *entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Size == entries[j].Size {
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		}
		return entries[i].Size > entries[j].Size
	})
	result := storageScanResult{
		Path:           s.root,
		Size:           s.progressSize,
		Files:          s.progressFiles,
		Directories:    s.progressDirectories,
		Skipped:        s.result.Skipped,
		ExcludedMounts: s.result.ExcludedMounts,
		Entries:        entries,
		Duration:       now.Sub(s.started),
		PolicyWarning:  s.result.PolicyWarning,
	}
	s.lastPublished = now
	s.publish(result)
}

func (m *monitorModel) renderStoragePage(width int) (string, []widgetPlacement) {
	bodyHeight := max(3, m.height-lipgloss.Height(m.renderMonitorPageHeader(width))-
		lipgloss.Height(m.renderMonitorPageFooter(width)))
	contentHeight := max(1, bodyHeight-2)
	contentWidth := max(1, width-4)
	m.storage.Rects = nil

	if m.storage.Err != nil && !m.storage.Scanning {
		lines := []string{
			dangerStyle.Render("Storage map unavailable"),
			dimStyle.Render(m.storage.Err.Error()),
		}
		for len(lines) < contentHeight {
			lines = append(lines, "")
		}
		return btopPanel(width, "STORAGE MAP", "ERROR", strings.Join(lines, "\n"),
			diskTitleStyle, colorDiskBorder), nil
	}
	if m.storage.Scanning && len(m.storage.Result.Entries) == 0 ||
		!m.storage.Scanning && m.storage.Result.FinishedAt.IsZero() {
		lines := m.renderStorageScanning(contentWidth, contentHeight)
		return btopPanel(width, "STORAGE MAP", m.storage.Scope+" · LOCAL FILESYSTEMS",
			strings.Join(lines, "\n"), diskTitleStyle, colorDiskBorder), nil
	}

	result := m.storage.Result
	mapHeight := max(1, contentHeight-2)
	entries := storageTreemapEntries(result.Entries, contentWidth, mapHeight)
	rects := layoutStorageTreemap(entries, 0, 0, contentWidth, mapHeight)
	if len(rects) > 0 {
		m.storage.Cursor = min(max(0, m.storage.Cursor), len(rects)-1)
	}
	cursor := m.storage.Cursor
	if m.storage.Scanning {
		cursor = -1
	}
	mapLines := renderStorageTreemap(rects, contentWidth, mapHeight, cursor, m.colorMode)

	breadcrumb := storageBreadcrumb(m.storage.Root, result.Path, contentWidth)
	summaryPrefix := ""
	if m.storage.Scanning {
		frames := []string{"◐", "◓", "◑", "◒"}
		frame := frames[(time.Now().UnixMilli()/250)%int64(len(frames))]
		breadcrumb = frame + " SCANNING  ·  " + breadcrumb
		summaryPrefix = "DISCOVERED  "
	}
	summary := fmt.Sprintf("%s%s  ·  %d FILES  ·  %d DIRS  ·  %d SKIPPED  ·  %d REMOTE MOUNTS EXCLUDED",
		summaryPrefix,
		bytes(result.Size), result.Files, max(0, int(result.Directories)-1),
		result.Skipped, result.ExcludedMounts)
	lines := []string{
		accentStyle.Render(ansi.Truncate(breadcrumb, contentWidth, "…")),
		dimStyle.Render(ansi.Truncate(summary, contentWidth, "…")),
	}
	lines = append(lines, mapLines...)
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	lines = lines[:contentHeight]

	panelY := 2
	mapY := panelY + 1 + 2
	for index := range rects {
		rects[index].X += 2
		rects[index].Y += mapY
	}
	m.storage.Rects = rects
	phase := ""
	if m.storage.Scanning {
		phase = "SCANNING · "
	}
	meta := fmt.Sprintf("%s · %s%s · %s", m.storage.Scope, phase, bytes(result.Size),
		compactStorageDuration(result.Duration))
	return btopPanel(width, "STORAGE MAP", meta, strings.Join(lines, "\n"),
		diskTitleStyle, colorDiskBorder), nil
}

func (m *monitorModel) renderStorageScanning(width, height int) []string {
	path := m.storage.Path
	if path == "" {
		path = m.storage.Root
	}
	frames := []string{"◐", "◓", "◑", "◒"}
	frame := frames[(time.Now().UnixMilli()/250)%int64(len(frames))]
	lines := []string{
		accentStyle.Render(frame+" SCANNING") + "  " + valueStyle.Render(ansi.Truncate(path, max(8, width-16), "…")),
		dimStyle.Render("Following local filesystems only · symlinks and remote mounts are excluded"),
		"",
	}
	barWidth := max(8, width-4)
	offset := int((time.Now().UnixMilli() / 100) % int64(barWidth))
	bar := make([]rune, barWidth)
	for index := range bar {
		bar[index] = '·'
	}
	for index := 0; index < min(10, barWidth); index++ {
		bar[(offset+index)%barWidth] = '━'
	}
	lines = append(lines, diskTitleStyle.Render(string(bar)))
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func storageBreadcrumb(root, path string, width int) string {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	label := "⌂ " + filepath.Base(root)
	if root == "/" {
		label = "⌂ /"
	}
	relative, err := filepath.Rel(root, path)
	if err == nil && relative != "." {
		label += "  /  " + strings.Join(strings.Split(relative, string(filepath.Separator)), "  /  ")
	}
	return ansi.Truncate(label, width, "…")
}

func storageTreemapEntries(entries []storageEntry, width, height int) []storageEntry {
	if len(entries) == 0 {
		return nil
	}
	limit := min(64, max(6, width*height/18))
	if len(entries) <= limit {
		return append([]storageEntry(nil), entries...)
	}
	visible := append([]storageEntry(nil), entries[:limit-1]...)
	other := storageEntry{
		Name: "Other", Synthetic: true, ItemCount: len(entries) - len(visible),
	}
	for _, entry := range entries[len(visible):] {
		other.Size += entry.Size
		other.Files += entry.Files
		other.Directories += entry.Directories
	}
	visible = append(visible, other)
	return visible
}

func layoutStorageTreemap(entries []storageEntry, x, y, width, height int) []storageMapRect {
	if len(entries) == 0 || width <= 0 || height <= 0 {
		return nil
	}
	type weightedEntry struct {
		entry  storageEntry
		weight uint64
	}
	weighted := make([]weightedEntry, 0, len(entries))
	for _, entry := range entries {
		weight := entry.Size
		if weight == 0 {
			weight = 1
		}
		weighted = append(weighted, weightedEntry{entry: entry, weight: weight})
	}
	var rectangles []storageMapRect
	var place func([]weightedEntry, int, int, int, int)
	place = func(items []weightedEntry, left, top, w, h int) {
		if len(items) == 0 || w <= 0 || h <= 0 {
			return
		}
		if len(items) == 1 || w == 1 && h == 1 {
			rectangles = append(rectangles, storageMapRect{
				Entry: items[0].entry, X: left, Y: top, Width: w, Height: h,
			})
			return
		}
		var total uint64
		for _, item := range items {
			total += item.weight
		}
		half := total / 2
		var firstWeight uint64
		split := 1
		for split < len(items) {
			next := firstWeight + items[split-1].weight
			if split > 1 && next > half {
				break
			}
			firstWeight = next
			split++
		}
		split--
		if split < 1 {
			split = 1
			firstWeight = items[0].weight
		}
		if split >= len(items) {
			split = len(items) - 1
			firstWeight = total - items[len(items)-1].weight
		}
		ratio := float64(firstWeight) / float64(total)
		if w >= h*2 {
			firstWidth := min(w-1, max(1, int(float64(w)*ratio+0.5)))
			place(items[:split], left, top, firstWidth, h)
			place(items[split:], left+firstWidth, top, w-firstWidth, h)
		} else {
			firstHeight := min(h-1, max(1, int(float64(h)*ratio+0.5)))
			place(items[:split], left, top, w, firstHeight)
			place(items[split:], left, top+firstHeight, w, h-firstHeight)
		}
	}
	place(weighted, x, y, width, height)
	return rectangles
}

func renderStorageTreemap(
	rects []storageMapRect,
	width, height, cursor int,
	mode colorMode,
) []string {
	if len(rects) == 0 {
		lines := []string{dimStyle.Render("This directory is empty or contains no allocated data.")}
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines
	}
	owners := make([][]int, height)
	for row := range owners {
		owners[row] = make([]int, width)
		for column := range owners[row] {
			owners[row][column] = -1
		}
	}
	for index, rect := range rects {
		fillWidth, fillHeight := rect.Width, rect.Height
		if fillWidth >= 5 {
			fillWidth--
		}
		if fillHeight >= 3 {
			fillHeight--
		}
		for row := rect.Y; row < rect.Y+fillHeight && row < height; row++ {
			for column := rect.X; column < rect.X+fillWidth && column < width; column++ {
				if row >= 0 && column >= 0 {
					owners[row][column] = index
				}
			}
		}
	}
	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		for column := 0; column < width; {
			owner := owners[row][column]
			end := column + 1
			for end < width && owners[row][end] == owner {
				end++
			}
			segmentWidth := end - column
			if owner < 0 {
				line.WriteString(strings.Repeat(" ", segmentWidth))
			} else {
				rect := rects[owner]
				label := storageRectLabel(rect, row, segmentWidth)
				line.WriteString(storageRectStyle(owner, owner == cursor, mode).
					Render(fixedCell(label, segmentWidth, false)))
			}
			column = end
		}
		lines[row] = line.String()
	}
	return lines
}

func storageRectLabel(rect storageMapRect, row, width int) string {
	if width < 3 {
		return ""
	}
	relative := row - rect.Y
	switch relative {
	case 0:
		prefix := ""
		if rect.Entry.IsDir {
			prefix = "▸ "
		} else if rect.Entry.Synthetic {
			prefix = "◇ "
		}
		return ansi.Truncate(prefix+rect.Entry.Name, width, "…")
	case 1:
		if rect.Height >= 2 {
			return ansi.Truncate(bytes(rect.Entry.Size), width, "…")
		}
	case 2:
		if rect.Height >= 4 && rect.Entry.IsDir {
			return ansi.Truncate(fmt.Sprintf("%d files", rect.Entry.Files), width, "…")
		}
	}
	return ""
}

func storageRectStyle(index int, selected bool, mode colorMode) lipgloss.Style {
	if selected {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#10131A")).
			Background(lipgloss.Color("#B9A4FF")).
			Bold(true)
	}
	backgrounds := []color.Color{
		colorNetworkBorder, colorMemoryBorder, colorDiskBorder, colorCPUBorder,
		colorGPUBorder, colorProcessBorder, lipgloss.Color("#5A3E52"), lipgloss.Color("#3E5A55"),
	}
	foreground := lipgloss.Color("#F5F7FF")
	if mode == colorModeLight {
		foreground = lipgloss.Color("#20242D")
	}
	return lipgloss.NewStyle().Foreground(foreground).
		Background(backgrounds[index%len(backgrounds)]).Bold(true)
}

func compactStorageDuration(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%dms", max(1, int(duration.Milliseconds())))
	}
	if duration < time.Minute {
		return duration.Round(100 * time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}
