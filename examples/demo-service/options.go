package main

import (
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	appio "github.com/yourorg/demo-service/app/io"
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

// appOptions 汇总 demo-service 的启动选项：框架配置、io/order 模块、脚本和
// 版本信息。热更新会用同一份 reloader 重建模块集合。
func appOptions(cfg *config.Config, reload *reloader, nacosCfg nacos.Config) []gofy.Option {
	return []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(systemenv.GetEnv()),
		gofy.WithLogConfig(cfg.Log),
		gofy.WithTelemetry(cfg.OTel),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithPIDFile(cfg.PIDFile),
		gofy.WithDatabase(cfg.Database(config.DefaultDatabase)),
		gofy.WithNacosRegistration(nacosCfg),
		gofy.WithHealthEndpoints(true),
		// 把入口请求的白名单 Header 注入上下文，供 demoserviceclient 转发到下游；
		// 身份头只有在 WithTrustedProxies 命中的代理转发时才采信。
		gofy.WithHeaderPropagation(true),
		// 请求进入时固定当前一代的 IO 快照，Repository 与 Service 用
		// appio.FromContext(ctx) 取连接：一次请求不会横跨 Reload 换资源。
		gofy.WithMiddleware(appio.Middleware()),
		// 应用层中间件（auth/audit/order-audit）在模块绑定路由之前注册。
		gofy.WithMiddlewareRegistrar(middlewareRegistrar(cfg)),
		gofy.WithModuleFactories(reload.factories(cfg)...),
		gofy.WithScripts(appscript.Register(cfg)...),
		gofy.WithVersionInfo(gofy.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
		}),
		gofy.WithShutdownTimeout(10 * time.Second),
	}
}
