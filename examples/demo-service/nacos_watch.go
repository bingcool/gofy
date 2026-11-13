package main

import (
	"context"
	"errors"
	"sync"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/nacos"
)

// nacosWatchModule keeps config.yaml in sync with Nacos and hands every change
// to the reloader instead of terminating the process.
// nacosWatchModule 把 Nacos 内容同步进 config.yaml，并把每次变更交给 reloader
// 原地重建模块；框架默认的 reload 行为是退出进程等待外部拉起。
type nacosWatchModule struct {
	cfg      nacos.Config
	logger   *zap.Logger
	onChange func(context.Context) error

	mu        sync.Mutex
	cancel    context.CancelFunc
	watchDone chan struct{}
}

func (m *nacosWatchModule) Name() string { return "nacos-watch" }

func (m *nacosWatchModule) Register(*support.Router) error { return nil }

// Start runs the watcher and the reload executor on separate goroutines.
// Reload retires this module from inside its own change handler, so one
// goroutine could never wait for itself; splitting them lets Stop wait for the
// watcher while the reload that triggered Stop finishes on the executor.
// Start 把监听与重载执行器放在两个 goroutine 上。Reload 会在自己的变更回调里
// 退役本模块，单个 goroutine 无法等待自己；拆开后 Stop 能等待监听退出，而触发
// Stop 的那次 Reload 在另一个 goroutine 上继续完成。
func (m *nacosWatchModule) Start(ctx context.Context) error {
	watchCtx, cancel := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	changes := make(chan struct{}, 1)

	m.mu.Lock()
	m.cancel = cancel
	m.watchDone = watchDone
	m.mu.Unlock()

	go func() {
		defer close(watchDone)
		err := nacos.Watch(watchCtx, m.cfg,
			nacos.WithLogger(m.logger),
			nacos.WithOnChange(func(context.Context, nacos.Result) error {
				// Coalesce notifications: the file on disk is the state, so a
				// change arriving during a reload only needs one more pass.
				// 合并通知：磁盘文件就是状态，Reload 期间到来的变更只需再跑一次。
				select {
				case changes <- struct{}{}:
				default:
				}
				return nil
			}),
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("nacos watcher stopped", zap.Error(err))
		}
	}()

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
