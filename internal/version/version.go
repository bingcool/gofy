// Package version derives build information from debug.ReadBuildInfo.
package version

import (
	"runtime"
	"runtime/debug"

	"github.com/bingcool/gofy/core"
)

// Read returns best-effort build information. It never fails and never
// panics.
func Read() core.VersionInfo {
	info := core.VersionInfo{
		Version:   "dev",
		Commit:    "none",
		BuildTime: "unknown",
		GoVersion: runtime.Version(),
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return info
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		info.Version = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				info.Commit = s.Value
			}
		case "vcs.time":
			if s.Value != "" {
				info.BuildTime = s.Value
			}
		}
	}
	return info
}
