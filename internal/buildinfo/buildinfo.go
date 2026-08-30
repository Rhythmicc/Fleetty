package buildinfo

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Info struct {
	Version      string   `json:"version"`
	Commit       string   `json:"commit"`
	BuildDate    string   `json:"build_date"`
	Modified     bool     `json:"modified"`
	Capabilities []string `json:"capabilities"`
	GoVersion    string   `json:"go_version"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
}

var capabilities = []string{
	"hub-groups-v1",
	"process-table-v2",
	"terminal-footer-v1",
}

func Current() Info {
	info := Info{
		Version: Version, Commit: Commit, BuildDate: BuildDate,
		Capabilities: append([]string(nil), capabilities...),
		GoVersion:    runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "unknown" && setting.Value != "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.BuildDate == "unknown" && setting.Value != "" {
					info.BuildDate = setting.Value
				}
			case "vcs.modified":
				info.Modified, _ = strconv.ParseBool(setting.Value)
			}
		}
	}
	sort.Strings(info.Capabilities)
	return info
}

func MissingCapabilities(required []string) []string {
	available := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		available[capability] = struct{}{}
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(required))
	for _, capability := range required {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		if _, exists := available[capability]; !exists {
			missing = append(missing, capability)
		}
	}
	sort.Strings(missing)
	return missing
}

func Write(writer io.Writer, asJSON bool) error {
	info := Current()
	if asJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(info)
	}
	commit := info.Commit
	if info.Modified {
		commit += "+dirty"
	}
	_, err := fmt.Fprintf(writer,
		"Fleetty %s (%s, %s, %s/%s, %s)\nCapabilities: %s\n",
		info.Version, commit, info.BuildDate, info.OS, info.Arch, info.GoVersion,
		strings.Join(info.Capabilities, ", "),
	)
	return err
}
