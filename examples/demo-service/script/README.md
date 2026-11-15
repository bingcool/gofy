# script

本目录演示如何用 gofy 的 `script` 机制编写一次性维护脚本（数据修复、索引重
建、批量迁移等）。脚本通过 `script` 子命令运行：不启动 HTTP 服务、不写 pid
文件、不注册到 Nacos，进程执行完 `Run` 后直接退出。

目录内的文件：

- `fix_order.go`：示例脚本 `fix-order`，按订单 ID 取消待支付订单，走 service 层；
- `gen_uuid.go`：示例脚本 `gen-uuid`，只验证 Redis 与 ID 生成器；
- `register.go`：把所有脚本收集成列表，给需要数据库的脚本套上 `app/appio` 装配代，
  再交给 `main.go` 的 `WithScripts`；
- `register_test.go`：`withIO` 包装的单元测试，不连真实数据库。

## 运行

在 `examples/demo-service` 目录下：

```bash
# 列出已注册脚本
go run . script --list
# 输出：
# ┌───────────┬─────────────────────────────────────┐
# │ NAME      │ DESCRIPTION                         │
# ├───────────┼─────────────────────────────────────┤
# │ fix-order │ fix inconsistent order data         │
# ├───────────┼─────────────────────────────────────┤
# │ gen-uuid  │ generate distributed ids from Redis │
# └───────────┴─────────────────────────────────────┘

# 默认直接修改数据；加 --dry-run 只预演、不写库
go run . script fix-order --order-id=123

# 预演：只打印将要执行的修改
go run . script fix-order --order-id=123 --dry-run=true
```

`--dry-run` 默认为 `false`，因此不传该参数时会真正执行数据修改；担心误操作时先
加 `--dry-run=true` 确认输出，再去掉该参数执行。

`script` 后面必须跟脚本名，再后面是脚本自己定义的 flag。脚本运行时会打印
`Runtime.Logger` 的日志；`Run` 返回错误时进程以退出码 `1` 结束（main 里的
`gofy.ExitCode` 负责映射，错误日志在终端显示为红色）。

常用等价写法：

| 命令 | 行为 |
| --- | --- |
| `demo-service script` | 列出脚本 |
| `demo-service script --list` / `-l` | 列出脚本 |
| `demo-service script --help` / `-h` | 查看 `script` 子命令帮助 |
| `demo-service script <name> [flags]` | 运行指定脚本 |

注意：`script` 子命令关闭了自身的 flag 解析，`--order-id` 这类参数会原样交给
脚本；`script fix-order --help` 也会转给脚本的 pflag，只打印该脚本的 flag 用法，
不会显示脚本名和 `Short` 说明。

## 新增脚本

一个脚本就是一个 `gofyscript.Script` 值：

```go
package script

import (
	"context"

	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
	"go.uber.org/zap"
)

// RebuildIndex rebuilds the order index.
func RebuildIndex(cfg *config.Config) gofyscript.Script {
	return gofyscript.Script{
		Name:  "rebuild-index",
		Short: "rebuild the order index",
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("force", false, "rebuild even when the index is current")
		},
		Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
			force, _ := fs.GetBool("force")
			rt.Logger.Info("rebuild index", zap.Bool("force", force))
			return nil
		},
	}
}
```

然后在 `register.go` 里追加，走 service 层的脚本一律套 `withIO`：

```go
func Register(cfg *config.Config) []gofyscript.Script {
	return []gofyscript.Script{
		withIO(cfg, FixOrder()),
		GenUUID(cfg),
		withIO(cfg, RebuildIndex(cfg)),
	}
}
```

不需要数据库、自己打开实例的脚本声明 `WithoutDatabase: true`，`withIO` 会原样
放行，注册时也可以不包（像 `GenUUID(cfg)` 那样）。

`main.go` 已经通过 `gofy.WithScripts(appscript.Register(cfg)...)` 注册
（[main.go:92](../main.go#L92)），新增脚本不需要改其他地方。

### Script 字段

| 字段 | 说明 |
| --- | --- |
| `Name` | 脚本名，小写字母开头，只含 `a-z`、`0-9`、`-`，最长 63 字符；不能重复，不能是 `list`、`help`、`completion`、`restart` |
| `Short` | `script --list` 显示的一行说明 |
| `Flags` | 用 `pflag` 定义参数，可为 nil |
| `Run` | 脚本主体，必填；返回 error 表示失败 |
| `WithoutDatabase` | 为 true 时不为该脚本创建数据库连接，`withIO` 也不启动 app/appio 装配代 |

`gofy.New` 和 `WithScripts` 都会调用 `script.Validate`，命名不合法、重名、
`Run` 为 nil 都会在启动时报错。

## Runtime

`Run` 拿到的 `*gofyscript.Runtime` 是 `*core.Runtime` 的别名：

| 字段/方法 | 说明 |
| --- | --- |
| `Logger` | 应用配置构建的 zap logger |
| `DB` | 框架托管的 `db.default`；配置里没有 `db` 或脚本声明 `WithoutDatabase` 时为 nil |
| `Env` / `Name` | 运行环境与服务名 |
| `Version` | 版本、提交、构建时间 |
| `Context()` | 脚本运行上下文，收到信号时取消 |
| `Fail(err)` | 脚本模式下是空操作，失败请直接 `return err` |

脚本的数据库连接是临时的：没有用 `WithDB` 注入连接时，框架按
`db.default` 建连，`Run` 返回后关闭，不会影响其他进程或长时间占用连接池。

## app/appio 装配代

脚本和请求共用 `service` / `repository` 代码，这些代码按 `appio.WithContext(ctx)`
取本代快照。为此 `register.go` 用 `withIO` 包一层：`Run` 之前
`appio.NewModule(...).Start` 打开并发布完整的一代（`db`、`redis`、`amqp`、
`kafka`、`rocketmq`、`uuidint`），`Run` 返回后 `Stop` 关闭并撤回，与一次请求
借用已发布一代的方式一致。`fix-order` 因此不需要自己发布快照。共用代码在入口
没发布本代时会直接 panic（`appio.WithContext`），不会带着 nil 句柄继续跑。

代价是脚本会按 `config.yaml` 建连全部启用的实例：某个启用中的实例不可达时脚本
在 `Start` 阶段就失败（退出码 `1`），不会带着缺失的句柄跑下去。只需要少数实例
的脚本（例如 `gen-uuid` 只碰 Redis）声明 `WithoutDatabase: true`，`withIO` 原样
放行，由脚本自己打开所需客户端。

## 配置与环境

- 脚本启动时**不会**从 Nacos 拉取配置：`main.go` 只在 `start` 调用时同步，
  脚本读取的是本地 `config.yaml`，避免一次维护任务被远端配置影响；
- 用 `ORDER_CONFIG=/path/to/config.yaml` 可以指定其他配置文件；
- `gen-uuid` 要求 `uuidint.redis_instance_name` 与 `uuidint.key` 显式配置，省略会在
  `config.Load` 阶段报错，不会套用默认值；
- `gen-uuid` 会按 `uuidint.fallback_redis_instance_name` 一并打开备用 Redis 实例；
  留空时只开主实例。备用必须已启用、且不能与 `--redis` 选中的实例同名，否则脚本
  在 `Run` 阶段直接报错，不会带着半套连接继续。备用可以是另一台独立 Redis，切换前
  的 10ms 停顿见 [`support/uuidint/README.md`](../../../support/uuidint/README.md)
  的「失败与降级」；
- 走 `withIO` 的脚本需要数据库：配置里没有 `db` 时 `rt.DB` 为 nil，包装层在
  `Start` 前返回 `apperr.Internal("database is not configured")`，`Run` 不会执行；
- 脚本不需要 HTTP 端口，也不要求 8080 空闲；不会与正在运行的服务实例冲突。

## 错误与退出码

| 情况 | 退出码 |
| --- | --- |
| 成功 | `0` |
| `Run` 返回错误、数据库或 app/appio 实例连接失败 | `1` |
| 脚本不存在、未知 flag、多余参数等用法错误 | `2` |

脚本可以直接返回 `error`，也可以返回 `apperr` 构造的业务错误；错误最终由
`main.go` 里的 `app.Execute` 统一记录并退出。

## 测试建议

把可复用的逻辑放进独立函数，`Run` 只负责解析参数和调用它，这样测试不需要真实
数据库。需要覆盖 `Run` 时，可以直接构造 flag 集合：

```go
fs := pflag.NewFlagSet("rebuild-index", pflag.ContinueOnError)
fs.Bool("force", false, "")
_ = fs.Parse([]string{"--force"})
```

再用 `core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop(), DB: db})` 构造
Runtime，数据库可以用测试库或 `gofytest` 里的辅助设施。
