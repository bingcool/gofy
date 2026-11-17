package commandline

import (
	"fmt"

	"github.com/spf13/cobra"
)

// StatusOptions are the flags of the status command.
// StatusOptions 是 status 命令的旗标；JSON 为真时输出机器可读的一份，供脚本与探针
// 消费，而不是给人看表格。
type StatusOptions struct {
	PIDFile string
	JSON    bool
}

// newStatusCmd reports the state of the HTTP instance recorded in the pid file.
// It is a subcommand rather than a flag on the root command because the root
// command starts the server: `status` must be readable while the service is
// down, and it must never fall through to a start.
// newStatusCmd 报告 pid 文件记录的 HTTP 实例状态。它做成子命令而不是根命令的旗标，
// 因为根命令会启动服务：服务停着的时候 `status` 必须能读，而且绝不能落到启动上。
func newStatusCmd(d Deps) *cobra.Command {
	opts := StatusOptions{PIDFile: d.DefaultPIDFile}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the status of the running instance",
		Args:  noArgs(d, "status"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if d.Status == nil {
				return fmt.Errorf("status: not available")
			}
			return d.Status(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.PIDFile, "pid-file", opts.PIDFile, "pid file to read instead of the configured one")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "print the status as JSON")
	return cmd
}

// newWorkerStatusCmd reports the state of every worker process that left a pid
// file, or of the selected group. --group is inherited from the parent command,
// so `worker --group=order status` and `worker status --group=order` both work.
// newWorkerStatusCmd 报告所有留下 pid 文件的 worker 进程状态，或只报告选中的分组。
// --group 继承自父命令，`worker --group=order status` 与 `worker status --group=order`
// 因此都可用。
func newWorkerStatusCmd(d Deps, group *string) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the status of worker processes",
		Args:  noArgs(d, "worker status"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if d.StatusWorker == nil {
				return fmt.Errorf("worker: status is not available")
			}
			return d.StatusWorker(cmd.Context(), WorkerOptions{Group: *group, JSON: jsonOut})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the status as JSON")
	return cmd
}
