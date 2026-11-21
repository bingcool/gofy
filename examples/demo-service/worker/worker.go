// Package worker builds the long-running groups started by
// `demo-service worker --group=<group>`.
//
// Each group is a subpackage that owns its members, its settings and its
// construction: group1/abc_register.go and group2/abc_register.go build the group
// (the abc_ prefix keeps that file at the top of the directory, above the member
// files), and every worker gets a file of its own (scan.go, heartbeat.go,
// summary.go) with its ticker, logging and error handling. Register builds all
// groups once; the worker subcommand then runs exactly one of them, so a group
// stays the deployment unit and the same binary serves every deployment.
// 包 worker 构造 `demo-service worker --group=<分组>` 启动的常驻分组。
//
// 每个分组是一个子包，成员、参数与构造都归它自己：group1/abc_register.go 与
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
//
// Consumers come from support/consumer: one constructor per broker, each
// returning something the group registers like any other worker. A subscription
// is therefore a file in the group — settings, handler and nothing else — rather
// than a block in a shared configuration file, which could only describe one
// subscription per broker. The envelope, the idempotency ledger and the error
// classification live behind those constructors, so every group follows the
// same rules.
// 消费者来自 support/consumer：每种 broker 一个构造器，返回的东西像其它 worker 一样
// 注册进分组。因此一个订阅就是分组里的一个文件——设置、handler，没有别的——而不是共享
// 配置文件里的一个块，那种形状每种 broker 只能描述一个订阅。信封、幂等台账与错误分类
// 都在构造器背后，因此每个分组遵循同一套规则。
package worker

import (
	supportai "github.com/bingcool/gofy/support/ai"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/worker"
	"github.com/yourorg/demo-service/serviceclients"
	"github.com/yourorg/demo-service/worker/group1"
	"github.com/yourorg/demo-service/worker/group2"
	"github.com/yourorg/demo-service/worker/group3"
)

// Register builds every group the binary knows about, in the order `worker
// --list` prints them. Adding a group means adding a package and one line here;
// nothing else in the binary changes.
//
// The only argument is the broker connection table: a consumer's own settings —
// topic, group, concurrency — live next to its handler in group1, so there is
// no second source of truth to pass in. The table itself comes from the
// application config, which already decodes kafka/amqp/rocketmq/redis blocks for
// the HTTP process.
// Register 构造二进制认识的所有分组，顺序与 `worker --list` 的输出一致。新增一个分组
// 只需加一个子包加这里一行，二进制其它部分不用改。
//
// 唯一的参数是 broker 连接表：消费者自己的设置——topic、消费组、并发——就在 group1 里
// 紧挨它的 handler，因此没有第二份来源需要传入。连接表本身来自应用配置，那里的
// kafka/amqp/rocketmq/redis 段已经被 HTTP 进程解码过一次。
//
// clients 是进程级的下游客户端，与 broker 连接表并列传入：group3 用它按服务名调用
// demo-service，从而把注册、发现与真实 HTTP 串成一条可独立运行的检查。传 nil 时
// DemoService() 返回 ErrNotConfigured 占位实现，分组仍可构造。
//
// llm 是 llm_ai 那一段配置，只用来决定 group3 是否挂上 LLM 巡检（enabled 为 false
// 时不挂）；它不在这里构造客户端，HTTP 进程因此不会因为一段不使用的 AI 配置而校验凭证。
func Register(conns gofyconsumer.Connections, clients *serviceclients.Clients, llm supportai.Config) ([]*gofyworker.Group, error) {
	workerGroup1, err := group1.NewGroup(conns)
	if err != nil {
		return nil, err
	}
	workerGroup2, err := group2.NewGroup()
	if err != nil {
		return nil, err
	}
	workerGroup3, err := group3.NewGroup(clients, llm)
	if err != nil {
		return nil, err
	}
	return []*gofyworker.Group{workerGroup1, workerGroup2, workerGroup3}, nil
}
