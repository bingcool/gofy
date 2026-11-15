package gofy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
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
	if worker := strings.TrimSpace(opts.Worker); worker != "" {
		// The wildcard means "every worker process on this host", not one file:
		// groups may have been started as separate processes, so resolving only
		// the wildcard run would leave the others running.
		// 通配符表示"本机所有 worker 进程"而不是某一个文件：分组可能是分别启动的，
		// 只解析通配符启动写的那个文件会漏掉其余进程。
		if commandline.IsAllWorkerGroup(worker) {
			return a.stopAllWorkers(ctx, pidfile.Resolve(path, workDir), opts.Force, opts.Timeout)
		}
		// A worker group writes its own pid file, so stopping one must resolve the
		// same suffixed path. Without this, `stop --worker=order` would target the
		// API server that shares the configured pid_file.
		// worker 分组写自己的 pid 文件，停止它必须解析同一个带后缀的路径。否则
		// `stop --worker=order` 会打到共用 pid_file 的 API 服务上。
		return a.stopPIDFile(ctx, pidfile.Resolve(workerPIDPath(path, worker), workDir), worker, opts.Force, opts.Timeout)
	}
	return a.stopPIDFile(ctx, pidfile.Resolve(path, workDir), "", opts.Force, opts.Timeout)
}

// stopAllWorkers stops every worker process that left a pid file next to the
// configured one. Errors are joined: one missing process must not keep the rest
// of the workers running while the operator believes they are all gone.
// stopAllWorkers 停止所有在配置的 pid 文件旁边留下 pid 文件的 worker 进程。错误会被
// 合并返回：一个进程已经不在，不能让其余 worker 继续运行而运维以为都停了。
func (a *App) stopAllWorkers(ctx context.Context, base string, force bool, timeout time.Duration) error {
	files, err := workerPIDFiles(base)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%w: no worker pid file matches %s", ErrNotRunning, workerPIDGlob(base))
	}
	var errs error
	for _, file := range files {
		if err := a.stopPIDFile(ctx, file, workerGroupFromPIDFile(base, file), force, timeout); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// stopPIDFile terminates the process recorded in fullPath and waits for it to
// exit, escalating to SIGKILL when force is set. worker names the target in
// logs and on the console; empty means the HTTP instance.
// stopPIDFile 终止 fullPath 记录的进程并等待它退出，force 为真时升级到 SIGKILL。
// worker 只用于日志与控制台输出中的目标名称，为空表示 HTTP 实例。
func (a *App) stopPIDFile(ctx context.Context, fullPath, worker string, force bool, timeout time.Duration) error {
	pid, err := pidfile.Read(fullPath)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotRunning, fullPath)
	}
	if !process.Alive(pid) {
		_ = os.Remove(fullPath)
		return fmt.Errorf("%w: pid %d", ErrNotRunning, pid)
	}

	logger := a.Logger()
	logger.Info("stopping application", zap.Int("pid", pid), zap.String("pid_file", fullPath), zap.String("worker", worker))
	c := a.con()
	target := fmt.Sprintf("应用【%s】", a.opts.name)
	if worker != "" {
		target = fmt.Sprintf("worker 分组【%s】", worker)
	}
	c.Info("正在停止 %s (pid %d)", target, pid)
	if err := process.Terminate(pid); err != nil {
		return err
	}

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
	if !force {
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
