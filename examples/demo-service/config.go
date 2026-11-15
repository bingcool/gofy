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

// isStartInvocation reports whether args select the foreground start (or no
// subcommand at all).
// isStartInvocation 判断参数是否指向前台 start 子命令，或干脆没有子命令；
// 只有这种情况才需要拉取 Nacos 配置。
func isStartInvocation(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				return false
			}
			continue
		}
		return arg == "start"
	}
	return true
}

// isWorkerStartInvocation reports whether args start a resident worker group.
// Query forms (--list, --help) are excluded so they keep working when the
// config server is down; every other subcommand is excluded too.
// isWorkerStartInvocation 判断参数是否要真正启动常驻 worker 分组。查询形态
// （--list、--help）与其它子命令都排除在外：配置中心不可用时它们仍要可用。
func isWorkerStartInvocation(args []string) bool {
	command := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		command = arg
		break
	}
	if command != "worker" {
		return false
	}
	for _, arg := range args {
		if arg == "--list" || strings.HasPrefix(arg, "--list=") ||
			arg == "--help" || arg == "-h" {
			return false
		}
	}
	return true
}

// configInvocation classifies the command line into startup config needs.
// sync pulls the current remote config once, so a freshly launched process
// never runs on a stale local config.yaml. watch hosts the watcher and applies
// the change strategy; it belongs to the HTTP process only, because a worker
// stays immutable at runtime and picks up changes through a rolling restart
// by the deployment layer.
// configInvocation 把命令行归类成启动期配置需求。sync 表示启动时拉一次远端配置，
// 避免新进程拿陈旧的本地 config.yaml 启动；watch 表示运行期挂监听并应用变更策略，
// 只属于 HTTP 进程——worker 运行期保持不变，配置变更由部署层滚动重启生效。
func configInvocation(args []string) (sync, watch bool) {
	if isStartInvocation(args) {
		return true, true
	}
	if isWorkerStartInvocation(args) {
		return true, false
	}
	return false, false
}
