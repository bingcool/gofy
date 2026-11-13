// Package rocketmq exposes the demo's named RocketMQ producers.
//
// 业务代码通过快照取连接（appio.WithContext(ctx).RocketMQ(name)），无请求上下文
// 时用 Default() 或 Get("order")。当前一代的 Set 用 atomic.Pointer 整体发布，
// enabled 为 false 的实例不建连，因此本地没有 RocketMQ 也能启动。
//
// v5 的消费者不属于生产者客户端，且只有拉取模式的 SimpleConsumer：需要消费时
// 用同一个 Config 调用 support/rocketmq.NewConsumer 单独创建，再由业务自行启动
// Consume。
package rocketmq

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	"go.uber.org/zap"
)

// DefaultInstance is the name published through Default.
// DefaultInstance 是 Default() 返回的实例名；该实例未启用时返回 nil。
const DefaultInstance = "default"

// Instance configures one named RocketMQ cluster connection.
// Instance 描述单个具名 RocketMQ 集群连接；Enabled 为 false 时该实例不建连。
type Instance struct {
	Enabled             bool `yaml:"enabled"`
	gofyrocketmq.Config `yaml:",inline"`
}

// Config maps instance names to their connection settings.
// Config 以实例名为键；名字可自由取，拼错只会多出一个实例而不会报错。
type Config map[string]Instance

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回名为 default 的实例；未启用时为 nil。
func Default() *gofyrocketmq.Client { return current.Load().Client(DefaultInstance) }

// Set holds one generation of producers. Open builds it, Install publishes it
// and Close releases it.
// Set 持有一个装配代的生产者：Open 构建、Install 发布、Close 释放；消费端不在
// 这里，需要时用同一份 Config 另建 Consumer。
type Set struct {
	registry *gofyrocketmq.Registry
	clients  map[string]*gofyrocketmq.Client
}

// Open connects every enabled instance. Instance names are free-form, so a
// typo creates a different instance rather than failing; config.Load rejects
// empty names before startup. Connections come from support/rocketmq NewIO,
// which caches one producer per alias for this generation.
// Open 为每个启用实例建连。实例名自由填写，拼错只会多出一个实例而不会报错，
// 空名字已由 config.Load 在启动前拒绝。连接来自 support/rocketmq 的 NewIO，
// 本代每个别名只缓存一个生产者。
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

// Client returns this generation's producer for name; nil when the instance is
// not enabled.
// Client 返回本代 name 对应的生产者；实例未启用或名字不存在时返回 nil。
func (s *Set) Client(name string) *gofyrocketmq.Client {
	if s == nil {
		return nil
	}
	return s.clients[name]
}

// Install publishes this generation to the package handles.
// Install 把本代整体发布为包级句柄，读方看到的要么是整代、要么是上一代。
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close releases this generation. The installed pointer is cleared only while
// it still points at this set, so a reload that already installed new
// connections keeps them intact.
// Close 释放本代生产者。只有发布指针仍指向自己时才清空，Reload 已安装的新连接
// 不会被旧代误删。
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	return s.registry.Close()
}

// NewIO returns the producer registered under name in this generation, opening
// the connection on first use.
// NewIO 返回本代 name 对应的生产者，首次调用时才真正建连；本代未注册该实例
// 时返回 ErrNotConfigured。
func (s *Set) NewIO(ctx context.Context, name string) (*gofyrocketmq.Client, error) {
	if s == nil || s.registry == nil {
		return nil, gofyrocketmq.ErrNotConfigured
	}
	return s.registry.NewIO(ctx, name)
}

// NewIO resolves name in the installed generation. Request paths go through the
// snapshot so the lookup cannot cross a reload.
// NewIO 在当前已发布一代里解析 name，供没有请求上下文的调用方使用；请求路径
// 应走快照（appio.WithContext），避免一次请求跨代取连接。
func NewIO(ctx context.Context, name string) (*gofyrocketmq.Client, error) {
	return current.Load().NewIO(ctx, name)
}

// Get returns the installed producer for name, or nil when the instance is not
// enabled.
// Get 返回已发布一代中 name 的生产者；实例未启用时返回 nil。
func Get(name string) *gofyrocketmq.Client {
	return current.Load().Client(name)
}

// Names returns the enabled instance names, sorted.
// Names 返回已启用实例名并排序，便于日志与诊断输出稳定。
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
