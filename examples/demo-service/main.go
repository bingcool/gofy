package main

import (
	"context"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/worker"
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

	// Classify the command line before opening any file. The order is the point:
	// `stop` must work while nacos.yaml is broken, and `version` must work while
	// config.yaml is broken — a command that needs neither must read neither.
	// 先给命令行分类，再打开任何文件。顺序本身就是重点：`stop` 必须在 nacos.yaml 损坏时
	// 可用，`version` 必须在 config.yaml 损坏时可用——两个都不需要的命令，一个都不读。
	plan := planCommand(os.Args[1:])

	var nacosCfg nacos.Config
	if plan.LoadNacos {
		loaded, err := loadNacosConfig()
		if err != nil {
			fatalf("load nacos config: %v", err)
		}
		nacosCfg = loaded
		if strings.TrimSpace(os.Getenv("NACOS_FILE")) == "" {
			nacosCfg.File = configPath()
		}
		// Startup pull: HTTP and worker processes both read the config once, so a
		// freshly launched process never runs on a stale local config.yaml. A
		// restart is a start: it pulls before the old process is stopped, so a
		// failed pull leaves the old process running instead of replacing it with
		// one on stale settings.
		// 启动期拉取：HTTP 与 worker 进程都只读一次配置，新起的进程因此不会用陈旧的本地
		// config.yaml。restart 属于启动：它在停旧进程之前拉取，因此拉取失败时旧进程继续运行，
		// 而不是被一个用旧配置起的新进程替换掉。
		if enableNacosSyncConfig && nacosCfg.Enabled && plan.Sync {
			if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
				fatalf("sync nacos config: %v", err)
			}
		}
	}
	// Only the HTTP process hosts the watcher: workers stay immutable at runtime
	// and pick up changes through a rolling restart by the deployment layer.
	// 只有 HTTP 进程挂监听：worker 运行期保持不变，配置变更由部署层滚动重启生效。
	watchEnabled := plan.Watch && enableNacosSyncConfig && nacosCfg.Enabled && nacosCfg.Watch

	cfg := &config.Config{}
	if plan.LoadConfig {
		loaded, err := config.Load(ctx, configPath())
		if err != nil {
			fatalf("load config: %v", err)
		}
		cfg = loaded
		// 进程显式设置了 GOFY_ENV 时以它为准；否则回落到 config.yaml 的 env，
		// 让 systemenv 与 Runtime.Env 读到的环境保持一致。
		envFromProcess := strings.TrimSpace(os.Getenv("GOFY_ENV")) != ""
		if !envFromProcess {
			if err := os.Setenv("GOFY_ENV", cfg.Env); err != nil {
				fatalf("set GOFY_ENV: %v", err)
			}
		}
	}

	reload := &reloader{
		nacosCfg:       nacosCfg,
		watch:          watchEnabled,
		httpCfg:        cfg.HTTP,
		envFromProcess: strings.TrimSpace(os.Getenv("GOFY_ENV")) != "",
	}

	// Every group is registered in one binary; `worker --group=<group>` picks
	// one at startup, so one image serves all worker deployments. A consumer's
	// topic and group are in its own file in the group, so a change there is a
	// rebuild — that is the trade for "one subscription, one file".
	// 所有分组都注册进同一个二进制，`worker --group=<分组>` 在启动时选择其中一个，
	// 因此一个镜像可以服务所有 worker 部署。消费者的 topic 与消费组在分组里它自己的
	// 文件中，改它们意味着重新编译——这是"一个订阅一个文件"换来的代价。
	var groups []*worker.Group
	if plan.RegisterGroups {
		built, err := appworker.Register(appworker.ConnectionsFrom(cfg))
		if err != nil {
			fatalf("build worker groups: %v", err)
		}
		groups = built
	}

	app, err := gofy.New(appOptions(cfg, plan, reload, nacosCfg, groups)...)
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
