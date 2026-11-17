package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/bingcool/gofy/support/internal/instanceinit"
)

// ErrUnknownInstance is returned when the requested name is not registered.
// ErrUnknownInstance 在请求的实例名未注册时返回。
var ErrUnknownInstance = errors.New("kafka: unknown instance")

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
//
// Consumers are not cached here: NewConsumer derives a separate client per
// consumer group from the client returned by NewIO.
// Registry 保存命名配置，并在首次使用时打开对应的 client。
// Configure 安装新一代并返回它，NewIO 使用最近安装的一代。每代只拥有自己打开的
// client，因此 Reload 可以先建新 Registry、切换使用、再关闭旧的，正在使用旧
// client 的请求不受影响。
// client 在注册表锁之外打开：不同名字并行初始化，同名并发调用共享同一次尝试，
// 失败的那次由下一次调用重试。需要按代隔离时请使用实例上的 Registry。
// 消费者不在此缓存：NewConsumer 会基于 NewIO 返回的客户端，为每个消费者组
// 派生独立的客户端。
type Registry struct {
	configs map[string]Config
	slots   *instanceinit.Slots[*Client]
}

var current atomic.Pointer[Registry]

// Configure validates the named configurations, installs them as the current
// generation and returns it. An empty map is allowed: NewIO then reports
// ErrUnknownInstance, which lets a reload disable every instance.
// Configure 校验命名配置，把它们安装为当前代并返回。允许传空 map：此时 NewIO
// 返回 ErrUnknownInstance，Reload 因此可以一次性停用所有实例。
func Configure(configs map[string]Config) (*Registry, error) {
	reg, err := NewRegistry(configs)
	if err != nil {
		return nil, err
	}
	// Closing the displaced generation is the point of the swap: once this
	// generation is current, nothing can reach the old connections through
	// NewIO, so nothing else would ever close them. A caller that keeps
	// per-generation handles uses NewRegistry + Install and closes the
	// displaced generation itself.
	// 关闭被换下的旧代正是换代的要点：本代成为当前代之后，NewIO 再也够不到旧连接，
	// 也没有别人会关闭它们。持有按代句柄的调用方应改用 NewRegistry + Install，并自行
	// 关闭被换下的旧代。
	if previous := reg.Install(); previous != nil {
		if err := previous.Close(); err != nil {
			return reg, fmt.Errorf("kafka: close displaced generation: %w", err)
		}
	}
	return reg, nil
}

// NewRegistry validates the named configurations without making them current.
// Do the eager opening through NewIO and call Install once every instance is
// ready, so a failed startup does not replace a working generation.
// NewRegistry 校验命名配置，但不把它们设为当前代。预打开请通过 NewIO 完成，
// 等所有实例就绪后再调用 Install，启动失败就不会替换掉可用的一代。
func NewRegistry(configs map[string]Config) (*Registry, error) {
	reg := &Registry{
		configs: make(map[string]Config, len(configs)),
		slots: instanceinit.New(func(name string, client *Client) error {
			if err := client.Close(); err != nil {
				return fmt.Errorf("kafka instance %q: %w", name, err)
			}
			return nil
		}),
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("kafka: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("kafka instance %q: %w", name, err)
		}
		reg.configs[name] = cfg
	}
	return reg, nil
}

// Install makes this generation current for package-level NewIO and returns the
// generation it displaced, which may be nil. Installing a closed generation is a
// no-op and reports nil. Configure closes the displaced generation; a caller
// that installs a registry directly owns that close.
// Install 把本代设为包级 NewIO 使用的当前代，并返回被它换下的旧代（可能为 nil）。
// 安装已关闭的代是空操作并返回 nil。Configure 会关闭被换下的旧代；直接 Install 的
// 调用方自己负责这次关闭。
func (r *Registry) Install() *Registry {
	if r == nil {
		return nil
	}
	var previous *Registry
	r.slots.Publish(func() { previous = current.Swap(r) })
	return previous
}

// NewIO returns the client registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
// NewIO 返回当前代中注册在 name 下的 client，首次使用时打开。Configure 之前
// 返回 ErrNotConfigured，名字未注册时返回 ErrUnknownInstance。
func NewIO(ctx context.Context, name string) (*Client, error) {
	reg := current.Load()
	if reg == nil {
		return nil, ErrNotConfigured
	}
	return reg.NewIO(ctx, name)
}

// CloseAllIO closes every client opened by the current generation and clears
// it, so a later NewIO reports ErrNotConfigured.
// CloseAllIO 关闭当前代打开的所有 client 并清除当前代，之后的 NewIO 返回
// ErrNotConfigured。
func CloseAllIO() error {
	reg := current.Load()
	if reg == nil {
		return nil
	}
	return reg.Close()
}

// NewIO returns the client registered under name in this generation.
// NewIO 返回本代中注册在 name 下的 client，未打开时按配置打开。
func (r *Registry) NewIO(ctx context.Context, name string) (*Client, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	client, err := r.slots.Get(ctx, name, func(ctx context.Context) (*Client, error) {
		cfg, ok := r.configs[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
		}
		client, err := New(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("kafka instance %q: %w", name, err)
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
// Close 释放本代已打开的 client，并阻止它继续打开新连接。关闭旧的一代不会
// 影响当前代。
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	current.CompareAndSwap(r, nil)
	return r.slots.Close()
}
