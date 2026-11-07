// Package kafka exposes the demo's named Kafka connections.
//
// 业务代码直接使用 kafka.Default，或按名字取用 kafka.Get("order")。句柄由 app/io
// 包在启动时通过 Open/Install 填充，关停时清空；enabled 为 false 的实例不建连，
// 因此本地没有 Kafka 也能启动。Producer 和 Consumer 用这里的 Client 创建：
// Producer 共享连接，Consumer 会为消费者组单独建一个客户端。
package kafka

import (
	"context"
	"fmt"
	"sort"
	"sync"

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

var (
	// Default 对应名为 default 的实例；未启用时为 nil。
	Default *gofykafka.Client

	mu        sync.RWMutex
	installed *Set
)

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

// Install publishes the set to Default and Get.
func (s *Set) Install() {
	if s == nil {
		return
	}
	mu.Lock()
	installed = s
	Default = s.clients[DefaultInstance]
	mu.Unlock()
}

// Close releases this generation. Handles are cleared only when this set is
// still the installed one, so a reload that already installed new
// connections keeps them intact.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	mu.Lock()
	if installed == s {
		installed = nil
		Default = nil
	}
	mu.Unlock()

	return s.registry.Close()
}

// NewIO returns the client registered under name in the installed generation.
// It is a thin wrapper over support/kafka, so business code that already
// imports this package can use the same alias lookup as the library.
func NewIO(ctx context.Context, name string) (*gofykafka.Client, error) {
	return gofykafka.NewIO(ctx, name)
}

// Get returns the installed client for name, or nil when the instance is not
// enabled.
func Get(name string) *gofykafka.Client {
	mu.RLock()
	defer mu.RUnlock()

	if installed == nil {
		return nil
	}
	return installed.clients[name]
}

// Names returns the enabled instance names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()

	if installed == nil {
		return nil
	}
	names := make([]string, 0, len(installed.clients))
	for name := range installed.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
