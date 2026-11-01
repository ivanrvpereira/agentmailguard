// Package version reports the release version of amg and amgd.
package version

import "runtime/debug"

// version is set by release builds: -ldflags "-X github.com/ivanrvpereira/agentmailguard/internal/version.version=v1.2.3".
var version string

// String returns the version set at build time, else the module version that the Go toolchain recorded from the
// Git tag or commit, else "dev".
func String() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
