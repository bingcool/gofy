package main

import (
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/app/appio"
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
		gofy.WithVersionInfo(gofy.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
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
	}
	if !plan.BuildServices {
		return opts
	}
	return append(opts,
		gofy.WithTelemetry(cfg.OTel),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithDatabase(cfg.Database(config.DefaultDatabase)),
		gofy.WithNacosRegistration(nacosCfg),
		gofy.WithHealthEndpoints(true),
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
		gofy.WithModuleFactories(reload.factories(cfg)...),
		gofy.WithScripts(appscript.Register(cfg)...),
	)
}
