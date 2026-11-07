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
type nacosWatchModule struct {
	cfg      nacos.Config
	logger   *zap.Logger
	onChange func(context.Context) error

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (m *nacosWatchModule) Name() string { return "nacos-watch" }

func (m *nacosWatchModule) Register(*support.Router) error { return nil }

func (m *nacosWatchModule) Start(ctx context.Context) error {
	// Tie the watcher to the application context: shutdown cancels it, and a
	// reload cancels it through Stop once the replacement is serving.
	watchCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		err := nacos.Watch(watchCtx, m.cfg,
			nacos.WithLogger(m.logger),
			nacos.WithOnChange(func(ctx context.Context, _ nacos.Result) error {
				if m.onChange == nil {
					return nil
				}
				return m.onChange(ctx)
			}),
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("nacos watcher stopped", zap.Error(err))
		}
	}()
	return nil
}

// Stop only cancels the watcher. Reload runs inside its change handler, so
// waiting for the goroutine here would deadlock the reload.
func (m *nacosWatchModule) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
