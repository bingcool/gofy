// Package redis exposes the demo's named Redis instances.
//
// 业务代码通过快照取连接（appio.FromContext(ctx).Redis(name)），
// 无请求上下文时用 Cache()、Session()、RateLimit()。当前一代的 Set 用
// atomic.Pointer 整体发布，enabled 为 false 的实例保持 nil。
package redis

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/bingcool/gofy"
	gofyotel "github.com/bingcool/gofy/support/otel"
	gofyredis "github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Supported instance names.
const (
	InstanceCache     = "cache"
	InstanceSession   = "session"
	InstanceRateLimit = "ratelimit"
)

var supported = []string{InstanceCache, InstanceSession, InstanceRateLimit}

// Instance configures one named Redis connection.
type Instance struct {
	Enabled          bool `yaml:"enabled"`
	gofyredis.Config `yaml:",inline"`
}

// Config maps instance names to their connection settings.
type Config map[string]Instance

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Cache 返回当前一代的 cache 实例；未启用时为 nil。
func Cache() *goredis.Client { return current.Load().Client(InstanceCache) }

// Session 返回当前一代的 session 实例；未启用时为 nil。
func Session() *goredis.Client { return current.Load().Client(InstanceSession) }

// RateLimit 返回当前一代的 ratelimit 实例；未启用时为 nil。
func RateLimit() *goredis.Client { return current.Load().Client(InstanceRateLimit) }

// Set holds one generation of connections. Open builds it, Install publishes it
// to the package handles and Close releases it.
type Set struct {
	registry *gofyredis.Registry
	clients  map[string]*goredis.Client
}

// Open connects every enabled instance. Unknown names are rejected so a typo
// does not silently leave a handle nil. The connections themselves come from
// support/redis NewIO, which caches one client per alias for this generation.
// When rt enables tracing, each client gets command-level client spans.
func Open(ctx context.Context, cfg Config, rt *gofy.Runtime) (*Set, error) {
	logger := zap.NewNop()
	if rt != nil && rt.Logger != nil {
		logger = rt.Logger
	}
	names := make([]string, 0, len(cfg))
	confs := make(map[string]gofyredis.Config, len(cfg))
	for name, inst := range cfg {
		if !inst.Enabled {
			continue
		}
		if !isSupported(name) {
			return nil, fmt.Errorf("redis: unknown instance %q (supported: %s)", name, strings.Join(supported, ", "))
		}
		names = append(names, name)
		confs[name] = inst.Config
	}
	sort.Strings(names)

	registry, err := gofyredis.NewRegistry(confs)
	if err != nil {
		return nil, err
	}

	set := &Set{
		registry: registry,
		clients:  make(map[string]*goredis.Client, len(names)),
	}
	for _, name := range names {
		client, err := registry.NewIO(ctx, name)
		if err != nil {
			_ = registry.Close()
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		if rt != nil && rt.Tracing {
			if err := gofyotel.InstrumentRedisWith(client, rt.TracerProvider); err != nil {
				_ = registry.Close()
				return nil, fmt.Errorf("redis instance %q tracing: %w", name, err)
			}
		}
		set.clients[name] = client
		logger.Info("redis instance connected",
			zap.String("instance", name),
			zap.String("addr", cfg[name].Addr),
			zap.Int("db", cfg[name].DB),
		)
	}
	if len(names) == 0 {
		logger.Info("redis instances disabled")
	}
	// 全部实例就绪后再切换当前一代：启动失败时旧模块的 NewIO 不受影响。
	registry.Install()
	return set, nil
}

// Client returns this generation's client for name; nil when the instance is
// not enabled.
func (s *Set) Client(name string) *goredis.Client {
	if s == nil {
		return nil
	}
	return s.clients[name]
}

// Install publishes this generation to the package handles.
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close releases this generation. The installed pointer is cleared only while
// it still points at this set, so a reload that already installed new
// connections keeps them intact.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	return s.registry.Close()
}

// NewIO returns the client registered under name in this generation, opening
// the connection on first use. An instance that is not enabled is not part of
// the registry, so the lookup reports ErrUnknownInstance.
func (s *Set) NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	if s == nil || s.registry == nil {
		return nil, gofyredis.ErrNotConfigured
	}
	return s.registry.NewIO(ctx, name)
}

// NewIO resolves name in the installed generation. It stays for callers that
// have no request context (scripts); request paths go through the snapshot so
// the lookup cannot cross a reload.
func NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	return current.Load().NewIO(ctx, name)
}

func isSupported(name string) bool {
	return slices.Contains(supported, name)
}
