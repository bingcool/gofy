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
// isRestartIgnorableError 判断停止时的错误是否其实表示"没有实例可停"（未在运行、
// 未启用 pid 文件、平台不支持）；restart 把它们当成普通启动处理。
func isRestartIgnorableError(err error) bool {
	return errors.Is(err, ErrNotRunning) ||
		errors.Is(err, ErrPIDFileDisabled) ||
		errors.Is(err, ErrUnsupported)
}
