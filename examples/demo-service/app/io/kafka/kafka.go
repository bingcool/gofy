// Package kafka exposes the demo's named Kafka connections.
//
// 业务代码通过快照取连接（appio.FromContext(ctx).Kafka(name)），无请求上下文
// 时用 Default() 或 Get("order")。当前一代的 Set 用 atomic.Pointer 整体发布，
// enabled 为 false 的实例不建连，因此本地没有 Kafka 也能启动。Producer 和
// Consumer 用这里的 Client 创建：Producer 共享连接，Consumer 会为消费者组
// 单独建一个客户端。
package kafka

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	gofykafka "github.com/bingcool/gofy/support/kafka"
	"go.uber.org/zap"
)

// DefaultInstance is the name published through Default.
const DefaultInstance = "default"

// Instance configures one named Kafka cluster connection.
type Instance struct {
	Enabled          bool `yaml:"enabled"`
	gofykafka.Config `yaml:",inline"`
}

// Config maps instance names to their connection settings.
type Config map[string]Instance

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回名为 default 的实例；未启用时为 nil。
func Default() *gofykafka.Client { return current.Load().Client(DefaultInstance) }

// Set holds one generation of clients. Open builds it, Install publishes it
// and Close releases it.
type Set struct {
	registry *gofykafka.Registry
	clients  map[string]*gofykafka.Client
}

// Open connects every enabled instance. Instance names are free-form, so a
// typo creates a different instance rather than failing; config.Load rejects
// empty names before startup. Connections come from support/kafka NewIO, which
// caches one client per alias for this generation.
func Open(ctx context.Context, cfg Config, logger *zap.Logger) (*Set, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	names := make([]string, 0, len(cfg))
	confs := make(map[string]gofykafka.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			names = append(names, name)
			confs[name] = inst.Config
		}
	}
	sort.Strings(names)

	registry, err := gofykafka.NewRegistry(confs)
	if err != nil {
		return nil, err
	}

	set := &Set{
		registry: registry,
		clients:  make(map[string]*gofykafka.Client, len(names)),
	}
	for _, name := range names {
		client, err := registry.NewIO(ctx, name)
		if err != nil {
			_ = registry.Close()
			return nil, fmt.Errorf("kafka instance %q: %w", name, err)
		}
		set.clients[name] = client
		logger.Info("kafka instance connected",
			zap.String("instance", name),
			zap.Int("brokers", len(cfg[name].Brokers)),
		)
	}
	if len(names) == 0 {
		logger.Info("kafka instances disabled")
	}
	// 全部实例就绪后再切换当前一代：启动失败时旧模块的 NewIO 不受影响。
	registry.Install()
	return set, nil
}

// Client returns this generation's client for name; nil when the instance is
// not enabled.
func (s *Set) Client(name string) *gofykafka.Client {
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
// the connection on first use.
func (s *Set) NewIO(ctx context.Context, name string) (*gofykafka.Client, error) {
	if s == nil || s.registry == nil {
		return nil, gofykafka.ErrNotConfigured
	}
	return s.registry.NewIO(ctx, name)
}

// NewIO resolves name in the installed generation. Request paths go through the
// snapshot so the lookup cannot cross a reload.
func NewIO(ctx context.Context, name string) (*gofykafka.Client, error) {
	return current.Load().NewIO(ctx, name)
}

// Get returns the installed client for name, or nil when the instance is not
// enabled.
func Get(name string) *gofykafka.Client {
	return current.Load().Client(name)
}

// Names returns the enabled instance names, sorted.
func Names() []string {
	set := current.Load()
	if set == nil {
		return nil
	}
	names := make([]string, 0, len(set.clients))
	for name := range set.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
