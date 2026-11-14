# 常驻任务（worker）使用指南

这份文档讲**怎么用**。为什么这样设计、否决了哪些方案，见 [`worker-daemon-design.md`](./worker-daemon-design.md)。

## 1. 一句话模型

- HTTP 服务负责请求，`start` 启动；
- 常驻任务负责循环，`worker --group=<g>` 启动；
- 两者是**同一个镜像、两个进程**，共用 config.yaml、日志配置与数据库；
- 分组（group）是部署单位：组内 worker 同生共死，一个分组对应一个 Deployment；
- 并发是 **goroutine 数量**，不是进程数。

```
demo-service start                      → HTTP，不启动任何 worker
demo-service worker --group=order       → 只跑 order 组，不开端口、不注册路由
demo-service stop                       → 停 HTTP
demo-service stop --worker=order        → 只停 order 组，不碰 HTTP
```

## 2. 先选原语，再写循环

多副本部署下，"会不会重复处理"取决于选哪个原语，而不是锁写得多严。

| 工作的形态 | 用什么 | 多副本安全的原因 |
| --- | --- | --- |
| 消息（Kafka / AMQP / RocketMQ） | `support/worker/consumer/*` | 消费组把分区/队列分给成员，同一分区只有一个消费者 |
| Redis 队列 | `support/queues` + `queuesconsumer` | 出队即删除，Lua 原子完成 |
| 表里 `status=0` 的待办行 | `support/claim` | `FOR UPDATE SKIP LOCKED` 行级抢占，配合 owner CAS |
| 绝不能执行两次（对账、日切、清理） | `support/lease` | 选主 + fencing token，只有持有者能写 |
| 跟随本 HTTP 实例的循环（nacos watch、预热） | `support.Starter` | 本来就该每个副本各跑一份 |

**不要**把扫表循环挂到 `Starter` 上：那样每个 Pod 都会扫同一张表。

## 3. 装配

### 3.1 main 里注册分组

worker 与 script 的装配位置相同，都在 `main`，用同一套 service/repository：

```go
svc := order.NewService(order.NewRepository(db))

scan, err := worker.NewGroup("order",
	worker.Func("order-scan", 2, func(ctx context.Context) error {
		return scanLoop(ctx, svc) // 与 HTTP handler 调用同一个 service 方法
	}),
)
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithName("demo-service"),
	gofy.WithDatabase(cfg.DB["default"]),
	gofy.WithWorkerGroups(scan),
	gofy.WithWorkerDrainTimeout(20*time.Second),
	gofy.WithWorkerHealth("0.0.0.0:9091"), // 可选，配合 readinessProbe
)
```

`WithWorkerGroups` 可以一次传多个分组；装配与配置的写法见 §3.4。

### 3.2 命令

| 命令 | 作用 |
| --- | --- |
| `app worker --list` | 列出分组、worker 与并发数，不连数据库 |
| `app worker --group=order` | 跑 order 组全部 worker |
| `app worker --group=order --name=order-scan` | 只跑组内指定 worker（可重复） |
| `app worker --group=order --concurrency=4` | 覆盖组内所有 worker 的实例数 |
| `app worker --group='*'` | 在一个进程里跑全部已注册分组，仅本地调试与冒烟测试 |
| `app stop --worker=order` | 按 pid 文件停掉该组 |
| `app stop --worker=order --force` | 跳过确认，超时发 SIGKILL |
| `app stop --worker='*'` | 停掉 `--group='*'` 起的那个进程（pid 后缀为 `all`） |

`--group='*'` 要加引号，否则 shell 会先把它展开成当前目录的文件名。通配符运行不允许
再配 `--name`：worker 名只属于某一个分组，跨组过滤会静默跳过不含它的分组，直接写
`--group=<拥有该 worker 的组>` 更清楚。

### 3.3 进程留下的东西

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| pid 文件 | `<pid_file>` 加 `-worker-<group>` 后缀，如 `app/demo-service-worker-order.pid` | 未配置 `pid_file` 则不写 |
| 日志 | `storage/log/worker/<date>/info/<worker>.log`、`error/<worker>.log`、`error/<worker>-system_error.log` | 文件名取 `--name` 指定的 worker；整组启动为 `worker` |
| 端口 | 默认不开；配了 `WithWorkerHealth` 才有 `/healthz` | 只报就绪，不做业务健康 |

### 3.4 多个分组

一个进程只跑一个分组，但**一个二进制注册全部分组**：`--group` 在启动时选一个，其余不启动。
这样同一个镜像既能当 HTTP 服务，也能按需要跑任意分组，不必为每个分组单独出一个二进制。

```go
// worker/worker.go：每个分组一个子包，这里只列清单
func Register(cfg Config) ([]*worker.Group, error) {
	first, err := group1.NewGroup(cfg.Group1)
	if err != nil {
		return nil, err
	}
	second, err := group2.NewGroup(cfg.Group2)
	if err != nil {
		return nil, err
	}
	return []*worker.Group{first, second}, nil
}

// main.go
groups, err := appworker.Register(cfg.Worker)
if err != nil {
	log.Fatalf("build worker groups: %v", err)
}
opts = append(opts, gofy.WithWorkerGroups(groups...))
```

实例数在分组构造时传给 `worker.Func(name, n, fn)`，所以 `main` 不维护第二张
`group/worker → n` 的表；要用命令行临时扩缩，再加 `worker --concurrency=<n>`。

配置按分组名分层，公共项留在 `worker` 顶层；每个分组块的类型定义在它自己的子包里，加一个
worker 不会碰到别的分组。分组块可以整段省略：`default` 标签在解码前生效，默认值就写在分组
自己的结构体上，块只在需要按环境覆盖时才出现：

```yaml
worker:
  health_addr: 127.0.0.1:9091   # 进程级：一个分组一个进程
  group1:
    scan:
      concurrency: 2
      interval: 5s
  group2:
    summary:
      concurrency: 1
      interval: 1h
```

demo 的 `config.yaml` 没有 `worker:` 段：扫描参数与消费者设置都写在各分组包里，一个订阅
一个文件；`config.yaml` 只留 broker 连接参数与 `pid_file` 这类公共项。探针也没启用。

各维度的隔离程度不一样，部署时按这张表对照：

| 维度 | 随分组隔离 | 说明 |
| --- | --- | --- |
| 进程 / Deployment | 是 | 一个分组一个 `worker --group=<g>`，扩缩容、发布互不影响 |
| pid 文件 | 是 | `-worker-<group>` 后缀，`stop --worker=<g>` 只停该组 |
| 日志 | 是 | 各自进程写各自的文件；`--name` 启动用 worker 名，整组启动用 `worker`，不按分组命名 |
| 并发数 | 是 | 键为 `group/worker`，写错名字 `New` 直接失败（不再静默退回 1 个实例） |
| 探针端口 | **否** | `WithWorkerHealth` 是进程级地址；k8s 里每组一个 Pod 不冲突，同机跑两组要换端口或用 `ORDER_CONFIG` 换配置 |

分组内的文件就两层：入口 `abc_register.go` 装 `Config` 与 `NewGroup`（分组本身、配置、
构造都归它），每个 worker 各占一个成员文件（`scan.go`、`heartbeat.go`）。入口带 `abc_`
前缀是为了排在最上面——裸的 `register.go` 按字母序落在 `heartbeat.go`、`scan.go` 之后，
看目录时入口沉在最底下；下划线前缀不行，Go 忽略 `_` 或 `.` 开头的文件，`_register.go`
等于把它从编译里摘掉，`Config` 与 `NewGroup` 随之消失，`Register` 里直接报 `undefined`。
前缀得是真实字符，`abc_` 就是给排序用的空壳，没有语义。

新增一个分组只要三步：新建一个分组子包（如 `group3/abc_register.go` + 成员文件，含配置结构）、在 `Register` 的返回列表里加
一项、`config.yaml` 加同名配置块。`main.go` 与部署清单不用改结构——复制一份清单，改
`--group` 和副本数即可。

**部署时不要在一个进程里跑多个分组**：pid、探针、`stop` 的粒度都是分组，混跑会出现"停一半"，
而且一个分组里某个 worker 反复失败会把另一个分组一起带走。分组就是部署单位，副本数与发布
节奏不同才是拆组的理由。

唯一的例外是 `worker --group='*'`：它把全部分组放进一个进程，pid 文件变成
`-worker-all.pid`，`stop --worker='*'` 一次停掉全部，任一分组失败会取消其余分组。这套语义
是给本地开发和冒烟测试用的——一条命令起来验证所有循环，省掉三次进程管理；它不解决部署问题，
生产仍然一组一个 Deployment。

## 4. 四种循环的写法

### 4.1 扫表：claim 认领

```go
queue, err := claim.New(db, claim.Config{
	Table:       "job_task",
	Job:         "order-settle",
	Lease:       90 * time.Second, // ≥ 处理耗时 p99 的 3 倍
	MaxAttempts: 5,
})

func scanLoop(ctx context.Context, q *claim.Queue) error {
	id, _ := worker.IdentityFrom(ctx)
	return worker.Ticker(ctx, 2000, func(ctx context.Context) error {
		drain := worker.DrainContext(ctx) // 停机后仍允许在途处理跑完
		tasks, err := q.Claim(drain, id.String(), 0)
		if err != nil {
			return err // Ticker 记一条 warn 后继续下一个 tick
		}
		for _, task := range tasks {
			if err := handle(drain, task); err != nil {
				_ = q.Fail(drain, task, err) // 退避后重试，超限转死亡态
				continue
			}
			_ = q.Succeed(drain, task) // owner CAS 失败返回 ErrLeaseLost
		}
		return nil
	})
}
```

`worker.Ticker` 管节拍与失败隔离：回调返回错误只记日志，panic 被恢复，循环继续；退出只有一条
路——ctx 取消。自己写 `ticker` + `select` 时最容易犯的错就是 `return err`，那会让一次数据库抖动
把整个 worker 交给重启策略。

建表语句用 `claim.DDL("job_task")`，里面带 `idx_claim(job,status,next_run_at,id)`——没有这个索引，`SKIP LOCKED` 会退化成全表扫描加锁。

`Succeed`/`Fail` 返回 `claim.ErrLeaseLost` 时，说明租约过期后行被别人接走了。**这时不要重放副作用**：短信可能已经发过两次。

### 4.2 MQ 消费

```go
consumer := kafkaconsumer.New("order-consumer", client, kafkaconsumer.Config{
	Handler: func(ctx context.Context, record *kgo.Record) error {
		return svc.Handle(ctx, record.Value)
	},
	Options: kafka.ConsumeOptions{ /* 组、topic、提交策略 */ },
})
```

适配器替你做了三件事：把投递 context 换成 drain context（否则 SIGTERM 一到，在途消息立刻取消、未 ack、重投）、只在停机时 `Close`（出错关掉会让重启策略复用不了同一个消费者）、错误原样交给重启策略。

`amqpconsumer` / `rocketmqconsumer` 的用法相同，只换构造参数。

适配器之上还有一层：`support/consumer` 把它们和信封、数据库幂等台账、错误分类打包成"一个订阅一个文件"。业务只写配置与 handler：

```go
var orderKafka = consumer.KafkaConfig{
	Name: "order-consumer", Topic: "demo.order.events", Group: "order-group",
	Idempotency: true,
	Handler: func(ctx context.Context, cfg consumer.KafkaConfig, msg consumer.KafkaMessage) error {
		return svc.Handle(ctx, msg.Envelope) // cfg 是本订阅；msg.Record 是原生 kgo.Record
	},
}

w, err := consumer.NewKafka(conns, orderKafka) // 满足 worker.Worker，直接进分组
```

多一条事件流就是多一个文件，不必回到配置文件——这正是它取代 `worker.yaml` 的原因。`examples/demo-service/worker/group1` 是完整用法。

| 关注点 | 做法 |
| --- | --- |
| 幂等 | `(consumer, message_id)` 唯一索引的台账表，INSERT 抢行即认领 |
| 错误分类 | 永久错误（解不出、未知 type）确认并记 dead；其它错误留给 broker 重投 |
| 幂等键 | Kafka 用 `message_id` 头或 `topic/partition@offset`；AMQP 用 `MessageId` |
| 链路 | 从消息属性提取 `traceparent`，把消费挂到生产者那条 trace 上 |

### 4.3 Redis 队列

```go
consumer := queuesconsumer.New("order-notify", queue, queuesconsumer.Config{
	Handler: func(ctx context.Context, msg *queues.Message) error {
		return svc.Notify(ctx, msg.Body)
	},
})
```

`queues.Queue.Consume` 负责空轮询退避、失败重投、panic 兜底。注意它**取出即删除**，对在途消息是"至多一次"：不能丢的场景用 MQ 或 claim。

### 4.4 全局单例：lease

```go
locker, _ := redislease.New(redisClient, redislease.Config{Prefix: "demo:"})

w := worker.Func("settle", 1, func(ctx context.Context) error {
	return lease.Run(ctx, locker, "settle:reconcile", lease.RunConfig{
		TTL:      15 * time.Second, // 续约按 TTL/3 进行
		Interval: 5 * time.Minute,  // 两次执行之间的间隔
	}, func(ctx context.Context, token int64) error {
		return settle(ctx, db, token)
	})
})
```

`lease.Run` 在租约被占时等待重试而不是返回，所以直接当 worker 的 `Run` 用。业务写入带上 token：

```sql
UPDATE settlement SET status = 2, fence = :token
 WHERE id = :id AND fence < :token;   -- 旧持有者的 token 更小，写入影响 0 行
```

不写 `fence < :token` 的锁等于把正确性押在"锁一定不会失效"上，而 GC 停顿、Redis 主从切换都会让它失效。

没有 Redis 时可以换成 Kubernetes 原生实现（`gofy-adapter/adapter/lease/k8slease`）：

```go
locker, err := k8slease.NewForConfig(restConfig, k8slease.Config{
	Namespace: "demo",
	Identity:  os.Getenv("POD_NAME"), // 必须每进程唯一
})
```

它用 `coordination.k8s.io/v1` 的 Lease 对象，RBAC 只要 leases 的 get/create/update；key 必须符合 DNS-1123（`settle-reconcile`，不能带冒号）。

## 5. 部署

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-worker-order
spec:
  replicas: 2                       # 抢占型/消费型都可以 >1；全局型用 1 + lease 兜底
  strategy:
    rollingUpdate: { maxUnavailable: 0, maxSurge: 1 }
  template:
    spec:
      terminationGracePeriodSeconds: 30   # 必须 > drain timeout（默认 20s）
      containers:
        - name: worker
          image: demo-service:latest
          command: ["/app/demo-service", "worker", "--group=order"]
          readinessProbe:
            httpGet: { path: /healthz, port: 9091 }
            periodSeconds: 5
            failureThreshold: 2
          resources:
            requests: { cpu: 100m, memory: 128Mi }
```

- 就绪探针只在配了 `WithWorkerHealth` 时可用；启动前 503、drain 时 503。
- `maxUnavailable: 0` 让滚动更新先起新 Pod 再停旧 Pod，消费组不会出现空窗口。
- 不要给 worker 配 liveness 探针：进程崩溃由 k8s 重启，业务卡住不该靠重启解决。

**停机预算**：`terminationGracePeriodSeconds` > `gofy.WithWorkerDrainTimeout` > 单次处理耗时。默认 30s > 20s，留 5s 给关连接与 flush 遥测。

## 6. 排障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `worker --group=x` 报 `worker group not found` | 分组没注册或名字拼错 | `worker --list` 核对 |
| 进程起来就退出，退出码 0 | `Run` 在 ctx 取消前返回了 nil | 循环必须阻塞到 `ctx.Done()`；这是 `ErrWorkerExited` |
| 日志文件找不到 | 看错目录 | `storage/log/worker/<date>/info/`，文件名是 worker 名（整组启动是 `worker`） |
| `stop --worker=x` 报 not running | 未配置 `pid_file`，或组名与启动时不同 | 检查 `app/*-worker-<group>.pid` |
| 两个 Pod 处理同一批行 | 没用 claim / 用了 `SELECT` 再 `UPDATE` | 换 `claim.Queue`，别自己写 SQL |
| 同一个全局任务跑了两次 | 只加锁没带 fencing token | 业务表加 `fence` 列，写入带 `fence < :token` |
| 每次发版都重复消费一批消息 | 停机时直接取消，没走 drain | 用 `consumer` 适配器，别自己写 `Consume` |
| healthz 一直 503 | 分组还在启动，或已进入 drain | 看日志有没有 `worker group started` |

## 7. 与 HTTP 进程的边界

| | HTTP (`start`) | worker (`worker --group`) |
| --- | --- | --- |
| 路由注册 | 有 | 无 |
| 端口监听 | 有 | 仅 healthz（可选） |
| Nacos 注册 | 有 | 无 |
| `support.Starter` | 会执行 | 不执行 |
| `script` | 可通过子命令 | 可通过子命令 |
| 停机 | 先停监听再 drain 请求 | 先停拉取再 drain 在途 |

worker 里不要用 `*gin.Context`、HTTP middleware 或 `support.Router`；需要"当前用户"这类请求级信息时，从消息体或任务行里读。
