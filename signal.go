package gofy

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext returns a context cancelled by SIGINT or SIGTERM.
// The application itself never registers signal handlers implicitly.
// SignalContext 返回一个在收到 SIGINT 或 SIGTERM 时被取消的 context。
// App 自身不会隐式注册信号处理，需要优雅退出时由调用方自行使用它。
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
