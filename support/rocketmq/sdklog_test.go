package rocketmq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDKLogRootIsWritable pins the reason this file exists: the SDK must not
// fall back to /logs/rocketmqlogs, which is unwritable on macOS.
// TestSDKLogRootIsWritable 固定这个文件存在的原因：SDK 不能再退回 macOS 上不可写的
// /logs/rocketmqlogs。
func TestSDKLogRootIsWritable(t *testing.T) {
	// The public constructors trigger the repair lazily; no init() is allowed
	// in a public package, so a test that only imports the package has not
	// configured anything yet.
	// 修复由公开构造函数懒触发；公开包不允许 init()，因此仅 import 的测试此时
	// 还没有完成配置。
	ensureSDKLogRoot()
	root := os.Getenv(sdkLogRootEnv)
	if root == "" {
		t.Fatalf("%s is unset; the SDK would use its /logs default", sdkLogRootEnv)
	}
	if root == "/logs/rocketmqlogs" || strings.HasPrefix(root, "/logs/") {
		t.Fatalf("%s = %q, points at the read-only root volume", sdkLogRootEnv, root)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("%s = %q is not absolute", sdkLogRootEnv, root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("SDK log root %q is not writable: %v", root, err)
	}
}

// TestConfigureSDKLogRootRespectsOverride keeps an explicit deployment setting
// intact: operators who route the SDK log elsewhere must not be overridden.
// TestConfigureSDKLogRootRespectsOverride 保证部署侧显式配置不被覆盖：运维把 SDK 日志
// 指向别处时，应当以它为准。
func TestConfigureSDKLogRootRespectsOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-rocketmq-logs")
	t.Setenv(sdkLogRootEnv, want)

	if got := configureSDKLogRoot(); got != want {
		t.Fatalf("configureSDKLogRoot() = %q, want %q", got, want)
	}
	if got := os.Getenv(sdkLogRootEnv); got != want {
		t.Fatalf("%s = %q, want %q", sdkLogRootEnv, got, want)
	}
}
