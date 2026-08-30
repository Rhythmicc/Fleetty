package main

import (
	byteutil "bytes"
	"strings"
	"testing"
)

func TestVersionCapabilityRequirement(t *testing.T) {
	var stdout, stderr byteutil.Buffer
	handled, err := runOperations([]string{
		"version",
		"--require-capability", "hub-groups-v1",
		"--require-capability", "process-table-v2",
		"--require-capability", "terminal-footer-v1",
	}, &stdout, &stderr)
	if err != nil || !handled {
		t.Fatalf("version capability preflight = handled %t, error %v, stderr %q", handled, err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Capabilities:") {
		t.Fatalf("version output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	_, err = runOperations([]string{"version", "--require-capability", "future-feature"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "future-feature") {
		t.Fatalf("missing capability error = %v", err)
	}
}
