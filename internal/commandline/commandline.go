// Package commandline builds the cobra command tree. It receives callbacks
// and descriptors only; it never imports the gofy root package.
// 包 commandline 负责构建 cobra 命令树。它只接收回调和描述信息，从不 import
// gofy 根包，因此不会与上层应用和框架生命周期产生循环依赖。
package commandline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bingcool/gofy/internal/console"
	"github.com/bingcool/gofy/support/worker"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// DefaultStartupTimeout is the budget a daemon parent allows the child to
// report readiness. It is generous on purpose: the child may be opening a
// database, binding a listener and registering with Nacos, and a budget that is
// too small reports failure for a process that is merely slow.
// DefaultStartupTimeout 是后台父进程留给子进程上报就绪的预算。它刻意宽松：子进程可能
// 正在打开数据库、绑定 listener、注册 Nacos，预算太小会把"慢"报成失败。
const DefaultStartupTimeout = 30 * time.Second

// StartOptions are the flags of the start command.
// StartOptions 是 start 命令的旗标；Daemon 为 true 时先 detach 到后台再让父进程退出。
type StartOptions struct {
	Daemon bool
	// StartupTimeout bounds how long the daemon parent waits for the child's
	// readiness report before giving up on confirming the start. It has no
	// meaning without Daemon.
	// StartupTimeout 限制后台父进程等待子进程就绪上报的时长，超时后不再确认启动结果。
	// 未设置 Daemon 时该值没有意义。
	StartupTimeout time.Duration
}

// StopOptions are the flags of the stop command.
// StopOptions 是 stop/restart 命令的旗标；Timeout 到期后只有 Force 为 true 才会发送 SIGKILL。
type StopOptions struct {
	PIDFile string
	Timeout time.Duration
	Force   bool
	// Worker selects a worker group instead of the HTTP instance. Only stop
	// exposes it: restarting a worker means running the group in the
	// foreground, which is `worker`, not `restart`.
	// Worker 选择 worker 分组而不是 HTTP 实例。只有 stop 暴露该旗标：重启 worker
	// 等于在前台运行分组，那是 `worker` 而不是 `restart`。
	Worker string
}

// Script describes a registered script for listing.
// Script 描述一个已注册脚本，仅用于 `script --list` 的展示。
type Script struct {
	Name  string
	Short string
}

// Command is a custom subcommand.
// Command 是一个自定义子命令；Flags 可为空，Run 必须非空，否则执行时会 panic。
type Command struct {
	Use   string
	Short string
	Flags func(*pflag.FlagSet)
	Run   func(ctx context.Context, fs *pflag.FlagSet) error
}

// Deps contains everything Build needs.
// Deps 包含 Build 所需的全部依赖。Start/Stop/Restart/RunScript/Version 等回调由调用方注入，
// 未设置 Out/ErrOut 时输出被丢弃，Usage 默认为原样返回错误。
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
	RunWorker func(ctx context.Context, opts WorkerOptions) error
	// StopWorker and RestartWorker back `worker stop` / `worker restart`. Their
	// options carry the group selector, so an empty Group means every worker
	// process on this host rather than the HTTP instance.
	// StopWorker / RestartWorker 支撑 `worker stop` / `worker restart`。它们的选项
	// 带着分组选择符，因此 Group 为空表示本机所有 worker 进程，而不是 HTTP 实例。
	StopWorker    func(ctx context.Context, opts WorkerOptions) error
	RestartWorker func(ctx context.Context, opts WorkerOptions) error
	// Status reports the HTTP instance; StatusWorker reports the worker
	// processes. They are separate callbacks because the two read different pid
	// files and answer different questions, and because a binary without worker
	// groups still has status.
	// Status 报告 HTTP 实例，StatusWorker 报告 worker 进程。两者分开是因为读不同的 pid
	// 文件、回答不同的问题，而且一个没有 worker 分组的二进制仍然有 status。
	Status       func(ctx context.Context, opts StatusOptions) error
	StatusWorker func(ctx context.Context, opts WorkerOptions) error
	Version      func(ctx context.Context, jsonOutput bool) error

	Scripts  []Script
	Commands []Command

	// WorkerGroups feeds `worker --list`; running a group goes through RunWorker.
	// WorkerGroups 供 `worker --list` 使用；真正运行分组走 RunWorker。
	WorkerGroups []WorkerGroup

	DefaultPIDFile     string
	DefaultStopTimeout time.Duration
}

// Build constructs a fresh command tree.
// Build 构建一棵全新的命令树；每次调用都返回独立实例，便于测试中重复构建而互不干扰。
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
	root.AddCommand(newStatusCmd(d))
	root.AddCommand(newScriptCmd(d))
	root.AddCommand(newWorkerCmd(d))
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
	cmd.Flags().DurationVar(&opts.StartupTimeout, "startup-timeout", DefaultStartupTimeout,
		"how long the daemon parent waits for the child to report ready before giving up")
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
		Short: "Stop a running instance or worker group using its pid file",
		Args:  noArgs(d, "stop"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !opts.Force {
				ok, err := confirmAction(d, "停止", fmt.Sprintf("应用【%s】", d.Name))
				if err != nil || !ok {
					return err
				}
			}
			return d.Stop(cmd.Context(), opts)
		},
	}
	addStopFlags(cmd, &opts)
	cmd.Flags().StringVar(&opts.Worker, "worker", "", "stop the worker group with this name instead of the HTTP instance ("+WorkerGroupAll+" = all groups)")
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		// The wildcard is a run-time selector, not a file name: it stands for the
		// pid file a `worker --group=*` process wrote, so resolve it to the token
		// that process used before validating.
		// 通配符是启动时的选择符，不是文件名：它指向 `worker --group=*` 进程写的那个
		// pid 文件，因此先解析成该进程用的后缀，再做校验。
		if opts.Worker == WorkerGroupAll {
			opts.Worker = WorkerAllPIDToken
		}
		// The group name becomes part of a pid file name, so reject anything
		// that could escape the directory before it reaches the filesystem.
		// 分组名会成为 pid 文件名的一部分，因此要在它到达文件系统之前挡掉任何可能
		// 越出目录的字符。
		if opts.Worker != "" && !worker.ValidName(opts.Worker) {
			return d.Usage(fmt.Errorf("stop: invalid worker group %q", opts.Worker))
		}
		return nil
	}
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
				ok, err := confirmAction(d, "重启", fmt.Sprintf("应用【%s】", d.Name))
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

// confirmAction asks for a yes/no confirmation of one action against one
// target. Anything but "yes" cancels.
// confirmAction 针对一个目标和一次操作请求 yes/no 确认，除 "yes" 之外的任何输入都视为
// 取消，此时返回 false 且不执行操作。
func confirmAction(d Deps, verb, target string) (bool, error) {
	c := console.New(d.Out, d.ErrOut)
	ok, err := c.Confirm(d.In, "你确定 [%s] %s? (yes or no): ", verb, target)
	if err != nil {
		return false, d.Usage(err)
	}
	if !ok {
		c.Info("你已放弃%s%s，应用继续运行中", verb, target)
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
	// The header makes each column self-explanatory; Table aligns by content and omits
	// colors when the output is not a terminal.
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
