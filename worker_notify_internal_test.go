package gofy

import (
	"errors"
	"testing"
)

// TestNilNotifierIsNoOp pins the contract every non-daemon process relies on:
// app.notifier is nil unless the process was started with a startup channel, and
// the child-side calls must not care. It is also the entire Windows
// implementation of the channel — notifierFromEnv returns nil there.
// TestNilNotifierIsNoOp 固化所有非后台进程依赖的契约：除非进程带着启动通道启动，否则
// app.notifier 为 nil，而子进程侧的调用不需要关心这一点。它同时就是启动通道在 Windows 上的
// 全部实现——那里的 notifierFromEnv 返回 nil。
func TestNilNotifierIsNoOp(t *testing.T) {
	var n *startupNotifier

	// None of these may panic or write anywhere.
	// 这些调用都不允许 panic，也不允许往任何地方写。
	n.ready()
	n.failed(errors.New("boom"))
	n.failed(nil)
	n.report("ready")
}
