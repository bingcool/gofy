package gofy

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/logger"
	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
	"github.com/bingcool/gofy/support/worker"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Option customizes the App during gofy.New. Options apply in the order given, so
// a later option overrides an earlier setting of the same field; an error from any
// option fails New and the process never enters the startup path.
// Option 在 gofy.New 期间定制 App。选项按传入顺序依次应用，后应用的选项会
// 覆盖先前的同名配置；任一选项返回错误都会让 New 失败，进程不进入启动流程。
type Option func(*options) error

// MiddlewareRegistration is the context passed to a MiddlewareRegistrar during
// startup composition. Registry already holds the built-in framework middleware,
// so a registrar only adds application-level aliases.
// MiddlewareRegistration 是启动装配阶段传给 MiddlewareRegistrar 的上下文。
// Registry 已包含框架内建中间件，registrar 只需补充应用层别名。
type MiddlewareRegistration struct {
	// Runtime is the runtime of this composition; use it to fetch Logger and other
	// dependencies.
	// Runtime 是本次装配的运行时，可用于取 Logger 等依赖。
	Runtime *core.Runtime
	// Registry is the single middleware registry of this composition; it travels
	// with the Router to the adapter, which resolves contract aliases through it.
	// Registry 是本次装配唯一的中间件注册表，随 Router 交给 adapter 解析契约别名。
	Registry *middleware.Registry
	// Responder is the App responder; application middleware should reuse it to
	// render errors.
	// Responder 是 App 的响应器，应用层中间件应复用它渲染错误。
	Responder support.Responder
}

// MiddlewareRegistrar registers application middleware into the App registry
// before modules bind routes. Startup and every Reload run it again with a new
// Runtime, Responder and empty registry, so a hot reload never reuses stale
// instances and config changes take effect.
// MiddlewareRegistrar 在模块绑定路由之前，把应用层中间件注册进 App 的注册表。
// 启动和每次 Reload 都会用新的 Runtime、Responder 和空注册表重新执行，因此
// 热更新不会复用旧实例，配置变化也能生效。
type MiddlewareRegistrar func(MiddlewareRegistration) error

// namePattern constrains the app name: a lowercase letter first, then only
// lowercase letters, digits and dashes, 63 characters at most. The name ends up in
// logs, the pid file, Nacos registration and version info.
// namePattern 约束应用名：小写字母开头，之后只允许小写字母、数字和连字符，
// 总长不超过 63。名字会进入日志、pid 文件、Nacos 注册与版本信息。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// options is the mutable configuration of an App: written only by Options during
// gofy.New, read-only afterwards; Reload also uses it to rebuild the next
// generation of the runtime.
// options 是 App 的可变配置集合：只在 gofy.New 期间由 Option 写入，之后由
// App 只读使用；Reload 也会用它重建新一代运行时。
type options struct {
	name    string           // 应用名：日志、pid 文件、Nacos 注册与版本信息共用
	env     string           // 环境标记，暴露为 Runtime.Env，并作为 tracing 的默认环境
	version core.VersionInfo // 显式版本信息（WithVersionInfo）
	hasVer  bool             // whether version info was set explicitly；是否显式设置过版本信息

	logger    *zap.Logger   // 外部注入的 logger；非 nil 时 App 不再自建，也不负责关闭
	logConfig logger.Config // 未注入 logger 时 App 用这份配置自己构建

	http      HTTPConfig // HTTP 监听地址、超时与 TLS 等配置
	apiPrefix string     // shared prefix prepended to module routes; leave empty when the contract already owns the full prefix；追加在模块路由前的共享前缀；契约自带完整前缀时应留空

	middleware           []support.Middleware  // business middleware installed after framework middleware；框架中间件之后安装的业务中间件
	middlewareRegistrars []MiddlewareRegistrar // functions registering app-level middleware aliases at assembly time；装配期注册应用层中间件别名的函数

	pidFile    string        // pid 文件路径；空表示不写 pid 文件
	pidPerm    os.FileMode   // pid 文件权限
	pidRefresh time.Duration // pid 文件刷新间隔；0 表示不刷新
	workDir    string        // 工作目录，也是相对 pid 路径的基准

	dbConfig  *gofydb.Config // App 自建、持有并负责关闭的数据库配置
	sqlPolicy *logger.Policy // 进程级 SQL 日志策略；nil 表示按连接旧行为，不采样不脱敏
	db        *gorm.DB       // 外部注入的数据库；App 不 Ping 也不关闭

	// nacosRegistration registers this instance with Nacos naming when
	// Register.Enabled is true: it registers once the HTTP listener is ready and
	// deregisters on exit.
	// nacosRegistration 在 Register.Enabled 为 true 时，把本实例注册到 Nacos
	// 命名服务：HTTP 监听就绪后注册，退出时注销。
	nacosRegistration *nacos.Config

	moduleFactories []support.ModuleFactory // 启动期构建模块的工厂（能拿到 Runtime）
	modules         []support.Module        // 直接注入的模块实例（拿不到 Runtime）

	scripts  []script.Script   // maintenance scripts；维护脚本
	commands []command.Command // 自定义 CLI 命令

	// Worker configuration: groups run only through the worker subcommand, never
	// with the HTTP server.
	// worker 相关配置：分组只通过 worker 子命令运行，从不随 HTTP 服务启动。
	workerGroups      []*worker.Group         // 常驻任务分组；`worker --group` 选择
	workerModules     []support.ModuleFactory // worker 进程的装配模块；Start 先于分组
	workerPolicy      worker.Policy           // 单个实例失败后的重启策略
	workerConcurrency map[string]int          // "group/worker" → 实例数
	workerDrain       time.Duration           // 停机时在途任务的完成预算
	workerHealth      string                  // worker 进程可选的 /healthz 监听地址

	shutdownTimeout time.Duration // 关闭预算：HTTP 优雅关闭与模块 Stop 共享的总时长

	// The flags and config objects below drive the built-in middleware and security
	// options installed at startup.
	// 下面这组开关与配置对象对应启动时安装的内建中间件和安全选项。
	responder         support.Responder            // unified response renderer；统一响应渲染器
	responderSet      bool                         // 是否被 WithResponder 显式替换过
	recovery          bool                         // 是否安装 Recovery
	accessLog         bool                         // whether the access log middleware is installed；是否安装访问日志
	accessLogOptions  *middleware.AccessLogOptions // 自定义访问日志配置；nil 用默认
	headerPropagation bool                         // 是否安装 HeaderPropagation
	cors              *support.CORSConfig          // CORS 策略；nil 表示不启用
	health            bool                         // 是否注册 /healthz、/readyz
	maxBody           int64                        // request body limit in bytes；请求体上限（字节）
	trusted           []string                     // 可信代理列表（IP 或 CIDR）
	alwaysOK          bool                         // 是否把所有响应统一成 HTTP 200
	serverConf        func(*http.Server) error     // 启动前定制 http.Server 的回调

	jwt             *support.JWTAuthConfig         // 模块路由的 JWT 校验配置
	rateLimit       *support.RateLimitConfig       // rate limit policy for module routes；模块路由的限流配置
	securityHeaders *support.SecurityHeadersConfig // security response headers；安全响应头配置

	telemetry *otel.Config // OpenTelemetry 配置

	// diagnostics holds the operational listener configuration. nil means no
	// diagnostics listener and no metrics pipeline.
	// diagnostics 保存运维监听配置；nil 表示不启动诊断监听，也不构建指标管道。
	diagnostics *diagnostics.Config
}

// defaultOptions returns the gofy.New defaults: app name "app", env "dev", console
// logging, default HTTP config, a 10s shutdown budget, Recovery and access log on,
// and a 4 MiB request body limit.
// defaultOptions 返回 gofy.New 的默认配置：应用名 app、环境 dev、console 日志、
// 默认 HTTP 配置、10s 关闭预算、开启 Recovery 与访问日志、4 MiB 请求体上限。
func defaultOptions() options {
	return options{
		name: "app",
		env:  "dev",
		logConfig: logger.Config{
			Level:    "info",
			Encoding: "console",
			Outputs:  []string{"stdout"},
		},
		http:            defaultHTTPConfig(),
		pidPerm:         0o644,
		shutdownTimeout: 10 * time.Second,
		workerPolicy:    worker.DefaultPolicy(),
		workerDrain:     20 * time.Second,
		responder:       support.NewResponder(support.ResponderConfig{}),
		recovery:        true,
		accessLog:       true,
		maxBody:         4 << 20,
	}
}

// WithName sets the application name (default "app"). The name feeds log fields,
// the pid file, Nacos registration and version info. Surrounding spaces are
// trimmed; it must start with a lowercase letter, contain only lowercase letters,
// digits and dashes and stay within 63 characters, otherwise New fails.
// WithName 设置应用名（默认 app），会进入日志字段、pid 文件、Nacos 注册和
// 版本信息。首尾空白会被去掉；名字必须以小写字母开头，只含小写字母、数字
// 和连字符，最长 63 个字符，否则在 New 阶段报错。
func WithName(name string) Option {
	return func(o *options) error {
		name = strings.TrimSpace(name)
		if !namePattern.MatchString(name) {
			return fmt.Errorf("gofy: invalid app name %q", name)
		}
		o.name = name
		return nil
	}
}

// WithEnv sets the environment marker (default "dev"), exposed as Runtime.Env and
// used as the default environment for tracing resource attributes. Spaces are
// trimmed and an empty value fails New.
// WithEnv 设置环境标记（默认 dev），暴露为 Runtime.Env，并作为 tracing 资源
// 属性的默认环境。首尾空白会被去掉，空值在 New 阶段报错。
func WithEnv(env string) Option {
	return func(o *options) error {
		env = strings.TrimSpace(env)
		if env == "" {
			return fmt.Errorf("gofy: env must not be empty")
		}
		o.env = env
		return nil
	}
}

// WithLogger injects an external logger. The App then stops building one from
// WithLogConfig and never Syncs or closes it, so its lifetime stays with the
// caller; a nil logger fails New.
// WithLogger 注入外部 logger。注入后 App 不再按 WithLogConfig 自建 logger，
// 也不负责 Sync 或关闭它，生命周期仍归调用方；logger 为 nil 时报错。
func WithLogger(logger *zap.Logger) Option {
	return func(o *options) error {
		if logger == nil {
			return fmt.Errorf("gofy: logger must not be nil")
		}
		o.logger = logger
		return nil
	}
}

// WithLogConfig sets the config the App uses when it builds its own logger and
// only applies when WithLogger was not injected. The config is validated first, so
// an illegal level, encoding or output target fails New.
// WithLogConfig 设置 App 自建 logger 时使用的配置，仅在未注入 WithLogger 时
// 生效。配置先经 Validate 校验，非法级别、编码或输出目标在 New 阶段报错。
func WithLogConfig(cfg logger.Config) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		o.logConfig = cfg
		return nil
	}
}

// WithHTTP configures the HTTP server: listen address and port, read-header, read,
// write and idle timeouts, the request header limit and gin's debug/release/test
// mode. Zero fields are first filled with framework defaults (port 8080, release
// mode, ...) and then validated as a whole, so a bad port or mode fails New.
// WithHTTP 配置 HTTP 服务器：监听地址与端口、读头/读/写/空闲超时、请求头
// 上限和 gin 的 debug/release/test 模式。零值字段会先补成框架默认值（端口
// 8080、release 模式等），再整体校验；端口或模式非法时在 New 阶段报错。
func WithHTTP(cfg HTTPConfig) Option {
	return func(o *options) error {
		cfg = cfg.withDefaults()
		if err := cfg.Validate(); err != nil {
			return err
		}
		o.http = cfg
		return nil
	}
}

// WithAPIPrefix prepends one shared prefix to every module route; empty by default
// and the value is normalized by NormalizePrefix. Each module also appends its own
// Prefix() or the default /<name>, so leave this empty when the contract route
// group already carries the full prefix (such as /api/v1/order), otherwise two
// prefix layers stack up.
// WithAPIPrefix 给所有模块路由追加一层共享前缀，默认空（不追加），传入值会
// 经 NormalizePrefix 规整。每个模块还会再拼上自己的 Prefix() 或默认的
// /<name>；契约路由分组已自带完整前缀（如 /api/v1/order）时不要再设置，
// 否则会叠加两层前缀。
func WithAPIPrefix(prefix string) Option {
	return func(o *options) error {
		o.apiPrefix = support.NormalizePrefix(prefix)
		return nil
	}
}

// WithMiddleware appends business middleware after the built-in middleware and
// before module routes, so it covers every module route uniformly. It can be
// called several times and accumulates in call order; a nil middleware fails New.
// WithMiddleware 追加业务中间件，安装在内建中间件之后、模块路由之前，因此
// 对模块路由统一生效。可多次调用，按传入顺序累积；任一中间件为 nil 都在
// New 阶段报错。
func WithMiddleware(mw ...support.Middleware) Option {
	return func(o *options) error {
		for _, m := range mw {
			if m == nil {
				return fmt.Errorf("gofy: middleware must not be nil")
			}
		}
		o.middleware = append(o.middleware, mw...)
		return nil
	}
}

// WithMiddlewareRegistrar registers an application-level middleware assembler. At
// startup and on every Reload the App first builds a registry containing the
// framework middleware and then runs these functions in registration order; an
// empty or duplicate alias fails composition instead of being silently overwritten.
// Contract group/route middleware aliases all resolve through that table, so
// business modules never create or pass a registry; what is registered here is
// "alias to instance", while the global install order still follows the With*
// options.
// WithMiddlewareRegistrar 注册一个应用级中间件装配函数。App 在启动和每次
// Reload 时先建好包含框架内建中间件的注册表，再按注册顺序执行这些函数；
// 别名重复或为空会在装配期报错，不会静默覆盖。
//
// 契约里的 group/route 中间件别名都从这份表解析，业务模块因此不需要再创建
// 或传递注册表。这里注册的是「别名 → 实例」，全局安装顺序仍由 With* 选项决定。
func WithMiddlewareRegistrar(fn MiddlewareRegistrar) Option {
	return func(o *options) error {
		if fn == nil {
			return fmt.Errorf("gofy: middleware registrar must not be nil")
		}
		o.middlewareRegistrars = append(o.middlewareRegistrars, fn)
		return nil
	}
}

// WithPIDFile enables the pid file and sets its path; no pid file is written by
// default and an empty string disables it. Relative paths resolve against
// WithWorkDir, and a missing parent directory is created with 0755.
// WithPIDFile 启用 pid 文件并设置路径，默认不写 pid 文件；传空串即关闭。
// 相对路径以 WithWorkDir 为基准解析，所在目录不存在时会以 0755 自动创建。
func WithPIDFile(path string) Option {
	return func(o *options) error {
		o.pidFile = strings.TrimSpace(path)
		return nil
	}
}

// WithPIDFilePerm sets the permission used when writing the pid file (0o644 by
// default). The owner must be able to write it (the 0o200 bit must be set),
// otherwise New fails.
// WithPIDFilePerm 设置写 pid 文件时使用的权限（默认 0o644）。权限必须对属主
// 可写（包含 0o200 位），否则在 New 阶段报错。
func WithPIDFilePerm(perm os.FileMode) Option {
	return func(o *options) error {
		if perm&0o200 == 0 {
			return fmt.Errorf("gofy: pid file perm %#o must be writable by the owner", perm)
		}
		o.pidPerm = perm
		return nil
	}
}

// WithPIDRefreshInterval rewrites the pid file periodically, for example to refresh
// its modification time when a container probes liveness through it. 0 disables
// refreshing and a negative value fails New.
// WithPIDRefreshInterval 周期性重写 pid 文件（例如容器里用 pid 文件做存活
// 探测时刷新修改时间）。0 表示不刷新，负数在 New 阶段报错。
func WithPIDRefreshInterval(interval time.Duration) Option {
	return func(o *options) error {
		if interval < 0 {
			return fmt.Errorf("gofy: pid refresh interval must not be negative")
		}
		o.pidRefresh = interval
		return nil
	}
}

// WithWorkDir sets the working directory, which is also the base directory for
// relative pid file paths. The directory must already exist at startup; the
// framework never creates it.
// WithWorkDir 设置工作目录，同时作为 pid 文件相对路径的基准目录。目录必须
// 在启动时已存在，框架不会自动创建。
func WithWorkDir(dir string) Option {
	return func(o *options) error {
		o.workDir = strings.TrimSpace(dir)
		return nil
	}
}

// WithDatabase lets the App open a MySQL-protocol connection from cfg; the pool
// belongs to the App, opened at startup and released at shutdown. cfg is validated
// first. It excludes WithDB and setting both fails New.
// WithDatabase 让 App 按 cfg 自建 MySQL 协议连接，连接池的生命周期由 App
// 负责：启动时打开，关闭时释放。cfg 先经 Validate 校验。它与 WithDB 互斥，
// 同时设置会在 New 阶段报错。
func WithDatabase(cfg gofydb.Config) Option {
	return func(o *options) error {
		c := cfg
		if err := c.Validate(); err != nil {
			return err
		}
		o.dbConfig = &c
		return nil
	}
}

// WithSQLPolicy attaches the process-wide SQL log policy (logger.NewSQLPolicy).
// One policy per process on purpose: its sampler and rate limiters mean "this
// process writes at most N per second", so handing a different instance to each
// connection would multiply the configured limits.
//
// A nil policy keeps the legacy behaviour: every connection logs by its own
// log_level, with no sampling, masks or limits. An application that exposes
// sql_log should build the policy once and pass the same pointer here and to
// appdb.Open, so both paths report into the same counters.
// WithSQLPolicy 挂上进程级 SQL 日志策略（logger.NewSQLPolicy）。每进程一份是有意的：
// 它的采样器与限流器意味着"本进程每秒最多写 N 条"，给每个连接传不同实例会让配置的上限
// 成倍放大。
//
// 传 nil 保持旧行为：每个连接按自己的 log_level 输出，不采样、不脱敏、不限流。对外暴露
// sql_log 的应用应当只构造一次策略，并把同一个指针同时传给这里与 appdb.Open，两条路径
// 才会共用同一组计数器。
func WithSQLPolicy(policy *logger.Policy) Option {
	return func(o *options) error {
		o.sqlPolicy = policy
		return nil
	}
}

// WithDB injects an existing database connection. The App does not Ping, migrate or
// close it, so it belongs to the caller. It excludes WithDatabase and a nil db
// fails New.
// WithDB 注入一个已存在的数据库连接。App 不 Ping、不迁移、也不关闭它，
// 生命周期归调用方。它与 WithDatabase 互斥，db 为 nil 时报错。
func WithDB(db *gorm.DB) Option {
	return func(o *options) error {
		if db == nil {
			return fmt.Errorf("gofy: db must not be nil")
		}
		o.db = db
		return nil
	}
}

// WithNacosRegistration publishes this instance to Nacos naming with cfg.Register
// when cfg.Register.Enabled is true and deregisters it on shutdown. The advertised
// port defaults to the actual HTTP listen port; registration happens after the
// listener is ready, Reload does not register again, and a failed registration
// aborts startup instead of running undiscoverable.
// The option is a no-op when cfg.Enabled or cfg.Register.Enabled is false; enabling
// registration without service_name fails New.
// WithNacosRegistration 在 cfg.Register.Enabled 为 true 时，用 cfg.Register
// 把本实例发布到 Nacos 命名服务，并在关闭时注销。对外端口默认取 HTTP 实际
// 监听端口；注册发生在监听就绪之后，Reload 不会重复注册，注册失败会中止
// 启动而不是带着不可发现的状态继续跑。
//
// cfg.Enabled 或 cfg.Register.Enabled 为 false 时该选项是 no-op；启用注册但
// 没填 service_name 时在 New 阶段报错。
func WithNacosRegistration(cfg nacos.Config) Option {
	return func(o *options) error {
		if cfg.Register.Enabled && strings.TrimSpace(cfg.Register.ServiceName) == "" {
			return fmt.Errorf("gofy: nacos service_register.service_name is required")
		}
		c := cfg
		o.nacosRegistration = &c
		return nil
	}
}

// WithModuleFactories registers module factories called during startup. They run
// again on startup and on every Reload with the current Runtime, and the returned
// modules then Register (bind routes) and Start (open IO), so a hot reload gets the
// new container and connections. Calls accumulate and a nil factory fails New.
// WithModuleFactories 注册启动期调用的模块工厂。每次启动和 Reload 都会用
// 当代 Runtime 重新调用，工厂返回的模块随后执行 Register（绑定路由）和
// Start（打开 IO），因此热更新能拿到新一代容器与连接。可多次调用累积，
// 任一工厂为 nil 都在 New 阶段报错。
func WithModuleFactories(factories ...support.ModuleFactory) Option {
	return func(o *options) error {
		for _, f := range factories {
			if f == nil {
				return fmt.Errorf("gofy: module factory must not be nil")
			}
		}
		o.moduleFactories = append(o.moduleFactories, factories...)
		return nil
	}
}

// WithModules injects module instances directly. Injected modules cannot reach the
// Runtime and are not rebuilt by Reload; use WithModuleFactories for modules that
// need the Runtime or must be replaced on hot reload. Calls accumulate and a nil
// module fails New.
// WithModules 直接注入模块实例。注入的实例拿不到 Runtime，Reload 时也不会
// 重建；需要 Runtime 或需要随热更新换代的模块请用 WithModuleFactories。
// 可多次调用累积，任一模块为 nil 都在 New 阶段报错。
func WithModules(modules ...support.Module) Option {
	return func(o *options) error {
		for _, m := range modules {
			if m == nil {
				return fmt.Errorf("gofy: module must not be nil")
			}
		}
		o.modules = append(o.modules, modules...)
		return nil
	}
}

// WithScripts registers maintenance scripts, run by name through the script
// subcommand. Registration validates name format, reserved words, duplicates and a
// non-nil Run, so an invalid set fails New. Calls accumulate.
// WithScripts 注册维护脚本，通过 script 子命令按名字执行。注册时统一校验
// 名字格式、保留字、重名和 Run 非空，非法集合在 New 阶段报错。可多次调用累积。
func WithScripts(scripts ...script.Script) Option {
	return func(o *options) error {
		if err := script.Validate(scripts); err != nil {
			return err
		}
		o.scripts = append(o.scripts, scripts...)
		return nil
	}
}

// WithCommands registers custom CLI subcommands. Registration validates name
// format, conflicts with the built-in commands
// (start/stop/script/worker/version/help/completion), duplicates and a non-nil
// Run, so an invalid set fails New. Calls accumulate.
// WithCommands 注册自定义 CLI 子命令。注册时统一校验名字格式、与内建命令
// （start/stop/script/worker/version/help/completion）的冲突、重名和 Run 非空，
// 非法集合在 New 阶段报错。可多次调用累积。
func WithCommands(cmds ...command.Command) Option {
	return func(o *options) error {
		if err := command.Validate(cmds); err != nil {
			return err
		}
		o.commands = append(o.commands, cmds...)
		return nil
	}
}

// WithWorkerGroups registers long-running worker groups, run through the worker
// subcommand. A group is the deployment unit: its workers live and die in one
// process. Validation (group name, worker names, duplicates) happens here, so an
// invalid group fails New. Calls accumulate; group names must stay unique.
// WithWorkerGroups 注册常驻任务分组，通过 worker 子命令运行。分组是部署单位：
// 组内 worker 在同一进程里同生共死。分组名、worker 名与重名都在这里校验，非法
// 分组会让 New 失败。可多次调用累积，分组名必须唯一。
func WithWorkerGroups(groups ...*worker.Group) Option {
	return func(o *options) error {
		seen := make(map[string]struct{}, len(o.workerGroups)+len(groups))
		for _, g := range o.workerGroups {
			seen[g.Name()] = struct{}{}
		}
		for _, g := range groups {
			if g == nil {
				return fmt.Errorf("gofy: worker group must not be nil")
			}
			if _, dup := seen[g.Name()]; dup {
				return fmt.Errorf("gofy: duplicate worker group %q", g.Name())
			}
			seen[g.Name()] = struct{}{}
			o.workerGroups = append(o.workerGroups, g)
		}
		return nil
	}
}

// WithWorkerModules registers the modules a worker process assembles before any
// group starts. It is the worker counterpart of WithModuleFactories: the same
// factory type, built from the same Runtime, but with no Router and no reload —
// a worker process assembles one generation for its whole life.
//
// Register is never called: a worker has no HTTP stack. Start runs after the
// Runtime exists and before the first group starts, so business code that
// resolves a handle on its first tick finds a published generation instead of
// failing per tick. Stop runs from the App's cleanup, after every group drained
// and before the framework closes the database it handed to the module.
//
// A factory that returns an error or a nil module aborts startup: a worker whose
// assembly failed must not pretend to accept work. That is the difference from
// an optional config section — a missing section is the module's business, a
// broken assembly is the process's.
// WithWorkerModules 注册 worker 进程在任一分组启动之前装配的模块。它是
// WithModuleFactories 的 worker 对应物：同样的工厂类型、同样从 Runtime 构造，但
// 没有 Router、没有热更新——worker 进程整个生命周期只装配一代。
//
// 它从不调用 Register：worker 没有 HTTP 栈。Start 在 Runtime 就绪之后、第一个分组
// 启动之前执行，因此首个 tick 就要取句柄的业务代码看到的是已发布的代际，而不是每个
// tick 报一次错。Stop 由 App 的 cleanup 执行，位置在所有分组 drain 之后、框架关闭
// 交给模块的数据库之前。
//
// 工厂返回错误或 nil 模块会中止启动：装配失败的 worker 不能继续装作能接活。这与
// "可选配置段"不同——缺一段是模块自己的判断，装配坏掉是进程的判断。
func WithWorkerModules(factories ...ModuleFactory) Option {
	return func(o *options) error {
		for _, f := range factories {
			if f == nil {
				return fmt.Errorf("gofy: worker module factory must not be nil")
			}
		}
		o.workerModules = append(o.workerModules, factories...)
		return nil
	}
}

// WithWorkerPolicy sets how a failed worker instance is handled (restart with
// backoff by default). Use ExitOnError for failures a restart cannot fix.
// WithWorkerPolicy 设置 worker 实例失败后的处理方式（默认按退避重启）；
// 重启修不好的失败用 ExitOnError。
func WithWorkerPolicy(policy worker.Policy) Option {
	return func(o *options) error {
		o.workerPolicy = policy
		return nil
	}
}

// WithWorkerConcurrency overrides the instance count per worker. Keys are
// "group/worker" and must address a worker some registered group actually owns:
// the check runs at the end of New, so a stale key fails startup instead of
// leaving that worker quietly running one instance. Values below one fail New
// as well. Calls merge.
// WithWorkerConcurrency 按 worker 覆盖实例数，键为 "group/worker"，必须对应某个已
// 注册分组里真实存在的 worker：校验在 New 结束时进行，因此过期的键会让启动失败，
// 而不是让那个 worker 静默只跑一个实例；与 WithWorkerGroups 的调用先后无关。小于 1
// 的值同样让 New 失败。可多次调用合并。
func WithWorkerConcurrency(concurrency map[string]int) Option {
	return func(o *options) error {
		for key, n := range concurrency {
			if strings.Count(key, "/") != 1 || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
				return fmt.Errorf("gofy: worker concurrency key %q must be group/worker", key)
			}
			if n < 1 {
				return fmt.Errorf("gofy: worker concurrency for %q must be at least 1", key)
			}
		}
		if o.workerConcurrency == nil {
			o.workerConcurrency = make(map[string]int, len(concurrency))
		}
		for key, n := range concurrency {
			o.workerConcurrency[key] = n
		}
		return nil
	}
}

// WithWorkerDrainTimeout sets how long in-flight work may still run after a
// worker process is asked to stop (20s by default). Keep it below the
// container's terminationGracePeriodSeconds, otherwise the scheduler kills the
// process mid-drain.
// WithWorkerDrainTimeout 设置 worker 进程收到停止信号后，在途任务还能运行多久
// （默认 20s）。它必须小于容器的 terminationGracePeriodSeconds，否则调度器会在
// drain 完成前强杀进程。
func WithWorkerDrainTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return fmt.Errorf("gofy: worker drain timeout must be positive")
		}
		o.workerDrain = timeout
		return nil
	}
}

// WithWorkerHealth exposes /healthz on addr inside a worker process: 200 once
// the group has started, 503 from the moment shutdown begins. It is off by
// default, because only a Deployment that defines a readiness probe needs it,
// and picking a port per group is an operational decision rather than a
// framework one.
// WithWorkerHealth 让 worker 进程在 addr 上暴露 /healthz：分组启动后返回 200，
// 进入停机后返回 503。它默认关闭：只有配置了就绪探针的 Deployment 才需要它，而给
// 每个分组挑一个端口属于运维决策，不该由框架替用户决定。
func WithWorkerHealth(addr string) Option {
	return func(o *options) error {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			o.workerHealth = ""
			return nil
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("gofy: worker health address %q must be host:port: %w", addr, err)
		}
		o.workerHealth = addr
		return nil
	}
}

// WithShutdownTimeout sets the total shutdown budget shared by the HTTP graceful
// shutdown and every module Stop (10s by default). It must be positive, otherwise
// New fails.
// WithShutdownTimeout 设置关闭总预算，HTTP 优雅关闭与各模块 Stop 共享这一
// 时长（默认 10s）。必须为正数，否则在 New 阶段报错。
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return fmt.Errorf("gofy: shutdown timeout must be positive")
		}
		o.shutdownTimeout = timeout
		return nil
	}
}

// WithResponder replaces the default response renderer. Once set, the framework and
// the middleware render both errors and success envelopes with it; a nil r fails
// New. When WithAlwaysOK is enabled as well, this custom responder must implement
// the AlwaysOK semantics itself.
// WithResponder 替换默认的响应渲染器。传入后框架和中间件都用它渲染错误与
// 成功包络；r 为 nil 时报错。若同时启用 WithAlwaysOK，需要由这个自定义
// 响应器自己落实 AlwaysOK 语义。
func WithResponder(r support.Responder) Option {
	return func(o *options) error {
		if r == nil {
			return fmt.Errorf("gofy: responder must not be nil")
		}
		o.responder = r
		o.responderSet = true
		return nil
	}
}

// WithRecovery toggles the panic recovery middleware, on by default. When enabled a
// panic becomes a 500 envelope and the stack only goes to the log, never to the
// client; http.ErrAbortHandler is still re-raised as is.
// WithRecovery 启用或关闭 panic 恢复中间件，默认开启。开启后 panic 会被转成
// 500 标准包络，堆栈只写日志不外泄；http.ErrAbortHandler 仍按原样向上抛出。
func WithRecovery(enabled bool) Option {
	return func(o *options) error { o.recovery = enabled; return nil }
}

// WithAccessLog toggles the access log, on by default. The access log is installed
// once in the global chain, so contract routes must not declare the access-log
// alias again or the same request would be logged twice.
// WithAccessLog 启用或关闭访问日志，默认开启。访问日志在全局链中安装一次，
// 契约路由不应再声明 access-log 别名，否则同一请求会被记录两次。
func WithAccessLog(enabled bool) Option {
	return func(o *options) error { o.accessLog = enabled; return nil }
}

// WithAccessLogOptions enables the access log with the given config and overrides
// WithAccessLog(false).
// The config is taken literally and is not merged with DefaultAccessLogOptions: to
// change only SkipPaths you must also set the switches you need, such as
// LogRequestID / LogUserAgent / LogClientIP / LogQuery, to true explicitly; nil
// slices are still defaulted inside AccessLog (SkipPaths, SensitiveQuery), as are
// the numeric defaults (SlowThreshold, MaxPathLength).
// WithAccessLogOptions 启用访问日志并应用给定配置，会覆盖 WithAccessLog(false)。
//
// 传入的配置按字面生效，不会与 DefaultAccessLogOptions 合并：只想改 SkipPaths
// 时，需要同时把 LogRequestID / LogUserAgent / LogClientIP / LogQuery 等你需要
// 的开关显式设为 true；切片为 nil 时仍由 AccessLog 内部补默认值（SkipPaths、
// SensitiveQuery）或默认数值（SlowThreshold、MaxPathLength）。
func WithAccessLogOptions(opts middleware.AccessLogOptions) Option {
	return func(o *options) error {
		o.accessLog = true
		o.accessLogOptions = &opts
		return nil
	}
}

// WithHeaderPropagation copies whitelisted inbound headers into the request context
// so interface-service clients can forward caller identity and the trace id
// downstream. Identity headers are only trusted from proxies registered through
// WithTrustedProxies; Authorization and Cookie are never forwarded.
// WithHeaderPropagation 把入口白名单请求头写入 request context，供
// interface-service 客户端把调用方身份和 trace id 转发到下游服务。身份头
// 只采信通过 WithTrustedProxies 登记的代理；Authorization 和 Cookie 永不透传。
func WithHeaderPropagation(enabled bool) Option {
	return func(o *options) error { o.headerPropagation = enabled; return nil }
}

// WithCORS enables CORS with the given policy. The config is validated first, so an
// invalid origin rule fails New instead of surfacing on the first request.
// WithCORS 按给定策略启用 CORS。配置先经 Validate 校验；非法来源规则在
// New 阶段报错，而不是等到请求进来才暴露。
func WithCORS(cfg support.CORSConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.cors = &c
		return nil
	}
}

// WithHealthEndpoints registers /healthz and /readyz, off by default. Health checks
// hang off the App directly, so JWT and rate limiting never apply to them and they
// never enter module routes.
// WithHealthEndpoints 注册 /healthz 与 /readyz，默认关闭。健康检查直接挂在
// App 上，不受 JWT 与限流影响，也不会进入模块路由。
func WithHealthEndpoints(enabled bool) Option {
	return func(o *options) error { o.health = enabled; return nil }
}

// WithJWTAuth requires a valid bearer token on module routes. The config is
// validated first, so a missing Parser or a header/prefix containing newlines fails
// New. Health checks, static handlers registered outside modules and unmatched paths
// are unaffected.
// WithJWTAuth 要求模块路由携带合法的 bearer token。配置先经 Validate 校验，
// Parser 缺失或头/前缀含换行时在 New 阶段报错。健康检查、模块之外注册的
// 静态处理器以及未匹配路径都不受影响。
func WithJWTAuth(cfg support.JWTAuthConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.jwt = &c
		return nil
	}
}

// WithRateLimit rate-limits module routes per client, method and route. A nil Store
// or a non-positive Limit or Window fails New; Store errors fail closed (503) by
// default unless FailOpen is configured.
// WithRateLimit 按客户端、方法和路由对模块路由限流。Store 为 nil、Limit 或
// Window 非正数时在 New 阶段报错；Store 出错默认失败关闭（503），除非配置
// FailOpen。
func WithRateLimit(cfg support.RateLimitConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.rateLimit = &c
		return nil
	}
}

// WithSecurityHeaders adds hardening headers such as nosniff, frame options and
// referrer policy to every response. Zero fields take the
// support.DefaultSecurityHeadersConfig defaults and HSTS, CSP and the like are only
// emitted when applicable; a config containing newlines fails New.
// WithSecurityHeaders 给每个响应加上 nosniff、frame options、referrer policy
// 等加固头。零值字段使用 support.DefaultSecurityHeadersConfig 的默认值，
// HSTS 与 CSP 等按条件输出；配置含换行时在 New 阶段报错。
func WithSecurityHeaders(cfg support.SecurityHeadersConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.securityHeaders = &c
		return nil
	}
}

// WithMaxBodyBytes limits the request body size (4 MiB by default). Requests over
// the limit are mapped to 413 by the binding layer; a non-positive value fails New.
// WithMaxBodyBytes 限制请求体大小（默认 4 MiB）。超过上限的请求由绑定层
// 映射为 413；非正数在 New 阶段报错。
func WithMaxBodyBytes(n int64) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("gofy: max body bytes must be positive")
		}
		o.maxBody = n
		return nil
	}
}

// WithTrustedProxies sets gin's trusted proxies (IP or CIDR); no proxy is trusted by
// default. WithHeaderPropagation uses the same list to decide whether forwarded
// identity headers are trustworthy, and an entry that is neither a valid IP nor a
// valid CIDR fails New. Calls accumulate.
// WithTrustedProxies 设置 gin 的可信代理列表（IP 或 CIDR），默认不信任任何
// 代理。WithHeaderPropagation 使用同一个列表判断转发来的身份头是否可信；
// 任一条目既不是合法 IP 也不是合法 CIDR 时在 New 阶段报错。可多次调用累积。
func WithTrustedProxies(proxies ...string) Option {
	return func(o *options) error {
		for _, p := range proxies {
			if net.ParseIP(p) == nil {
				if _, _, err := net.ParseCIDR(p); err != nil {
					return fmt.Errorf("gofy: invalid trusted proxy %q", p)
				}
			}
		}
		o.trusted = append(o.trusted, proxies...)
		return nil
	}
}

// WithAlwaysOK makes every response use HTTP 200 while the business code stays in
// the envelope; NoContent responses keep 204. When the Responder was not replaced
// explicitly, the framework switches to a default responder that supports AlwaysOK.
// WithAlwaysOK 让所有响应都使用 HTTP 200，业务码仍保留在响应包络里；
// NoContent 响应保持 204。未显式替换 Responder 时，框架会自动切换成支持
// AlwaysOK 的默认响应器。
func WithAlwaysOK(enabled bool) Option {
	return func(o *options) error { o.alwaysOK = enabled; return nil }
}

// WithServerConfigurator runs before the http.Server starts, to set Server fields
// the framework does not cover. An error from the callback fails startup and a nil
// fn fails New.
// WithServerConfigurator 在 http.Server 启动前调用，用于设置框架未覆盖的
// Server 字段。回调返回错误会让启动失败；fn 为 nil 时报错。
func WithServerConfigurator(fn func(*http.Server) error) Option {
	return func(o *options) error {
		if fn == nil {
			return fmt.Errorf("gofy: server configurator must not be nil")
		}
		o.serverConf = fn
		return nil
	}
}

// WithVersionInfo sets explicit version info, overriding the defaults: the
// version falls back to the framework's Version constant, and commit/build time
// are read from the build info when available. Without it the App reports
// version.Read(Version).
// WithVersionInfo 设置显式的版本信息，覆盖默认值：版本默认取框架的 Version 常量，
// commit/构建时间在可用时从构建信息读取；不设置时 App 报告 version.Read(Version)
// 的结果。
func WithVersionInfo(info core.VersionInfo) Option {
	return func(o *options) error {
		o.version = info
		o.hasVer = true
		return nil
	}
}

// WithTelemetry enables OpenTelemetry tracing with the given pipeline. When
// cfg.Enabled is true the App installs the Gin server span middleware, instruments
// the MySQL pool it owns, exports spans over OTLP and shuts the exporter down during
// cleanup. Service name, version and environment default to the app name, build
// version and environment marker.
// Tracing is fixed at startup: changing the config needs a process restart and
// Reload does not rebuild it.
// WithTelemetry 按给定管道启用 OpenTelemetry tracing。cfg.Enabled 为 true 时，
// App 会安装 Gin server span 中间件、为自己持有的 MySQL 连接池埋点、通过
// OTLP 导出 span，并在清理阶段关闭 exporter。服务名、版本和环境默认取应用名、
// 构建版本和环境标记。
//
// tracing 在启动时固定：修改配置需要重启进程，Reload 不会重建它。
func WithTelemetry(cfg otel.Config) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.telemetry = &c
		return nil
	}
}

// WithDiagnostics enables the operational endpoints: Prometheus metrics at
// /metrics and net/http/pprof under /debug/pprof/. They are served on a listener
// of their own, never on the application port, so business routing, middleware
// and authentication cannot accidentally expose a heap dump.
//
// The listener is off by default and binds to 127.0.0.1:6060 when enabled. A
// container deployment that scrapes from another Pod sets addr to 0.0.0.0:6060
// and relies on the network policy for isolation. Both endpoint groups are on by
// default; DisableMetrics or DisablePprof removes one of them.
//
// Enabling metrics also installs the Gin middleware that records request count,
// duration and in-flight requests, plus the Go runtime gauges (goroutines, heap,
// GC). The route label is the registered pattern, so a path parameter never
// creates a new time series. /healthz and /readyz are excluded: a probe every few
// seconds would otherwise dominate every rate and latency percentile on the
// dashboard.
//
// The endpoints are fixed at startup; Reload does not rebuild them.
// WithDiagnostics 打开运维端点：/metrics 提供 Prometheus 指标，/debug/pprof/ 提供
// net/http/pprof。它们跑在独立监听上，绝不使用应用端口，业务路由、中间件与鉴权因此
// 不可能意外暴露堆转储。
//
// 监听默认关闭，开启后默认绑定 127.0.0.1:6060。从其他 Pod 抓取的容器部署应把 addr
// 设为 0.0.0.0:6060，由网络策略负责隔离。两组端点默认都开；DisableMetrics 或
// DisablePprof 用来去掉其中之一。
//
// 开启指标还会安装记录请求数、耗时与在途请求的 Gin 中间件，以及 Go 运行时 gauge
// （协程、堆、GC）。route 标签取注册的路由模式，路径参数因此不会造出新时间线。
// /healthz 与 /readyz 被排除：否则每几秒一次的探针会主导看板上的所有速率与延迟分位。
//
// 端点配置在启动时固定，Reload 不会重建。
func WithDiagnostics(cfg diagnostics.Config) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.diagnostics = &c
		return nil
	}
}
