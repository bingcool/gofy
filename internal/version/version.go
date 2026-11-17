// Package version derives build information from debug.ReadBuildInfo.
// 包 version 从 debug.ReadBuildInfo 推导构建信息，供 `version` 子命令展示。
package version

import (
	"runtime"
	"runtime/debug"

	"github.com/bingcool/gofy/core"
)

// Read returns best-effort build information. It never fails and never
// panics.
//
// The version is the caller's: the framework version is the `Version` constant
// in the root package, not something this package owns or derives. Build info
// cannot answer it reliably — a module built from a local `replace` reports
// v0.0.0 or (devel) — so it is only used for the commit and build time.
// Read 尽力返回构建信息，不会失败也不会 panic。
//
// 版本由调用方给出：框架版本是根包里的 `Version` 常量，不由本包拥有或推导。构建信息无法
// 可靠回答版本——本地 replace 构建的模块报 v0.0.0 或 (devel)——因此它只用来取 commit
// 与构建时间。
func Read(version string) core.VersionInfo {
	info := core.VersionInfo{
		Version:   version,
		Commit:    "none",
		BuildTime: "unknown",
		GoVersion: runtime.Version(),
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return info
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
