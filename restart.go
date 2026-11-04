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
	if err := a.stop(ctx, opts); err != nil && !isRestartIgnorableError(err) {
		return err
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
