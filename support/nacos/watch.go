package nacos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"go.uber.org/zap"
)

// Watcher keeps a local config file in sync with Nacos using long polling.
// Watcher 通过长轮询让本地配置文件与 Nacos 保持一致。
type Watcher struct {
	client *Client
	logger *zap.Logger

	onChange       func(ctx context.Context, result Result) error
	drain          func(ctx context.Context) error
	resume         func(ctx context.Context) error
	strategy       ChangeStrategy
	restartPIDFile string
	gracePeriod    time.Duration

	// execSelf, terminate and pause are replaceable so a test can observe
	// dispatch without re-executing the test binary, signalling the test
	// process, or actually sleeping through a grace period.
	// execSelf、terminate 与 pause 可替换，测试能观察分发而不必真的重执行测试
	// 二进制、给测试进程发信号，或真的等待一个 grace period。
	execSelf  func() error
	terminate func(string) (int, error)
	pause     func(context.Context, time.Duration) bool
}

// WatchOption customizes a Watcher.
// WatchOption 用于定制 Watcher。
type WatchOption func(*Watcher)

// WithLogger sets the logger used by the watcher. The default discards logs.
// WithLogger 设置 watcher 使用的日志器；默认丢弃日志。
func WithLogger(logger *zap.Logger) WatchOption {
	return func(w *Watcher) {
		if logger != nil {
			w.logger = logger
		}
	}
}

// WithOnChange runs fn after a changed config has been written. The new file
// is already in place when fn is called.
// WithOnChange 在变更后的配置写入完成后执行 fn；调用 fn 时新文件已就位。
func WithOnChange(fn func(ctx context.Context, result Result) error) WatchOption {
	return func(w *Watcher) { w.onChange = fn }
}

// WithDrain runs fn before a restart or exit action, after the changed config
// has been written. It is the quiesce hook: the application should stop
// accepting new requests here, so the grace period that follows is spent on
// work already in flight. It is not called for reload, which keeps serving
// throughout. A returned error is logged but does not cancel the action —
// refusing new work is best effort, while the config change must still land.
// WithDrain 在 restart / exit 动作之前、变更配置落盘之后执行 fn。它是摘流钩子：
// 应用应在这里停止接受新请求，随后的 grace period 才留给在途任务。reload 全程
// 继续服务，因此不会调用它。返回的错误只记日志、不取消动作：拒绝新请求是尽力
// 而为，配置变更仍必须生效。
func WithDrain(fn func(ctx context.Context) error) WatchOption {
	return func(w *Watcher) { w.drain = fn }
}

// WithResume runs fn when the restart or exit action failed after the drain
// hook already ran. Its job is to undo the drain: reopen the request gate and
// republish the instance, so the process keeps serving instead of staying alive,
// deregistered and answering 503 until someone notices.
//
// If fn itself fails, the watcher terminates the process rather than leaving a
// zombie: a supervisor can start a fresh process, while a drained process that
// cannot be resumed has no way back into service on its own.
// WithResume 在 drain 钩子已经执行、而 restart / exit 动作失败时调用 fn。它的职责
// 是撤销摘流：重新打开请求闸门并把实例重新注册，让进程继续服务，而不是活着、已注销、
// 一直 503 直到有人发现。
//
// fn 本身失败时，watcher 会终止进程而不是留下僵尸：监管者能拉起新进程，而一个摘流后
// 无法恢复的进程没有自救的办法。
func WithResume(fn func(ctx context.Context) error) WatchOption {
	return func(w *Watcher) { w.resume = fn }
}

// WithRestartOnChange forces the exit strategy and terminates the process
// recorded in pidFile (empty: the current process) after a config change. It
// is the compatibility form of setting nacos.config_change_strategy: exit;
// prefer configuring the strategy so the deployment owns the choice.
//
// The watcher does not spawn a replacement: a supervisor such as systemd,
// Kubernetes or a container restart policy must restart the process, and the
// new process pulls the config at startup. Windows does not support the
// termination signal; the error is reported by the watcher.
// WithRestartOnChange 强制使用 exit 策略，在配置变更后终止 pidFile 记录的进程
// （为空表示当前进程）。它是 nacos.config_change_strategy: exit 的兼容写法；
// 更推荐配置策略，让部署形态自己决定行为。
//
// watcher 不会拉起新进程，需要由 systemd、Kubernetes 或容器重启策略等外部
// 监管者重启，新进程启动时会重新拉取配置。Windows 不支持该终止信号，
// 错误由 watcher 上报。
func WithRestartOnChange(pidFile string) WatchOption {
	return func(w *Watcher) {
		w.strategy = StrategyExit
		w.restartPIDFile = pidFile
	}
}

// NewWatcher validates cfg and returns a watcher.
// NewWatcher 校验 cfg 并返回 watcher。
func NewWatcher(cfg Config, opts ...WatchOption) (*Watcher, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		client: client, logger: zap.NewNop(), strategy: client.cfg.ChangeStrategy,
		gracePeriod: client.cfg.RestartGracePeriod,
		execSelf:    selfRestart, terminate: restartProcess, pause: sleep,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w, nil
}

// Watch runs a watcher until ctx is cancelled.
// Watch 运行 watcher 直到 ctx 被取消。
func Watch(ctx context.Context, cfg Config, opts ...WatchOption) error {
	w, err := NewWatcher(cfg, opts...)
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

// Close releases the idle connections of the client the watcher created. Run
// calls it on return, so the explicit call is only needed when a Watcher was
// built but never started.
// Close 释放 watcher 自建客户端的空闲连接。Run 返回时会调用它，因此只有构建了
// Watcher 却从未启动时才需要显式调用。
func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}
	return w.client.Close()
}

// Run blocks until ctx is cancelled. Listener and apply failures are logged
// and retried after RetryDelay; a bad remote config never replaces the file.
// Run 阻塞直到 ctx 被取消。监听或应用失败会记录日志并在 RetryDelay 后重试；
// 远端配置非法时不会替换本地文件。
func (w *Watcher) Run(ctx context.Context) error {
	// The watcher builds its own client in NewWatcher; close its idle
	// connections on every return path. Exec skips defers, but the process
	// image replaces the transport then anyway.
	// watcher 在 NewWatcher 里自建客户端；每个返回路径都要关闭它的空闲连接。
	// exec 会跳过 defer，但那时进程映像已经连同 transport 一起被替换。
	defer w.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	if !w.client.cfg.Enabled {
		return ErrDisabled
	}
	cfg := w.client.cfg
	w.logger.Info("nacos watcher started",
		zap.String("addr", cfg.baseURL()),
		zap.String("namespace", cfg.Namespace),
		zap.String("data_id", cfg.DataID),
		zap.String("group", cfg.Group),
		zap.String("file", cfg.File),
		zap.String("strategy", string(w.strategy)),
		zap.Duration("grace_period", w.gracePeriod),
	)

	current, err := w.prime(ctx)
	if err != nil && ctx.Err() == nil {
		w.logger.Warn("nacos initial sync failed", zap.Error(err))
	}
	// applied is the digest whose content this process has actually acted on.
	// It trails current while a change keeps failing to apply, and that gap is
	// what makes the retry below possible: Sync only reports Changed when it
	// rewrote the local file, and a failed apply leaves the new content already
	// written, so Changed is false on every retry.
	// applied 是进程真正应用过的内容摘要。变更应用失败时它落后于 current，正是这个差值
	// 让下面的重试成立：Sync 只在重写本地文件时置 Changed，而 apply 失败时新内容已经写
	// 好，重试时 Changed 恒为 false。
	applied := current

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		listenStart := time.Now()
		changed, err := w.client.Listen(ctx, current)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.logger.Warn("nacos listen failed", zap.Error(err))
			if !sleep(ctx, cfg.RetryDelay) {
				return ctx.Err()
			}
			continue
		}
		if !changed {
			// A correct server answers "no change" only after holding the
			// request for the full listen timeout. An answer at less than half
			// of it means the hold was cut short — proxy, error page, server
			// misconfiguration — and retrying at once would spin. Back off for
			// RetryDelay in that case.
			// 正常服务端只有在挂满监听超时之后才会回答"没有变更"。不足一半就回答
			// 说明挂起被中途截断——代理、错误页、服务端配置错误——立刻重试会紧循环。
			// 这种情况按 RetryDelay 退避。
			if time.Since(listenStart) < w.client.listenHold()/2 {
				if !sleep(ctx, cfg.RetryDelay) {
					return ctx.Err()
				}
			}
			continue
		}

		result, err := w.client.Sync(ctx)
		if err != nil {
			w.logger.Error("nacos config update failed", zap.Error(err))
			if !sleep(ctx, cfg.RetryDelay) {
				return ctx.Err()
			}
			continue
		}
		if result.Digest == applied {
			// Nothing to act on: either the content is what this process already
			// applied, or the server reported a change that Sync found identical
			// to the local file. Either way the cursor can move forward.
			// 没有需要执行的内容：要么这份内容本进程已经应用过，要么服务端报的变更在
			// Sync 看来与本地文件一致。无论哪种，游标都可以往前走。
			current = result.Digest
			continue
		}
		w.logger.Info("nacos config updated", zap.String("file", result.File))
		if err := w.apply(ctx, result); err != nil {
			// The change must not be lost. Keeping the cursor where it is makes
			// the next Listen report the change again (the server compares the
			// digest it is given with its own), so the apply is replayed; the
			// backoff keeps a handler that fails for a persistent reason — an
			// unreachable dependency, a bad value it cannot accept — from
			// spinning.
			// 变更不能丢。游标留在原处，下一次 Listen 会再次报出这次变更（服务端比对收到
			// 的摘要与自己的），apply 因此被重放；退避则避免持续失败的 handler（依赖不可达、
			// 某个值无法接受）紧循环。
			w.logger.Error("nacos config change handler failed", zap.Error(err))
			if !sleep(ctx, cfg.RetryDelay) {
				return ctx.Err()
			}
			continue
		}
		current, applied = result.Digest, result.Digest
	}
}

// prime writes the current remote config to disk and returns its digest. If
// the server is unreachable, it falls back to the digest of the local file so
// polling can retry without treating the file as changed.
// prime 先把当前远端配置写入磁盘并返回其 digest。服务端不可达时退回本地文件
// 的 digest，使后续轮询继续重试而不会误判文件已变更。
func (w *Watcher) prime(ctx context.Context) (string, error) {
	result, err := w.client.Sync(ctx)
	if err == nil {
		return result.Digest, nil
	}
	if data, readErr := os.ReadFile(w.client.cfg.File); readErr == nil {
		return digest(data), err
	}
	return "", err
}

// apply dispatches a written change according to the deployment strategy:
// reload hands the result to WithOnChange, restart replaces the process image,
// and exit terminates it for an external supervisor. Only reload calls the
// callback — the other two quiesce, wait out the grace period and start the
// process from main again.
// apply 按部署策略分发已落盘的变更：reload 交给 WithOnChange，restart 替换进程
// 映像，exit 终止进程交由外部监管者。只有 reload 会调回调；另外两种先摘流、
// 等满 grace period，再让进程从 main 重新运行。
func (w *Watcher) apply(ctx context.Context, result Result) error {
	switch w.strategy {
	case StrategyRestart:
		if err := w.prepare(ctx); err != nil {
			return err
		}
		// exec skips defers, so flush buffered logs before the image is lost.
		// exec 会跳过 defer，替换映像前先把缓冲日志刷出去。
		w.logger.Info("nacos config changed, re-executing process", zap.String("file", result.File))
		_ = w.logger.Sync()
		if err := w.execSelf(); err != nil {
			return w.recoverAfterFailure(ctx, "re-exec", err)
		}
		return nil
	case StrategyExit:
		if err := w.prepare(ctx); err != nil {
			return err
		}
		pid, err := w.terminate(w.restartPIDFile)
		if err != nil {
			return w.recoverAfterFailure(ctx, "terminate", err)
		}
		w.logger.Info("nacos config changed, terminating process",
			zap.Int("pid", pid), zap.String("file", result.File))
		return nil
	default:
		var errs []error
		if w.onChange != nil {
			if err := w.onChange(ctx, result); err != nil {
				errs = append(errs, fmt.Errorf("change handler: %w", err))
			}
		}
		return errors.Join(errs...)
	}
}

// recoverAfterFailure undoes the drain when the action that was supposed to
// replace or terminate the process failed. Without it the process stays alive
// with every request answered 503 and its Nacos registration withdrawn — worse
// than a plain failure, because nothing in the process would ever change that
// state again.
//
// When the resume hook is missing or fails, the watcher asks for termination:
// exiting is recoverable by a supervisor, a permanently drained process is not.
// recoverAfterFailure 在"本应替换或终止进程的动作"失败时撤销摘流。没有它，进程会
// 一直活着、所有请求返回 503、Nacos 注册也已撤销 —— 这比直接失败更糟，因为进程内部
// 再没有任何东西会改变这个状态。
//
// resume 钩子缺失或执行失败时，watcher 转而请求终止进程：退出可以由监管者恢复，而
// 永久摘流的进程不能。
func (w *Watcher) recoverAfterFailure(ctx context.Context, action string, cause error) error {
	w.logger.Error("nacos process action failed", zap.String("action", action), zap.Error(cause))
	if w.resume != nil {
		resumeErr := w.resume(ctx)
		if resumeErr == nil {
			w.logger.Warn("nacos service resumed after failed process action", zap.String("action", action))
			return fmt.Errorf("%s: %w", action, cause)
		}
		w.logger.Error("nacos resume failed, terminating instead of staying drained",
			zap.String("action", action), zap.Error(resumeErr))
	}
	pid, termErr := w.terminate(w.restartPIDFile)
	if termErr != nil {
		return fmt.Errorf("%s: %w (resume unavailable and terminate failed: %v)", action, cause, termErr)
	}
	w.logger.Warn("nacos process terminating after failed action",
		zap.Int("pid", pid), zap.String("action", action))
	return fmt.Errorf("%s: %w", action, cause)
}

// prepare quiesces the service and waits before a restart or exit. The drain
// hook stops new HTTP requests (typically App.StopAccepting); the fixed pause
// then gives requests, worker ticks and consumer handlers already in flight a
// chance to finish. Nothing here joins those tasks, so the action still cuts
// off work that outlives the configured seconds.
//
// prepare must not call App.Shutdown: it runs inside the watcher goroutine, and
// a shutdown would stop the very module this goroutine belongs to and wait for
// it — a self-join deadlock. That is why the hook only flips a drain gate.
// prepare 在 restart / exit 之前摘流并等待。摘流钩子停止接受新的 HTTP 请求
// （通常是 App.StopAccepting），随后的固定等待给在途请求、worker tick 与
// consumer handler 一点收尾时间。这里不等待任何具体任务，超过设定秒数仍未
// 跑完的工作照样会被切断。
//
// prepare 不能调用 App.Shutdown：它运行在 watcher goroutine 里，Shutdown 会去
// 停本 goroutine 所属的模块并等它退出，形成自等待死锁。因此钩子只翻摘流闸门。
func (w *Watcher) prepare(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if w.drain != nil {
		if err := w.drain(ctx); err != nil {
			w.logger.Warn("nacos drain hook failed", zap.Error(err))
		}
	}
	if w.gracePeriod <= 0 {
		return nil
	}
	w.logger.Info("nacos config changed, draining before process action",
		zap.Duration("grace_period", w.gracePeriod))
	pause := w.pause
	if pause == nil {
		pause = sleep
	}
	if !pause(ctx, w.gracePeriod) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("nacos: grace period interrupted")
	}
	return nil
}

func restartProcess(pidFile string) (int, error) {
	pid := os.Getpid()
	if pidFile != "" {
		filePID, err := pidfile.Read(pidFile)
		if err != nil {
			return 0, fmt.Errorf("read pid file %s: %w", pidFile, err)
		}
		pid = filePID
	}
	if !process.Alive(pid) {
		return 0, fmt.Errorf("process %d is not running", pid)
	}
	if err := process.Terminate(pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
