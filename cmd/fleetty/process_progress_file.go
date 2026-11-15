package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	processProgressFileLimit    = 1 << 20
	processProgressFileMax      = 256
	processProgressFileInterval = 5 * time.Second
	processProgressFileStale    = 2 * time.Minute
)

type progressFileRequest struct {
	Version  int     `json:"v"`
	Action   string  `json:"action"`
	PID      int     `json:"pid,omitempty"`
	Start    uint64  `json:"start_ticks,omitempty"`
	Path     string  `json:"path,omitempty"`
	DoneKey  string  `json:"done_key,omitempty"`
	TotalKey string  `json:"total_key,omitempty"`
	Total    *uint64 `json:"total,omitempty"`
	Label    string  `json:"label,omitempty"`
}

type progressFileEntry struct {
	request   progressFileRequest
	uid       int
	start     string
	last      *processProgress
	lastError string
}

type progressFileStatus struct {
	PID      int              `json:"pid"`
	Start    uint64           `json:"start_ticks"`
	UID      int              `json:"uid"`
	Path     string           `json:"path"`
	Progress *processProgress `json:"progress,omitempty"`
}

func (req progressFileRequest) validate() error {
	if req.Version != 1 {
		return errors.New("invalid progress file command version")
	}
	hasSource := req.Path != "" || req.DoneKey != "" || req.TotalKey != "" || req.Total != nil || req.Label != ""
	switch req.Action {
	case "attach-file", "detach-file":
		if req.PID <= 0 || req.Start == 0 {
			return errors.New("positive --pid and --start-ticks are required")
		}
	case "list-files":
		if req.PID == 0 && req.Start == 0 && !hasSource {
			return nil
		}
		return errors.New("list-files takes no options")
	default:
		return errors.New("unknown progress file command")
	}
	if req.Action == "detach-file" {
		if hasSource {
			return errors.New("detach-file accepts only --pid and --start-ticks")
		}
		return nil
	}
	if !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path || len(req.Path) > 3072 || strings.ContainsRune(req.Path, 0) {
		return errors.New("--path must be a clean absolute path of at most 3072 bytes")
	}
	if req.DoneKey == "" || req.DoneKey == req.TotalKey || len(req.DoneKey) > 256 || len(req.TotalKey) > 256 {
		return errors.New("provide distinct, nonempty counter keys of at most 256 bytes")
	}
	if (req.TotalKey == "") == (req.Total == nil) || (req.Total != nil && *req.Total == 0) {
		return errors.New("provide either --total-key or a positive --total")
	}
	if len(req.Label) > 256 {
		return errors.New("--label must be at most 256 bytes")
	}
	return nil
}

func (r *progressReceiver) manageProgressFiles(peerUID int, line []byte) progressReply {
	if peerUID != 0 || os.Geteuid() != 0 || runtime.GOOS != "linux" {
		return progressReply{Error: "progress file commands require a root Linux receiver and root peer"}
	}
	var req progressFileRequest
	if err := decodeProgressRequest(line, &req); err != nil {
		return progressReply{Error: "invalid progress file command"}
	}
	if err := req.validate(); err != nil {
		return progressReply{Error: err.Error()}
	}
	switch req.Action {
	case "attach-file":
		if err := r.attachProgressFile(req); err != nil {
			return progressReply{Error: err.Error()}
		}
	case "detach-file":
		r.mu.Lock()
		if entry := r.files[req.PID]; entry != nil && entry.request.Start == req.Start {
			delete(r.files, req.PID)
		}
		r.mu.Unlock()
	case "list-files":
		files := r.listProgressFiles(time.Now())
		return progressReply{Files: &files}
	}
	return progressReply{}
}

func (r *progressReceiver) attachProgressFile(req progressFileRequest) error {
	start, uid, err := processProgressFileIdentity(req.PID)
	if err != nil || start != strconv.FormatUint(req.Start, 10) {
		return errors.New("target process identity changed or process exited")
	}
	req.Label = cleanProgressLabel(req.Label)
	entry := &progressFileEntry{request: req, uid: uid, start: start}
	value, err := readProgressFile(entry)
	if !entry.matchesProcess() {
		return errors.New("target process identity changed or process exited")
	}
	if err != nil {
		return err
	}
	entry.last = value
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.files[req.PID] == nil && len(r.files) >= processProgressFileMax {
		return errors.New("progress file registrations are full")
	}
	r.files[req.PID] = entry
	return nil
}

func (entry *progressFileEntry) matchesProcess() bool {
	start, uid, err := processProgressFileIdentity(entry.request.PID)
	return err == nil && start == entry.start && uid == entry.uid
}

func (entry *progressFileEntry) reading(now time.Time) *processProgress {
	if entry == nil || entry.last == nil {
		return nil
	}
	value := *entry.last
	value.Error = entry.lastError
	value.Stale = value.Error != "" || now.Sub(*value.ObservedAt) >= processProgressFileStale
	return &value
}

func (r *progressReceiver) listProgressFiles(now time.Time) []progressFileStatus {
	r.mu.Lock()
	files := make([]progressFileStatus, 0, len(r.files))
	for _, entry := range r.files {
		files = append(files, progressFileStatus{
			PID: entry.request.PID, Start: entry.request.Start, UID: entry.uid,
			Path: entry.request.Path, Progress: entry.reading(now),
		})
	}
	r.mu.Unlock()
	sort.Slice(files, func(i, j int) bool { return files[i].PID < files[j].PID })
	return files
}

func (r *progressReceiver) sampleProgressFiles() {
	ticker := time.NewTicker(processProgressFileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.refreshProgressFiles()
		}
	}
}

func (r *progressReceiver) refreshProgressFiles() {
	r.mu.Lock()
	files := make([]*progressFileEntry, 0, len(r.files))
	for _, entry := range r.files {
		files = append(files, entry)
	}
	r.mu.Unlock()
	for _, entry := range files {
		var value *processProgress
		var err error
		alive := entry.matchesProcess()
		if alive {
			value, err = readProgressFile(entry)
			alive = entry.matchesProcess()
		}
		r.mu.Lock()
		if r.files[entry.request.PID] == entry {
			switch {
			case !alive:
				delete(r.files, entry.request.PID)
			case err != nil:
				entry.lastError = err.Error()
			default:
				entry.last, entry.lastError = value, ""
			}
		}
		r.mu.Unlock()
	}
}

func readProgressFile(entry *progressFileEntry) (*processProgress, error) {
	req := entry.request
	file, err := openProcessProgressFile(req.PID, req.Path, entry.uid)
	if err != nil {
		return nil, fmt.Errorf("open progress file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat progress file: %w", err)
	}
	if !info.Mode().IsRegular() || !progressOwnedBy(info, entry.uid) {
		return nil, errors.New("progress file must be regular and owned by the target UID")
	}
	if info.Size() > processProgressFileLimit {
		return nil, errors.New("progress file exceeds 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, processProgressFileLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read progress file: %w", err)
	}
	if len(data) > processProgressFileLimit {
		return nil, errors.New("progress file exceeds 1 MiB")
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat progress file: %w", err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || !progressOwnedBy(after, entry.uid) {
		return nil, errors.New("progress file changed while being read")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, errors.New("progress file must contain one JSON object")
	}
	done, err := progressFileCounter(fields, req.DoneKey)
	if err != nil {
		return nil, err
	}
	var total uint64
	if req.Total != nil {
		total = *req.Total
	} else {
		total, err = progressFileCounter(fields, req.TotalKey)
		if err != nil {
			return nil, err
		}
	}
	if total == 0 || done > total {
		return nil, errors.New("progress counts require 0 <= done <= total and total > 0")
	}
	observed := info.ModTime()
	return &processProgress{Done: done, Total: total, Label: req.Label, Source: "progress-file", ObservedAt: &observed}, nil
}

func progressFileCounter(fields map[string]json.RawMessage, key string) (uint64, error) {
	value, err := strconv.ParseUint(string(fields[key]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("progress counter %q must be an unsigned integer", key)
	}
	return value, nil
}
