package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bingcool/gofy/support/internal/instanceinit"
)

var (
	// ErrNotConfigured is returned when Configure has not been called or the
	// registry has already been closed.
	// 未调用 Configure，或当前代已被 Close 时返回；可用 errors.Is 判定。
	ErrNotConfigured = errors.New("redis: no instance is configured")
	// ErrUnknownInstance is returned when the requested name is not registered.
	// 请求的实例名不在当前代的配置里时返回，错误信息末尾会带上该名称。
	ErrUnknownInstance = errors.New("redis: unknown instance")
)

// Registry holds named configurations and opens their clients on first use.
//
// Configure installs a new generation and returns it; NewIO uses the most
// recently installed generation. Each generation owns the clients it opened,
// so a reload can build a new Registry, start using it, and close the previous
// one without disturbing in-flight users of the old clients.
//
// Clients open outside the registry lock: different names initialize in
// parallel, concurrent callers of one name share a single attempt, and a failed
// attempt is retried by the next call. Prefer the instance Registry for
// per-generation isolation.
// Registry 持有按名称索引的配置，并在首次使用时创建对应 client；名称两侧空白会被裁剪。
//
// Configure 安装新一代并返回；包级 NewIO 使用最近一次安装的代。每代独占自己打开的
// client，因此 Reload 可先构建新 Registry、切换后再关闭旧代，不会影响仍在用旧
// client 的请求。
//
// 创建 client 时不持锁：不同名称并行初始化，同一名称的并发调用共享同一次尝试，
// 失败的尝试由下一次调用重试；需要按代隔离时优先使用实例级 Registry。
type Registry struct {
	configs map[string]Config
	slots   *instanceinit.Slots[*goredis.Client]
}

var current atomic.Pointer[Registry]

// Configure validates the named configurations, installs them as the current
// generation and returns it. An empty map is allowed: NewIO then reports
// ErrUnknownInstance, which lets a reload disable every instance.
// 校验配置后安装为当前代并返回该代；允许空 map，此时 NewIO 一律返回
// ErrUnknownInstance，可用它一次性停用所有实例。校验失败时不改动当前代。
func Configure(configs map[string]Config) (*Registry, error) {
	reg, err := NewRegistry(configs)
	if err != nil {
		return nil, err
	}
	reg.Install()
	return reg, nil
}

// NewRegistry validates the named configurations without making them current.
// Do the eager opening through NewIO and call Install once every instance is
// ready, so a failed startup does not replace a working generation.
// 只校验配置，不切换当前代；预热连接时对返回的 Registry 调用 NewIO，等全部实例
// 就绪后再 Install，这样启动失败不会顶掉正在工作的旧代。
func NewRegistry(configs map[string]Config) (*Registry, error) {
	reg := &Registry{
		configs: make(map[string]Config, len(configs)),
		slots: instanceinit.New(func(name string, client *goredis.Client) error {
			if err := Close(client); err != nil {
				return fmt.Errorf("redis instance %q: %w", name, err)
			}
			return nil
		}),
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("redis: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		reg.configs[name] = cfg
	}
	return reg, nil
}

// Install makes this generation current for package-level NewIO. Installing a
// closed generation is a no-op.
// 把该代设为包级 NewIO 的当前代；nil 或已关闭的代调用无任何效果，
// 替换不是原子的"先关后开"，旧代仍需调用方自行 Close。
func (r *Registry) Install() {
	if r == nil {
		return
	}
	r.slots.Publish(func() { current.Store(r) })
}

// NewIO returns the client registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
// 返回当前代中该名称对应的 client，首次调用时创建；尚未 Configure 返回
// ErrNotConfigured，名称未注册返回 ErrUnknownInstance。返回的 client 归 Registry
// 所有，调用方不得关闭它。
func NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	reg := current.Load()
	if reg == nil {
		return nil, ErrNotConfigured
	}
	return reg.NewIO(ctx, name)
}

// CloseAllIO closes every client opened by the current generation and clears
// it, so a later NewIO reports ErrNotConfigured.
// 关闭当前代打开的全部 client 并清空当前代，之后 NewIO 返回
// ErrNotConfigured；无可关闭的代时返回 nil。进程退出阶段可放心重复调用。
func CloseAllIO() error {
	reg := current.Load()
	if reg == nil {
		return nil
	}
	return reg.Close()
}

// NewIO returns the client registered under name in this generation.
// 返回本代中该名称对应的 client，首次调用时创建；方法接收者为 nil 或该代已
// Close 时返回 ErrNotConfigured，未注册名称返回包装了 ErrUnknownInstance 的错误。
func (r *Registry) NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	client, err := r.slots.Get(ctx, name, func(ctx context.Context) (*goredis.Client, error) {
		cfg, ok := r.configs[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
		}
		client, err := New(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		return client, nil
	})
	if errors.Is(err, instanceinit.ErrClosed) {
		return nil, ErrNotConfigured
	}
	return client, err
}

// Close releases the clients opened by this generation and stops it from
// opening more. Closing an earlier generation does not affect the current one.
// 释放本代已打开的 client 并禁止再创建；只有仍是当前代时才清空全局指针，
// 因此关闭旧代不会影响当前代。关闭后本代不可复用，需要时请重新 Configure。
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	current.CompareAndSwap(r, nil)
	return r.slots.Close()
}
