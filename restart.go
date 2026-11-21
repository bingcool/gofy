package gofy

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/internal/commandline"
)

// restart stops the instance recorded in the pid file, if any, then runs the
// server in the foreground. It never daemonizes: process supervision belongs
// to systemd or the container runtime.
// restart 先停止 pid 文件记录的实例（如果存在），再在前台运行服务器。它从不
// 自行 daemonize：进程托管交给 systemd 或容器运行时。
func (a *App) restart(ctx context.Context, opts commandline.StopOptions) error {
	c := a.con()
	c.Info("正在重启应用【%s】", a.opts.name)
	if err := a.stop(ctx, opts); err != nil {
		if isRestartIgnorableError(err) {
			c.Note("未发现运行中的实例，直接启动")
		} else {
			return err
		}
	}
	return a.Run(ctx)
}

// isRestartIgnorableError reports stop errors that mean there is nothing to
// stop. Restart treats them as a plain start.
//
// A wildcard stop joins one error per pid file, and errors.Is walks every leaf:
// "this pid file was stale" would mark the whole join ignorable even when
// another worker refused to exit. A join is therefore ignorable only when every
// leaf is; a wrapped single error is judged by what it wraps.
// isRestartIgnorableError 判断停止时的错误是否其实表示"没有实例可停"（未在运行、
// 未启用 pid 文件、平台不支持）；restart 把它们当成普通启动处理。
//
// 通配符停止会为每个 pid 文件合并一个错误，而 errors.Is 会遍历所有叶子：
// "这个 pid 文件已过期"会让整个 join 变成可忽略，即使另一个 worker 根本没退出。
// 因此只有全部叶子都可忽略时 join 才可忽略；单链包装按被包装的错误判断。
func isRestartIgnorableError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isRestartIgnorableError(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapped.Unwrap(); inner != nil {
			return isRestartIgnorableError(inner)
		}
	}
	return errors.Is(err, ErrNotRunning) ||
		errors.Is(err, ErrPIDFileDisabled) ||
		errors.Is(err, ErrUnsupported)
}
