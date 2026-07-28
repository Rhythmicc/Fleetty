package buildinfo

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	return Info{
		Version: Version, Commit: Commit, BuildDate: BuildDate,
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
}

func Write(writer io.Writer, asJSON bool) error {
	info := Current()
	if asJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(info)
	}
	_, err := fmt.Fprintf(
		writer, "Fleetty %s (%s, %s, %s/%s, %s)\n",
		info.Version, info.Commit, info.BuildDate, info.OS, info.Arch, info.GoVersion,
	)
	return err
}
