package gofy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"go.uber.org/zap"
)

func (a *App) stop(ctx context.Context, opts commandline.StopOptions) error {
	if runtime.GOOS == "windows" {
		return ErrUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}

	path := opts.PIDFile
	if path == "" {
		path = a.opts.pidFile
	}
	if path == "" {
		return ErrPIDFileDisabled
	}
	workDir, err := a.resolveWorkDir()
	if err != nil {
		return err
	}
	fullPath := pidfile.Resolve(path, workDir)

	pid, err := pidfile.Read(fullPath)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotRunning, fullPath)
	}
	if !process.Alive(pid) {
		_ = os.Remove(fullPath)
		return fmt.Errorf("%w: pid %d", ErrNotRunning, pid)
	}

	logger := a.Logger()
	logger.Info("stopping application", zap.Int("pid", pid), zap.String("pid_file", fullPath))
	c := a.con()
	c.Info("正在停止应用【%s】 (pid %d)", a.opts.name, pid)
	if err := process.Terminate(pid); err != nil {
		return err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = a.opts.shutdownTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = process.WaitExit(waitCtx, pid, 100*time.Millisecond)
	if err == nil {
		logger.Info("application stopped", zap.Int("pid", pid))
		c.Success("应用【%s】已停止 (pid %d)", a.opts.name, pid)
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if !opts.Force {
		return fmt.Errorf("%w: pid %d", ErrStopTimeout, pid)
	}

	logger.Warn("sending SIGKILL", zap.Int("pid", pid))
	c.Warn("应用【%s】停止超时，发送 SIGKILL (pid %d)", a.opts.name, pid)
	if err := process.Kill(pid); err != nil {
		return err
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), time.Second)
	defer killCancel()
	if err := process.WaitExit(killCtx, pid, 50*time.Millisecond); err != nil {
		return fmt.Errorf("%w: pid %d", ErrStopTimeout, pid)
	}
	logger.Info("application killed", zap.Int("pid", pid))
	c.Success("应用【%s】已强制停止 (pid %d)", a.opts.name, pid)
	return nil
}
