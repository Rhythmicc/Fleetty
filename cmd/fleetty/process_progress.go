package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

const (
	processProgressTTL   = 30 * time.Second
	processProgressLimit = 4096
)

type processProgress struct {
	Done       uint64     `json:"done"`
	Total      uint64     `json:"total"`
	Label      string     `json:"label,omitempty"`
	Source     string     `json:"source,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	Stale      bool       `json:"stale,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type progressRequest struct {
	Version int     `json:"v"`
	Done    *uint64 `json:"done,omitempty"`
	Total   uint64  `json:"total,omitempty"`
	Label   string  `json:"label,omitempty"`
	Clear   bool    `json:"clear,omitempty"`
}

type progressReply struct {
	Error string                `json:"error,omitempty"`
	Files *[]progressFileStatus `json:"files,omitempty"`
}

type progressEntry struct {
	value   processProgress
	start   string
	uid     int
	updated time.Time
}

type progressReceiver struct {
	listener *net.UnixListener
	lock     *os.File
	mu       sync.Mutex
	entries  map[int]progressEntry
	files    map[int]*progressFileEntry
	stop     chan struct{}
}

func progressSocketPath(uid int) string {
	if uid == 0 && runtime.GOOS == "linux" {
		return "/run/fleetty-progress-v1.sock"
	}
	parent := "/tmp"
	if runtime.GOOS == "linux" {
		// User-level service and clients share the host's /dev/shm even
		// when the system service has a private /tmp.
		parent = "/dev/shm"
	}
	return filepath.Join(parent, fmt.Sprintf("fleetty-progress-v1-%d", uid), "progress.sock")
}

func listenProgress(path string, shared bool) (*progressReceiver, error) {
	parent := filepath.Dir(path)
	if !shared {
		if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 || !progressOwnedBy(info, os.Geteuid()) {
			return nil, errors.New("progress socket directory must be owned by this user with mode 0700")
		}
	}
	// A stable lock serializes cleanup of stale socket names and prevents a
	// second dashboard from replacing a live listener.
	fd, err := unix.Open(path+".lock", unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	locked := false
	defer func() {
		if !locked {
			lock.Close()
		}
	}()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || !progressOwnedBy(info, os.Geteuid()) {
		return nil, errors.New("invalid progress socket lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !progressOwnedBy(info, os.Geteuid()) {
			return nil, errors.New("refusing to replace an unexpected progress socket path")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0600)
	if shared {
		mode = 0666
	}
	if err := os.Chmod(path, mode); err != nil {
		listener.Close()
		return nil, err
	}
	r := &progressReceiver{
		listener: listener, lock: lock, entries: make(map[int]progressEntry),
		files: make(map[int]*progressFileEntry), stop: make(chan struct{}),
	}
	locked = true
	go r.serve()
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		go r.sampleProgressFiles()
	}
	return r, nil
}

func progressOwnedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

func (r *progressReceiver) Close() {
	if r != nil {
		close(r.stop)
		r.listener.Close()
		r.lock.Close()
	}
}

func (r *progressReceiver) serve() {
	workers := make(chan struct{}, 16)
	for {
		conn, err := r.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case workers <- struct{}{}:
			go func() {
				defer func() { <-workers }()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				_ = json.NewEncoder(conn).Encode(r.receive(conn))
			}()
		default:
			conn.Close()
		}
	}
}

func (r *progressReceiver) receive(conn *net.UnixConn) progressReply {
	pid, uid, err := progressPeer(conn)
	if err != nil || pid <= 0 {
		return progressReply{Error: "progress sender credentials unavailable"}
	}
	line, err := bufio.NewReaderSize(conn, processProgressLimit).ReadSlice('\n')
	if err != nil {
		return progressReply{Error: "expected one newline-terminated progress message within 4096 bytes"}
	}
	var command struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(line, &command) == nil && command.Action != "" {
		return r.manageProgressFiles(uid, line)
	}
	if err := r.receiveDirectProgress(pid, uid, line); err != nil {
		return progressReply{Error: err.Error()}
	}
	return progressReply{}
}

func decodeProgressRequest(line []byte, request any) error {
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}

func (r *progressReceiver) receiveDirectProgress(pid, uid int, line []byte) error {
	start, owner, err := processProgressIdentity(pid)
	if err != nil || owner != uid {
		return errors.New("progress sender is no longer available")
	}
	var req progressRequest
	if err := decodeProgressRequest(line, &req); err != nil || req.Version != 1 {
		return errors.New("invalid progress v1 message")
	}
	if !req.Clear && (req.Done == nil || req.Total == 0 || *req.Done > req.Total || len(req.Label) > 256) {
		return errors.New("progress requires 0 <= done <= total, total > 0 and a label of at most 256 bytes")
	}
	if req.Clear && (req.Done != nil || req.Total != 0 || req.Label != "") {
		return errors.New("clear cannot be combined with a progress reading")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if req.Clear {
		delete(r.entries, pid)
		return nil
	}
	now := time.Now()
	if len(r.entries) >= 8192 {
		r.prune(now)
		if _, exists := r.entries[pid]; !exists && len(r.entries) >= 8192 {
			return errors.New("progress receiver is full")
		}
	}
	r.entries[pid] = progressEntry{
		value: processProgress{Done: *req.Done, Total: req.Total, Label: cleanProgressLabel(req.Label), Source: "socket", ObservedAt: &now},
		start: start, uid: uid, updated: now,
	}
	return nil
}

func cleanProgressLabel(label string) string {
	return truncate(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, label)), 48)
}

func (r *progressReceiver) prune(now time.Time) {
	for pid, entry := range r.entries {
		if now.Sub(entry.updated) >= processProgressTTL {
			delete(r.entries, pid)
		}
	}
}

func (r *progressReceiver) attach(processes []processInfo) {
	if r == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	r.prune(now)
	haveProgress := len(r.entries) != 0 || len(r.files) != 0
	r.mu.Unlock()
	if !haveProgress {
		return
	}
	for i := range processes {
		p := &processes[i]
		p.Progress = r.lookup(p.PID, p.UID, p.State, now)
	}
}

func (r *progressReceiver) lookup(pid, uid int, state string, now time.Time) *processProgress {
	if strings.HasPrefix(state, "Z") {
		return nil
	}
	r.mu.Lock()
	entry, direct := r.entries[pid]
	file := r.files[pid]
	reading := file.reading(now)
	r.mu.Unlock()
	direct = direct && uid == entry.uid && now.Sub(entry.updated) < processProgressTTL
	haveFile := reading != nil && uid == file.uid
	if direct {
		start, owner, err := processProgressIdentity(pid)
		if err == nil && owner == uid && start == entry.start {
			return &entry.value
		}
	}
	if haveFile && file.matchesProcess() {
		return reading
	}
	return nil
}

func (r *progressReceiver) detail(pid, uid int, state string) *processProgress {
	if r == nil {
		return nil
	}
	return r.lookup(pid, uid, state, time.Now())
}
