package gofy

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/process"
)

// start runs the server in the foreground, or re-executes itself detached when
// --daemon is set.
// start 在前台运行服务器；设置 --daemon 时改为以脱离态重新执行自身。
func (a *App) start(ctx context.Context, opts commandline.StartOptions) error {
	if !opts.Daemon {
		return a.Run(ctx)
	}
	if runtime.GOOS == "windows" {
		panic("gofy: start --daemon=1 is not supported on windows")
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("gofy: resolve executable: %w", err)
	}
	// Appending --daemon=false makes the child parse the same command line
	// without re-detaching.
	// 追加 --daemon=false，让子进程解析同一份命令行但不会再次脱离。
	args := append(append([]string{}, os.Args[1:]...), "--daemon=false")
	c := a.con()
	pid, err := process.Detach(executable, args)
	if err != nil {
		return fmt.Errorf("gofy: daemonize: %w", err)
	}
	c.Success("应用【%s】已在后台启动 (pid %d)", a.opts.name, pid)
	// No binary name in the hint, for the same reason as the worker detach path:
	// the process cannot know whether it was started as `go run .`, by absolute
	// path or by a name on PATH, so the application name may not be a command.
	// 提示里不带二进制名，理由与 worker 的脱离路径相同：进程不知道自己是被 `go run .`、
	// 绝对路径还是 PATH 上的名字启动的，应用名未必是一条可执行的命令。
	c.Note("使用 status 查看状态，stop --force=1 停止")
	return nil
}
