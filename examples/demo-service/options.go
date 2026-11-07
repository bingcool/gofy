package main

import (
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	appscript "github.com/yourorg/demo-service/script"
)

// appOptions 汇总 demo-service 的启动选项：框架配置、io/order 模块、脚本和
// 版本信息。热更新会用同一份 reloader 重建模块集合。
func appOptions(cfg *config.Config, reload *reloader, nacosCfg nacos.Config) []gofy.Option {
	return []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(systemenv.GetEnv()),
		gofy.WithLogConfig(cfg.Log),
		gofy.WithTelemetry(cfg.OTel),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithPIDFile(cfg.PIDFile),
		gofy.WithDatabase(cfg.Database(config.DefaultDatabase)),
		gofy.WithNacosRegistration(nacosCfg),
		gofy.WithHealthEndpoints(true),
		// 把入口请求的白名单 Header 注入上下文，供 demoserviceclient 转发到下游；
		// 身份头只有在 WithTrustedProxies 命中的代理转发时才采信。
		gofy.WithHeaderPropagation(true),
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
