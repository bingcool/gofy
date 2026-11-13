package gofy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/console"
	"github.com/bingcool/gofy/logger"
	"github.com/bingcool/gofy/script"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/spf13/pflag"
)

// Execute parses os.Args[1:] and dispatches start, stop, script, version or a
// custom command. It never calls os.Exit.
// Execute 解析 os.Args[1:] 并分发 start、stop、script、version 或自定义命令；
// 它从不调用 os.Exit，退出码由调用方根据返回的错误决定。
func (a *App) Execute(ctx context.Context) error {
	err := a.ExecuteArgs(ctx, os.Args[1:])
	// Argument errors happen before the logger is built, so app.Logger() in main is
	// still Nop. Without an injected logger, write to stderr directly so the user sees
	// more than the exit code of go run.
	// 参数错误发生在 logger 构建之前，main 里的 app.Logger() 还是 Nop。
	// 没有注入 logger 时直接写 stderr，避免用户只看到 go run 的退出码。
	if err != nil && a.opts.logger == nil && isCLIError(err) {
		console.New(os.Stderr, os.Stderr).Error("%v", err)
	}
	return err
}

func isCLIError(err error) bool {
	return errors.Is(err, ErrUsage) || errors.Is(err, ErrUnknownCommand)
}

// ExecuteArgs is Execute with explicit arguments, which makes it testable.
// ExecuteArgs 是带显式参数的 Execute，便于测试；ctx 为 nil 时使用
// context.Background()。
func (a *App) ExecuteArgs(ctx context.Context, args []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !a.isKnownCommand(args[0]) {
		return fmt.Errorf("%w: %w: %s", ErrUsage, ErrUnknownCommand, args[0])
	}

	scripts := make([]commandline.Script, 0, len(a.opts.scripts))
	for _, s := range a.opts.scripts {
		scripts = append(scripts, commandline.Script{Name: s.Name, Short: s.Short})
	}
	custom := make([]commandline.Command, 0, len(a.opts.commands))
	for _, c := range a.opts.commands {
		custom = append(custom, a.customCommand(c))
	}

	root := commandline.Build(commandline.Deps{
		Name:   a.opts.name,
		Short:  a.opts.name + " service",
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		In:     os.Stdin,
		Usage: func(err error) error {
			return fmt.Errorf("%w: %v", ErrUsage, err)
		},
		Start:              a.start,
		Stop:               a.stop,
		Restart:            a.restart,
		RunScript:          a.runScript,
		Version:            a.printVersion,
		Scripts:            scripts,
		Commands:           custom,
		DefaultPIDFile:     a.opts.pidFile,
		DefaultStopTimeout: a.opts.shutdownTimeout,
	})
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func (a *App) isKnownCommand(name string) bool {
	switch name {
	case "start", "stop", "restart", "script", "version", "help", "completion":
		return true
	}
	for _, c := range a.opts.commands {
		if c.Use == name {
			return true
		}
	}
	return false
}

func (a *App) customCommand(spec command.Command) commandline.Command {
	return commandline.Command{
		Use:   spec.Use,
		Short: spec.Short,
		Flags: spec.Flags,
		Run: func(ctx context.Context, fs *pflag.FlagSet) error {
			return a.withRuntime(ctx, spec.WithoutDatabase, func(rt *Runtime) error {
				return spec.Run(ctx, rt, fs)
			})
		},
	}
}

func (a *App) runScript(ctx context.Context, name string, args []string) error {
	spec, ok := script.Find(a.opts.scripts, name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrScriptNotFound, name)
	}
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if spec.Flags != nil {
		spec.Flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() > 0 {
		args := fs.Args()
		if strings.Contains(args[0], "=") {
			return fmt.Errorf("%w: unexpected arguments: %v (script flags need a leading --, e.g. --%s)", ErrUsage, args, args[0])
		}
		return fmt.Errorf("%w: unexpected arguments: %v (script flags need a leading --)", ErrUsage, args)
	}
	return a.withRuntime(ctx, spec.WithoutDatabase, func(rt *Runtime) error {
		return spec.Run(ctx, rt, fs)
	})
}

// withRuntime builds a short-lived Runtime for scripts and custom commands.
// It starts no HTTP server, writes no pid file and makes Runtime.Fail a no-op.
// withRuntime 为脚本和自定义命令构建一个短生命周期的 Runtime：不启动 HTTP
// 服务、不写 pid 文件，Runtime.Fail 是 no-op，因此调用 Fail 不会中断进程。
func (a *App) withRuntime(ctx context.Context, withoutDatabase bool, fn func(*Runtime) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lg, system, ownLogger, err := a.buildLoggers()
	if err != nil {
		return err
	}
	// Expose it to app.Logger() so main can still log script errors after Execute
	// returns.
	// 暴露给 app.Logger()，让 main 在 Execute 返回后仍能记录脚本错误。
	a.setLogger(lg, system, ownLogger)
	if ownLogger {
		defer func() {
			_ = logger.Sync(lg)
			if system != lg {
				_ = logger.Sync(system)
			}
		}()
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		db    = a.opts.db
		ownDB = false
	)
	if !withoutDatabase && db == nil && a.opts.dbConfig != nil {
		db, ownDB, err = a.buildDB(runCtx, lg)
		if err != nil {
			return err
		}
	}
	if ownDB {
		defer func() { _ = gofydb.Close(db) }()
	}

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:    a.opts.name,
		Env:     a.opts.env,
		Logger:  lg,
		DB:      db,
		Version: a.version,
		Ctx:     runCtx,
		Fail:    func(error) {},
	})
	return fn(rt)
}

func (a *App) printVersion(_ context.Context, jsonOutput bool) error {
	if jsonOutput {
		data, err := json.MarshalIndent(a.version, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(data))
		return err
	}
	_, err := fmt.Fprintf(os.Stdout,
		"version: %s\ncommit: %s\nbuild_time: %s\ngo: %s\n",
		a.version.Version, a.version.Commit, a.version.BuildTime, a.version.GoVersion,
	)
	return err
}
