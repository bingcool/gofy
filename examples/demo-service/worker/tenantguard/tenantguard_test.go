package tenantguard

import (
	"testing"

	"github.com/bingcool/gofy/support/tenant"
)

// The switch is the only thing this package owns: off clears the options so
// Ensure short-circuits, on stores a copy so a caller mutating its own value
// cannot change what every worker installs.
// 开关是本包唯一持有的东西："关"清空选项让 Ensure 直接返回，"开"保存副本，
// 调用方后续修改自己的值不会改变所有 worker 安装的内容。
func TestConfigureSwitch(t *testing.T) {
	t.Cleanup(func() { Configure(false, tenant.Options{}) })

	Configure(false, tenant.Options{OverwriteExplicit: true})
	if Enabled() {
		t.Fatal("Configure(false) left the section enabled")
	}

	opts := tenant.Options{OverwriteExplicit: true}
	Configure(true, opts)
	if !Enabled() {
		t.Fatal("Configure(true) did not enable the section")
	}

	// Mutating the caller's copy must not reach the stored value.
	// 修改调用方的副本不能影响已保存的值。
	opts.OverwriteExplicit = false
	stored := options.Load()
	if stored == nil || !stored.OverwriteExplicit {
		t.Fatalf("stored options were not copied: %+v", stored)
	}

	Configure(false, tenant.Options{})
	if Enabled() {
		t.Fatal("Configure(false) did not clear the section")
	}
}

// A disabled section must not require a runtime: worker groups build before the
// runtime exists, and an entry may call Ensure with nothing to install.
// 关闭的段不应要求 Runtime：worker 分组在 Runtime 存在之前构造，入口可能在无池可装时
// 调用 Ensure。
func TestEnsureDisabledIsNoOp(t *testing.T) {
	t.Cleanup(func() { Configure(false, tenant.Options{}) })
	Configure(false, tenant.Options{})
	if err := Ensure(nil); err != nil {
		t.Fatalf("Ensure(nil) with the section off = %v, want nil", err)
	}
}

// An enabled section with no pool is a wiring error, not a silent pass: the
// worker must fail instead of running unfiltered.
// 启用后没有连接池属于接线错误，不能静默通过：worker 必须失败，而不是裸跑。
func TestEnsureEnabledRequiresRuntime(t *testing.T) {
	t.Cleanup(func() { Configure(false, tenant.Options{}) })
	Configure(true, tenant.Options{})
	if err := Ensure(nil); err == nil {
		t.Fatal("Ensure(nil) with the section on = nil, want an error")
	}
}
