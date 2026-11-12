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
	"github.com/bingcool/gofy/support/middleware"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Option 在 gofy.New 期间定制 App。选项按传入顺序依次应用，后应用的选项会
// 覆盖先前的同名配置；任一选项返回错误都会让 New 失败，进程不进入启动流程。
type Option func(*options) error

// MiddlewareRegistration 是启动装配阶段传给 MiddlewareRegistrar 的上下文。
// Registry 已包含框架内建中间件，registrar 只需补充应用层别名。
type MiddlewareRegistration struct {
	// Runtime 是本次装配的运行时，可用于取 Logger 等依赖。
	Runtime *core.Runtime
	// Registry 是本次装配唯一的中间件注册表，随 Router 交给 adapter 解析契约别名。
	Registry *middleware.Registry
	// Responder 是 App 的响应器，应用层中间件应复用它渲染错误。
	Responder support.Responder
}

// MiddlewareRegistrar 在模块绑定路由之前，把应用层中间件注册进 App 的注册表。
// 启动和每次 Reload 都会用新的 Runtime、Responder 和空注册表重新执行，因此
// 热更新不会复用旧实例，配置变化也能生效。
type MiddlewareRegistrar func(MiddlewareRegistration) error

// namePattern 约束应用名：小写字母开头，之后只允许小写字母、数字和连字符，
// 总长不超过 63。名字会进入日志、pid 文件、Nacos 注册与版本信息。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// options 是 App 的可变配置集合：只在 gofy.New 期间由 Option 写入，之后由
// App 只读使用；Reload 也会用它重建新一代运行时。
type options struct {
	name    string           // 应用名：日志、pid 文件、Nacos 注册与版本信息共用
	env     string           // 环境标记，暴露为 Runtime.Env，并作为 tracing 的默认环境
	version core.VersionInfo // 显式版本信息（WithVersionInfo）
	hasVer  bool             // 是否显式设置过版本信息

	logger    *zap.Logger   // 外部注入的 logger；非 nil 时 App 不再自建，也不负责关闭
	logConfig logger.Config // 未注入 logger 时 App 用这份配置自己构建

	http      HTTPConfig // HTTP 监听地址、超时与 TLS 等配置
	apiPrefix string     // 追加在模块路由前的共享前缀；契约自带完整前缀时应留空

	middleware           []support.Middleware  // 框架中间件之后安装的业务中间件
	middlewareRegistrars []MiddlewareRegistrar // 装配期注册应用层中间件别名的函数

	pidFile    string        // pid 文件路径；空表示不写 pid 文件
	pidPerm    os.FileMode   // pid 文件权限
	pidRefresh time.Duration // pid 文件刷新间隔；0 表示不刷新
	workDir    string        // 工作目录，也是相对 pid 路径的基准

	dbConfig *gofydb.Config // App 自建、持有并负责关闭的数据库配置
	db       *gorm.DB       // 外部注入的数据库；App 不 Ping 也不关闭

	// nacosRegistration 在 Register.Enabled 为 true 时，把本实例注册到 Nacos
	// 命名服务：HTTP 监听就绪后注册，退出时注销。
	nacosRegistration *nacos.Config

	moduleFactories []support.ModuleFactory // 启动期构建模块的工厂（能拿到 Runtime）
	modules         []support.Module        // 直接注入的模块实例（拿不到 Runtime）

	scripts  []script.Script   // 维护脚本
	commands []command.Command // 自定义 CLI 命令

	shutdownTimeout time.Duration // 关闭预算：HTTP 优雅关闭与模块 Stop 共享的总时长

	// 下面这组开关与配置对象对应启动时安装的内建中间件和安全选项。
	responder         support.Responder            // 统一响应渲染器
	responderSet      bool                         // 是否被 WithResponder 显式替换过
	recovery          bool                         // 是否安装 Recovery
	accessLog         bool                         // 是否安装访问日志
	accessLogOptions  *middleware.AccessLogOptions // 自定义访问日志配置；nil 用默认
	headerPropagation bool                         // 是否安装 HeaderPropagation
	cors              *support.CORSConfig          // CORS 策略；nil 表示不启用
	health            bool                         // 是否注册 /healthz、/readyz
	maxBody           int64                        // 请求体上限（字节）
	trusted           []string                     // 可信代理列表（IP 或 CIDR）
	alwaysOK          bool                         // 是否把所有响应统一成 HTTP 200
	serverConf        func(*http.Server) error     // 启动前定制 http.Server 的回调

	jwt             *support.JWTAuthConfig         // 模块路由的 JWT 校验配置
	rateLimit       *support.RateLimitConfig       // 模块路由的限流配置
	securityHeaders *support.SecurityHeadersConfig // 安全响应头配置

	telemetry *otel.Config // OpenTelemetry 配置
}

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
		responder:       support.NewResponder(support.ResponderConfig{}),
		recovery:        true,
		accessLog:       true,
		maxBody:         4 << 20,
	}
}

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

// WithPIDFile 启用 pid 文件并设置路径，默认不写 pid 文件；传空串即关闭。
// 相对路径以 WithWorkDir 为基准解析，所在目录不存在时会以 0755 自动创建。
func WithPIDFile(path string) Option {
	return func(o *options) error {
		o.pidFile = strings.TrimSpace(path)
		return nil
	}
}

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

// WithWorkDir 设置工作目录，同时作为 pid 文件相对路径的基准目录。目录必须
// 在启动时已存在，框架不会自动创建。
func WithWorkDir(dir string) Option {
	return func(o *options) error {
		o.workDir = strings.TrimSpace(dir)
		return nil
	}
}

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

// WithCommands 注册自定义 CLI 子命令。注册时统一校验名字格式、与内建命令
// （start/stop/script/version/help/completion）的冲突、重名和 Run 非空，
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

// WithRecovery 启用或关闭 panic 恢复中间件，默认开启。开启后 panic 会被转成
// 500 标准包络，堆栈只写日志不外泄；http.ErrAbortHandler 仍按原样向上抛出。
func WithRecovery(enabled bool) Option {
	return func(o *options) error { o.recovery = enabled; return nil }
}

// WithAccessLog 启用或关闭访问日志，默认开启。访问日志在全局链中安装一次，
// 契约路由不应再声明 access-log 别名，否则同一请求会被记录两次。
func WithAccessLog(enabled bool) Option {
	return func(o *options) error { o.accessLog = enabled; return nil }
}

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

// WithHeaderPropagation 把入口白名单请求头写入 request context，供
// interface-service 客户端把调用方身份和 trace id 转发到下游服务。身份头
// 只采信通过 WithTrustedProxies 登记的代理；Authorization 和 Cookie 永不透传。
func WithHeaderPropagation(enabled bool) Option {
	return func(o *options) error { o.headerPropagation = enabled; return nil }
}

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

// WithHealthEndpoints 注册 /healthz 与 /readyz，默认关闭。健康检查直接挂在
// App 上，不受 JWT 与限流影响，也不会进入模块路由。
func WithHealthEndpoints(enabled bool) Option {
	return func(o *options) error { o.health = enabled; return nil }
}

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

// WithAlwaysOK 让所有响应都使用 HTTP 200，业务码仍保留在响应包络里；
// NoContent 响应保持 204。未显式替换 Responder 时，框架会自动切换成支持
// AlwaysOK 的默认响应器。
func WithAlwaysOK(enabled bool) Option {
	return func(o *options) error { o.alwaysOK = enabled; return nil }
}

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

// WithVersionInfo 设置显式的版本信息，覆盖从构建信息（ldflags）自动读取的
// 版本；不设置时 App 使用 version.Read() 的结果。
func WithVersionInfo(info core.VersionInfo) Option {
	return func(o *options) error {
		o.version = info
		o.hasVer = true
		return nil
	}
}

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
