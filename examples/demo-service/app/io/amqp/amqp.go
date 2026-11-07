// Package amqp exposes the demo's named AMQP (RabbitMQ) connections.
//
// 业务代码直接使用 amqp.Default，或按名字取用 amqp.Get("order")。句柄由 app/io
// 包在启动时通过 Open/Install 填充，关停时清空；enabled 为 false 的实例不建连，
// 因此本地没有 RabbitMQ 也能启动。
package amqp

import (
	"context"
	"fmt"
	"sort"
	"sync"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	"go.uber.org/zap"
)

// DefaultInstance is the name published through Default.
const DefaultInstance = "default"

// Instance configures one named AMQP connection.
type Instance struct {
	Enabled         bool `yaml:"enabled"`
	gofyamqp.Config `yaml:",inline"`
}

// Config maps instance names to their connection settings.
type Config map[string]Instance

var (
	// Default 对应名为 default 的实例；未启用时为 nil。
	Default *gofyamqp.Client

	mu        sync.RWMutex
	installed *Set
)

// Set holds one generation of clients. Open builds it, Install publishes it
// and Close releases it.
type Set struct {
	registry *gofyamqp.Registry
	clients  map[string]*gofyamqp.Client
}

// Open connects every enabled instance. Instance names are free-form, so a
// typo creates a different instance rather than failing; config.Load rejects
// empty names before startup. Connections come from support/amqp NewIO, which
// caches one client per alias for this generation.
func Open(ctx context.Context, cfg Config, logger *zap.Logger) (*Set, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	names := make([]string, 0, len(cfg))
	confs := make(map[string]gofyamqp.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			names = append(names, name)
			confs[name] = inst.Config
		}
	}
	sort.Strings(names)

	registry, err := gofyamqp.NewRegistry(confs)
	if err != nil {
		return nil, err
	}

	set := &Set{
		registry: registry,
		clients:  make(map[string]*gofyamqp.Client, len(names)),
	}
	for _, name := range names {
		client, err := registry.NewIO(ctx, name)
		if err != nil {
			_ = registry.Close()
			return nil, fmt.Errorf("amqp instance %q: %w", name, err)
		}
		set.clients[name] = client
		logger.Info("amqp instance connected",
			zap.String("instance", name),
			zap.Int("hosts", len(cfg[name].Hosts)),
		)
	}
	if len(names) == 0 {
		logger.Info("amqp instances disabled")
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
// It is a thin wrapper over support/amqp, so business code that already imports
// this package can use the same alias lookup as the library.
func NewIO(ctx context.Context, name string) (*gofyamqp.Client, error) {
	return gofyamqp.NewIO(ctx, name)
}

// Get returns the installed client for name, or nil when the instance is not
// enabled.
func Get(name string) *gofyamqp.Client {
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
