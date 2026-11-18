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
	"github.com/yourorg/demo-service/serviceclients"
	appworker "github.com/yourorg/demo-service/worker"
)

var (
	version   = "1.0.0"
	commit    = "none"
	buildTime = "unknown"
)

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
		// one on stale settings. The gate is the Nacos config itself: `nacos.enabled`
		// (or NACOS_ENABLED=0 to opt out) decides whether the two-file layout is
		// active, and `enable_nacos_config_change_restart` decides whether the
		// watcher keeps running. There is no second switch in code that could
		// silently disable the configured strategy.
		// 启动期拉取：HTTP 与 worker 进程都只读一次配置，新起的进程因此不会用陈旧的本地
		// config.yaml。restart 属于启动：它在停旧进程之前拉取，因此拉取失败时旧进程继续运行，
		// 而不是被一个用旧配置起的新进程替换掉。开关就是 Nacos 配置本身：`nacos.enabled`
		// （或用 NACOS_ENABLED=0 关闭）决定双文件布局是否生效，
		// `enable_nacos_config_change_restart` 决定监听是否持续。代码里没有第二道开关，
		// 不会静默绕过配置好的策略。
		if nacosCfg.Enabled && plan.Sync {
			if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
				fatalf("sync nacos config: %v", err)
			}
		}
	}
	// Only the HTTP process hosts the watcher: workers stay immutable at runtime
	// and pick up changes through a rolling restart by the deployment layer.
	// 只有 HTTP 进程挂监听：worker 运行期保持不变，配置变更由部署层滚动重启生效。
	watchEnabled := plan.Watch && nacosCfg.Enabled && nacosCfg.Watch

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

	// The same address does double duty: registration publishes it as instance
	// metadata, so a caller resolving this instance can use it instead of the pod
	// IP, and the client falls back to it when a discovered instance is
	// unreachable. It lives in config.yaml (pulled from the config center), which
	// is exactly what makes a local process see dev's address.
	// 同一个地址有两个用途：注册时作为实例 metadata 发布，解析到本实例的调用方可以用它
	// 替代 Pod IP；客户端在解析到的实例不可达时用它兜底。它放在 config.yaml（从配置
	// 中心拉取），本地进程因此拿到的正是 dev 的地址。
	if plan.LoadConfig && nacosCfg.Register.Enabled {
		nacosCfg.Register.SetInnerExternalBaseURI(cfg.Client.InnerExternalBaseURI)
	}

	// Downstream clients are built once per process, after the config is known
	// and before any module starts. They are deliberately outside the reload
	// path: the resolver's cache, cursor and singleflight table must survive a
	// config change, and the Nacos connection itself only changes on restart.
	// 下游客户端每个进程构造一次，位置在配置就绪之后、模块启动之前。它刻意不在 Reload
	// 路径上：resolver 的缓存、游标与单飞表必须跨配置变更存活，而 Nacos 连接本身只有
	// 重启才会变。
	var clients *serviceclients.Clients
	if plan.BuildServices && plan.LoadNacos {
		built, err := serviceclients.New(cfg.Client, nacosCfg)
		if err != nil {
			fatalf("init service clients: %v", err)
		}
		clients = built
		defer func() { _ = clients.Close() }()
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
		built, err := appworker.Register(appworker.ConnectionsFrom(cfg), clients)
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

	// The clients report a degraded call through the application's logger, so
	// they need a way to reach it. A getter, not a logger: App.Logger() answers
	// a no-op until Execute builds the real one, and this line runs before that.
	// The addressing line itself comes from the consumer (group3 logs the same
	// Source() string at startup), because a log emitted here would be dropped
	// by that no-op logger.
	// 客户端通过 application 的 logger 报告降级，因此需要一条能拿到它的路径。给的是
	// getter 而不是 logger：Execute 之前 App.Logger() 是 no-op，而这一行就发生在之前。
	// 寻址信息由使用方打印（group3 启动时输出同一个 Source() 字符串）——在这里打会
	// 被那个 no-op logger 丢掉。
	if clients != nil {
		clients.SetLogger(app.Logger)
	}

	if err := app.Execute(ctx); err != nil {
		app.Logger().Error("demo-service exit", zap.Error(err))
		os.Exit(gofy.ExitCode(err))
	}
}
