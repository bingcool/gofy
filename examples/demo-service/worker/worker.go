// Package worker builds the long-running groups started by
// `demo-service worker --group=<group>`.
//
// Each group is a subpackage that owns its members, its config and its
// construction: group1/abc_register.go and group2/abc_register.go build the group
// (the abc_ prefix keeps that file at the top of the directory, above the member
// files), and every worker gets a file of its own (scan.go, heartbeat.go,
// summary.go) with its ticker, logging and error handling. Register builds all
// groups once; the worker subcommand then runs exactly one of them, so a group
// stays the deployment unit and the same binary serves every deployment.
// 包 worker 构造 `demo-service worker --group=<分组>` 启动的常驻分组。
//
// 每个分组是一个子包，成员、配置与构造都归它自己：group1/abc_register.go 与
// group2/abc_register.go 负责构造分组（abc_ 前缀让该文件排在目录最上面，在成员文件
// 之前），每个 worker 各占一个文件（scan.go、heartbeat.go、summary.go），ticker、
// 日志与错误处理都留在里面。Register 一次构造全部分组，worker 子命令只运行其中一个，
// 因此分组始终是部署单位，同一个二进制可以服务所有部署。
//
// Workers reuse the same repository-level data access as the HTTP handlers instead
// of reimplementing queries, and they never touch support.Router: a worker
// process has no HTTP stack.
// worker 复用与 HTTP 处理器相同的数据访问，而不是另写一套查询；它们也不碰
// support.Router：worker 进程里没有 HTTP 栈。
package worker

import (
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/worker/group1"
	"github.com/yourorg/demo-service/worker/group2"
)

// Register builds every group the binary knows about, in the order `worker
// --list` prints them. Adding a group means adding a package and one line here;
// nothing else in the binary changes.
// Register 构造二进制认识的所有分组，顺序与 `worker --list` 的输出一致。新增一个分组
// 只需加一个子包加这里一行，二进制其它部分不用改。
func Register(cfg Config) ([]*gofyworker.Group, error) {
	first, err := group1.NewGroup(cfg.Group1)
	if err != nil {
		return nil, err
	}
	second, err := group2.NewGroup(cfg.Group2)
	if err != nil {
		return nil, err
	}
	return []*gofyworker.Group{first, second}, nil
}
