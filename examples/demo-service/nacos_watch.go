package main

import (
	"context"
	"errors"
	"sync"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/nacos"
)

// nacosWatchModule keeps config.yaml in sync with Nacos and dispatches each
// change by the configured strategy. With reload it hands the change to the
// reloader; with restart or exit the framework re-executes or terminates the
// process, so no reload executor is started here.
// nacosWatchModule 把 Nacos 内容同步进 config.yaml，并按配置策略分发每次变更：
// reload 交给 reloader 原地重建；restart / exit 由框架重新执行或终止进程，
// 此时不启动重载执行器。
type nacosWatchModule struct {
	cfg      nacos.Config
	logger   *zap.Logger
	onChange func(context.Context) error
	// onDrain quiesces the app before a restart or exit. It must not shut the
	// app down: it runs inside the watcher goroutine, and Shutdown would wait
	// for that same watcher to exit.
	// onDrain 在 restart / exit 之前给应用摘流。它不能关闭应用：该钩子运行在
	// watcher goroutine 里，Shutdown 会反过来等这个 watcher 退出。
	onDrain func(context.Context) error

	mu        sync.Mutex
	cancel    context.CancelFunc
	watchDone chan struct{}
}

func (m *nacosWatchModule) Name() string { return "nacos-watch" }

func (m *nacosWatchModule) Register(*support.Router) error { return nil }

// Start runs the watcher and, only for the reload strategy, a separate reload
// executor. Reload retires this module from inside its own change handler, so
// one goroutine could never wait for itself; splitting them lets Stop wait for
// the watcher while the reload that triggered Stop finishes on the executor.
// Restart and exit need no executor: the framework ends the process itself.
// Start 启动监听；只有 reload 策略才另外启动重载执行器。Reload 会在自己的变更
// 回调里退役本模块，单个 goroutine 无法等待自己；拆开后 Stop 能等待监听退出，
// 而触发 Stop 的那次 Reload 在另一个 goroutine 上继续完成。restart / exit
// 由框架直接结束进程，不需要执行器。
func (m *nacosWatchModule) Start(ctx context.Context) error {
	watchCtx, cancel := context.WithCancel(ctx)
	watchDone := make(chan struct{})

	m.mu.Lock()
	m.cancel = cancel
	m.watchDone = watchDone
	m.mu.Unlock()

	opts := []nacos.WatchOption{nacos.WithLogger(m.logger)}
	if m.onDrain != nil {
		// Only restart / exit call this hook; reload keeps serving requests.
		// 只有 restart / exit 会调它；reload 全程继续对外服务。
		opts = append(opts, nacos.WithDrain(m.onDrain))
	}
	var changes chan struct{}
	// Only reload needs a callback and an executor. The other two strategies
	// end this process, so a reload running in parallel would be wasted work.
	// 只有 reload 需要回调与执行器；另外两种策略会结束当前进程，并行跑一次
	// Reload 只是白做。
	if m.cfg.ChangeStrategy == "" || m.cfg.ChangeStrategy == nacos.StrategyReload {
		changes = make(chan struct{}, 1)
		opts = append(opts, nacos.WithOnChange(func(context.Context, nacos.Result) error {
			// Coalesce notifications: the file on disk is the state, so a
			// change arriving during a reload only needs one more pass.
			// 合并通知：磁盘文件就是状态，Reload 期间到来的变更只需再跑一次。
			select {
			case changes <- struct{}{}:
			default:
			}
			return nil
		}))
	}

	go func() {
		defer close(watchDone)
		err := nacos.Watch(watchCtx, m.cfg, opts...)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("nacos watcher stopped", zap.Error(err))
		}
	}()

	if changes != nil {
		go func() {
			for {
				select {
				case <-watchCtx.Done():
					return
				case <-changes:
					if watchCtx.Err() != nil {
						return
					}
					if m.onChange == nil {
						continue
					}
					if err := m.onChange(watchCtx); err != nil && !errors.Is(err, context.Canceled) {
						m.logger.Error("nacos config reload failed", zap.Error(err))
					}
				}
			}
		}()
	}
	return nil
}

// Stop cancels the watcher and waits for it to exit, so this generation never
// reads the config file again after its handles are released. The reload
// executor is not joined: during a Reload it is the goroutine calling Stop.
// Stop 取消监听并等它退出，本代句柄释放后不会再读配置文件。重载执行器不作
// 等待：Reload 期间它就是调用 Stop 的那个 goroutine。
func (m *nacosWatchModule) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	cancel, watchDone := m.cancel, m.watchDone
	m.cancel, m.watchDone = nil, nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if watchDone != nil {
		select {
		case <-watchDone:
		case <-ctx.Done():
		}
	}
	return nil
}
