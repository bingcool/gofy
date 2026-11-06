// Package redis exposes the demo's named Redis instances.
//
// 业务代码直接使用包级句柄：redis.Cache.Get(...)、redis.Session.Get(...)、
// redis.RateLimit.Ping(...) 等。句柄由 app/io 包在启动时通过
// Open/Install 填充，关停时清空；enabled 为 false 的实例保持 nil。
package redis

import (
	"context"
	"errors"
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
	clients map[string]*goredis.Client
}

// Open connects every enabled instance. Unknown names are rejected so a typo
// does not silently leave a handle nil.
func Open(ctx context.Context, cfg Config, logger *zap.Logger) (*Set, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	names := make([]string, 0, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	set := &Set{clients: make(map[string]*goredis.Client, len(names))}
	closeOpened := func() {
		for _, client := range set.clients {
			_ = gofyredis.Close(client)
		}
	}

	for _, name := range names {
		if !isSupported(name) {
			closeOpened()
			return nil, fmt.Errorf("redis: unknown instance %q (supported: %s)", name, strings.Join(supported, ", "))
		}
		inst := cfg[name].Config
		client, err := gofyredis.New(ctx, inst)
		if err != nil {
			closeOpened()
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		set.clients[name] = client
		logger.Info("redis instance connected",
			zap.String("instance", name),
			zap.String("addr", inst.Addr),
			zap.Int("db", inst.DB),
		)
	}
	if len(names) == 0 {
		logger.Info("redis instances disabled")
	}
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

	var errs []error
	for name, client := range s.clients {
		if err := gofyredis.Close(client); err != nil {
			errs = append(errs, fmt.Errorf("redis instance %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func isSupported(name string) bool {
	return slices.Contains(supported, name)
}
