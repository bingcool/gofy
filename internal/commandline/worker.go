package commandline

import (
	"fmt"
	"strings"

	"github.com/bingcool/gofy/internal/console"
	"github.com/spf13/cobra"
)

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
				return d.Usage(fmt.Errorf("worker: --group is required (see worker --list)"))
			}
			if d.RunWorker == nil {
				return fmt.Errorf("worker: no worker groups registered")
			}
			return d.RunWorker(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Group, "group", "", "worker group to run")
	cmd.Flags().StringSliceVar(&opts.Names, "name", nil, "run only the named workers (repeatable)")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 0, "override the instance count of every worker (0 = config)")
	cmd.Flags().BoolVar(&opts.List, "list", false, "list groups and workers, then exit")
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
	console.New(d.Out, d.ErrOut).Table(rows)
	return nil
}
