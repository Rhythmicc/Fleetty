package buildinfo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentDeclaresStableCapabilities(t *testing.T) {
	info := Current()
	for _, required := range []string{"hub-groups-v1", "process-table-v2", "terminal-footer-v1"} {
		found := false
		for _, capability := range info.Capabilities {
			if capability == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("capabilities %v do not contain %q", info.Capabilities, required)
		}
	}
}

func TestMissingCapabilities(t *testing.T) {
	missing := MissingCapabilities([]string{"hub-groups-v1", "future-feature", "future-feature", ""})
	if len(missing) != 1 || missing[0] != "future-feature" {
		t.Fatalf("missing capabilities = %v", missing)
	}
}

func TestWriteJSONIncludesDeploymentContract(t *testing.T) {
	var output bytes.Buffer
	if err := Write(&output, true); err != nil {
		t.Fatal(err)
	}
	var info Info
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Capabilities) == 0 || strings.TrimSpace(info.Version) == "" {
		t.Fatalf("incomplete build info: %#v", info)
	}
}
