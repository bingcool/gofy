package gofy

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/internal/commandline"
)

// restart stops the instance recorded in the pid file, if any, then runs the
// server in the foreground. It never daemonizes: process supervision belongs
// to systemd or the container runtime.
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
func isRestartIgnorableError(err error) bool {
	return errors.Is(err, ErrNotRunning) ||
		errors.Is(err, ErrPIDFileDisabled) ||
		errors.Is(err, ErrUnsupported)
}
