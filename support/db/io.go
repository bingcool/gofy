package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/bingcool/gofy/support/internal/instanceinit"
)

var (
	// ErrNotConfigured is returned when Configure has not been called or the
	// registry has already been closed.
	// 未调用 Configure 或注册表已关闭时返回 ErrNotConfigured。
	ErrNotConfigured = errors.New("db: no instance is configured")
	// ErrUnknownInstance is returned when the requested name is not registered.
	// 请求的名字未注册时返回 ErrUnknownInstance。
	ErrUnknownInstance = errors.New("db: unknown instance")
)

// Registry holds named configurations and opens their pools on first use.
//
// Configure installs a new generation and returns it; NewIO uses the most
// recently installed generation. The framework-managed db.default is not part
// of the registry: use Runtime.DB (or the application's own handle) for it.
//
// Pools open outside the registry lock: different names initialize in parallel,
// concurrent callers of one name share a single attempt, and a failed attempt is
// retried by the next call. Configure/NewIO/Close are the only entry points;
// prefer the instance Registry for per-generation isolation.
// Registry 持有命名配置，并在首次使用时打开对应的 pool。
//
// Configure 安装新一代配置并返回；NewIO 使用最近安装的一代。框架管理的
// db.default 不在注册表内：它请使用 Runtime.DB（或应用自己的句柄）。
//
// pool 在注册表锁之外打开：不同名字可并行初始化，同一名字的并发调用共享一次
// 尝试，失败的下一次调用会重试。Configure/NewIO/Close 是唯一入口；需要按代
// 隔离时优先使用实例级 Registry。
type Registry struct {
	configs map[string]Config
	logger  *zap.Logger
	slots   *instanceinit.Slots[*gorm.DB]
}

var current atomic.Pointer[Registry]

// Configure validates the named configurations, installs them as the current
// generation and returns it. An empty map is allowed: NewIO then reports
// ErrUnknownInstance, which lets a reload disable every instance.
// Configure 校验命名配置，把它们安装为当前一代并返回。允许空 map：此时 NewIO
// 返回 ErrUnknownInstance，Reload 可借此停用所有实例。
func Configure(configs map[string]Config, logger *zap.Logger) (*Registry, error) {
	reg, err := NewRegistry(configs, logger)
	if err != nil {
		return nil, err
	}
	reg.Install()
	return reg, nil
}

// NewRegistry validates the named configurations without making them current.
// Do the eager opening through NewIO and call Install once every instance is
// ready, so a failed startup does not replace a working generation.
// NewRegistry 校验命名配置但不设为当前一代。预热请通过 NewIO 完成，并在所有实例
// 就绪后再调用 Install，避免启动失败覆盖掉原本可用的一代。
func NewRegistry(configs map[string]Config, logger *zap.Logger) (*Registry, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	reg := &Registry{
		configs: make(map[string]Config, len(configs)),
		logger:  logger,
		slots: instanceinit.New(func(name string, pool *gorm.DB) error {
			if err := Close(pool); err != nil {
				return fmt.Errorf("db instance %q: %w", name, err)
			}
			return nil
		}),
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("db: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("db instance %q: %w", name, err)
		}
		reg.configs[name] = cfg
	}
	return reg, nil
}

// Install makes this generation current for package-level NewIO. Installing a
// closed generation is a no-op.
// Install 把本代设为包级 NewIO 的当前一代；安装已关闭的一代是空操作。
func (r *Registry) Install() {
	if r == nil {
		return
	}
	r.slots.Publish(func() { current.Store(r) })
}

// NewIO returns the pool registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
// NewIO 返回当前一代中名为 name 的 pool，首次使用时打开。Configure 之前返回
// ErrNotConfigured，名字未注册时返回 ErrUnknownInstance。
func NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	reg := current.Load()
	if reg == nil {
		return nil, ErrNotConfigured
	}
	return reg.NewIO(ctx, name)
}

// CloseAllIO closes every pool opened by the current generation and clears it,
// so a later NewIO reports ErrNotConfigured.
// CloseAllIO 关闭当前一代打开的所有 pool 并清除它，之后 NewIO 返回
// ErrNotConfigured。
func CloseAllIO() error {
	reg := current.Load()
	if reg == nil {
		return nil
	}
	return reg.Close()
}

// NewIO returns the pool registered under name in this generation.
// NewIO 返回本代中名为 name 的 pool。
func (r *Registry) NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	pool, err := r.slots.Get(ctx, name, func(ctx context.Context) (*gorm.DB, error) {
		cfg, ok := r.configs[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
		}
		pool, err := New(ctx, cfg, r.logger)
		if err != nil {
			return nil, fmt.Errorf("db instance %q: %w", name, err)
		}
		return pool, nil
	})
	if errors.Is(err, instanceinit.ErrClosed) {
		return nil, ErrNotConfigured
	}
	return pool, err
}

// Close releases the pools opened by this generation and stops it from opening
// more. Closing an earlier generation does not affect the current one.
// Close 释放本代打开的 pool 并阻止继续打开；关闭旧的一代不影响当前一代。
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	current.CompareAndSwap(r, nil)
	return r.slots.Close()
}
