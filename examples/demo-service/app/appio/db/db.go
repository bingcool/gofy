// Package db exposes the demo's named MySQL-protocol connections.
//
// 业务代码不直接读包级变量：一律通过 app/appio 的快照取连接
// （appio.WithContext(ctx).DBConnect(ctx, "default")），脚本、守护进程与请求走同一条
// 路径。当前一代的 Set 用 atomic.Pointer 整体发布，读写不再共享可变包变量。
//
// Db 就是框架托管的 db.default（Runtime.DB），order、product 由 app/appio 包
// 按 db.order、db.product 在启动时打开；配置里没有的实例不在本代 Registry 里，
// 按名字解析时返回 ErrUnknownInstance。其余名字（如 selectDb）注册进 registry，
// 首次 NewIO 时才建连。
// 脚本与请求一样跑完整的 app/appio 装配代（见 script/register.go），拿到的也是本代
// 命名实例；只有单元测试用 NewSet 发布一个只带 default 的快照。
package db

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bingcool/gofy"
	gofydb "github.com/bingcool/gofy/support/db"
	gofyotel "github.com/bingcool/gofy/support/otel"
	gofysqllog "github.com/bingcool/gofy/support/sqllog"
	"github.com/bingcool/gofy/support/tenant"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 已打开的实例名，与配置里的 db.<name> 一一对应。
//
// DefaultInstance 是框架托管的 db.default：它由 Runtime 打开，刻意不进
// registry（否则 NewIO 会再建一个池），按这个名字解析时要走 Set.Default。
const (
	DefaultInstance = "default"
	InstanceOrder   = "order"
	InstanceProduct = "product"
)

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回当前一代的 db.default；尚未安装时为 nil。
func Default() *gorm.DB {
	return current.Load().Default()
}

// Set holds one generation of connections. Open builds it, Install publishes it
// and Close releases the extra instances; the default connection belongs to the
// framework and is never closed here.
// Set 持有一个装配代的连接：Open 构建、Install 发布、Close 只释放额外实例；
// db.default 由框架持有，本包从不关闭它。
type Set struct {
	defaultDB *gorm.DB
	registry  *gofydb.Registry
	order     *gorm.DB
	product   *gorm.DB
}

// NewSet builds a generation that carries only db.default and owns no extra
// pool. It is the script/test path: the caller publishes a pool the framework
// opened instead of running the io module lifecycle.
// NewSet 构造只含 db.default、不持有额外连接池的一代，供脚本与测试使用：
// 调用方把框架已打开的池直接发布成快照，不跑 io 模块的生命周期。
func NewSet(defaultDB *gorm.DB) *Set {
	return &Set{defaultDB: defaultDB}
}

// Default returns the framework-managed db.default of this generation.
// Default 返回本代的 db.default；它的生命周期归框架，与本包无关。
func (s *Set) Default() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.defaultDB
}

// Instance returns the eagerly opened pool for name; nil when the instance is
// not configured in this generation.
// Instance 返回本代在启动时已打开的连接；实例未配置时返回 nil，调用方需自行
// 判空后回落到 NewIO。
func (s *Set) Instance(name string) *gorm.DB {
	if s == nil {
		return nil
	}
	switch name {
	case InstanceOrder:
		return s.order
	case InstanceProduct:
		return s.product
	default:
		return nil
	}
}

// Open opens db.order and db.product eagerly. defaultDB is the framework-managed
// db.default and is only published, not owned. Any other instance name (selectDb
// 等兼容 MySQL 协议的库) is registered for lazy opening via NewIO. Extra
// connections come from support/db NewIO for this generation.
//
// When rt enables tracing, the eager pools also get the GORM span plugin;
// db.default is instrumented by the framework that owns it. Lazy instances are
// not instrumented automatically: 业务取到连接后可自行 db.Use(gofyotel.NewGORMPlugin(...))。
//
// Tenant isolation is opt-in via WithTenant. It is installed after the tracing
// plugin, because that plugin replaces Statement.Context in its before callback
// and the tenant handler must read the final context. Tenant isolation is off
// by default so a project without multi-tenancy keeps ordinary GORM behaviour.
// 租户隔离通过 WithTenant 显式启用。它在链路插件之后安装——后者会在自己的 before
// 回调里替换 Statement.Context，租户 Handler 必须读到最终 context。默认关闭，
// 不需要多租户的项目保持普通 GORM 行为。
type OpenOption func(*openOptions)

type openOptions struct {
	tenant    *tenant.Options
	sqlPolicy *gofysqllog.Policy
	sqlLogger *zap.Logger
}

// WithSQLPolicy hands the process-wide SQL log policy to every pool this
// generation opens. The same pointer the framework got through
// gofy.WithSQLPolicy, so named instances and the default pool report into one
// set of counters.
// WithSQLPolicy 把进程级 SQL 日志策略交给本代打开的每个池。它与框架通过
// gofy.WithSQLPolicy 拿到的是同一个指针，因此命名实例与 default 池共用一组计数器。
func WithSQLPolicy(policy *gofysqllog.Policy) OpenOption {
	return func(o *openOptions) { o.sqlPolicy = policy }
}

// WithSQLLogger sends SQL entries to the dedicated channel.
// WithSQLLogger 把 SQL 条目送到独立通道。
func WithSQLLogger(l *zap.Logger) OpenOption {
	return func(o *openOptions) { o.sqlLogger = l }
}

// WithTenant installs the tenant plugin on every pool this generation opens.
// A nil option (or not calling WithTenant) leaves tenant isolation disabled.
// WithTenant 给本代打开的每个池安装租户插件。不传该选项时租户隔离保持关闭。
func WithTenant(opts tenant.Options) OpenOption {
	return func(o *openOptions) { o.tenant = &opts }
}

// callbackInspector is the exported part of GORM's unexported processor type
// used to check whether our callbacks are already installed.
// callbackInspector 是 GORM 未导出 processor 类型中已导出的部分，用来判断插件是否已安装。
type callbackInspector interface {
	Get(name string) func(*gorm.DB)
}

// EnsureTenant installs the tenant plugin on pool when it is not installed yet.
// It is the exported form of installTenant, for entry points that hold a pool the
// assembly did not open: tests that need a filtered pool without running a
// module, and an application assembling a second pool outside appio. Worker
// processes do not need it — their generation is built with WithTenant, so the
// plugin is installed at Open like it is on the HTTP path.
//
// The install is idempotent for the same reason installTenant is: db.default is a
// process-wide singleton and a second db.Use fails.
// EnsureTenant 在池上安装租户插件（尚未安装时）。它是 installTenant 的导出版本，
// 供持有"非装配点打开"的连接池的入口使用：需要在不跑模块的情况下拿到带过滤的池的测试，
// 以及 appio 之外自建第二个池的应用。worker 进程不需要它——它那一代用 WithTenant
// 构建，插件与 HTTP 路径一样在 Open 时安装。
//
// 幂等原因与 installTenant 相同：db.default 是进程级单例，第二次 db.Use 会失败。
func EnsureTenant(pool *gorm.DB, opts tenant.Options) error {
	return installTenant(pool, opts)
}

// installMu serializes the check-and-install below. The inspector guard alone is
// not enough: worker groups start at the same time and each calls EnsureTenant
// on the same pool, so two of them can both observe "not installed" before
// either registers — GORM then logs a duplicated callback for every callback the
// loser registers. The lock costs nothing on the hot path because install
// happens once per pool.
// installMu 把下面的"先查后装"串行化。光有 inspector 判断不够：worker 分组同时启动，
// 各自对同一个池调用 EnsureTenant，两个调用可能在对方注册完成前都看到"尚未安装"——GORM
// 随后会对后者的每个回调报重复注册。安装每个池只发生一次，这把锁不在热路径上。
var installMu sync.Mutex

// installTenant is idempotent: db.default is owned by the framework and shared
// across Reload generations, so Open may see the same *gorm.DB more than once.
// Calling db.Use twice on it would fail with a duplicate-callback error.
// installTenant 是幂等的：db.default 由框架持有、跨 Reload 代复用，Open 因此可能
// 多次看到同一个 *gorm.DB；对它重复 db.Use 会因回调重复注册而失败。
func installTenant(pool *gorm.DB, opts tenant.Options) error {
	if pool == nil {
		return nil
	}
	installMu.Lock()
	defer installMu.Unlock()
	if inspector, ok := any(pool.Callback().Query()).(callbackInspector); ok &&
		inspector.Get("gofy:tenant:before_query") != nil {
		return nil
	}
	return tenant.Instrument(pool, opts)
}

// Open builds this generation's Set. defaultDB may be nil: a deployment that
// configures no `db.default` (or no `db` section at all) still builds a
// generation, it simply has no framework-managed pool — Default() returns nil
// and NewDBIO(ctx, "default") reports ErrNotConfigured. The named instances in cfg
// are opened regardless, so a service can use only `db.order` if that is what
// it declares.
// Open 构建本代的 Set。defaultDB 允许为 nil：没有配 `db.default`（甚至整段 `db`
// 都不存在）的部署同样有一代句柄，只是没有框架托管的连接池——Default() 返回 nil，
// NewDBIO(ctx, "default") 报 ErrNotConfigured。cfg 里的命名实例照常打开，因此一个服务
// 可以只声明 `db.order` 并使用它。
func Open(ctx context.Context, defaultDB *gorm.DB, cfg map[string]gofy.DatabaseConfig, rt *gofy.Runtime, opts ...OpenOption) (*Set, error) {
	var o openOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	logger := zap.NewNop()
	if rt != nil && rt.Logger != nil {
		logger = rt.Logger
	}

	// db.default: tracing was installed by the framework before the io module
	// starts, so the tenant plugin naturally comes after it.
	// db.default 的链路插件由框架在 io 模块启动前装好，租户插件因此天然排在其后。
	if o.tenant != nil {
		if err := installTenant(defaultDB, *o.tenant); err != nil {
			return nil, fmt.Errorf("db default tenant: %w", err)
		}
	}

	extra := make(map[string]gofydb.Config, len(cfg))
	for name, c := range cfg {
		if name == "default" {
			// db.default is opened by the framework; NewIO would create a
			// second pool, so it stays out of the registry.
			// db.default 由框架打开，再走 NewIO 会多建一个池，因此不进 registry。
			continue
		}
		extra[name] = c
	}

	registry, err := gofydb.NewRegistry(extra, logger,
		gofydb.WithSQLPolicy(o.sqlPolicy),
		gofydb.WithSQLLogger(o.sqlLogger),
	)
	if err != nil {
		return nil, err
	}

	set := &Set{defaultDB: defaultDB, registry: registry}
	open := func(name string) (*gorm.DB, error) {
		if _, ok := cfg[name]; !ok {
			return nil, nil
		}
		pool, err := registry.NewIO(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("db instance %q: %w", name, err)
		}
		if rt != nil && rt.Tracing {
			if err := pool.Use(gofyotel.NewGORMPlugin(rt.TracerProvider)); err != nil {
				return nil, fmt.Errorf("db instance %q tracing: %w", name, err)
			}
		}
		if o.tenant != nil {
			if err := installTenant(pool, *o.tenant); err != nil {
				return nil, fmt.Errorf("db instance %q tenant: %w", name, err)
			}
		}
		logger.Info("db instance connected", zap.String("instance", name))
		return pool, nil
	}

	if set.order, err = open(InstanceOrder); err != nil {
		_ = registry.Close()
		return nil, err
	}
	if set.product, err = open(InstanceProduct); err != nil {
		_ = registry.Close()
		return nil, err
	}
	// 全部实例就绪后再切换当前一代：启动失败时旧模块的 NewIO 不受影响。
	registry.Install()
	return set, nil
}

// NewIO returns the pool registered under name in this generation, opening the
// connection on first use. db.default is framework-managed and stays out of the
// registry; use Default for it. A script snapshot built with NewSet owns no
// registry, so it reports ErrNotConfigured.
// NewIO 返回本代 name 对应的连接池，首次调用时才真正建连。db.default 不在
// registry 里，需要它请用 Default；NewSet 建的脚本快照没有 registry，查询返回
// ErrNotConfigured。
func (s *Set) NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	if s == nil || s.registry == nil {
		return nil, gofydb.ErrNotConfigured
	}
	return s.registry.NewIO(ctx, name)
}

// NewIO resolves name in the installed generation. It stays for callers that
// have no request context (scripts); request paths go through the snapshot so
// the lookup cannot cross a reload.
// NewIO 在已发布一代里解析 name，保留给没有请求上下文的调用方（脚本）；请求
// 路径走快照，避免一次请求跨代取连接。
func NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	return current.Load().NewIO(ctx, name)
}

// Install publishes this generation. Readers see the whole set or the previous
// one, never a half-updated handle.
// Install 发布本代：读方看到的要么是整代、要么是上一代，不会读到半更新的句柄。
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close releases every extra pool of this generation. The installed pointer is
// cleared only while it still points at this set, so a reload that already
// installed new connections keeps them intact.
// Close 释放本代全部额外连接池。只有发布指针仍指向自己时才清空，Reload 已安装
// 的新连接不会被旧代误删。
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	if s.registry == nil {
		return nil
	}
	return s.registry.Close()
}
