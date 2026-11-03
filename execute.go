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
	"github.com/bingcool/gofy/logging"
	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support/mysql"
	"github.com/spf13/pflag"
)

// Execute parses os.Args[1:] and dispatches start, stop, script, version or a
// custom command. It never calls os.Exit.
func (a *App) Execute(ctx context.Context) error {
	return a.ExecuteArgs(ctx, os.Args[1:])
}

// ExecuteArgs is Execute with explicit arguments, which makes it testable.
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
		Usage: func(err error) error {
			return fmt.Errorf("%w: %v", ErrUsage, err)
		},
		Start:              a.Run,
		Stop:               a.stop,
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
	case "start", "stop", "script", "version", "help", "completion":
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
		return fmt.Errorf("%w: unexpected arguments: %v", ErrUsage, fs.Args())
	}
	return a.withRuntime(ctx, spec.WithoutDatabase, func(rt *Runtime) error {
		return spec.Run(ctx, rt, fs)
	})
}

// withRuntime builds a short-lived Runtime for scripts and custom commands.
// It starts no HTTP server, writes no pid file and makes Runtime.Fail a no-op.
func (a *App) withRuntime(ctx context.Context, withoutDatabase bool, fn func(*Runtime) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lg, ownLogger, err := a.buildLogger()
	if err != nil {
		return err
	}
	if ownLogger {
		defer func() { _ = logging.Sync(lg) }()
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
		defer func() { _ = mysql.Close(db) }()
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
