//go:build windows

package gofy

import "testing"

// TestNotifierFromEnvIgnoresWindows pins that Windows never joins a startup
// channel, even when the variable is present. A stray GOFY_DAEMON_NOTIFY can
// arrive from a copied environment or a hand-written script; trusting it would
// write one line of text into whatever handle happens to carry that number.
// TestNotifierFromEnvIgnoresWindows 固化 Windows 永不加入启动通道，即使环境变量存在。
// 复制来的环境或手写脚本都可能带来一个 GOFY_DAEMON_NOTIFY；盲信它会往编号恰好相同的
// 任意句柄里写一行文本。
func TestNotifierFromEnvIgnoresWindows(t *testing.T) {
	// The literal is process.NotifyEnv's value on Unix. It has no Windows
	// counterpart — that is the point of the test — so it is spelled out here.
	// 这个字面量就是 Unix 上 process.NotifyEnv 的值。它在 Windows 没有对应常量（正是本测试
	// 的意义），因此在这里直接写出。
	t.Setenv("GOFY_DAEMON_NOTIFY", "3")
	if n := notifierFromEnv(); n != nil {
		t.Fatalf("notifierFromEnv = %v, want nil on windows", n)
	}
}
