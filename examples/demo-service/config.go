package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/bingcool/gofy/support/nacos"
)

// configPath 返回配置文件路径：ORDER_CONFIG 优先，否则使用工作目录下的 config.yaml。
func configPath() string {
	if p := os.Getenv("ORDER_CONFIG"); p != "" {
		return p
	}
	return "config.yaml"
}

// loadNacosConfig prefers the swoolefy-style nacos.yaml + application.yaml
// pair and falls back to NACOS_* environment variables when nacos.yaml is
// absent.
// loadNacosConfig 优先读 swoolefy 风格的 nacos.yaml + application.yaml 组合，
// nacos.yaml 不存在时回退到 NACOS_* 环境变量，两者都没有则表示未启用同步。
func loadNacosConfig() (nacos.Config, error) {
	cfg, err := nacos.Load("nacos.yaml", "application.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return nacos.FromEnv(), nil
	}
	return cfg, err
}

// commandPlan is what one command line needs before the App exists. The ordering
// matters more than the fields: reading nacos.yaml or config.yaml can fail, and a
// failure there must not block a command that never needed the file.
// commandPlan 是一条命令行在 App 存在之前需要什么。比起字段，顺序更要紧：读 nacos.yaml
// 或 config.yaml 都可能失败，而这个失败不应该挡住一个根本不需要该文件的命令。
type commandPlan struct {
	// Kind names the command, for tests and for the startup log.
	// Kind 是命令名称，供测试与启动日志使用。
	Kind string
	// LoadNacos reads nacos.yaml / application.yaml. Only commands that may
	// pull or register need it; a corrupted Nacos file must not stop a running
	// service.
	// LoadNacos 表示读取 nacos.yaml / application.yaml。只有可能拉取或注册的命令需要它；
	// 损坏的 Nacos 文件不该阻止停止一个正在运行的服务。
	LoadNacos bool
	// Sync pulls the remote config once before anything else happens. It is true
	// for every command that starts a process, including restart: a restart that
	// skipped the pull would start the new process on the old local file.
	// Sync 表示在做任何事之前先拉一次远端配置。所有会启动进程的命令都为真，包括 restart：
	// 跳过拉取的 restart 会用旧的本地文件启动新进程。
	Sync bool
	// Watch hosts the config watcher. HTTP only: workers are immutable at runtime
	// and pick changes up through a rolling restart.
	// Watch 表示挂配置监听。只属于 HTTP 进程：worker 运行期不变，配置变更靠滚动重启生效。
	Watch bool
	// LoadConfig reads config.yaml. Only print commands (version, help) skip it.
	// LoadConfig 表示读取 config.yaml。只有打印类命令（version、help）跳过它。
	LoadConfig bool
	// RegisterGroups builds the worker groups from config, which `worker --list`
	// needs and a signal-only command does not.
	// RegisterGroups 表示按配置构建 worker 分组：`worker --list` 需要它，纯发信号的命令不需要。
	RegisterGroups bool
	// BuildServices wires HTTP, database, modules, middleware and telemetry.
	// Control commands leave all of it out, so a broken database does not block
	// `stop`.
	// BuildServices 表示装配 HTTP、数据库、模块、中间件与 telemetry。控制类命令全部跳过，
	// 数据库不可用因此不会挡住 `stop`。
	BuildServices bool
}

// planCommand classifies the command line. It runs before any file is opened,
// which is the whole point: a command that only sends signals must survive a
// broken nacos.yaml, and `version` must survive a broken config.yaml.
// planCommand 对命令行分类。它在打开任何文件之前运行，这正是重点：只发信号的命令必须在
// nacos.yaml 损坏时仍可用，`version` 必须在 config.yaml 损坏时仍可用。
func planCommand(args []string) commandPlan {
	if hasHelpFlag(args) {
		return commandPlan{Kind: "help"}
	}
	command := firstCommand(args)
	switch command {
	case "":
		return httpStartPlan("http-start")
	case "start":
		return httpStartPlan("http-start")
	case "stop":
		// Signals only: the pid path comes from config.yaml, nothing else is
		// read.
		// 只发信号：pid 路径来自 config.yaml，其余文件一概不读。
		return commandPlan{Kind: "stop", LoadConfig: true}
	case "status":
		// Read-only: the pid path and the listen address come from config.yaml,
		// and neither the database nor Nacos is touched. Asking what is running
		// must work while the service is down, and must not block on a config
		// center the process may have lost.
		// 只读：pid 路径与监听地址来自 config.yaml，数据库与 Nacos 都不碰。询问"在跑什么"
		// 必须在服务停着时可用，也不能被那个进程可能已经连不上的配置中心挡住。
		return commandPlan{Kind: "status", LoadConfig: true}
	case "restart":
		return commandPlan{Kind: "http-restart", LoadNacos: true, Sync: true, LoadConfig: true, BuildServices: true}
	case "script":
		return commandPlan{Kind: "script", LoadConfig: true, BuildServices: true}
	case "version", "help":
		return commandPlan{Kind: "print"}
	case "worker":
		return planWorker(args)
	default:
		// An unknown command is reported by the CLI; loading the config first
		// keeps the behaviour identical to a valid command.
		// 未知命令由 CLI 报错；先加载配置让行为与合法命令一致。
		return commandPlan{Kind: "command", LoadConfig: true, BuildServices: true}
	}
}

func httpStartPlan(kind string) commandPlan {
	return commandPlan{
		Kind:           kind,
		LoadNacos:      true,
		Sync:           true,
		Watch:          true,
		LoadConfig:     true,
		RegisterGroups: true,
		BuildServices:  true,
	}
}

// planWorker classifies the worker subcommands. stop only needs the pid path;
// --list needs the groups but no broker and no Nacos; everything else starts a
// process and therefore pulls the config first.
// planWorker 对 worker 子命令分类。stop 只需要 pid 路径；--list 需要分组但不需要 broker
// 与 Nacos；其余形态都会启动进程，因此先拉配置。
func planWorker(args []string) commandPlan {
	for _, arg := range args {
		if arg == "--list" || strings.HasPrefix(arg, "--list=") {
			return commandPlan{Kind: "worker-list", LoadConfig: true, RegisterGroups: true}
		}
	}
	index := -1
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "worker" {
			index = i
		}
		break
	}
	if index < 0 {
		return commandPlan{Kind: "worker-run", LoadConfig: true, RegisterGroups: true, BuildServices: true}
	}
	sub := workerSubcommand(args[index+1:])
	switch sub {
	case "stop", "status":
		// Both only read pid files: no broker connections and no Nacos, so a
		// worker can be inspected and stopped while its dependencies are down.
		// 两者都只读 pid 文件：不建 broker 连接也不碰 Nacos，因此依赖挂着时仍能查看和
		// 停止 worker。
		return commandPlan{Kind: "worker-" + sub, LoadConfig: true}
	case "restart":
		return commandPlan{Kind: "worker-restart", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true}
	default:
		return commandPlan{Kind: "worker-run", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true}
	}
}

// firstCommand returns the first token that is not a flag, or "" when the whole
// line is flags (the bare `app` form starts HTTP).
// firstCommand 返回第一个非旗标 token；整行都是旗标时返回空串（裸 `app` 形态启动 HTTP）。
func firstCommand(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

// hasHelpFlag reports whether the line asks for help, wherever the flag sits.
// Help never needs a config file: printing usage must work while the operator is
// fixing the very file that is broken.
// hasHelpFlag 判断命令行是否在求助，无论旗标出现在哪里。帮助永远不需要配置文件：运维正在
// 修的就是那个坏文件时，打印用法必须可用。
func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// workerSubcommand returns the first token after `worker` that is not a flag
// value, which is where cobra looks for a subcommand. Skipping the values of
// value-taking flags keeps `worker --group stop` a start of the group named
// "stop" instead of a stop of every group.
// workerSubcommand 返回 `worker` 之后第一个不是旗标取值的 token，也就是 cobra 找子命令
// 的位置。跳过取值的旗标，`worker --group stop` 因此仍然是启动名为 stop 的分组，而不是
// 停止全部分组。
func workerSubcommand(args []string) string {
	valueFlags := map[string]struct{}{
		"--group":           {},
		"--name":            {},
		"--concurrency":     {},
		"--timeout":         {},
		"--startup-timeout": {},
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
		if _, ok := valueFlags[arg]; ok {
			i++
		}
	}
	return ""
}
