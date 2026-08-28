// Package version holds the build version, injected via ldflags.
package version

import "runtime/debug"

var (
	Version   = "dev"
	GitCommit string
)

//nolint:gochecknoinits // fallback so `go install ...@latest` still reports a real version
func init() {
	if Version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
}
