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

// Run blocks until ctx is cancelled. Listener and apply failures are logged
// and retried after RetryDelay; a bad remote config never replaces the file.
// Run 阻塞直到 ctx 被取消。监听或应用失败会记录日志并在 RetryDelay 后重试；
// 远端配置非法时不会替换本地文件。
func (w *Watcher) Run(ctx context.Context) error {
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

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		current = result.Digest
		if !result.Changed {
			continue
		}
		w.logger.Info("nacos config updated", zap.String("file", result.File))
		if err := w.apply(ctx, result); err != nil {
			w.logger.Error("nacos config change handler failed", zap.Error(err))
		}
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
		return w.execSelf()
	case StrategyExit:
		if err := w.prepare(ctx); err != nil {
			return err
		}
		pid, err := w.terminate(w.restartPIDFile)
		if err != nil {
			return fmt.Errorf("exit: %w", err)
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
