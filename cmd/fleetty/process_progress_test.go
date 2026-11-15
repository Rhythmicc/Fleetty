package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestProcessProgressSocket(t *testing.T) {
	// Darwin's sockaddr_un is shorter than some Go test temp paths.
	dir, err := os.MkdirTemp("/tmp", "fleetty-progress-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "p.sock")
	r, err := listenProgress(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if duplicate, err := listenProgress(path, false); err == nil {
		duplicate.Close()
		t.Fatal("second listener took over the socket")
	}
	send := func(line string) string {
		t.Helper()
		c, err := net.DialTimeout("unix", path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		var reply progressReply
		if err := json.NewDecoder(c).Decode(&reply); err != nil {
			t.Fatal(err)
		}
		return reply.Error
	}
	pid, uid := os.Getpid(), os.Geteuid()
	if err := send(`{"v":1,"done":42,"total":100,"label":"factor\u001b\u202e"}`); err != "" {
		t.Fatal(err)
	}
	rows := []processInfo{{PID: pid, UID: uid}, {PID: pid, UID: uid + 1}, {PID: pid, UID: uid, State: "Z"}, {PID: pid + 1, UID: uid}}
	r.attach(rows)
	if p := rows[0].Progress; p == nil || p.Done != 42 || p.Label != "factor" {
		t.Fatalf("progress not attached to sender: %+v", p)
	}
	m := &monitorModel{
		width: 160, height: 44, colorMode: colorModeDark, profile: machineProfileGPU,
		snapshot: monitorSnapshot{Processes: []processInfo{rows[0]}, MemoryTotal: 1, DiskTotal: 1},
	}
	m.snapshot.Processes[0].Command = "progress-worker"
	overview, _ := m.renderOverviewPage(160)
	compute, _ := m.renderComputePage(160)
	for _, page := range []string{overview, compute} {
		plain := ansi.Strip(page)
		if !strings.Contains(plain, "progress-worker · factor") || !strings.Contains(plain, "42%") ||
			!strings.Contains(plain, "━") {
			t.Fatalf("socket report did not render in the process table")
		}
	}
	for _, p := range rows[1:] {
		if p.Progress != nil {
			t.Fatalf("wrong process attributed: %+v", p)
		}
	}
	for _, bad := range []string{
		`{"v":1,"pid":1,"done":1,"total":2}`, `{"v":1,"total":2}`, `{"v":2,"done":1,"total":2}`,
		`{"v":1,"done":3,"total":2}`, `{"v":1,"done":0,"total":0}`,
		`{"v":1,"done":0,"total":1,"clear":true}`, "{} {}", strings.Repeat(" ", 4100),
	} {
		if err := send(bad); err == "" {
			t.Fatalf("invalid report accepted: %.100q", bad)
		}
	}
	r.mu.Lock()
	entry := r.entries[pid]
	entry.start = "reused-pid"
	r.entries[pid] = entry
	r.mu.Unlock()
	if p := r.detail(pid, uid, "R"); p != nil {
		t.Fatalf("stale identity attached: %+v", p)
	}
	if err := send(`{"v":1,"done":0,"total":100}`); err != "" {
		t.Fatal(err)
	}
	r.mu.Lock()
	entry = r.entries[pid]
	entry.updated = time.Now().Add(-processProgressTTL)
	r.entries[pid] = entry
	r.mu.Unlock()
	if p := r.detail(pid, uid, "R"); p != nil {
		t.Fatalf("expired report attached: %+v", p)
	}
	if err := send(`{"v":1,"clear":true}`); err != "" {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) != 0 {
		t.Fatal("clear failed")
	}
}

func TestProcessProgressTableFits(t *testing.T) {
	p := processInfo{PID: 123, Command: "factor-timing", User: "test", State: "R",
		Progress: &processProgress{Done: 42, Total: 100, Label: "数值分解"}}
	for _, width := range []int{45, 62, 100, 160} {
		format := newProcessFormat(width)
		for _, mode := range []colorMode{colorModeDark, colorModeLight} {
			for _, selected := range []bool{false, true} {
				row := format.renderRow(p, 1, selected, mode, 1<<30)
				plain := ansi.Strip(row)
				if lipgloss.Width(row) != lipgloss.Width(format.header()) || !strings.HasSuffix(plain, "42%") {
					t.Fatalf("width %d: progress changed the row geometry: %q", width, plain)
				}
				if strings.Contains(plain, "━") != (format.columns[len(format.columns)-1].width >= 30) {
					t.Fatalf("width %d: unexpected meter: %q", width, plain)
				}
			}
		}
	}
}

func TestProcessProgressFileCLI(t *testing.T) {
	base := []string{"attach-file", "--pid", "123", "--start-ticks", "456", "--path", "/var/tmp/progress.json", "--done-key", "completed"}
	for _, suffix := range [][]string{nil, {"--total", "0"}, {"--total-key", "completed"}, {"--total", "40", "--total-key", "total"}} {
		if _, err := parseProgressFileCommand(append(append([]string(nil), base...), suffix...), io.Discard); err == nil {
			t.Fatalf("accepted invalid counter options %v", suffix)
		}
	}
	if req, err := parseProgressFileCommand(append(base, "--total", "40"), io.Discard); err != nil || req.Total == nil || *req.Total != 40 {
		t.Fatalf("fixed total was not parsed: %+v, %v", req, err)
	}
	if req, err := parseProgressFileCommand(append(base, "--total-key", "total"), io.Discard); err != nil || req.Total != nil || req.TotalKey != "total" {
		t.Fatalf("total key was not parsed: %+v, %v", req, err)
	}
	if _, err := parseProgressFileCommand([]string{"list-files", "--pid", "123"}, io.Discard); err == nil {
		t.Fatal("list-files accepted an unrelated switch")
	}
}
