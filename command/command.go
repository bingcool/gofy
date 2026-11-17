// Package command defines custom CLI commands registered by an application.
// 包 command 定义应用注册的自定义 CLI 命令；注册时会校验名字格式、保留字与重名。
package command

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bingcool/gofy/core"
	"github.com/spf13/pflag"
)

// Runtime is the shared runtime passed to custom commands.
// Runtime 是传给自定义命令的共享运行时。
type Runtime = core.Runtime

// Command is a custom subcommand.
// Command 是一个自定义子命令：Use 必须是单个单词，Run 不能为空。
type Command struct {
	Use             string // single word, e.g. "migrate"；命令名必须是单个单词，不能含空格
	Short           string
	Flags           func(fs *pflag.FlagSet)
	Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
	WithoutDatabase bool
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// reservedNames mirrors the built-in subcommands registered by
// internal/commandline. A custom command with one of these names would be
// shadowed by the built-in: cobra keeps the command that was added first, and
// the built-ins are added before custom ones.
// reservedNames 对应 internal/commandline 注册的内建子命令。同名自定义命令会被
// 内建命令遮蔽：cobra 保留先加入的命令，而内建命令先于自定义命令注册。
var reservedNames = map[string]struct{}{
	"start":      {},
	"stop":       {},
	"restart":    {},
	"status":     {},
	"script":     {},
	"worker":     {},
	"version":    {},
	"help":       {},
	"completion": {},
}

// Validate checks command names, reserved words, duplicates and required
// fields. reserved allows the caller to reserve additional names.
// Validate 校验命令名、保留字、重名和必填字段；reserved 让调用方额外保留一些名字，
// 例如与其他子命令集合共享的命名空间。
func Validate(cmds []Command, reserved ...string) error {
	blocked := make(map[string]struct{}, len(reservedNames)+len(reserved))
	for name := range reservedNames {
		blocked[name] = struct{}{}
	}
	for _, name := range reserved {
		blocked[name] = struct{}{}
	}

	seen := make(map[string]struct{}, len(cmds))
	for i, c := range cmds {
		if c.Use == "" || strings.ContainsAny(c.Use, " \t") {
			return fmt.Errorf("command: invalid use %q at index %d (want a single word)", c.Use, i)
		}
		if !namePattern.MatchString(c.Use) {
			return fmt.Errorf("command: invalid name %q at index %d", c.Use, i)
		}
		if _, ok := blocked[c.Use]; ok {
			return fmt.Errorf("command: name %q is reserved", c.Use)
		}
		if _, ok := seen[c.Use]; ok {
			return fmt.Errorf("command: duplicate name %q", c.Use)
		}
		seen[c.Use] = struct{}{}
		if c.Run == nil {
			return fmt.Errorf("command: %q has nil Run", c.Use)
		}
	}
	return nil
}
