// Package rocketmq exposes the demo's named RocketMQ producers.
//
// 业务代码直接使用 rocketmq.Default，或按名字取用 rocketmq.Get("order")。句柄由
// app/io 包在启动时通过 Open/Install 填充，关停时清空；enabled 为 false 的实例
// 不建连，因此本地没有 RocketMQ 也能启动。
//
// v5 的消费者不属于生产者客户端，且只有拉取模式的 SimpleConsumer：需要消费时
// 用同一个 Config 调用 support/rocketmq.NewConsumer 单独创建，再由业务自行启动
// Consume。
package rocketmq

import (
	"context"
	"fmt"
	"sort"
	"sync"

	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	"go.uber.org/zap"
)

// DefaultInstance is the name published through Default.
const DefaultInstance = "default"

// Instance configures one named RocketMQ cluster connection.
type Instance struct {
	Enabled             bool `yaml:"enabled"`
	gofyrocketmq.Config `yaml:",inline"`
}

// Config maps instance names to their connection settings.
type Config map[string]Instance

var (
	// Default 对应名为 default 的实例；未启用时为 nil。
	Default *gofyrocketmq.Client

	mu        sync.RWMutex
	installed *Set
)

// Set holds one generation of producers. Open builds it, Install publishes it
// and Close releases it.
type Set struct {
	registry *gofyrocketmq.Registry
	clients  map[string]*gofyrocketmq.Client
}

// Open connects every enabled instance. Instance names are free-form, so a
// typo creates a different instance rather than failing; config.Load rejects
// empty names before startup. Connections come from support/rocketmq NewIO,
// which caches one producer per alias for this generation.
func Open(ctx context.Context, cfg Config, logger *zap.Logger) (*Set, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	names := make([]string, 0, len(cfg))
	confs := make(map[string]gofyrocketmq.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			names = append(names, name)
			confs[name] = inst.Config
		}
	}
	sort.Strings(names)

	registry, err := gofyrocketmq.NewRegistry(confs)
	if err != nil {
		return nil, err
	}

	set := &Set{
		registry: registry,
		clients:  make(map[string]*gofyrocketmq.Client, len(names)),
	}
	for _, name := range names {
		client, err := registry.NewIO(ctx, name)
		if err != nil {
			_ = registry.Close()
			return nil, fmt.Errorf("rocketmq instance %q: %w", name, err)
		}
		set.clients[name] = client
		logger.Info("rocketmq producer connected",
			zap.String("instance", name),
			zap.String("endpoint", confs[name].Endpoint),
		)
	}
	if len(names) == 0 {
		logger.Info("rocketmq instances disabled")
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

// NewIO returns the producer registered under name in the installed
// generation. It is a thin wrapper over support/rocketmq, so business code that
// already imports this package can use the same alias lookup as the library.
func NewIO(ctx context.Context, name string) (*gofyrocketmq.Client, error) {
	return gofyrocketmq.NewIO(ctx, name)
}

// Get returns the installed producer for name, or nil when the instance is not
// enabled.
func Get(name string) *gofyrocketmq.Client {
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
