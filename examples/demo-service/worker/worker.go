// Package worker builds the long-running groups started by
// `demo-service worker --group=<group>`.
//
// Two files per group keep it obvious where a change belongs: the group file
// (group1_register.go, group2_register.go) holds its membership and its config,
// and every worker gets a file of its own (scan.go, heartbeat.go, summary.go)
// with its ticker, logging and error handling. Register builds all groups once;
// the worker subcommand then runs exactly one of them, so a group stays the
// deployment unit and the same binary serves every deployment.
// 包 worker 构造 `demo-service worker --group=<分组>` 启动的常驻分组。
//
// 每个分组两个文件，改动该落在哪里一目了然：分组文件（group1_register.go、
// group2_register.go）放成员与配置，每个 worker 各占一个文件（scan.go、heartbeat.go、
// summary.go），ticker、日志与错误处理都留在里面。Register 一次构造全部分组，worker
// 子命令只运行其中一个，因此分组始终是部署单位，同一个二进制可以服务所有部署。
//
// Workers reuse the same repository-level data access as the HTTP handlers instead
// of reimplementing queries, and they never touch support.Router: a worker
// process has no HTTP stack.
// worker 复用与 HTTP 处理器相同的数据访问，而不是另写一套查询；它们也不碰
// support.Router：worker 进程里没有 HTTP 栈。
package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// Register builds every group the binary knows about, in the order `worker
// --list` prints them. Adding a group means adding a file and one line here;
// nothing else in the binary changes.
// Register 构造二进制认识的所有分组，顺序与 `worker --list` 的输出一致。新增一个分组
// 只需加一个文件加这里一行，二进制其它部分不用改。
func Register(cfg Config) ([]*gofyworker.Group, error) {
	group1, err := newGroup1(cfg.Group1)
	if err != nil {
		return nil, err
	}
	group2, err := newGroup2(cfg.Group2)
	if err != nil {
		return nil, err
	}
	return []*gofyworker.Group{group1, group2}, nil
}
