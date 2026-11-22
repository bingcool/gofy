package commandline

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/internal/console"
	"github.com/bingcool/gofy/worker"
	"github.com/spf13/cobra"
)

// workerNameRule is the group/worker naming contract, quoted in errors so an
// operator does not have to find it in the docs.
// workerNameRule 是分组与 worker 的命名约定，出现在错误里，运维不必去文档里找。
const workerNameRule = "^[a-z][a-z0-9-]{0,62}$"

// validateWorkerGroup rejects a --group value that cannot name a worker group.
// The value is interpolated into a pid file path, so a separator or ".." must
// be refused here instead of reaching filepath.Clean: clean would resolve it
// outside the directory that holds the pid file.
// validateWorkerGroup 拒绝不可能是分组名的 --group 值。该值会被插入 pid 文件路径，
// 因此分隔符或 ".." 必须在这里拒绝，而不是交给 filepath.Clean——clean 会把它解析到
// pid 文件所在目录之外。
func validateWorkerGroup(command, group string, required bool) error {
	g := strings.TrimSpace(group)
	if g == "" {
		if required {
			return fmt.Errorf("%s: --group is required (use --group=%s for every group, see worker --list)", command, WorkerGroupAllAlias)
		}
		return nil
	}
	if IsAllWorkerGroup(g) {
		return nil
	}
	if !worker.ValidName(g) {
		return fmt.Errorf("%s: invalid worker group %q; names must match %s", command, g, workerNameRule)
	}
	return nil
}

// WorkerGroupAll is the --group value that selects every registered group.
// WorkerGroupAll 是选中全部已注册分组的 --group 取值。
const WorkerGroupAll = "*"

// WorkerGroupAllAlias is the quote-free spelling of the wildcard. A shell
// expands an unquoted * before the process starts — zsh refuses to run the
// command at all ("no matches found"), bash passes the word through only
// because it found no match — so the wildcard needs an alias no shell reads as
// a glob.
// WorkerGroupAllAlias 是通配符的免引号写法。shell 在进程启动之前就会展开不加引号的
// *：zsh 直接拒绝执行（no matches found），bash 只是恰好因为没有匹配项才原样传下来。
// 因此通配符需要一个任何 shell 都不会当成 glob 的别名。
const WorkerGroupAllAlias = "all"

// WorkerAllPIDToken is the pid-file suffix that stands for "every group": the
// wildcard run writes it, and `stop --worker=*` resolves it.
// WorkerAllPIDToken 是代表"全部分组"的 pid 文件后缀：通配符启动时写它，
// `stop --worker=*` 也解析它。
const WorkerAllPIDToken = "all"

// IsAllWorkerGroup reports whether group selects every worker process. The
// wildcard and its alias mean the same thing everywhere: run every registered
// group, or stop every worker that left a pid file.
// IsAllWorkerGroup 报告 group 是否选中所有 worker 进程。通配符与它的别名在任何地方
// 含义相同：运行全部已注册分组，或停止所有留下 pid 文件的 worker。
func IsAllWorkerGroup(group string) bool {
	g := strings.TrimSpace(group)
	return g == WorkerGroupAll || g == WorkerGroupAllAlias
}

// WorkerOptions are the flags of the worker command and its subcommands.
// WorkerOptions 是 worker 命令及其子命令的旗标。
type WorkerOptions struct {
	// Group selects the group to run. Empty means every group for stop and
	// restart, and is rejected when running.
	// Group 选择要运行的分组。stop / restart 时空值表示全部分组；运行分组时不允许为空。
	Group string
	// Names narrows the group to the listed workers; empty runs every member.
	// Names 把分组缩小到列出的 worker；为空表示运行全部成员。
	Names []string
	// Concurrency overrides every worker's instance count when positive.
	// Concurrency 为正数时覆盖所有 worker 的实例数。
	Concurrency int
	// List prints the registered groups and exits without starting anything.
	// List 打印已注册的分组后退出，不启动任何 worker。
	List bool
	// Daemon detaches into the background and exits the parent. `worker restart`
	// defaults it to true: restarting a service that was running in the
	// background must not silently leave it attached to the terminal.
	// Daemon 脱离到后台并让父进程退出。`worker restart` 默认为 true：重启一个本来在
	// 后台跑的服务，不能悄悄把它挂到当前终端上。
	Daemon bool
	// Force skips the confirmation and escalates to SIGKILL on timeout. Only
	// stop and restart use it.
	// Force 跳过确认，并在超时后升级为 SIGKILL。只有 stop 与 restart 使用它。
	Force bool
	// Timeout bounds how long stop waits for the process to exit.
	// Timeout 限制 stop 等待进程退出的时长。
	Timeout time.Duration
	// StartupTimeout bounds how long a daemon parent waits for the child's
	// first readiness report before giving up on confirming the start.
	// StartupTimeout 限制后台父进程等待子进程首次就绪上报的时长，超时后不再确认启动结果。
	StartupTimeout time.Duration
	// JSON makes `worker status` emit machine-readable output. It has no meaning
	// for the run form, which is why it is not a persistent flag.
	// JSON 让 `worker status` 输出机器可读的结果。它对运行形态没有意义，因此不是
	// 持久旗标。
	JSON bool
}

// WorkerStartArgs rebuilds the argv that starts a worker group. The daemon
// child must run this, never the command the parent received: a detached
// `worker restart` would stop again, ask for confirmation with no terminal and
// return, so the service stays down while the parent reports success.
//
// Only flags the run form understands are emitted. --force and --timeout belong
// to stop and restart, and `worker` rejects them.
// WorkerStartArgs 重建启动 worker 分组的 argv。daemon 子进程必须执行它，而不是父进程
// 收到的命令：脱离终端的 `worker restart` 会再停一次、在没有终端时等确认然后直接返回，
// 服务停在原地而父进程已经报了成功。
//
// 只输出运行形态认识的旗标；--force 与 --timeout 属于 stop / restart，`worker` 会拒绝。
func WorkerStartArgs(opts WorkerOptions) []string {
	args := []string{"worker", "--group=" + opts.Group}
	if opts.Concurrency > 0 {
		args = append(args, "--concurrency="+strconv.Itoa(opts.Concurrency))
	}
	for _, name := range opts.Names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		args = append(args, "--name="+name)
	}
	// Written explicitly even though false is the default: the child inherits
	// nothing from the parent's command line, and this flag is the one that
	// keeps a restart from detaching a second time.
	// 即使 false 是默认值也显式写出：子进程不继承父进程的命令行，而这个旗标正是防止
	// restart 再次脱离的开关。
	return append(args, "--daemon=false")
}

// WorkerInfo describes one worker for `worker --list`.
// WorkerInfo 描述 `worker --list` 输出中的一个 worker。
type WorkerInfo struct {
	Name        string
	Concurrency int
}

// WorkerGroup describes one group for `worker --list`.
// WorkerGroup 描述 `worker --list` 输出中的一个分组。
type WorkerGroup struct {
	Name    string
	Workers []WorkerInfo
}

func newWorkerCmd(d Deps) *cobra.Command {
	var opts WorkerOptions
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the long-running workers of one group",
		Args:  noArgs(d, "worker"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.List {
				return listWorkers(d)
			}
			if err := validateWorkerGroup("worker", opts.Group, true); err != nil {
				return d.Usage(err)
			}
			if d.RunWorker == nil {
				return fmt.Errorf("worker: no worker groups registered")
			}
			return d.RunWorker(cmd.Context(), opts)
		},
	}
	// --group is persistent, so `worker stop` and `worker restart` take the same
	// selector in the same positions as the run form, including before the
	// subcommand (`worker --group=order stop`).
	// --group 是持久旗标，`worker stop` / `worker restart` 因此与启动形态共用同一个
	// 选择符，位置也一致，包括写在子命令之前（`worker --group=order stop`）。
	cmd.PersistentFlags().StringVar(&opts.Group, "group", "",
		"worker group to run, or --group="+WorkerGroupAllAlias+" ("+WorkerGroupAll+" when quoted) for every group")
	cmd.Flags().StringSliceVar(&opts.Names, "name", nil, "run only the named workers (repeatable)")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 0, "override the instance count of every worker (0 = config)")
	cmd.Flags().BoolVar(&opts.List, "list", false, "list groups and workers, then exit")
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", false, "detach into the background and exit the parent process")
	cmd.Flags().DurationVar(&opts.StartupTimeout, "startup-timeout", DefaultStartupTimeout,
		"how long the daemon parent waits for the child to report ready before giving up")
	cmd.AddCommand(newWorkerStopCmd(d, &opts.Group))
	cmd.AddCommand(newWorkerRestartCmd(d, &opts.Group))
	cmd.AddCommand(newWorkerStatusCmd(d, &opts.Group))
	return cmd
}

// newWorkerStopCmd stops the worker processes recorded in their pid files. It is
// a subcommand rather than `stop --worker=<g>` because the address of a worker
// is the worker itself: `worker stop` reads as the counterpart of
// `worker --group=<g>`, and --group keeps the meaning it has on the run form.
// newWorkerStopCmd 按 pid 文件停止 worker 进程。它做成子命令而不是
// `stop --worker=<g>`：worker 的地址就是 worker 本身，`worker stop` 读起来正是
// `worker --group=<g>` 的逆操作，而 --group 的含义与启动形态一致。
func newWorkerStopCmd(d Deps, group *string) *cobra.Command {
	var force bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop worker processes using their pid files",
		Args:  noArgs(d, "worker stop"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateWorkerGroup("worker stop", *group, false); err != nil {
				return d.Usage(err)
			}
			if d.StopWorker == nil {
				return fmt.Errorf("worker: stopping a group is not available")
			}
			if !force {
				ok, err := confirmAction(d, "停止", workerSelectorTarget(*group))
				if err != nil || !ok {
					return err
				}
			}
			return d.StopWorker(cmd.Context(), WorkerOptions{Group: *group, Force: force, Timeout: timeout})
		},
	}
	addWorkerStopFlags(d, cmd, &timeout, &force)
	return cmd
}

// newWorkerRestartCmd stops the selected workers and starts them again. Daemon
// defaults to true here: a restart is an operation on a service, and a service
// that used to run detached has to come back detached.
// newWorkerRestartCmd 停止选中的 worker 后重新启动。这里 Daemon 默认为 true：restart
// 是针对服务的操作，原先脱离终端运行的服务必须以脱离态回来。
func newWorkerRestartCmd(d Deps, group *string) *cobra.Command {
	opts := WorkerOptions{Daemon: true}
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop worker processes, then start the group again",
		Args:  noArgs(d, "worker restart"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateWorkerGroup("worker restart", *group, false); err != nil {
				return d.Usage(err)
			}
			if d.RestartWorker == nil {
				return fmt.Errorf("worker: restarting a group is not available")
			}
			if !opts.Force {
				ok, err := confirmAction(d, "重启", workerSelectorTarget(*group))
				if err != nil || !ok {
					return err
				}
			}
			opts.Group = *group
			return d.RestartWorker(cmd.Context(), opts)
		},
	}
	addWorkerStopFlags(d, cmd, &opts.Timeout, &opts.Force)
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", opts.Daemon,
		"restart in the background; --daemon=false stays in the foreground")
	cmd.Flags().DurationVar(&opts.StartupTimeout, "startup-timeout", DefaultStartupTimeout,
		"how long the daemon parent waits for the child to report ready before giving up")
	return cmd
}

func addWorkerStopFlags(d Deps, cmd *cobra.Command, timeout *time.Duration, force *bool) {
	*timeout = d.DefaultStopTimeout
	cmd.Flags().DurationVar(timeout, "timeout", *timeout, "how long to wait for the process to exit")
	cmd.Flags().BoolVar(force, "force", false, "skip confirmation and send SIGKILL when the timeout expires")
}

// workerSelectorTarget names the target of a stop or restart for the prompt.
// workerSelectorTarget 为确认提示描述 stop / restart 的目标。
func workerSelectorTarget(group string) string {
	if g := strings.TrimSpace(group); g != "" && !IsAllWorkerGroup(g) {
		return fmt.Sprintf("worker 分组【%s】", g)
	}
	return "全部 worker 进程"
}

func listWorkers(d Deps) error {
	if len(d.WorkerGroups) == 0 {
		_, err := fmt.Fprintln(d.Out, "no worker groups registered")
		return err
	}
	// Concurrency comes from the application config, so the table answers the
	// question operators actually ask: how many instances will this group run.
	// 并发数来自应用配置，因此这张表直接回答运维最关心的问题：这个分组会起几个
	// 实例。
	rows := make([][]string, 0, len(d.WorkerGroups)+1)
	rows = append(rows, []string{"GROUP", "WORKER", "CONCURRENCY"})
	for _, g := range d.WorkerGroups {
		for _, w := range g.Workers {
			rows = append(rows, []string{g.Name, w.Name, fmt.Sprintf("%d", w.Concurrency)})
		}
	}
	c := console.New(d.Out, d.ErrOut)
	c.Table(rows)
	// The wildcard is the one selector a shell can eat, so the table that lists
	// the groups ends by naming the spelling that survives every shell.
	// 通配符是唯一会被 shell 吃掉的选择符，因此列出分组的表在末尾给出任何 shell 都能
	// 原样传下来的写法。
	c.Note("run every group: worker --group=%s (an unquoted %s is expanded by the shell)", WorkerGroupAllAlias, WorkerGroupAll)
	return nil
}
