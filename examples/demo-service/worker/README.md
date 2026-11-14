# worker

本目录演示 gofy 的常驻任务（worker）：一个进程跑一组 worker，分组就是部署单位。
与另外两种形态的区别只有边界：

| 形态 | 入口 | 生命周期 | 典型用途 |
| --- | --- | --- | --- |
| HTTP | `demo-service start` | 常驻，监听端口，写 pid | 对外接口 |
| script | `demo-service script <name>` | 跑完即退，不写 pid | 数据修复、一次性迁移 |
| worker | `demo-service worker --group=<g>` | 常驻，不监听业务端口 | 扫表、MQ 消费、定时汇总 |

worker 进程没有 HTTP 栈，也不注册路由；它拿到的是与 HTTP 处理器相同的 service 与
repository，因此查询逻辑只有一份。

## 目录

```
worker/
├── worker.go              # Register：构造全部分组，交给 main
├── group1/
│   ├── abc_register.go    # group1 的 NewGroup
│   ├── scan.go            # order-scan：扫表 + 它的 Defaults
│   └── heartbeat.go       # order-heartbeat：存活心跳
└── group2/
    ├── abc_register.go    # group2 的 NewGroup
    └── summary.go         # report-summary：小时级汇总 + 它的 Defaults
```

`abc_` 前缀只是为了让入口文件排在成员文件之前，没有语义；下划线开头不行，Go 会忽略
`_register.go`，`NewGroup` 会跟着从编译里消失。

每个分组自成一个包，参数、构造、成员都在包内。给 group1 加一个 worker，不需要
碰 group2 的任何文件。

## 两个分组各做什么

| 分组 | worker | 实例数 | 节奏 | 说明 |
| --- | --- | --- | --- | --- |
| group1 | `order-scan` | 2 | 5s | 统计待支付订单；真实队列在这里调 `claim.Queue.Claim` |
| group1 | `order-heartbeat` | 1 | 10s | 不依赖数据库，留一行可 grep 的存活日志 |
| group2 | `report-summary` | 1 | 1h | 按状态聚合订单，多实例会重复写同一份报表 |

拆两个分组不是为了好看：group1 是秒级、随订单量扩缩；group2 是小时级、只要一份。
两者的副本数、发布节奏、资源画像都不同，混在一个进程里只能取交集。

## 运行

`--group` 必填，缺省直接报用法错误并退出：

```bash
$ go run . worker
[FAIL] gofy: usage error: worker: --group is required (see worker --list)
```

先看清有哪些分组、各自会起几个实例（不连数据库、不启动任何 worker）：

```bash
$ go run . worker --list
┌────────┬─────────────────┬─────────────┐
│ GROUP  │ WORKER          │ CONCURRENCY │
├────────┼─────────────────┼─────────────┤
│ group1 │ order-scan      │ 2           │
├────────┼─────────────────┼─────────────┤
│ group1 │ order-heartbeat │ 1           │
├────────┼─────────────────┼─────────────┤
│ group2 │ report-summary  │ 1           │
└────────┴─────────────────┴─────────────┘
```

启动整组。前台运行，日志同时写 stdout 与文件：

```bash
$ go run . worker --group=group1
{"level":"info","caller":"gofy/worker_host.go:112","msg":"worker group started","group":"group1","workers":2,"drain_timeout":20}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#0","interval":5}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#1","interval":5}
```

`workers:2` 是这个分组的成员数（`order-scan` 与 `order-heartbeat`），不是实例数；
`order-scan` 起了 2 个 goroutine，靠日志里的 `worker` 字段区分（`#0`、`#1`）。

本地调试时可以用通配符把全部注册分组放进同一个进程：

```bash
$ go run . worker --group='*'
{"level":"info","caller":"gofy/worker_host.go:145","msg":"worker groups started","workers":3,"groups":["group1","group2"]}
```

它只解决“一条命令起全部循环”，不改变部署语义：pid 变成 `-worker-all.pid`，
`stop --worker='*'` 一次停掉全部，任一分组失败会取消其余分组。引号不能省，否则 shell 先
展开 `*`；也不能和 `--name` 同用——worker 名只属于一个分组，跨组过滤会静默跳过不含它的组。

只跑组内某个 worker，或临时改实例数（不改代码）：

```bash
go run . worker --group=group1 --name=order-scan
go run . worker --group=group1 --name=order-scan --name=order-heartbeat
go run . worker --group=group1 --concurrency=1     # 组内所有 worker 都变 1 个实例
```

`--name` 可重复，也接受逗号分隔；`--concurrency=0`（默认）表示沿用 `worker.Func(name, n, run)`
里的数字。覆盖只在本次启动生效：`worker --list` 读的是代码里的值，不会跟着 `--concurrency`
变。

> 本地调试建议先 `go build -o /tmp/demo .` 再跑二进制：`go run` 会把信号交给 go 工具链，
> Ctrl+C 下 worker 的 drain 过程和退出码看不准确。

## 进程留下的东西

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| pid 文件 | `app/demo-service-worker-group1.pid` | 默认 pid 文件的 `-worker-<分组>` 后缀；未配置 `pid_file` 则不写 |
| pid 文件 | `app/demo-service-worker-all.pid` | `--group='*'` 时用这个后缀，`stop --worker='*'` 解析同一个文件 |
| 日志 | `storage/log/worker/<date>/info/worker-h15.log` | 整组启动写 `worker-<host>.log` |
| 日志 | `storage/log/worker/<date>/info/order-scan-h15.log` | `--name` 只跑一个 worker 时用它的名字 |

日志文件不带分组名：分组已经在每行日志的 `worker` 字段里（`group1/order-scan#0`）。
要一个 worker 一组文件，就用 `--name` 单独起；整组启动共用一份，靠字段区分。

demo 没开就绪探针：`options.go` 里没有 `gofy.WithWorkerHealth`，worker 进程不监听任何
端口。要接 readinessProbe 就在那里加一行地址，行为是启动前 503、就绪后 200、drain 时
回到 503。

## 停止

```bash
$ go run . stop --worker=group1 --force
[INFO] 正在停止 worker 分组【group1】 (pid 80656)
[ OK ] worker 分组【group1】 已停止 (pid 80656)

# 停掉 --group='*' 起的那个进程
$ go run . stop --worker='*' --force
[INFO] 正在停止 worker 分组【all】 (pid 80656)
[ OK ] worker 分组【all】 已停止 (pid 80656)
```

不带 `--force` 会先问一次 `你确定 [停止] 应用【demo-service】? (yes or no):`。
停止走 SIGTERM：worker 的拉取先停（`runCtx` 取消），在途任务继续跑到
`drain_timeout`（默认 20s，`gofy.WithWorkerDrainTimeout` 可改），然后进程退出，pid
文件清理。超时未退才在 `--force` 下补 SIGKILL。

`stop` 的粒度是分组，`stop --worker=group1` 不会碰到 group2，也不会碰 HTTP 进程。
`restart` 只用于 HTTP 实例，worker 用 `stop` + 重新启动。

## 参数

每个 worker 的参数就写在它自己的文件里，结构体和取值放在一起，一个文件读完就知道会起几个
实例、多久跑一次。`config.yaml` 没有 `worker:` 段：

```go
// group1/scan.go
type ScanWorkerConfig struct {
	Concurrency int           // 进程内扫描实例数
	Interval    time.Duration // 两次扫描的间隔
}

var scanDefaults = ScanWorkerConfig{
	Concurrency: 2,
	Interval:    5 * time.Second,
}
```

`abc_register.go` 只负责把 worker 装进分组，不碰数字：`newScanWorker(scanDefaults)`。
改参数就改这个 `scanDefaults`；临时改实例数用 `--group=group1 --concurrency=4`，不必动代码。
结构体上没有 `yaml` 标签，配置里写 `worker:` 会被严格解码拒绝——它不再是配置来源。

三条约束：

- **实例数在 `worker.Func` 里是 `uint8`**：代码里给的是字面量，超出 `1..255` 编译期就会报错，
  不会静默截断成另一个数字。
- **`--concurrency` 优先级最高**：命令行覆盖 worker 自己声明的数字，临时压测不必改文件。
- **worker 名全应用唯一**：日志文件名取自 worker 名，两个分组用同一个名字会写到同一个
  文件（demo 里是 `order-scan`、`order-heartbeat`、`report-summary`）。

并发实例共享同一个 `Worker` 值，`Run` 里不能有可变的接收者状态；需要每实例独立的数据
就放在 `Run` 内部，从 `worker.IdentityFrom(ctx)` 取 `group/name#index`。

## 新增任务

组内加一个 worker，只动那个分组包：

1. 新建成员文件，例如 `group1/settle.go`，用 `worker.Func("order-settle", 1, run)` 定义；
2. 参数结构体与它的 `xxxDefaults` 都写在成员文件里（照 `ScanWorkerConfig` + `scanDefaults`），
   `abc_register.go` 只把 worker 追加进 `NewGroup`；
3. 在同一个文件的 `NewGroup` 里把它追加进去；
4. `go run . worker --list` 核对，列表里出现新名字即可。

## 新增分组

1. 新建子包 `worker/group3/`，加 `abc_register.go`（只含 `NewGroup`）与成员文件（含各自的参数结构体与 defaults）；
2. `worker/worker.go` 的 `Register` 里构造该分组并加进返回列表；
3. `go run . worker --list` 核对，`worker --group=group3` 即可启动。

`main.go` 与部署清单不用改结构：复制一份 Deployment，改 `--group=group3` 和副本数即可。
同一个镜像服务所有分组，因此不必为每个分组单独出一个二进制。

## 排障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `[FAIL] worker: --group is required` | 漏了 `--group` | `worker --list` 看分组名 |
| 退出码 1，终端打印 `[FAIL] gofy: worker group not found: "x"` | `--group` 写错 | `worker --list` 看有哪些分组 |
| 退出码 1，终端打印 `[FAIL] worker: unknown worker: "x" in group "group1"` | `--name` 不在该分组里 | `worker --list` 看组内成员 |
| 进程起来就退出、退出码 0 | `Run` 在 ctx 取消前返回了 nil | 循环必须阻塞到 `ctx.Done()` |
| 日志找不到 | 目录看错 | `storage/log/worker/<date>/info/`，整组是 `worker-<host>.log` |
| `stop --worker=x` 报 not running | 未配置 `pid_file`，或分组名与启动时不同 | 查 `app/*-worker-<group>.pid` |
| `--group='*'` 配 `--name` 报 usage error | 通配符按组运行，`--name` 只能指定一个组内的 worker | 改成 `--group=<拥有该 worker 的组>` |
| `--list` 的数字和实际不同 | 用了 `--concurrency` | 覆盖只在本次启动生效，`--list` 读的是代码里的数字 |

## 相关文档

- `docs/worker-guide.md`：四种循环（claim / MQ / Redis 队列 / lease）的写法与部署建议
- `docs/worker-daemon-design.md`：设计与验收记录
- `support/worker/README.md`：包 API、`Policy` 重启策略、drain 语义
- `examples/demo-service/storage/log/README.md`：日志目录与文件命名规则
