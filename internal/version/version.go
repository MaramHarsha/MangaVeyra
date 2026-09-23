// Package version exposes the build identity of the MangaVeyra binary.
//
// The values are overridden at link time, for example:
//
//	go build -ldflags "-X github.com/MaramHarsha/MangaVeyra/internal/version.Version=1.2.0 \
//	  -X github.com/MaramHarsha/MangaVeyra/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/MaramHarsha/MangaVeyra/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the semantic version of this build (without a leading "v").
var Version = "1.0.0"

// Commit is the source control revision this binary was built from.
var Commit = ""

// BuildDate is the UTC build timestamp in RFC 3339 format.
var BuildDate = ""

// Info is a snapshot of the build identity, suitable for JSON responses.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	GoVersion string `json:"go_version"`
}

// Get returns the build identity. When Commit was not set through ldflags it
// falls back to the VCS revision recorded by the Go toolchain, if any.
func Get() Info {
	commit, date := Commit, BuildDate
	if commit == "" || date == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if commit == "" && len(s.Value) >= 7 {
						commit = s.Value[:7]
					}
				case "vcs.time":
					if date == "" {
						date = s.Value
					}
				}
			}
		}
	}
	return Info{Version: Version, Commit: commit, BuildDate: date, GoVersion: runtime.Version()}
}

// String renders the build identity on one line, e.g. "MangaVeyra 1.0.0 (abc1234, 2026-09-23T10:00:00Z)".
func (i Info) String() string {
	s := "MangaVeyra " + i.Version
	switch {
	case i.Commit != "" && i.BuildDate != "":
		s += fmt.Sprintf(" (%s, %s)", i.Commit, i.BuildDate)
	case i.Commit != "":
		s += fmt.Sprintf(" (%s)", i.Commit)
	}
	return s + " " + i.GoVersion
}
