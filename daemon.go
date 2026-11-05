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
	args := append(append([]string{}, os.Args[1:]...), "--daemon=false")
	c := a.con()
	pid, err := process.Detach(executable, args)
	if err != nil {
		return fmt.Errorf("gofy: daemonize: %w", err)
	}
	c.Success("应用【%s】已在后台启动 (pid %d)", a.opts.name, pid)
	c.Note("使用 %s stop --force=1 停止", a.opts.name)
	return nil
}
