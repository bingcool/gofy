package main

import (
	"context"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	appworker "github.com/yourorg/demo-service/worker"
)

var (
	version   = "1.0.0"
	commit    = "none"
	buildTime = "unknown"
)

// enableNacosSyncConfig 控制启动时是否从 Nacos 同步配置并监听变更。
// 简单项目只维护本地 config.yaml 时设为 false；不创建 nacos.yaml 且不设置
// NACOS_* 环境变量时，配置本身也是禁用的，效果相同。
// 该开关只影响配置同步，不影响 nacos.yaml 中显式开启的服务注册。
const enableNacosSyncConfig = false

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	nacosCfg, err := loadNacosConfig()
	if err != nil {
		fatalf("load nacos config: %v", err)
	}
	if strings.TrimSpace(os.Getenv("NACOS_FILE")) == "" {
		nacosCfg.File = configPath()
	}
	// stop/version/script must work without reaching Nacos.
	// stop/version/script 子命令不访问 Nacos 也必须可用，因此只在 start 时拉配置。
	pullConfig := enableNacosSyncConfig && nacosCfg.Enabled && isStartInvocation(os.Args[1:])
	if pullConfig {
		if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
			fatalf("sync nacos config: %v", err)
		}
	}

	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		fatalf("load config: %v", err)
	}
	// 进程显式设置了 GOFY_ENV 时以它为准；否则回落到 config.yaml 的 env，
	// 让 systemenv 与 Runtime.Env 读到的环境保持一致。
	envFromProcess := strings.TrimSpace(os.Getenv("GOFY_ENV")) != ""
	if !envFromProcess {
		if err := os.Setenv("GOFY_ENV", cfg.Env); err != nil {
			fatalf("set GOFY_ENV: %v", err)
		}
	}

	reload := &reloader{
		nacosCfg:       nacosCfg,
		watch:          pullConfig && nacosCfg.Watch,
		httpCfg:        cfg.HTTP,
		envFromProcess: envFromProcess,
	}

	// Every group is registered in one binary; `worker --group=<group>` picks
	// one at startup, so one image serves all worker deployments. Register takes
	// no config: a group's settings live in its own package.
	// 所有分组都注册进同一个二进制，`worker --group=<分组>` 在启动时选择其中一个，
	// 因此一个镜像可以服务所有 worker 部署。Register 不接收配置：分组的参数在自己
	// 的包里。
	groups, err := appworker.Register()
	if err != nil {
		fatalf("build worker groups: %v", err)
	}

	app, err := gofy.New(appOptions(cfg, reload, nacosCfg, groups)...)
	if err != nil {
		fatalf("init application: %v", err)
	}
	// Publish the app before Execute starts the watcher: a config change may
	// arrive as soon as the watcher polls, and the callback needs to find it.
	// 必须在 Execute 启动监听之前发布 app：监听首次轮询就可能带来配置变更，
	// 回调需要立刻能找到运行中的应用。
	reload.app.Store(app)

	if err := app.Execute(ctx); err != nil {
		app.Logger().Error("demo-service exit", zap.Error(err))
		os.Exit(gofy.ExitCode(err))
	}
}
