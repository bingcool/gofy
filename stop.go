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
	record, err := pidfile.ReadRecord(fullPath)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotRunning, fullPath)
	}
	pid := record.PID
	if !process.Alive(pid) {
		_ = os.Remove(fullPath)
		return fmt.Errorf("%w: pid %d", ErrNotRunning, pid)
	}
	// A pid file outlives a crash, and the kernel recycles pid numbers. Signalling
	// on liveness alone can hit an unrelated process, so the identity check comes
	// before the signal: a mismatch means this file describes a process that is
	// gone, and the stale file is removed instead of acted upon.
	// pid 文件会比崩溃的进程活得更久，而内核会回收 pid。只看存活就发信号可能打到无关进程，
	// 因此身份校验放在信号之前：不匹配说明这份文件描述的进程已经消失，此时删掉陈旧文件而不是
	// 照着它动手。
	verdict := record.Verify()
	if verdict == pidfile.IdentityMismatch {
		_ = os.Remove(fullPath)
		return fmt.Errorf("%w: pid %d (%s) now belongs to another process", ErrNotRunning, pid, fullPath)
	}

	logger := a.Logger()
	if verdict == pidfile.IdentityUnknown {
		// A recycled pid must not be signalled on liveness alone: the check
		// cannot prove this process is the one that wrote the file. Let the
		// operator opt in explicitly instead of warning and carrying on.
		// pid 可能被复用，不能只凭存活就发信号：校验无法证明眼前进程就是写文件那个。
		// 让运维用 --force 显式承担风险，而不是警告完照常执行。
		if !force {
			return fmt.Errorf("%w: pid %d (%s); pass --force to stop on liveness only",
				ErrIdentityUnverified, pid, fullPath)
		}
		logger.Warn("pid file has no process identity; stopping on liveness only because --force was given",
			zap.Int("pid", pid),
			zap.String("pid_file", fullPath),
		)
	}
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
		removePIDFile(logger, fullPath, pid)
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
	removePIDFile(logger, fullPath, pid)
	logger.Info("application killed", zap.Int("pid", pid))
	c.Success("%s 已强制停止 (pid %d)", target, pid)
	return nil
}

// removePIDFile deletes the pid file once its process is confirmed gone. The
// graceful path usually gets there first through the stopped process's own
// cleanup; this call covers SIGKILL and OOM kills, which never run it and would
// otherwise leave a file that the next start has to reject as a live stale pid.
// A failed removal only warns: the process is already gone, and reporting it as
// a stop failure would invite a retry that can only answer "not running".
// removePIDFile 在确认 pid 对应的进程消失后删除 pid 文件。正常停止通常已由被停进程自己
// 清理；这里覆盖 SIGKILL 与 OOM 这类根本来不及清理的场景，否则下一次启动就得面对一个
// "仍然活着"的陈旧 pid。删除失败只告警：进程已经不在，把它报成停止失败只会招来一次必然
// 返回"未运行"的重试。
func removePIDFile(logger *zap.Logger, fullPath string, pid int) {
	if err := pidfile.RemoveIfOwner(fullPath, pid); err != nil {
		logger.Warn("remove pid file failed", zap.String("path", fullPath), zap.Error(err))
	}
}
