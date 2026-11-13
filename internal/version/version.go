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
// Read 尽力返回构建信息，不会失败也不会 panic；未注入版本时回退为 dev/none/unknown，
// 因此调用方无需处理错误分支。
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
