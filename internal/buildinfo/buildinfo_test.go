package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveInjectedBuild(t *testing.T) {
	d := resolve("v0.1.0", "1234567890abcdef", "2026-07-04T10:00:00Z", nil)
	if d.Version != "v0.1.0" || d.Commit != "1234567890abcdef" || d.Modified {
		t.Fatalf("details = %+v", d)
	}
	if !strings.Contains(d.String(), "listnr v0.1.0 commit 1234567890ab") {
		t.Fatalf("String() = %q", d.String())
	}
}

func TestResolveGoVCSFallback(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-20260704100000-abcdef123456+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.time", Value: "2026-07-04T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	d := resolve("", "", "", info)
	if d.Version != "dev-abcdef123456-dirty" || !d.Modified {
		t.Fatalf("details = %+v", d)
	}
}
