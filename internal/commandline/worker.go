package commandline

import (
	"fmt"
	"strings"

	"github.com/bingcool/gofy/internal/console"
	"github.com/spf13/cobra"
)

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
// group, or address every worker that left a pid file.
// IsAllWorkerGroup 报告 group 是否选中所有 worker 进程。通配符与它的别名在任何地方
// 含义相同：运行全部已注册分组，或指向所有留下 pid 文件的 worker。
func IsAllWorkerGroup(group string) bool {
	g := strings.TrimSpace(group)
	return g == WorkerGroupAll || g == WorkerGroupAllAlias
}

// WorkerOptions are the flags of the worker command.
// WorkerOptions 是 worker 命令的旗标。
type WorkerOptions struct {
	// Group selects the group to run.
	// Group 选择要运行的分组。
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
	// Daemon detaches into the background and exits the parent process instead of
	// holding the terminal that started it.
	// Daemon 脱离到后台并让父进程退出，而不是占用启动它的终端。
	Daemon bool
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
			if strings.TrimSpace(opts.Group) == "" {
				return d.Usage(fmt.Errorf("worker: --group is required (use --group=%s for every group, see worker --list)", WorkerGroupAllAlias))
			}
			if d.RunWorker == nil {
				return fmt.Errorf("worker: no worker groups registered")
			}
			return d.RunWorker(cmd.Context(), opts)
		},
	}
	// --group is persistent so the selector keeps working from subcommands added
	// later, in the same position as on the run form.
	// --group 是持久旗标，后续加的子命令因此也能用同一个选择符，位置与启动形态一致。
	cmd.PersistentFlags().StringVar(&opts.Group, "group", "",
		"worker group to run, or --group="+WorkerGroupAllAlias+" ("+WorkerGroupAll+" when quoted) for every group")
	cmd.Flags().StringSliceVar(&opts.Names, "name", nil, "run only the named workers (repeatable)")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 0, "override the instance count of every worker (0 = config)")
	cmd.Flags().BoolVar(&opts.List, "list", false, "list groups and workers, then exit")
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", false, "detach into the background and exit the parent process")
	return cmd
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
