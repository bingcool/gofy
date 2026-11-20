package main

import (
	"runtime"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/middleware"
	appscript "github.com/yourorg/demo-service/script"
)

// middlewareRegistrar 把 app/middleware 里的应用层别名注册进 gofy 启动时创建
// 的共享注册表。契约路由只写逻辑名，模块不再自己创建或注入注册表；热更新
// 会用新配置重新执行一次，jwt 变更无需重启进程。
func middlewareRegistrar(cfg *config.Config) gofy.MiddlewareRegistrar {
	return func(reg gofy.MiddlewareRegistration) error {
		return middleware.Register(reg.Registry, middleware.Deps{
			Logger:    reg.Runtime.Logger,
			JWT:       cfg.JWT,
			Responder: reg.Responder,
		})
	}
}

// appOptions 汇总 demo-service 的启动选项：框架配置、io/order 模块、脚本、
// 常驻任务分组和版本信息。热更新会用同一份 reloader 重建模块集合。
//
// 选项按命令的 plan 分层：控制命令（stop / worker stop）只拿到身份、pid 文件与版本，
// 不装配数据库、模块和中间件——它们只发信号，配置解析失败时也不该被数据库拖住。
// appOptions 按命令行分类分层装配，这是 P2-04 的落点。
func appOptions(cfg *config.Config, plan commandPlan, reload *reloader, nacosCfg nacos.Config, groups []*worker.Group) []gofy.Option {
	opts := []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(systemenv.GetEnv()),
		gofy.WithPIDFile(cfg.PIDFile),
		// WithVersionInfo replaces the framework default wholesale, so every
		// field it wants to report has to be filled here — an omitted GoVersion
		// shows up as an empty line in `version`, not as a framework fallback.
		// WithVersionInfo 是整体替换框架默认值，因此它想报告的每个字段都必须在这里填；
		// 漏掉 GoVersion 的表现是 `version` 输出一行空白，而不是回退到框架的值。
		gofy.WithVersionInfo(gofy.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
			GoVersion: runtime.Version(),
		}),
		gofy.WithShutdownTimeout(10 * time.Second),
	}
	if plan.BuildServices {
		// Control commands keep the default logger: they must not depend on log
		// directories being writable to send a signal.
		// 控制命令沿用默认 logger：发一个信号不应该依赖日志目录可写。
		opts = append(opts, gofy.WithLogConfig(cfg.Log))
	}
	if plan.RegisterGroups {
		// 常驻任务只通过 worker 子命令运行，HTTP 进程不会启动它们。这里注册
		// 全部分组，`worker --group=<分组>` 只运行其中一个。
		opts = append(opts, gofy.WithWorkerGroups(groups...))
		// worker 进程的 IO 代际：只装数据库。worker 复用 service 与 repository，
		// 两层都通过 appio.WithContext(ctx).NewDBIO 取句柄，因此它们需要的不是一条
		// Runtime.DB，而是一代已发布的快照。broker 连接不在这里装配——消费者按名字
		// 解析自己的实例（见 worker/connections.go），再开一套只会重复建连。
		//
		// The worker's IO generation carries the database only. The service and
		// repository layers resolve handles through appio, so what they need is a
		// published snapshot, not a Runtime.DB field. Broker connections are
		// deliberately absent: consumers resolve their own instances by name.
		dbOpts := cfg.Tenant.OpenOptions()
		if reload.policy != nil {
			dbOpts = append(dbOpts, appdb.WithSQLPolicy(reload.policy))
		}
		opts = append(opts, gofy.WithWorkerModules(
			appio.NewDBOnlyModuleFactory(cfg.DB, dbOpts...),
		))
	}
	// Health endpoints are a property of this deployment's configuration, not of
	// the process asking: `status` runs in another process and has to know
	// whether probing the target's /healthz is meaningful. Registering them has
	// no effect outside a server, so the control commands can carry the flag.
	// 健康端点是这套部署的配置属性，不是"发问的那个进程"的属性：`status` 跑在另一个
	// 进程里，它需要知道探测目标的 /healthz 是否有意义。注册路由在服务之外没有副作用，
	// 因此控制类命令也带着这个开关。
	opts = append(opts, gofy.WithHealthEndpoints(true))
	if !plan.BuildServices {
		return opts
	}
	// The database is the one section that changes an option's presence rather
	// than its value: WithDatabase validates the DSN, so passing a zero-value
	// config for an absent section would fail startup with "db: dsn is
	// required". A deployment without a db block simply has no
	// framework-managed pool; the framework accepts a nil Runtime.DB.
	// 数据库是唯一"要不要传"而非"传什么值"的段落：WithDatabase 会校验 DSN，为缺失
	// 的段传零值配置会以 "db: dsn is required" 启动失败。没有 db 段的部署就是没有
	// 框架托管的连接池，框架接受 nil 的 Runtime.DB。
	if dbCfg, ok := cfg.Database(config.DefaultDatabase); ok {
		opts = append(opts, gofy.WithDatabase(dbCfg))
	}
	return append(opts,
		gofy.WithTelemetry(cfg.OTel),
		// 运维端点跑独立端口：/metrics 给 Prometheus，/debug/pprof/ 给排障。
		// 默认关闭（配置里 enabled: false），需要时在 config.yaml 打开。
		// The operational endpoints live on their own port; off unless config.yaml
		// turns them on.
		gofy.WithDiagnostics(cfg.Diagnostics),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithNacosRegistration(nacosCfg),
		// 把入口请求的白名单 Header 注入上下文，供 demoserviceclient 转发到下游；
		// 身份头只有在 WithTrustedProxies 命中的代理转发时才采信。
		gofy.WithHeaderPropagation(true),
		// 请求的 IO 快照由框架按代绑定：appio.Module 实现了
		// support.RequestBinder，请求拿到哪一代的 lease，就固定哪一代的快照。
		// 这里的中间件只是兜底：ctx 已经固定过时它不覆盖，因此不会把旧 handler
		// 配上新快照。
		gofy.WithMiddleware(appio.Middleware()),
		// 应用层中间件（auth/audit/order-audit）在模块绑定路由之前注册。
		gofy.WithMiddlewareRegistrar(middlewareRegistrar(cfg)),
		gofy.WithSQLPolicy(reload.policy),
		gofy.WithModuleFactories(reload.factories(cfg)...),
		gofy.WithScripts(appscript.Register(cfg)...),
	)
}
