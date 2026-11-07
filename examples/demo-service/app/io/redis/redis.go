// Package redis exposes the demo's named Redis instances.
//
// 业务代码直接使用包级句柄：redis.Cache.Get(...)、redis.Session.Get(...)、
// redis.RateLimit.Ping(...) 等。句柄由 app/io 包在启动时通过
// Open/Install 填充，关停时清空；enabled 为 false 的实例保持 nil。
package redis

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

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

// 业务可直接取用的实例句柄；对应实例未启用或未启动时为 nil。
var (
	Cache     *goredis.Client
	Session   *goredis.Client
	RateLimit *goredis.Client
)

var mu sync.RWMutex

// Set holds one generation of connections. Open builds it, Install publishes it
// to the package handles and Close releases it.
type Set struct {
	registry *gofyredis.Registry
	clients  map[string]*goredis.Client
}

// Open connects every enabled instance. Unknown names are rejected so a typo
// does not silently leave a handle nil. The connections themselves come from
// support/redis NewIO, which caches one client per alias for this generation.
func Open(ctx context.Context, cfg Config, logger *zap.Logger) (*Set, error) {
	if logger == nil {
		logger = zap.NewNop()
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

// Install publishes the set to the package handles.
func (s *Set) Install() {
	if s == nil {
		return
	}
	mu.Lock()
	Cache = s.clients[InstanceCache]
	Session = s.clients[InstanceSession]
	RateLimit = s.clients[InstanceRateLimit]
	mu.Unlock()
}

// Close releases this generation. Handles are cleared only when they still
// point at this set, so a reload that already installed new connections keeps
// them intact.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	mu.Lock()
	if Cache == s.clients[InstanceCache] {
		Cache = nil
	}
	if Session == s.clients[InstanceSession] {
		Session = nil
	}
	if RateLimit == s.clients[InstanceRateLimit] {
		RateLimit = nil
	}
	mu.Unlock()

	return s.registry.Close()
}

// NewIO returns the client registered under name in the installed generation.
// It is a thin wrapper over support/redis, so business code that already
// imports this package can use the same alias lookup as the library.
func NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	return gofyredis.NewIO(ctx, name)
}

func isSupported(name string) bool {
	return slices.Contains(supported, name)
}
