package gofy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/console"
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
	// A failure that happens before any logger exists would leave the exit code as
	// its only trace: main reports it through app.Logger(), which is still Nop.
	// Write it to stderr instead, so a mistyped `worker --group` says what is wrong
	// instead of exiting 1 in silence.
	// 在 logger 建立之前失败时，退出码会是唯一的痕迹：main 通过 app.Logger() 记录，
	// 而它此时还是 Nop。这类错误直接写到 stderr，于是 `worker --group` 拼错时会说明
	// 哪里不对，而不是静默退出 1。
	if err != nil && a.reportToStderr() {
		console.New(os.Stderr, os.Stderr).Error("%v", err)
	}
	return err
}

// reportToStderr reports whether the error has nowhere else to go: no logger was
// injected at construction, and none has been built for a long-running mode.
// Both halves matter — an injected logger is already reachable from main, and a
// built one means the code that failed has logged it.
// reportToStderr 判断错误是否无处可写：构造时没有注入 logger，运行模式也还没构建出
// logger。两半都必要：注入了 logger 时 main 能记录；已构建时失败点自己会记。
func (a *App) reportToStderr() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.logger == nil && !a.ownLogger
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
		RunWorker:          a.runWorker,
		StopWorker:         a.stopWorker,
		RestartWorker:      a.restartWorker,
		Status:             a.status,
		StatusWorker:       a.workerStatus,
		Version:            a.printVersion,
		Scripts:            scripts,
		Commands:           custom,
		WorkerGroups:       a.workerDescriptors(),
		DefaultPIDFile:     a.opts.pidFile,
		DefaultStopTimeout: a.opts.shutdownTimeout,
	})
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func (a *App) isKnownCommand(name string) bool {
	switch name {
	case "start", "stop", "restart", "status", "script", "worker", "version", "help", "completion":
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
	lg, system, access, ownLogger, err := a.buildLoggers()
	if err != nil {
		return err
	}
	// Expose it to app.Logger() so main can still log script errors after Execute
	// returns.
	// 暴露给 app.Logger()，让 main 在 Execute 返回后仍能记录脚本错误。
	a.setLogger(lg, system, access, ownLogger)
	if ownLogger {
		defer syncLoggers(lg, system, access)
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
