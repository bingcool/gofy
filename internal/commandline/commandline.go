// Package commandline builds the cobra command tree. It receives callbacks
// and descriptors only; it never imports the gofy root package.
package commandline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bingcool/gofy/internal/console"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// StartOptions are the flags of the start command.
type StartOptions struct {
	Daemon bool
}

// StopOptions are the flags of the stop command.
type StopOptions struct {
	PIDFile string
	Timeout time.Duration
	Force   bool
}

// Script describes a registered script for listing.
type Script struct {
	Name  string
	Short string
}

// Command is a custom subcommand.
type Command struct {
	Use   string
	Short string
	Flags func(*pflag.FlagSet)
	Run   func(ctx context.Context, fs *pflag.FlagSet) error
}

// Deps contains everything Build needs.
type Deps struct {
	Name   string
	Short  string
	Out    io.Writer
	ErrOut io.Writer
	In     io.Reader

	Usage func(error) error

	Start     func(ctx context.Context, opts StartOptions) error
	Stop      func(ctx context.Context, opts StopOptions) error
	Restart   func(ctx context.Context, opts StopOptions) error
	RunScript func(ctx context.Context, name string, args []string) error
	Version   func(ctx context.Context, jsonOutput bool) error

	Scripts  []Script
	Commands []Command

	DefaultPIDFile     string
	DefaultStopTimeout time.Duration
}

// Build constructs a fresh command tree.
func Build(d Deps) *cobra.Command {
	if d.Usage == nil {
		d.Usage = func(err error) error { return err }
	}
	if d.Out == nil {
		d.Out = io.Discard
	}
	if d.ErrOut == nil {
		d.ErrOut = io.Discard
	}

	root := &cobra.Command{
		Use:           d.Name,
		Short:         d.Short,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return d.Usage(fmt.Errorf("unexpected arguments: %v", args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return d.Start(cmd.Context(), StartOptions{})
		},
	}
	root.SetOut(d.Out)
	root.SetErr(d.ErrOut)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return d.Usage(err)
	})

	root.AddCommand(newStartCmd(d))
	root.AddCommand(newStopCmd(d))
	root.AddCommand(newRestartCmd(d))
	root.AddCommand(newScriptCmd(d))
	root.AddCommand(newVersionCmd(d))
	for _, spec := range d.Commands {
		root.AddCommand(newCustomCmd(d, spec))
	}
	return root
}

func newStartCmd(d Deps) *cobra.Command {
	var opts StartOptions
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the HTTP server",
		Args:  noArgs(d, "start"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return d.Start(cmd.Context(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", false, "detach into the background and exit the parent process")
	return cmd
}

func noArgs(d Deps, name string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return d.Usage(fmt.Errorf("%s: unexpected arguments: %v", name, args))
		}
		return nil
	}
}

func newStopCmd(d Deps) *cobra.Command {
	opts := newStopOptions(d)
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running instance using its pid file",
		Args:  noArgs(d, "stop"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !opts.Force {
				ok, err := confirmAction(d, "停止")
				if err != nil || !ok {
					return err
				}
			}
			return d.Stop(cmd.Context(), opts)
		},
	}
	addStopFlags(cmd, &opts)
	return cmd
}

func newRestartCmd(d Deps) *cobra.Command {
	opts := newStopOptions(d)
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop the running instance, then start in the foreground",
		Args:  noArgs(d, "restart"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !opts.Force {
				ok, err := confirmAction(d, "重启")
				if err != nil || !ok {
					return err
				}
			}
			return d.Restart(cmd.Context(), opts)
		},
	}
	addStopFlags(cmd, &opts)
	return cmd
}

// confirmAction asks for a yes/no confirmation. Anything but "yes" cancels.
func confirmAction(d Deps, verb string) (bool, error) {
	c := console.New(d.Out, d.ErrOut)
	ok, err := c.Confirm(d.In, "你确定 [%s] 应用【%s】? (yes or no): ", verb, d.Name)
	if err != nil {
		return false, d.Usage(err)
	}
	if !ok {
		c.Info("你已放弃%s应用【%s】，应用继续运行中", verb, d.Name)
	}
	return ok, nil
}

func newStopOptions(d Deps) StopOptions {
	return StopOptions{
		PIDFile: d.DefaultPIDFile,
		Timeout: d.DefaultStopTimeout,
	}
}

func addStopFlags(cmd *cobra.Command, opts *StopOptions) {
	cmd.Flags().StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "path to the pid file")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", opts.Timeout, "how long to wait for the process to exit")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "skip confirmation and send SIGKILL when the timeout expires")
}

func newScriptCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:                "script [name] [flags]",
		Short:              "Run a registered maintenance script",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || args[0] == "--list" || args[0] == "-l" {
				return listScripts(d)
			}
			if args[0] == "--help" || args[0] == "-h" {
				return cmd.Help()
			}
			return d.RunScript(cmd.Context(), args[0], args[1:])
		},
	}
}

func listScripts(d Deps) error {
	if len(d.Scripts) == 0 {
		_, err := fmt.Fprintln(d.Out, "no scripts registered")
		return err
	}
	// 表头让列含义一目了然；Table 会按内容对齐，非终端输出不含颜色。
	rows := make([][]string, 0, len(d.Scripts)+1)
	rows = append(rows, []string{"NAME", "DESCRIPTION"})
	for _, s := range d.Scripts {
		rows = append(rows, []string{s.Name, s.Short})
	}
	console.New(d.Out, d.ErrOut).Table(rows)
	return nil
}

func newVersionCmd(d Deps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  noArgs(d, "version"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return d.Version(cmd.Context(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print build information as JSON")
	return cmd
}

func newCustomCmd(d Deps, spec Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   spec.Use,
		Short: spec.Short,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return d.Usage(fmt.Errorf("%s: unexpected arguments: %v", spec.Use, args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return spec.Run(cmd.Context(), cmd.Flags())
		},
	}
	if spec.Flags != nil {
		spec.Flags(cmd.Flags())
	}
	return cmd
}
