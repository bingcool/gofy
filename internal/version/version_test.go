package version

import (
	"runtime"
	"testing"
)

// TestReadUsesCallerVersion pins the contract the root package relies on: the
// version comes from the caller (the framework's Version constant), never from
// a module version that a local replace would turn into v0.0.0, while the
// remaining fields still come from the build.
// TestReadUsesCallerVersion 固定根包依赖的契约：版本来自调用方（框架的 Version 常量），
// 绝不会是会被本地 replace 变成 v0.0.0 的模块版本；其余字段仍取自构建信息。
func TestReadUsesCallerVersion(t *testing.T) {
	info := Read("9.8.7")
	if info.Version != "9.8.7" {
		t.Fatalf("Read(9.8.7).Version = %q", info.Version)
	}
	if info.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	if info.Commit == "" || info.BuildTime == "" {
		t.Fatalf("commit/build time lost: %+v", info)
	}
}
