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
	// A worker group writes its own pid file, so stopping one must resolve the
	// same suffixed path. Without this, `stop --worker=order` would target the
	// API server that shares the configured pid_file.
	// worker 分组写自己的 pid 文件，停止它必须解析同一个带后缀的路径。否则
	// `stop --worker=order` 会打到共用 pid_file 的 API 服务上。
	if opts.Worker != "" {
		path = workerPIDPath(path, opts.Worker)
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
	logger.Info("stopping application", zap.Int("pid", pid), zap.String("pid_file", fullPath), zap.String("worker", opts.Worker))
	c := a.con()
	target := fmt.Sprintf("应用【%s】", a.opts.name)
	if opts.Worker != "" {
		target = fmt.Sprintf("worker 分组【%s】", opts.Worker)
	}
	c.Info("正在停止 %s (pid %d)", target, pid)
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
		c.Success("%s 已停止 (pid %d)", target, pid)
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if !opts.Force {
		return fmt.Errorf("%w: pid %d", ErrStopTimeout, pid)
	}

	logger.Warn("sending SIGKILL", zap.Int("pid", pid))
	c.Warn("%s 停止超时，发送 SIGKILL (pid %d)", target, pid)
	if err := process.Kill(pid); err != nil {
		return err
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), time.Second)
	defer killCancel()
	if err := process.WaitExit(killCtx, pid, 50*time.Millisecond); err != nil {
		return fmt.Errorf("%w: pid %d", ErrStopTimeout, pid)
	}
	logger.Info("application killed", zap.Int("pid", pid))
	c.Success("%s 已强制停止 (pid %d)", target, pid)
	return nil
}
