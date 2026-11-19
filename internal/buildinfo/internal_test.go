package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestComplete(t *testing.T) {
	t.Parallel()
	unset := build{version: Dev, commit: Unknown, time: Unknown}
	installed := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	if got := complete(unset, installed, true); got.version != "v0.1.0" || got.commit != Unknown {
		t.Errorf("go install: %+v", got)
	}
	checkout := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.time", Value: "2026-11-18T19:00:00Z"},
		},
	}
	if got := complete(unset, checkout, true); got.version != Dev || got.commit != "abc123" || got.time != "2026-11-18T19:00:00Z" {
		t.Errorf("go build in a checkout: %+v", got)
	}
	if got := complete(unset, nil, false); got != unset {
		t.Errorf("no build info: %+v", got)
	}
	// Values set by make build (-ldflags -X) win.
	set := build{version: "v9.9.9", commit: "fromldflags", time: Unknown}
	if got := complete(set, checkout, true); got.version != "v9.9.9" || got.commit != "fromldflags" {
		t.Errorf("ldflags should win: %+v", got)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	if got := Describe("tool", "v1.2.3", "c0ffee", "today"); !strings.HasPrefix(got, "tool v1.2.3 (commit c0ffee, built today") {
		t.Errorf("got %q", got)
	}
}
