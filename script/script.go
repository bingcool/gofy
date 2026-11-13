// Package script defines one-off maintenance tasks executed through the
// `script` subcommand.
// 包 script 定义通过 `script` 子命令执行的一次性维护任务。
// 脚本在独立进程中运行，不承担常驻服务的职责，也不参与 HTTP 路由注册。
package script

import (
	"context"
	"fmt"
	"regexp"

	"github.com/bingcool/gofy/core"
	"github.com/spf13/pflag"
)

// Runtime is the shared runtime passed to scripts.
// Runtime 是传给脚本的共享运行时，与 HTTP 服务侧使用同一套配置、日志与数据库句柄。
type Runtime = core.Runtime

// Script is a registered maintenance task.
// Script 是一个已注册的维护任务。Name 需符合小写命名规则且不能与保留字冲突，
// WithoutDatabase 为 true 时框架不会为该脚本初始化数据库，从而避免无谓的连接开销。
type Script struct {
	Name            string
	Short           string
	Flags           func(fs *pflag.FlagSet)
	Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
	WithoutDatabase bool
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var reservedNames = map[string]struct{}{
	"list":       {},
	"help":       {},
	"completion": {},
	"restart":    {},
}

// Validate checks names, reserved words, duplicates and required fields.
// Validate 校验名称格式、保留字、重名以及必填字段；应在注册阶段调用，
// 让配置错误在启动时就暴露，而不是等到执行脚本时才失败。
func Validate(scripts []Script) error {
	seen := make(map[string]struct{}, len(scripts))
	for i, s := range scripts {
		if !namePattern.MatchString(s.Name) {
			return fmt.Errorf("script: invalid name %q at index %d", s.Name, i)
		}
		if _, ok := reservedNames[s.Name]; ok {
			return fmt.Errorf("script: name %q is reserved", s.Name)
		}
		if _, ok := seen[s.Name]; ok {
			return fmt.Errorf("script: duplicate name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
		if s.Run == nil {
			return fmt.Errorf("script: %q has nil Run", s.Name)
		}
	}
	return nil
}

// Find returns the script with the given name.
// Find 按名称查找脚本；未找到时返回零值和 false，调用方必须先判断 ok 再使用返回值。
func Find(scripts []Script, name string) (Script, bool) {
	for _, s := range scripts {
		if s.Name == name {
			return s, true
		}
	}
	return Script{}, false
}
