// Package version reports the build version.
package version

import (
	"runtime/debug"
	"strings"
)

// version is set at build time with
//
//	-ldflags "-X github.com/jongwow/meerchat/internal/version.version=v1.2.3"
var version string

// Version returns the release version, falling back to module build info.
func Version() string {
	if version != "" {
		return version
	}
	// `go install ...@v1.2.3` records the tag; local builds record a
	// pseudo-version (v0.0.0-<date>-<commit>) that is not worth showing.
	if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Version, "v") && !strings.HasPrefix(bi.Main.Version, "v0.0.0-") {
		return bi.Main.Version
	}
	return "dev"
}
