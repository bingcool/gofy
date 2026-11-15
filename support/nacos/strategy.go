package nacos

import (
	"fmt"
	"strings"
)

// ChangeStrategy selects what a watcher does after a changed config has been
// written to disk. The choice belongs to the deployment, not to the content
// being delivered, so it lives in application.yaml (a local file) rather than
// in the remote config that Sync writes over config.yaml.
// ChangeStrategy 决定 watcher 把变更后的配置写入磁盘之后做什么。选择取决于部署形态，
// 而非下发的内容，因此放在本地文件 application.yaml 里，而不是会被 Sync 覆盖的
// 远端 config.yaml 中。
type ChangeStrategy string

const (
	// StrategyReload applies the change in place through WithOnChange, keeping
	// the process and its pid. It is the local-development shape and the default.
	// StrategyReload 通过 WithOnChange 原地应用变更，进程与 pid 不变；适用于本地开发，
	// 也是默认值。
	StrategyReload ChangeStrategy = "reload"
	// StrategyRestart re-executes the current binary so the whole service
	// starts again without a supervisor. The pid stays the same, but no defer
	// runs, so in-flight requests are cut short.
	// StrategyRestart 重新执行当前二进制，无需外部监管即可重启整个服务。pid 不变，但
	// defer 不会执行，在途请求会被直接切断。
	StrategyRestart ChangeStrategy = "restart"
	// StrategyExit terminates the process and leaves the restart to an external
	// supervisor such as Kubernetes, a Docker restart policy or systemd.
	// StrategyExit 终止进程，把重启交给 Kubernetes、Docker restart policy、systemd
	// 等外部监管者。
	StrategyExit ChangeStrategy = "exit"
)

// ParseChangeStrategy validates a value from a file or the environment. An
// unknown value is an error rather than a silent fall back: a typo in a
// deployment's restart strategy would otherwise leave production doing an
// in-place reload.
// ParseChangeStrategy 校验来自文件或环境变量的取值。未知值直接报错而不是静默回落：
// 部署策略拼错时若悄悄回落到默认值，生产环境就会变成原地热更新。
func ParseChangeStrategy(value string) (ChangeStrategy, error) {
	s := ChangeStrategy(strings.ToLower(strings.TrimSpace(value)))
	switch s {
	case StrategyReload, StrategyRestart, StrategyExit:
		return s, nil
	default:
		return "", fmt.Errorf("nacos: invalid config_change_strategy %q (want reload, restart or exit)", value)
	}
}
