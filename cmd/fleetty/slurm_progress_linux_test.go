package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlurmJobIDFromProcChecksProcessIdentity(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "123 (worker process) S"+strings.Repeat(" 0", 18)+" 456 0\n")
	write("cgroup", "0::/system.slice/slurmstepd.scope/job_338751/step_batch/user/task_0\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	uid := os.Getuid()
	if got := slurmJobIDFromProc(root, uid, "456"); got != 338751 {
		t.Fatalf("authenticated process cgroup returned %d", got)
	}
	if slurmJobIDFromProc(root, uid+1, "456") != 0 || slurmJobIDFromProc(root, uid, "455") != 0 {
		t.Fatal("wrong UID or recycled PID was accepted")
	}
	write("stat", "123 (worker process) Z"+strings.Repeat(" 0", 18)+" 456 0\n")
	if slurmJobIDFromProc(root, uid, "456") != 0 {
		t.Fatal("zombie process was accepted")
	}
}
