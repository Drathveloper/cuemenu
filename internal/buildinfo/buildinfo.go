// Package buildinfo describes the build of a program: the version, commit
// and date that `make build` sets with -ldflags -X, completed with what Go
// embeds in the binary when they were not set.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Values of the build information when nothing sets them.
const (
	Dev     = "dev"
	Unknown = "unknown"
)

type build struct {
	version, commit, time string
}

// Describe returns "NAME VERSION (commit COMMIT, built TIME)".
func Describe(name, version, commit, built string) string {
	info, ok := debug.ReadBuildInfo()
	out := complete(build{version: version, commit: commit, time: built}, info, ok)
	return fmt.Sprintf("%s %s (commit %s, built %s)", name, out.version, out.commit, out.time)
}

// complete fills what -ldflags did not set with the data Go embeds:
// `go install module@version` records the module version, and `go build`
// in a git checkout records the commit and its time.
func complete(out build, info *debug.BuildInfo, ok bool) build {
	if !ok {
		return out
	}
	if out.version == Dev && info.Main.Version != "" && info.Main.Version != "(devel)" {
		out.version = info.Main.Version
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && out.commit == Unknown {
			out.commit = setting.Value
		}
		if setting.Key == "vcs.time" && out.time == Unknown {
			out.time = setting.Value
		}
	}
	return out
}
