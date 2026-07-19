// Package buildinfo reports the source revision used to build listnr.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// These values are populated by the Makefile's -ldflags. Plain `go build`
// falls back to the VCS metadata embedded by the Go toolchain.
var (
	Version     string
	Commit      string
	CommitTime  string
	current     Details
	currentOnce sync.Once
)

type Details struct {
	Version    string `json:"version"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Modified   bool   `json:"modified"`
	GoVersion  string `json:"go_version"`
}

func Current() Details {
	currentOnce.Do(func() {
		info, _ := debug.ReadBuildInfo()
		current = resolve(Version, Commit, CommitTime, info)
	})
	return current
}

func resolve(version, commit, commitTime string, info *debug.BuildInfo) Details {
	moduleVersion := ""
	d := Details{
		Version:    version,
		Commit:     commit,
		CommitTime: commitTime,
		GoVersion:  runtime.Version(),
	}
	if info != nil {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			moduleVersion = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if d.Commit == "" {
					d.Commit = setting.Value
				}
			case "vcs.time":
				if d.CommitTime == "" {
					d.CommitTime = setting.Value
				}
			case "vcs.modified":
				d.Modified = setting.Value == "true"
			}
		}
	}
	if strings.HasSuffix(d.Version, "-dirty") {
		d.Modified = true
	}
	if d.Version == "" {
		if moduleVersion != "" && !strings.HasPrefix(moduleVersion, "v0.0.0-") {
			d.Version = moduleVersion
		} else if d.Commit == "" {
			d.Version = "dev"
		} else {
			d.Version = "dev-" + shortCommit(d.Commit)
			if d.Modified {
				d.Version += "-dirty"
			}
		}
	}
	return d
}

func (d Details) String() string {
	parts := []string{"listnr " + d.Version}
	if d.Commit != "" {
		parts = append(parts, "commit "+shortCommit(d.Commit))
	}
	if d.CommitTime != "" {
		parts = append(parts, "commit-time "+d.CommitTime)
	}
	if d.Modified && !strings.HasSuffix(d.Version, "-dirty") {
		parts = append(parts, "modified")
	}
	parts = append(parts, d.GoVersion)
	return strings.Join(parts, " ")
}

func UserAgent() string {
	version := strings.TrimPrefix(Current().Version, "v")
	return fmt.Sprintf("listnr/%s (+https://github.com/vrypan/listnr)", version)
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
