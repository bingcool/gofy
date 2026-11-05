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
type Watcher struct {
	client *Client
	logger *zap.Logger

	onChange       func(ctx context.Context, result Result) error
	restart        bool
	restartPIDFile string
}

// WatchOption customizes a Watcher.
type WatchOption func(*Watcher)

// WithLogger sets the logger used by the watcher. The default discards logs.
func WithLogger(logger *zap.Logger) WatchOption {
	return func(w *Watcher) {
		if logger != nil {
			w.logger = logger
		}
	}
}

// WithOnChange runs fn after a changed config has been written. The new file
// is already in place when fn is called.
func WithOnChange(fn func(ctx context.Context, result Result) error) WatchOption {
	return func(w *Watcher) { w.onChange = fn }
}

// WithRestartOnChange terminates the process recorded in pidFile (empty: the
// current process) after a config change. The watcher does not spawn a
// replacement; a supervisor such as systemd, Kubernetes or a container
// restart policy must restart the process, and the new process pulls the
// config at startup.
//
// Windows does not support the termination signal; the error is reported by
// the watcher.
func WithRestartOnChange(pidFile string) WatchOption {
	return func(w *Watcher) {
		w.restart = true
		w.restartPIDFile = pidFile
	}
}

// NewWatcher validates cfg and returns a watcher.
func NewWatcher(cfg Config, opts ...WatchOption) (*Watcher, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	w := &Watcher{client: client, logger: zap.NewNop()}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w, nil
}

// Watch runs a watcher until ctx is cancelled.
func Watch(ctx context.Context, cfg Config, opts ...WatchOption) error {
	w, err := NewWatcher(cfg, opts...)
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

// Run blocks until ctx is cancelled. Listener and apply failures are logged
// and retried after RetryDelay; a bad remote config never replaces the file.
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

func (w *Watcher) apply(ctx context.Context, result Result) error {
	var errs []error
	if w.onChange != nil {
		if err := w.onChange(ctx, result); err != nil {
			errs = append(errs, fmt.Errorf("change handler: %w", err))
		}
	}
	if w.restart {
		pid, err := restartProcess(w.restartPIDFile)
		if err != nil {
			errs = append(errs, fmt.Errorf("restart: %w", err))
		} else {
			w.logger.Info("nacos config applied, termination signal sent",
				zap.Int("pid", pid), zap.String("file", result.File))
		}
	}
	return errors.Join(errs...)
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
