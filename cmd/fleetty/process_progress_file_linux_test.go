package main

import (
	stdbytes "bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessProgressFileLinux(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the end-to-end file receiver requires a root Linux test peer")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "p.sock")
	receiver, err := listenProgress(socket, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(receiver.Close)

	// The child is a live process that does not send a progress socket report.
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	start, err := readProcessStartTicks(pid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "progress.json")
	mtime := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	write := func(name, content string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(path, `{"logical_completed":42,"logical_total":100,"details":{"ignored":true}}`, mtime)
	req := progressFileRequest{Version: 1, Action: "attach-file", PID: pid, Start: start, Path: path, DoneKey: "logical_completed", TotalKey: "logical_total", Label: "factor"}
	wrong := req
	wrong.Start++
	if err := sendProgressFileCommand(socket, wrong, io.Discard); err == nil {
		t.Fatal("accepted wrong start ticks")
	}
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if reply := receiver.manageProgressFiles(65534, line); reply.Error == "" {
		t.Fatal("accepted file management from unprivileged peer")
	}
	if err := sendProgressFileCommand(socket, req, io.Discard); err != nil {
		t.Fatal(err)
	}

	rows := []processInfo{{PID: pid, UID: 0, State: "S"}, {PID: pid, UID: 65534}, {PID: pid, UID: 0, State: "Z"}}
	receiver.attach(rows)
	if p := rows[0].Progress; p == nil || p.Done != 42 || p.Total != 100 || p.Label != "factor" || p.Source != "progress-file" || !p.Stale || !p.ObservedAt.Equal(mtime) || p.Error != "" {
		t.Fatalf("file sample missing or mislabeled: %+v", p)
	}
	for _, row := range rows[1:] {
		if row.Progress != nil {
			t.Fatalf("wrong row received file progress: %+v", row)
		}
	}

	second := time.Now().Truncate(time.Second)
	replacement := filepath.Join(dir, "new.json")
	write(replacement, `{"logical_completed":70,"logical_total":100}`, second)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	receiver.refreshProgressFiles()
	if p := receiver.detail(pid, 0, "S"); p == nil || p.Done != 70 || p.Stale || !p.ObservedAt.Equal(second) {
		t.Fatalf("atomic replacement did not update sample: %+v", p)
	}
	for _, content := range []string{`{"logical_completed":`, `{"logical_completed":null,"logical_total":100}`, `{"logical_completed":101,"logical_total":100}`, strings.Repeat(" ", processProgressFileLimit+1)} {
		write(path, content, time.Now())
		receiver.refreshProgressFiles()
		var out stdbytes.Buffer
		if err := sendProgressFileCommand(socket, progressFileRequest{Version: 1, Action: "list-files"}, &out); err != nil {
			t.Fatal(err)
		}
		var files []progressFileStatus
		if err := json.Unmarshal(out.Bytes(), &files); err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || files[0].PID != pid || files[0].Start != start || files[0].Path != path || files[0].Progress == nil {
			t.Fatalf("missing registration or sample: %s", out.String())
		}
		if p := files[0].Progress; p.Done != 70 || !p.Stale || !p.ObservedAt.Equal(second) || p.Error == "" {
			t.Fatalf("invalid sample replaced last valid counters or timestamp: %+v", p)
		}
	}

	// A valid direct heartbeat wins when both sources cover the process.
	receiver.mu.Lock()
	receiver.entries[pid] = progressEntry{value: processProgress{Done: 81, Total: 100, Source: "socket"}, uid: 0, start: strconv.FormatUint(start, 10), updated: time.Now()}
	receiver.mu.Unlock()
	if p := receiver.detail(pid, 0, "S"); p == nil || p.Source != "socket" || p.Done != 81 {
		t.Fatalf("fresh socket report did not take priority: %+v", p)
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	receiver.refreshProgressFiles()
	if p := receiver.detail(pid, 0, "S"); p != nil || len(receiver.listProgressFiles(time.Now())) != 0 {
		t.Fatalf("exited process still has progress: %+v", p)
	}
}

func TestProcessProgressFileRejectsUnsafeFilesLinux(t *testing.T) {
	pid, uid := os.Getpid(), os.Geteuid()
	dir := t.TempDir()
	file := filepath.Join(dir, "real.json")
	if err := os.WriteFile(file, []byte(`{"done":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(dir, "parent-link")
	fileLink := filepath.Join(dir, "file-link")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, fileLink); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		path string
		uid  int
	}{{dir, uid}, {fileLink, uid}, {filepath.Join(parentLink, "real.json"), uid}, {file, uid + 1}} {
		if f, err := openProcessProgressFile(pid, spec.path, spec.uid); err == nil {
			f.Close()
			t.Fatalf("accepted unsafe file %q with expected UID %d", spec.path, spec.uid)
		}
	}
	total := uint64(4)
	p, err := readProgressFile(&progressFileEntry{uid: uid, request: progressFileRequest{PID: pid, Path: file, DoneKey: "done", Total: &total}})
	if err != nil || p.Done != 3 || p.Total != 4 {
		t.Fatalf("fixed total: %+v, %v", p, err)
	}
}
