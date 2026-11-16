# Gofy 最新代码架构复审与问题清单

> 审查日期：2026-11-16  
> 审查范围：Gofy 主仓库、`gofy-interface-api`、`gofy-adapter`，以及主仓库中的 `examples/demo-service`。  
> 审查方式：基于 `db271b5` 的代码（复核说明见 §1.1.1），按实际调用链检查实现、状态转换、资源所有权及失败路径；不是只按提交说明或 README 推断。  
> 重要说明：本报告是源码级静态审查。本轮没有在本地真实 MySQL、Kafka、RabbitMQ、RocketMQ、Redis 或 Kubernetes 环境中执行完整集成测试，也没有运行全仓库 `make check-all`。涉及并发、时序和外部系统行为的结论，均按当前源码路径作出；文中标明了需要补充的验证用例。

## 1. 审查基线与整体判断

### 1.1 代码基线

本轮读取的仓库与提交基线：

| 仓库 | 本轮基线 | 关注范围 |
|---|---|---|
| `bingcool/gofy` | `db271b562f0169ed27a9a0d275d4d2930a680be6` | Generation、Reload、IO Snapshot、Worker Host、Worker Runner、Consumer、Claim、Lease、Nacos、demo-service |
| `bingcool/gofy-interface-api` | `5e5e1c3c8fd69353585b0b3073b868d12ad07f66` | Client options、SSE 重连、ConnectTimeout 派生 Transport 生命周期 |
| `bingcool/gofy-adapter` | `c5ef2a90acda4c92dcd91c05bb53482af331f7a0` | Kubernetes Lease Adapter、单进程 Acquire 约束、依赖边界和文档 |

代码定位以对应仓库默认分支文件为准，重要文件包括：

- Gofy：`generation.go`、`reload.go`、`start.go`、`worker_host.go`、`worker_health.go`、`stop.go`、`internal/pidfile/pidfile.go`、`internal/process/detach_unix.go`。
- Worker：`support/worker/runner.go`、`support/worker/ready.go`、`support/worker/consumer/*`。
- Consumer：`support/consumer/consume.go`、`support/consumer/kafka.go`、`support/consumer/amqp.go`、`support/consumer/rocketmq.go`、`support/consumer/queues.go`。
- 业务示例：`examples/demo-service/main.go`、`config.go`、`reload.go`、`worker/worker.go`、`worker/group1/*`、`app/appio/*`、`app/module/order/query/*`。
- 接口 Client：`gofy-interface-api/client/client.go`、`client/options.go`、`client/call.go`、`client/sse.go`。
- K8s Lease：`gofy-adapter/adapter/lease/k8slease/k8slease.go`。

### 1.1.1 复核说明（2026-11-16 13:20，HEAD `f2fb599`）

报告的初审基线是 `db271b5`，写这份报告时 `origin/main` 实际已到 `b84d869`。两者差异只
涉及 `support/lock`、demo 的 `appio/redislock` 与文档，没有触及本报告审查的 worker /
consumer / generation 路径，因此结论不受影响；但"基于当前默认分支代码"的说法不准确，
已在上方改为标注具体基线。

复核时逐条重读 HEAD 上的代码，12 条问题**全部成立**，证据见 §8。其中 5 处需要修正：

1. **P1-02 的作用域**：pid 文件是每台机器本地的，文件锁只能挡住同一主机上的并发启动，
   挡不住 Kubernetes 里同 group 的两个 Pod（各自文件系统）；不要把它当成跨副本互斥。
2. **P2-05 的修复建议**：只把过期比较换成数据库 `NOW()` 没有用——`updated_at` 是应用侧
   写入的，列里的时间仍是被写入时那台机器的时钟。写入与比较必须同时用数据库时间。
3. **P2-02 与 P1-01 相互掩盖**：默认 `worker restart` 现在根本起不来子进程（P1-01），
   所以"新进程用旧配置"只在 `--force` / `--daemon=false` 时才实际发生。
4. **P2-02 的前提已被最新配置弱化**：demo 的 `enableNacosSyncConfig` 在 `f2fb599` 改为
   `false`，`start` 与 `restart` 都不再同步远端配置；本条只在开关打开或真实项目保留
   sync 时成立。
5. **P2-06 与 P1-05 是同一个问题的两个层面**，不应各算一条。

复核没有发现报告之外的 P1 级问题。

### 1.2 总体结论

本轮优化方向是正确的。当前仓库已经形成比较清晰的框架分层：

1. Gofy 根包负责 App 生命周期、HTTP、Reload、Worker Host 和 CLI；
2. `support` 提供 DB、Redis、MQ、Worker、Consumer、Claim、Lease 等基础能力；
3. `gofy-interface-api` 独立承载接口契约和类型化 Client；
4. `gofy-adapter` 承载依赖较重、又不应进入框架核心依赖图的基础设施适配；
5. `demo-service` 展示业务模块、Repository/Query、IO 资源和 Worker 分组的组装方式。

**没有看到需要推倒重来的架构问题。** 目前更重要的是补齐少数控制面与消息处理链路的失败语义。特别是本轮新增的 `worker restart` / `daemon` 命令，涉及父进程、子进程、TTY、PID 文件和确认提示，不能只验证命令能否被解析，必须验证最终子进程是否真的启动。

### 1.3 本轮问题概览

| 优先级 | 问题 | 影响 | 建议 |
|---|---|---|---|
| P1 | 默认 `worker restart` 的 daemon 子进程再次执行 restart 并重复确认 | 正常 restart 路径可能没有启动 Worker，但父进程已报告成功 | 修复子进程命令规范化，禁止子进程再次执行控制命令 |
| P1 | Worker PID 检查与写入不是原子互斥 | 两个并发启动可能同时通过检查并运行同一分组 | 增加进程级独占锁，锁生命周期覆盖整个 Worker 进程 |
| P1 | Consumer 的 `park()` 台账写入失败仍返回 ACK | 死信状态没有持久化，消息却可能从 Broker 删除 | 台账写入失败必须转成可重试错误，不得 ACK |
| P1 | 多种 Consumer 在实际 Consume/Subscribe 开始前就报告 Ready | 探针可能在消费者真正加入消费状态前变绿 | 将 Ready 移到各协议实际建立订阅之后 |
| P1/P2 | ReadyGate 无法识别同一个 Worker 实例的重复 Ready | Runner 重启时可能把同一实例重复计数，提前关闭 readiness gate | 每个 Runner 实例只允许成功贡献一次 Ready |
| P2 | daemon 父进程没有等待子进程真正 Ready | 子进程启动失败时父进程仍打印“后台启动成功” | 增加父子启动握手，只有子进程 Ready 才返回成功 |
| P2 | `worker restart` 不执行 Nacos Sync | 手工重启会按本地旧配置启动 | 明确 restart 的配置刷新语义，或提供不阻断 stop 的刷新流程 |
| P2 | stop 仅凭 PID 判断进程归属 | PID 已被系统复用时，可能误发信号给无关进程 | PID 文件增加进程身份校验信息 |
| P2 | Nacos 配置文件解析发生在所有命令之前 | 配置文件损坏时 stop/help/version 等运维命令也可能无法执行 | 将配置加载推迟到确实需要配置的命令路径 |
| P2 | Consumer processing lease 依赖应用机本地时钟 | 跨节点时钟偏差可能导致过早接管或迟迟不能接管 | 尽量使用数据库时间作过期判断；明确 lease 必须覆盖最长 Handler 时长 |
| P2 | ReadyGate 的重复信号保护注释与实际实现不符 | 重复 Ready 会递减其他实例的 pending 计数 | 补充按实例去重的状态和测试 |
| P2（业务示例） | 订单事件 Handler 只判断目标状态不同，没有校验状态迁移顺序 | 乱序事件可能把较新的订单状态覆盖回旧状态 | 按事件版本或允许的状态迁移条件更新 |

下文按问题逐一说明触发路径、根因、影响与修复建议。

---

## 2. P1：需要优先修复的问题

## P1-01：默认 `worker restart` 的 daemon 重启链路会再次执行确认

**严重级别：P1**

### 涉及代码

- `internal/commandline/worker.go`：`newWorkerRestartCmd()`
- `worker_host.go`：`restartWorker()`、`runWorker()`、`daemonizeWorker()`
- `internal/process/detach_unix.go`：`Detach()`
- `internal/console/console.go`：`Confirm()`
- `internal/commandline/commandline.go`：`confirmAction()`

### 实际调用链

以默认命令为例：

```bash
demo-service worker restart --group=group1
```

当前默认 `WorkerOptions{Daemon: true}`。控制链路大致如下：

```text
父进程：worker restart --group=group1
        │
        ├─ Cobra 执行 confirmAction()
        │    └─ 用户输入 yes
        │
        └─ restartWorker()
             ├─ stop(group1)
             └─ runWorker(Daemon=true)
                  └─ daemonizeWorker()
                       ├─ args := os.Args[1:]
                       ├─ args 仍然是 worker restart --group=group1
                       ├─ 追加 --daemon=false
                       └─ process.Detach(executable, args)
                              │
                              ▼
                       子进程重新执行：
                       worker restart --group=group1 --daemon=false
                              │
                              ├─ 再次进入 newWorkerRestartCmd()
                              ├─ 再次执行 confirmAction()
                              └─ 子进程 stdin 已由 Detach 重定向到 /dev/null
```

`Confirm()` 遇到 EOF 时返回 `false, nil`；`confirmAction()` 把它当作用户放弃。`RunE` 在 `!ok` 时返回 `err`，此处 `err == nil`，因此子进程正常返回，不会继续调用 `RestartWorker()` 或 `runWorker()`。

与此同时，父进程的 `daemonizeWorker()` 在 `process.Detach()` 成功后就打印后台启动成功并返回。父进程无法知道子进程已经因二次确认而放弃。

### 影响

在没有 `--force` 的常规默认用法下：

1. 旧 Worker 被停止；
2. 父进程创建子进程；
3. 子进程再次询问确认，但 stdin 是 EOF；
4. 子进程不启动 Worker；
5. 父进程已经报告“后台启动成功”。

这会形成“命令成功、服务实际停止”的运维故障。`--force` 跳过确认，或显式使用 `--daemon=false`，可能绕过该问题，但不能要求用户通过这些参数才能让默认 restart 正常工作。

### 根因

`daemonizeWorker()` 复用了原始 CLI 命令行，而没有把“要执行的最终动作”转换为纯启动命令。daemon 子进程不应该再进入 `worker restart` 控制流程。

### 修复建议

不要直接复制 `os.Args[1:]` 作为 restart 子进程的命令。应构造明确的 Worker 启动参数：

```text
原命令：worker restart --group=group1

daemon 子进程应执行：worker --group=group1 --daemon=false
```

要求：

- 子进程直接执行 Worker group 启动路径，不得再次执行 stop/restart；
- 不得再次执行交互式确认；
- 保留用户实际需要的启动参数，例如 group、name、concurrency、timeout 等真正属于 Worker 启动的选项；
- 不应把 `--force`、restart 子命令或 stop 专用参数错误传递给 Worker 启动路径；
- 参数应来自解析后的 `WorkerOptions`，而不是简单字符串拼接原始 `os.Args`。

更稳妥的方式是引入一个内部专用的子进程启动标记，或直接构造 `worker --group=... --daemon=false` 的 argv。不要把内部标记暴露成用户必须掌握的运维参数。

### 必须补充的回归测试

| 测试 | 预期 |
|---|---|
| 默认 `worker restart --group=x`，用户确认 yes | 旧进程退出，新进程进入 Worker Run 并最终 Ready |
| 默认 restart，子进程 stdin 为 EOF | 子进程仍能启动，因为它不再执行确认 |
| `worker restart --force --group=x` | 正常启动，不重复确认 |
| `worker restart --daemon=false --group=x` | 前台启动，不产生第二次 restart |
| `worker restart --group=all` | 新进程启动全部分组，不递归调用 restart |
| 子进程启动失败 | 父进程返回失败，不打印“后台启动成功” |

---

## P1-02：PID 文件的“检查后写入”不是原子互斥，可能启动重复 Worker

**严重级别：P1**

### 涉及代码

- `worker_host.go`：`runWorker()` 中的 PID 检查和 `pidfile.Write()`
- `worker_host.go`：`daemonizeWorker()` 中的 daemon 前置 PID 检查
- `internal/pidfile/pidfile.go`：`Write()`、`Read()`

### 当前实现

前台启动流程是：

```text
读取 pid 文件
   ↓
检查 process.Alive(existing)
   ↓
如果没有活着的进程
   ↓
pidfile.Write(path, os.Getpid(), perm)
```

daemon 父进程会先做一次类似检查，但真正的子进程启动时仍然会再次读取并检查 PID 文件，然后执行写入。

`pidfile.Write()` 的确采用同目录临时文件 + `rename`，避免读到半截 PID 内容，但它只保证**文件内容原子替换**，不保证**进程启动所有权原子竞争**。

### 并发触发方式

两个启动命令同时执行时，可能发生：

```text
进程 A                         进程 B
------                         ------
Read(pid) -> 不存在
                               Read(pid) -> 不存在
Alive -> false
                               Alive -> false
Write(pid=A)
                               Write(pid=B)
启动 Worker                    启动 Worker
```

最后 PID 文件可能只留下 B，但 A 和 B 都在消费。停止命令只能通过 PID 文件找到其中一个进程。

daemon 模式的父进程前置检查不能消除这个竞态，因为两个父进程仍可能同时通过检查；子进程的检查与写入也同样不是独占操作。

### 影响

- 同一分组出现两个进程同时消费；
- 对于“一个分组由一个 Deployment/进程管理”的部署约束，实例数量失控；
- PID 文件只能指向最后写入者，另一进程成为无法通过当前 pid 文件管理的孤儿；
- `worker stop` 可能只停止其中一个，运维误以为整个分组已停止；
- 订单扫描、定时任务、报表等非天然多副本安全任务可能重复执行。

**范围限定**：pid 文件在每台机器本地，下面建议的文件锁只能挡住**同一主机**上的并发
启动（systemd 重启与手工启动撞车、两个 shell 同时执行）。Kubernetes 中同一 group 的
两个 Pod 各有自己的文件系统与 pid 文件，这把锁对它们无效——那种重复要靠副本数约束或
`support/lease` 之类的跨进程选举解决。不要把本项当成"跨副本互斥"。

### 修复建议

引入独立的**进程锁文件**或等价的原子锁机制。推荐在支持的平台上使用 `flock`：

1. 启动 Worker 时先打开独立 lock file；
2. 通过 `LOCK_EX | LOCK_NB` 原子取得锁；
3. 取得锁后再写 PID 文件、启动业务 Worker；
4. 整个 Worker 进程生命周期持续持有锁文件描述符；
5. 进程退出时释放锁；进程崩溃时由操作系统释放锁；
6. `stop` 仍然读取 PID 文件，但停止前应校验 PID 对应的进程身份。

不要直接在当前 `pidfile.Write()` 的临时文件上加锁：它会 rename 替换 inode，锁住旧 inode 不能保护新路径。应使用稳定路径的独立 lock file，或者采用其他不会因 rename 失去锁语义的实现。

### 必须补充的测试

- 两个进程同时启动同一 group，只允许一个取得锁；
- 两个进程分别启动不同 group，应允许同时运行；
- `--group=all` 与单 group 启动的锁粒度符合约定；
- 持锁进程 SIGKILL 后，新进程可取得锁；
- PID 文件写入失败时释放锁并清理临时资源；
- 子进程启动失败不会留下无法恢复的锁文件状态。

---

## P1-03：Consumer 的 `park()` 持久化失败仍返回 nil，底层会 ACK 消息

**严重级别：P1**

### 涉及代码

- `support/consumer/consume.go`：`Processor.Handle()`、`park()`
- `support/worker/consumer/amqpconsumer/*`
- `support/worker/consumer/kafkaconsumer/*`
- `support/worker/consumer/rocketmqconsumer/*`
- `support/worker/consumer/queuesconsumer/*`

### 当前执行路径

`Processor.Handle()` 在两种情况下调用 `p.park()`：

1. `DecodeEnvelope(msg)` 失败；
2. Handler 返回 `Permanent(err)`。

但当前 `park()` 是不返回错误的函数：

```go
func (p *Processor) park(...) {
    if err := p.deduper.park(...); err != nil {
        p.logger.Warn("consume mark dead failed", ...)
    }
}
```

之后 `Handle()` 仍然返回 `nil`。

例如永久错误路径：

```text
Handler 返回 Permanent(err)
          ↓
Processor.Handle 调用 park()
          ↓
数据库写 dead 状态失败
          ↓
park 只记 Warn，不把错误返回给 Handle
          ↓
Processor.Handle 返回 nil
          ↓
底层 Adapter 按 nil 执行 ACK / Commit
```

### 影响

当启用了幂等台账、但数据库暂时不可用或 `park` SQL 执行失败时：

- 消息被底层 Broker ACK 或 offset commit；
- `worker_consumed_messages` 中没有对应的 `dead` 记录；
- 若 Broker 没有另外配置 DLQ，消息正文可能永久丢失；
- 运维只能在日志中看到一次 `consume mark dead failed`，无法从台账查到该消息的失败原因。

这与当前 Consumer 的设计目标冲突：台账是记录消息终态和错误的持久化依据，不能在写失败时仍把消息确认掉。

### 修复建议

让 `park()` 返回 `error`，由 `Processor.Handle()` 决定 ACK 语义：

```go
if err := p.park(ctx, msg.ID, attempt, cause); err != nil {
    return fmt.Errorf("persist dead message %s: %w", msg.ID, err)
}
return nil
```

具体约束：

- 幂等台账开启时，`dead` 状态写入失败必须返回可重试错误，底层不得 ACK；
- `DecodeEnvelope` 失败时也应遵循同样规则；
- 只有 `dead` 状态已成功落库，才允许返回 nil 让 Broker ACK；
- 未启用幂等台账时，永久错误是否直接 ACK 是另一条明确策略，可以保留现有行为；
- 不应把数据库写失败的错误包装成 `Permanent(err)`，否则某些 Adapter 会按永久错误丢弃。

对于 Kafka Manual Commit，`park` 失败应使当前 offset 不提交；对于 AMQP 应 Nack/Requeue；RocketMQ 不应 Ack；Redis Queue 应进入可重试路径，或根据队列能力明确返回失败。

### 与 `succeed()` 写入失败的区别

`done` 标记失败但业务 Handler 已经成功时，当前代码选择 ACK，意味着后续只有生产者再次投递同一个 Message ID 时才可能重跑。这是 at-least-once 下可接受但必须明确的取舍：业务副作用要幂等。

但 `park()` 失败不同：这代表系统既没有成功业务处理，也没有成功持久化终态，仍然 ACK 会丢失唯一可追踪的失败消息。建议优先修复 `park()`，不要把两种情况混为一谈。

### 必须补充的测试

| 场景 | 预期 |
|---|---|
| Permanent Handler + dead UPDATE 成功 | 台账为 dead，返回 nil，消息 ACK |
| Permanent Handler + dead UPDATE 失败 | 返回 retryable error，不 ACK |
| DecodeEnvelope 失败 + dead UPDATE 失败 | 返回 retryable error，不 ACK |
| 幂等关闭 + Permanent Handler | 按既定永久错误策略 ACK |
| dead 状态已存在，再次投递 | Skip 并 ACK |

---

## P1-04：Consumer 在实际 Subscribe/Consume 之前就报告 Ready

**严重级别：P1**

### 涉及代码

- `support/worker/ready.go`
- `support/worker/runner.go`
- `support/consumer/kafka.go`
- `support/consumer/amqp.go`
- `support/consumer/rocketmq.go`
- `support/consumer/queues.go`
- `support/kafka/consumer.go`
- `support/amqp/queue.go`

### 问题不是 Runner 没等待 Ready，而是 Adapter 调用 Ready 的时机仍然过早

本轮增加了 `ReadyReporter` / `Ready(ctx)`，Runner 会等待所有声明需要报告的 worker 实例。但检查具体协议代码后，发现多个消费者在调用实际消费 Adapter 之前就执行了 Ready。

#### Kafka

`KafkaConsumer.Run()` 的顺序：

```text
创建 support/kafka.Client
          ↓
NewConsumer() 构建一个专用的 franz-go kgo.Client
          ↓
构建 kafkaconsumer Worker
          ↓
client.Ping(ctx)
          ↓
gofyworker.Ready(ctx)
          ↓
w.Run(ctx)
          ↓
Consumer.Consume()
          ↓
PollRecords()，才真正进入 Consumer Group 的拉取与分区流程
```

这里还有一个细节：`NewConsumer()` 对受管 Client 会创建一个独立的 consumer `kgo.Client`；`client.Ping()` 调用的是原始 `Client` 的 `raw.Ping()`，不是新创建的 consumer client。也就是说，Ready 前的 Ping 不能证明专用 consumer client 已经成功 Poll 或加入消费组。

#### AMQP

`AMQPConsumer.Run()` 当前顺序：

```text
amqp.New() 建立连接
          ↓
NewQueue() 构造 Queue 对象
          ↓
构建 amqpconsumer Worker
          ↓
Ready(ctx)
          ↓
w.Run(ctx)
          ↓
Queue.Consume()
          ↓
consumeOnce() 创建 channel、QoS、declare topology、ConsumeWithContext
```

`support/amqp/queue.go` 中 topology 声明和 `ConsumeWithContext()` 发生在 `Queue.Consume()` 内部。当前 `Ready()` 在 `w.Run()` 之前，因此 Ready 时队列拓扑甚至可能还没有声明，消费者也没有拿到 deliveries channel。

#### RocketMQ

`RocketMQConsumer.Run()` 构造 `SimpleConsumer` 和低层 Worker 后，先调用 `Ready(ctx)`，然后才执行 `w.Run(ctx)`，即真正的 `Consumer.Consume()`。

RocketMQ v5 消费者的启动/Receive 过程在 Consume 路径内进行。仅完成 `NewConsumer()` 不等价于消费端已经启动并可以接收消息。

#### Redis Queue

Redis Queue 与 Broker 长连接订阅不同，连接建立后通过轮询 Pop 取消息。Ready 应以实际 Redis 连接可用和队列访问可用为依据；目前至少也应验证消费循环能够成功执行一次队列操作，而不是只创建 Queue wrapper 就置绿。

### 影响

Kubernetes readiness 或外部运维探针可能在以下状态时就返回 200：

- Kafka consumer group 尚未 Poll、尚未完成分区分配；
- RabbitMQ 的 queue/exchange/binding 尚未声明或 Consumer 尚未建立；
- RocketMQ SimpleConsumer 尚未开始 Receive；
- 首次真正消费启动时才发生的连接/订阅错误还未暴露。

这样新 Pod 可能被认为已启动，而实际没有消费者正在接活。对 Worker 进程而言，Ready 最重要的含义就是“已具备实际消费能力”，所以不能只以客户端对象创建成功或基础连接 Ping 成功作为唯一条件。

### 修复建议

将 Ready 从高层 `support/consumer/*.Run()` 中前移/移入到**协议消费循环确认成功的时刻**，例如增加适配器级 `OnReady` 或 `ReadyFunc`：

- Kafka：consumer 专用 kgo client 成功完成首次 Poll，确认至少完成必要的组协调/分区分配后报告 Ready；不能使用生产侧 Client 的 Ping 代替。
- AMQP：成功创建 channel、完成 QoS、声明拓扑，并成功调用 `ConsumeWithContext()` 获得 delivery stream 后报告 Ready。
- RocketMQ：SDK SimpleConsumer 完成 Start/Receive 启动步骤且无初始化错误后报告 Ready。
- Redis Queue：至少完成一次无错误的连接/队列访问，或采用明确的“已具备轮询能力”确认动作。

不要等待第一条业务消息才 Ready，否则空队列会导致服务永远 NotReady。Ready 要验证消费基础设施已成功启动，而不是要求已有消息。

### 必须补充的测试

- Kafka：基础 Client Ping 成功，但 Consumer 首次 Poll 失败，Ready 不得关闭；
- Kafka：Consumer 首次 Poll/组协调成功后，Ready 才关闭；
- AMQP：连接成功但 topology declare 失败，Ready 不得关闭；
- AMQP：`ConsumeWithContext()` 建立成功后 Ready 才关闭；
- RocketMQ：构造成功但实际 Start/Receive 失败，Ready 不得关闭；
- 一个 group 中任意消费者未准备完成，整个 group 不得 Ready；
- 任意消费者启动失败并达到重试上限，进程不应报告 Ready。

---

## P1-05：ReadyGate 不能识别同一个 Worker 实例的重复 Ready

**严重级别：P1/P2（建议按 P1-04 一并修复）**

### 涉及代码

- `support/worker/ready.go`：`readyGate.signal()`
- `support/worker/runner.go`：`Runner.Run()`、`supervise()`

### 当前实现

Runner 根据每个 `ReadyReporter` worker 的 concurrency 计算 pending 数量。`supervise()` 对同一个 worker 实例进行自动重启，但每次重启仍然会创建携带同一个 `readyGate` 的 context。

当前 `readyGate.signal()` 只做：

```go
if g.pending > 0 {
    g.pending--
}
```

它没有实例 ID，也没有针对每个 supervise 实例的 `sync.Once`。因此同一个实例的多次 `Ready(ctx)` 会被当成多个实例成功上报。

代码注释声称“Extra signals are ignored”，但当前实现只能忽略 gate 已完全关闭之后的额外信号，不能识别“同一实例重复上报、但其他实例仍未上报”的情况。

### 触发时间线

例如同一 Worker 并发数为 2：

```text
pending = 2

实例 A 第一次启动成功并 Ready
pending = 1

实例 A 随后遇到错误，Run 返回，Runner 自动重启实例 A

实例 A 第二次启动成功并再次 Ready
pending = 0，ReadyGate 关闭

实例 B 实际尚未完成连接
```

此时整个 Runner 会提前报告 Ready。

### 修复建议

Ready 计数应按 Runner 内每个逻辑实例去重，而不是按 Ready 调用次数计数。

可以在 `supervise()` 为一个 `Identity` 创建共享的 `readyOnce`，后续重启仍复用同一 `sync.Once`；或由 ReadyGate 接收稳定的 instance key 并记录已完成实例集合。

注意：

- 每个逻辑实例只贡献一次启动 Ready；
- Worker Run 重试不能再次消耗 pending；
- 不同 concurrency index 必须各自贡献一次；
- gate 关闭后额外 Ready 仍为安全 no-op。

### 测试

两个实例并发启动，A 首次 Ready 后返回错误并重启，A 第二次 Ready 时 B 仍阻塞；断言 `Runner.Ready()` 仍然未关闭，直到 B 真正 Ready。

---

## 3. P2：重要但可以在 P1 后处理的问题

## P2-01：daemon 启动没有父子进程 Ready 握手

### 涉及代码

- `worker_host.go`：`daemonizeWorker()`
- `internal/process/detach_unix.go`：`Detach()`
- `worker_host.go`：`runWorker()`

### 当前语义

`daemonizeWorker()` 调用 `process.Detach()` 后，只要操作系统成功创建子进程，就打印“后台启动成功”。父进程没有等待子进程完成以下关键步骤：

- logger 建立；
- telemetry 初始化；
- DB 初始化；
- Runner 构造；
- PID 文件写入；
- health endpoint 监听；
- 所有消费者真正 Ready。

子进程 stdout/stderr 被重定向到 `/dev/null`。如果子进程在 DB 初始化、health 端口绑定、broker 连接或 Worker 启动阶段失败，用户可能只看到父进程成功消息。

### 影响

这是“启动命令报告”和“服务真实可用”之间的状态不一致。和 P1-01 的 restart 递归确认组合时尤其容易出现假成功。

### 修复建议

增加父子启动握手，而不是把 `exec.Start()` 成功等同于服务启动成功：

1. 父进程创建一条专用 IPC/pipe；
2. 子进程完成初始化并且整个 Worker group Ready 后回传 Ready 消息；
3. 子进程初始化失败时回传错误摘要并以非零退出；
4. 父进程在有限启动预算内等待 Ready 或启动失败；
5. 超时则检查子进程是否存活并返回清晰错误；
6. 只有收到 Ready，父进程才输出“后台启动成功”。

如果为了控制复杂度暂时不做 pipe，也至少要等到 PID 文件生成并验证进程存活，但这仍不能证明消费端已 Ready，因此只能作为过渡方案。

### 测试

- 子进程正常启动：父进程等到 Ready 后返回成功；
- DB 不可达：父进程返回启动失败；
- health 端口冲突：父进程返回启动失败；
- Broker 不可达且达到重试上限：父进程不得返回成功；
- 子进程启动期间提前退出：父进程能检测到退出，不留下假成功结果。

---

## P2-02：`worker restart` 不执行 Nacos Sync，重启后可能继续使用本地旧配置

> 与 P1-01 的关系：默认 `worker restart` 目前起不来子进程（子进程二次确认后正常退出），
> 所以"新进程用旧配置"在默认路径下先被 P1-01 掩盖，只有 `--force` 或 `--daemon=false`
> 时才会实际发生。修好其中一个不会自动修好另一个。
>
> 与最新代码的差异：demo 的 `enableNacosSyncConfig` 在 `f2fb599` 已改为 `false`，
> 同步整体关闭，`worker start` 与 `restart` 都不再拉远端配置。因此本条的前提（start 会
> sync、restart 不会）只在把开关打开、或真实项目保留 sync 时成立；开关关掉时两者行为
> 一致，问题被配置掩盖而不是被修掉。

### 涉及代码

- `examples/demo-service/main.go`
- `examples/demo-service/config.go`：`configInvocation()`、`isWorkerStartInvocation()`
- `worker_host.go`：`restartWorker()`

### 当前行为

`configInvocation()` 明确把 `worker stop` 和 `worker restart` 排除在 Nacos Sync 之外。这个决定对 `stop` 是合理的：停止服务不应该依赖配置中心可用。

但 `worker restart` 与 stop 不同：它不只是发信号，`restartWorker()` 在停止旧进程后还会在当前 CLI 进程中直接执行 `runWorker()`。当前 CLI 进程在 `main()` 中读取的是本地 `config.yaml`，而 restart 之前没有执行 Nacos Sync。

因此：

- Nacos 中已经更新配置；
- 本地 `config.yaml` 仍是旧版本；
- 执行 `worker restart --group=group1`；
- 新 Worker 使用本地旧配置，而不是 Nacos 当前配置。

### 是否属于 Bug

这是一个需要明确的命令语义，而不是简单的代码错误。当前代码注释强调 stop/restart 控制命令在 Nacos 不可用时也应可用；但 restart 同时承担了启动职责，和 stop 不完全相同。

### 建议

建议把命令语义拆清楚：

- `worker stop`：绝不依赖 Nacos，只根据 PID 文件停止；
- `worker restart`：可以设计成“用本地已同步配置重启”，也可以设计成“先停止，再刷新配置，再启动”；但必须在文档里明确；
- 若 restart 需要最新配置，配置刷新失败时不能静默启动旧配置，也不能让旧进程已经停止后只留下空窗。

一种更稳妥的流程是：在停止旧实例之前先尝试同步并校验新配置；同步成功后再停止和启动。若运维要求 Nacos 不可用时仍能 restart，则提供明确的 `--use-local-config` 或默认本地回退策略，并把回退记录到日志。不要让这个语义隐藏在 `configInvocation()` 的字符串判断中。

### 测试

本地配置版本 A、Nacos 配置版本 B，执行 restart；断言启动后 Worker 连接参数/业务设置究竟是 A 还是 B，并与文档承诺一致。另测 Nacos 不可达时的 stop 和 restart。

---

## P2-03：PID 文件只有 PID，没有进程身份，存在 PID 重用误杀风险

### 涉及代码

- `internal/pidfile/pidfile.go`
- `stop.go`：`stopPIDFile()`
- `internal/process/*`

### 当前问题

`stopPIDFile()` 读取 PID 后，使用 `process.Alive(pid)` 判断进程是否存在，然后向该 PID 发送终止信号。若进程异常退出，PID 文件未能清理，而操作系统后来把同一个 PID 分配给另一个无关进程，单纯 `kill(pid, 0)` 无法识别 PID 已被复用。

### 影响

在极端但真实的长期运行主机上，`worker stop` 可能终止一个与 Gofy 无关的进程。

### 修复建议

PID 文件可以从单纯整数升级为结构化记录，例如：

```json
{
  "pid": 12345,
  "started_at": "...",
  "executable": "/path/to/demo-service",
  "group": "group1",
  "process_token": "..."
}
```

停止前至少校验 PID 与 executable / process start time 的一致性。Linux 可从 `/proc/<pid>/stat` 等信息校验进程启动时间和可执行路径；macOS 需采用对应的进程信息接口。不要仅凭 PID 存活就把它视为原 Worker。

该项可以晚于 P1-02 的独占锁实现，但最终应形成完整的进程所有权模型。

---

## P2-04：所有 CLI 命令在分发前都读取 Nacos 配置，运维命令被本地配置损坏牵连

### 涉及代码

- `examples/demo-service/main.go`
- `examples/demo-service/config.go`
- `support/nacos/config.go`：`Load()`、`readNacosFile()`、`readApplicationFile()`

### 当前行为

`main()` 在分析 `configInvocation()` 之前就调用 `loadNacosConfig()`。这表示 `worker stop`、`worker --list`、`--help`、`version`、`script` 等命令虽然不执行 Nacos 网络请求，但仍会先读取/解析 `nacos.yaml` 和可选的 `application.yaml`。

随后，`main()` 还会无条件读取本地 `config.yaml`，再构建 Worker groups 和 App。

因此“这些命令不访问 Nacos 服务端”是成立的，但“这些命令完全不依赖 Nacos 配置文件”并不成立。若 `nacos.yaml` 或 `application.yaml` 语法损坏、权限错误，停止 Worker 的命令也可能在到达 PID 控制路径之前失败。

### 影响

运维在配置变更过程中破坏了 Nacos 本地配置文件时，可能连 `worker stop` 都无法执行。停止一个有问题的服务，原则上不应依赖完整解析业务配置。

### 修复建议

把 CLI 分类前移，尽量做到：

- `worker stop`、`stop`：只加载 PID 文件所需的最小配置；
- `version`、`--help`、`worker --list`：不加载外部配置；
- `script`：仅加载脚本确实需要的配置；
- `start`、`worker start`：加载完整配置并按策略进行 Nacos Sync。

如果 `worker --list` 必须靠业务代码注册组清单，则至少拆分出不触发外部配置解析的静态 Worker 描述信息，或者明确说明它仍依赖本地 config 文件。

---

## P2-05：Consumer processing lease 依赖应用节点本地时钟

### 涉及代码

- `support/consumer/consume.go`：`leaseExpired()`、`claim()`
- `support/consumer/consume.go`：`ProcessorConfig.ProcessingLease`

### 当前实现

`processing` 租约复用了 `updated_at`，但 `leaseExpired()` 在应用进程中使用：

```go
 time.Since(row.UpdatedAt) >= d.lease
```

即过期判断依赖当前 Worker 节点的本地系统时钟，而不是数据库时钟。

### 风险

不同 Worker 节点存在时钟偏差时：

- 时钟快的节点可能提前判断 lease 已过期，抢先接管；
- 时钟慢的节点可能认为 lease 仍有效，延迟重试；
- 早期接管会使旧 Handler 尚未结束时新 Handler 已经进入，attempt fencing 虽能阻止旧 Handler 修改台账，却不能回滚旧 Handler 已经产生的业务副作用。

此外，`ProcessingLease` 不会在 Handler 执行过程中自动续期，必须大于所有合法 Handler 的最长运行时间，否则长 Handler 可能被其他节点接管。

### 修复建议

优先使用数据库时间判断过期，并在接管 UPDATE 中把 `updated_at` 过期条件也放入 SQL CAS 中，避免仅依赖本地时钟：

```sql
UPDATE worker_consumed_messages
SET status = 'processing', attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
  AND status = 'processing'
  AND attempts = ?
  AND updated_at <= <database_now_minus_lease>;
```

不同数据库的时间间隔语法应由对应 Dialect 实现或抽象，不要拼接不安全字符串。

**这条建议本身有个坑**：`updated_at` 现在由应用侧写入（GORM 的自动时间戳）。只把比较
换成数据库 `NOW()` 并不能消除时钟偏差——列里存的仍然是被写入时那台机器的时钟，只是把
偏差从"比较"挪到了"写入"。要真正以数据库时间为准，两边都必须是数据库时间：

```sql
-- 写入与比较同源，时钟偏差才真正消失
UPDATE worker_consumed_messages
SET status = 'processing', attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'processing' AND attempts = ?
  AND updated_at <= CURRENT_TIMESTAMP - INTERVAL 5 MINUTE; -- 租约长度
```

若不想改写入侧，就不要假装这能解决偏差——保留应用时钟，并在文档里明确"所有 worker
节点必须 NTP 同步"，这是一条更弱但诚实的约束。

如果将来确实有分钟级以上长 Handler，再考虑 lease renewal；当前阶段不建议为了理论完整性先引入复杂续租机制。至少要在 Worker 指南中明确 `ProcessingLease` 必须超过最长合法 Handler 时长，并给出超时处理规范。

---

## P2-06：ReadyGate 的注释承诺“重复信号忽略”，实现没有按实例去重

> 与 P1-05 是同一个缺陷的两个层面（P1-05 讲时序后果，本条讲 API 契约），修复时合并
> 处理，不单独排期。

本问题与 P1-05 同源，但即使 Ready 调用时机修正，也应单独补齐 ReadyGate 的 API 保证。

当前 `readyGate.signal()` 只看全局 pending 数，不知道调用者属于哪个 Worker 实例。其注释“Extra signals are ignored”并不完全符合实现。

建议在 P1-05 修复时一起处理：

- `supervise()` 为每个 Identity 维护一个跨重启的 `sync.Once`；
- 或 Gate 接收稳定的 instance key，已 Ready 的 key 不再递减 pending；
- 用并发测试覆盖同一实例多次 Ready、不同实例 Ready、Run 重启后重复 Ready。

不要只把注释改成“调用者必须保证只调用一次”；Runner 自动重启会自然产生多次 Run 调用，底层 adapter 也很容易在 reconnect 流程多次发 Ready。

---

## P2-07：订单事件 Handler 没有约束状态迁移顺序，乱序事件可能回退状态

### 涉及代码

- `examples/demo-service/worker/group1/handler_order.go`
- `examples/demo-service/worker/group1/producer.go`
- 订单状态契约：`interface-service` 中的 order enum

### 当前实现

`applyOrderEvent()` 根据 event type 得到目标状态，然后执行：

```go
Where("id = ? AND status <> ?", payload.OrderID, string(target)).
Update("status", string(target))
```

该更新只能防止相同目标状态重复写入，不能判断这个目标状态是否允许从当前状态迁移而来。

### 风险

如果 `order.cancelled` 已先被处理，而较早发生的 `order.paid` 事件后到，后者会把订单状态再次更新为 Paid。事件重复与事件乱序是两个不同问题：幂等只能解决重复，不能自动解决乱序。

当前 demo producer 主要演示 `order.paid` 事件扇出，因此这个问题不一定会在默认 demo 数据中触发；但 Handler 同时声明支持 `order.cancelled`，并且消息系统本来就采用 at-least-once / 多分区处理，所以状态迁移语义应当明确。

### 修复建议

根据业务状态机选择一种约束：

1. **合法前置状态条件**：例如只允许 Pending → Paid，Pending/Paid → Cancelled；
2. **事件版本/序号**：订单聚合增加 revision，只有事件版本更大时才应用；
3. **事件时间 + tie-breaker**：若业务明确允许按事件时间排序，需同时处理相同时间的 tie-breaker；
4. **状态单调性**：对于不能回退的终态，SQL 条件禁止从 Cancelled 回到 Paid。

不要简单在应用层先 SELECT 当前状态再 UPDATE；并发 Handler 下仍有竞态。迁移条件应进入数据库更新条件或事务锁定范围。

---

## 4. 已修复问题与本轮正向变化

下列问题在本轮检查到的代码中已有实际修复，建议保留，不要在后续文档里继续按旧基线重复列为待办。

| 上轮问题 | 当前代码状态 | 评价 |
|---|---|---|
| Generation lease 未 defer release、admission 检查与关闭有竞态 | 当前 Generation 准入使用同步门闩，获得 lease 后 defer release | 方向正确；继续保留并发 StopAccepting 测试 |
| Handler generation 与 IO Snapshot 不一致 | `support.RequestBinder` 由每代模块构造并随 generation 发布；demo `appio.Module` 捕获本代 Snapshot | 解决了框架不应依赖业务 appio 的依赖方向问题，设计合理 |
| Claim 结束状态缺少每次 claim fencing | Succeed/Fail/Dead 使用 `owner + attempts + status` 做 CAS | 不需要立即新增 claim_token 列；attempts 当前可提供单调 fencing |
| Consumer ledger 允许 processing 中并发执行 | 状态机区分有效 processing、过期 processing、failed、done、dead；有效 processing 返回 `ErrInFlight` 不 ACK | 并发状态机比上一轮完整；但 `park()` 写入失败仍 ACK，见 P1-03 |
| Consumer 旧 Handler 覆盖新 Handler 的状态 | succeed/fail/park 按 attempts 与 status 限制写回 | 正确；应保留 stale claim 测试 |
| Worker Host 早于 Runner 启动就报告 Ready | Host 会等待 Runner.Ready 汇总后才设置 health Ready | Host 汇总层有改善；协议层 Ready 调用仍然过早，见 P1-04 |
| Worker drain 时 readiness 状态 | 当前 `runCtx.Done()` 后将 worker health 设为 stopping | 已修正“drain 期间仍然 200”的旧问题，需保留测试 |
| RocketMQ 低层 Worker 的一次性 Consumer 与 Runner restart 不匹配 | 新增 `NewFactory()`，每次 Run 创建新 Consumer | 修复正确；生产路径优先用高层 `NewRocketMQ()` |
| `WithQuery` nil `url.Values` | Call options 已初始化 query map | 已修复 |
| SSE reconnect 固定使用旧 BaseURL | 每次 reconnect 都重新执行 resolver | 已修复 |
| `WithConnectTimeout` 派生 Transport 缓存无界 | 上限 32 个派生池，`Client.Close()` 释放自有 idle connections | 有界且所有权语义更清楚 |
| K8s Lease 同一 Locker 并发 Acquire 同 key | 增加进程内 held map，重复 Acquire 返回 `ErrHeld` | 已修复同进程并发共享 token 的问题；跨进程 fencing 仍以 K8s LeaseTransitions 为准 |
| K8s Lease Adapter 在 README 中缺失 | 已补文档并补 import guard | 正确 |
| Demo Query 层语义不明确 | 复杂只读查询独立在 Query 层，`MaxOrderID` 不再暗示“最近订单” | 结构合理，避免把复杂列表 SQL 塞进 Repository |
| Nacos restart/exit 失败后的恢复路径 | 新增 ResumeAccepting / WithResume 相关路径 | 比旧版永久摘流状态更完整；仍要补动作失败和重新注册失败的集成测试 |
| Nacos 启动期注册和摘流竞态 | 当前注册完成后复查 admission 状态，必要时注销 | 修复思路正确 |

### 4.1 仓库分拆与依赖方向

目前拆仓职责是合理的：

```text
gofy
  ├─ 框架核心、HTTP、Worker Host、基础 support
  └─ 不依赖接口契约仓库

gofy-interface-api
  └─ 纯契约 + 类型化 HTTP Client，不依赖 Gofy/Gin

gofy-adapter
  ├─ Gofy/Gin 相关 Adapter
  ├─ Nacos Resolver
  └─ Kubernetes Lease Adapter

业务服务
  ├─ 依赖 gofy
  ├─ 按需要依赖 gofy-interface-api
  └─ 按需要依赖 gofy-adapter
```

`gofy-adapter` 使用本地 sibling `replace` 方便联调是合理的；发布版本时应继续使用明确版本，不应把本地路径 replace 带入正式模块版本。

### 4.2 不建议继续扩张的部分

这些部分目前的方向合理，不应为了追求“全功能框架”而增加不必要抽象：

- Query 层继续保持只读查询定位；Repository 不承担复杂统计 SQL；
- Claim（数据库任务行认领）与 Lease（分布式互斥）保持分离；
- Worker 分组作为部署单位，不在一个 Worker 进程里混合不同生命周期/扩缩策略的业务组；
- Consumer 继续采用统一 Envelope、Trace、错误分类与幂等台账，不要求每种 MQ 再造一套处理链；
- Kafka Manual Commit 继续作为业务事件的安全默认；不要让 Handler 自己随意提交位点；
- Redis 即时队列和 ZSET 延迟队列继续明确“弹出即删除/在途至多一次”的可靠性边界，不要把它包装成与 MQ 一样的持久化语义；
- Nacos grace period 仍是固定等待而不是等待真实 Handler Join。文档已说明预算约束，不建议假称它能保证所有在途任务完成。

---

## 5. 修复顺序建议

### 阶段 A：先修 Worker 控制面

1. 修复 P1-01：daemon restart 子进程必须直接启动 Worker，不能递归执行 restart；
2. 修复 P1-02：增加进程级独占锁，避免同一 group 并发启动；
3. 修复 P2-01：增加 daemon 父子 Ready 握手；
4. 补充 restart/daemon/stop 的真实子进程集成测试；
5. 定义 restart 的 Nacos 配置刷新语义。

验收条件：默认 `worker restart` 在普通交互终端中可以停止旧 Worker 并真正启动新 Worker；后台子进程 Ready 前，父进程不会打印成功。

### 阶段 B：修正 Consumer 可靠性

1. 修复 P1-03：`park()` 返回错误，台账写失败不 ACK；
2. 补充每种 Adapter 对可重试错误、永久错误、死信写入失败的确认行为测试；
3. 将 processing lease 过期判断尽可能改为数据库时钟；
4. 对合法 Handler 最大时长和 `ProcessingLease` 配置写清楚。

验收条件：幂等台账无法写入 dead 状态时，消息必须仍可被 Broker 重投；不会出现无 dead 记录、无 DLQ、消息却已 ACK 的路径。

### 阶段 C：闭合 Worker readiness

1. 将 Ready 放到实际协议 Subscribe/Consume 成功之后；
2. Kafka 必须确认专用 Consumer client 的首次 Poll / Group 状态，而不是 Ping 原始 producer Client；
3. ReadyGate 按逻辑 Worker 实例去重，重启同一实例不能重复扣 pending；
4. 为 Kafka / AMQP / RocketMQ / Redis 分别补启动成功和启动失败测试。

验收条件：没有真正消费能力时探针始终 503；所有必需实例都准备完成后才 200；同一实例重启不会替其他实例完成 readiness。

### 阶段 D：增强运维安全

1. PID 文件增加进程身份信息，避免 PID reuse 误杀；
2. CLI 命令分类后再加载必要配置；
3. Worker restart 的 Nacos 配置刷新方式形成明确契约；
4. 补充 Nacos restart/exit 动作失败、恢复闸门和重新注册失败的测试。

### 阶段 E：业务示例状态机

1. 为订单事件定义合法状态迁移；
2. 通过数据库条件或 revision fencing 防止乱序事件回退状态；
3. 增加 paid/cancelled 乱序投递测试。

---

## 6. 本轮建议新增的自动化测试矩阵

| 范围 | 核心测试 | 目的 |
|---|---|---|
| Worker restart | 默认 daemon restart 端到端测试 | 捕获子进程递归确认问题 |
| Worker daemon | 父进程等待子进程 Ready | 防止启动阶段失败仍报告成功 |
| PID | 同 group 并发启动两个 OS 进程 | 证明进程锁是原子的 |
| PID | 不同 group 并发启动 | 确保锁粒度没有误伤其他分组 |
| Consumer ledger | park UPDATE 失败 | 验证不 ACK、不 commit |
| Consumer ledger | dead 状态持久化成功 | 验证 ACK 与 dead 记录一致 |
| Kafka readiness | Producer Client Ping 成功但专用 Consumer Poll 失败 | 验证不以错误 Client 的 Ping 报 Ready |
| Kafka readiness | 首次成功 Poll/加入组 | 验证 Ready 时机 |
| AMQP readiness | declare / Consume 建立失败 | 验证 Ready 不提前关闭 |
| RocketMQ readiness | SDK 启动/Receive 失败 | 验证 Ready 不提前关闭 |
| ReadyGate | 两实例中 A Ready 后重启并再次 Ready，B 仍未 Ready | 验证重复 Ready 不会提前关闭 gate |
| Nacos restart | 远端配置 B、本地配置 A 后 restart | 验证配置刷新语义符合文档 |
| CLI | Nacos YAML 损坏时 worker stop | 验证运维控制命令是否可独立运行 |
| Consumer lease | 两台机器模拟时钟偏差 | 验证过期判断不依赖节点时钟 |
| Order Handler | cancelled 先到、paid 后到 | 验证状态不会回退 |

---

## 7. 最终结论

Gofy 当前的主要架构决策是成立的，前一轮文档中的核心问题已经有多项实际修复。现在不适合继续增加大量新的框架能力，也不需要重做模块、Worker 或 Consumer 的总体抽象。

**下一步最重要的是把“启动成功”“消费就绪”“消息已持久化终态”“进程所有权”这四类状态的边界真正闭合。**

建议优先按以下顺序处理：

```text
P1
────────────────────────────────────
① worker restart 子进程命令规范化
② PID 进程锁，消除并发重复启动
③ Consumer park 持久化失败不得 ACK
④ Ready 必须发生在真实 Subscribe/Consume 之后
⑤ ReadyGate 按实例去重，防止重启重复计数

P2
────────────────────────────────────
⑥ daemon 启动握手
⑦ restart 的 Nacos 配置刷新语义
⑧ PID reuse 校验
⑨ CLI 配置加载按命令延迟
⑩ Consumer lease 数据库时钟
⑪ 订单状态迁移约束
```

完成以上事项后，当前 Gofy 架构在框架生命周期、Worker 托管、消息消费和运维控制方面会更完整。除这些边界问题外，其余模块分层和仓库职责建议保持，不要为了抽象而抽象。

---

## 8. 复核结论（2026-11-16 13:20，HEAD `f2fb599`）

逐条在 HEAD 上重读代码的结果。行号为复核时的事实位置，后续改动可能使其漂移。

复核之后，P1 的五条已按顺序修复（见 §9）；表中仍保留原始结论，便于对照"当时为什么成立"。

| 编号 | 结论 | 证据（HEAD） | 说明 |
|---|---|---|---|
| P1-01 | 成立 | `internal/commandline/worker.go:158-181`（restart 默认 `Daemon: true`，无 `--force` 时确认）；`worker_host.go:328`（子进程 argv 仍是 `worker restart`）；`internal/process/detach_unix.go:28-31`（stdio 指向 `/dev/null`）；`internal/console/console.go:99-105`（EOF → `false, nil`） | 默认 restart 会停掉旧进程、起一个立刻退出的子进程，父进程已打印成功 |
| P1-02 | 成立（限同主机） | `worker_host.go:129-136`（Read → Alive → Write）；`worker_host.go:318-322`（daemon 前置同款检查） | 文件锁挡同机并发；跨 Pod 无效，见正文的范围限定 |
| P1-03 | 成立 | `support/consumer/consume.go:535-548`（`park` 只记 Warn、无返回值）；`:449` / `:481` 调用后 `return nil` | 台账写 dead 失败仍然 ACK |
| P1-04 | 成立 | `support/consumer/kafka.go:253-256`（`client.Ping` 后立即 Ready，`w.Run` 在 `:266`）；`support/kafka/client.go:150-161`（Ping 走原始 client）；`support/kafka/consumer.go:266-281`（受管情况另建 kgo.Client）；`amqp.go:220-224`；`support/amqp/queue.go:56-64`（NewQueue 只校验，声明在 `Consume` 内）；`rocketmq.go:198`；`queues.go:188` | 四个消费者都在真正 Consume/声明之前上报就绪 |
| P1-05 | 成立 | `support/worker/ready.go:76-92`（`signal` 无实例键）；`support/worker/runner.go:276`（每次重启复用同一 gate）、`:183-190`（按实例数累加 pending） | 同一实例重启后再次 Ready 会替兄弟实例扣数 |
| P2-01 | 成立 | `worker_host.go:329-342`（Detach 成功即打印"已在后台启动"） | 与 P1-01 组合时假成功最明显 |
| P2-02 | 成立（语义待定） | `examples/demo-service/config.go:78-81`（`stop` / `restart` 被排除在 Nacos sync 外）；`worker_host.go:367-386`（restart 在本进程里直接 `runWorker`） | 与 P1-01 互相掩盖；demo 的 `enableNacosSyncConfig` 现为 `false`，前提被配置弱化 |
| P2-03 | 成立 | `internal/pidfile/pidfile.go`（`Write`/`Read` 只处理整数）；`stop.go:83-101`（`Alive` 即发信号） | PID 复用即可误杀无关进程 |
| P2-04 | 成立 | `examples/demo-service/main.go:32`（`loadNacosConfig`）→ `:50`（`configInvocation`）→ `:58`（`config.Load`） | 控制命令也会被损坏的配置文件挡住 |
| P2-05 | 成立（建议已修正） | `support/consumer/consume.go:803-816`（`time.Since(row.UpdatedAt)`） | 原建议只换比较侧无效，写入侧同样要用数据库时间 |
| P2-06 | 与 P1-05 合并 | `support/worker/ready.go:76-92` 与注释不一致 | 同一缺陷的 API 层面，不单独排期 |
| P2-07 | 成立（示例层） | `examples/demo-service/worker/group1/handler_order.go:89`（`status <> target` 即更新） | 乱序事件可把 Cancelled 改回 Paid |

复核方法：只读代码，不启动 broker。P1-01 的时序（确认 EOF → 子进程退出）与 P1-05 的
计数（重启复用 gate）都可由代码路径直接推出，不需要真实进程；其余各项是单点事实。

复核**没有**发现报告之外的 P1 级问题；也没有发现报告中"已修复"清单里与代码不符的条目。

---

## 9. P1 修复记录（2026-11-16 下午）

五条 P1 按依赖顺序修复，每条一个提交、都带回归测试；`make check` 全绿。

| 编号 | 提交 | 修法 | 回归测试 |
|---|---|---|---|
| P1-01 | `5b938ff` | daemon 子进程的 argv 由 `commandline.WorkerStartArgs` 从解析后的选项重建为 `worker --group=... [--name=...] --daemon=false`，不再复制 `os.Args`，也不再携带 `--force` / `--timeout` | `TestWorkerStartArgs`（含"argv 里不能出现 restart/force/timeout"）、`TestDaemonizeWorkerStartsTheWorkerCommand`（注入 detach 钩子断言 argv） |
| P1-03 | `a8b07e5` | `Processor.park` 返回错误并由 `Handle` 传播；dead 状态未落库时不得 ACK。`claim` 的 attempts 预算仍然兜底 | `TestLedgerRetriesWhenParkFails`、`TestLedgerRetriesUndecodableMessageWhenParkFails`（SQLite 触发器让 UPDATE 失败）、`TestLedgerAcknowledgesPermanentFailureWhenParkSucceeds` |
| P1-05 | `5cffc5d` | 闸门改为 `map[Identity]struct{}`，`Ready(ctx)` 从 runner 注入的身份取键，重复信号不再消耗兄弟实例名额；登记集合与 goroutine 用同一份实例列表 | `TestReadyGateIgnoresASecondSignalFromTheSameInstance`、`TestReadyIsNotClosedByARepeatedSignal` |
| P1-04 | `74db36a` | 四个传输层的 `ConsumeOptions` 新增 `OnReady`：Kafka 首次无致命错误 poll 之后、AMQP 拓扑声明 + `ConsumeWithContext` 之后、RocketMQ `Start` 之后、Redis 队列首次 pop 之后；`support/consumer` 改由它上报就绪。延迟订阅也补 `ReadyReporter` | `TestConsumerReportsReadyAfterStart` / `...WhenStartFails`（RocketMQ 假消费者）、`TestConsumeReportsReadyOnceAfterFirstPop`、`TestDelayConsumeReportsReadyOnceAfterFirstPop`；本地 RabbitMQ 实跑确认 ready 在订阅之后 |
| P1-02 | `7df9820` | 新增 `internal/proclock`（`flock` / `LockFileEx`），`claimWorkerPID` 先抢 `<pid>.lock` 再检查并写 pid；锁随进程退出释放 | `TestAcquireIsExclusive`、`TestAcquireAfterReleaseSucceeds`、`TestClaimWorkerPIDIsExclusive`、`TestClaimWorkerPIDOverwritesAStaleFile`；`/tmp/demo-svc` 起两个进程，第二个 `ErrAlreadyRunning`，前一个退出后第三个能启动 |

六条 P2 随后也已修复，见 §10。

---

## 10. P2 修复记录（2026-11-16 下午，接 §9）

| 编号 | 提交 | 修法 | 回归测试 |
|---|---|---|---|
| P2-03 | `ba3be7b` | pid 文件由纯整数改为 JSON 记录（pid / executable / claimed_at / token）。token 取进程启动标识：Linux 用 boot id + `/proc/<pid>/stat` 的 starttime，macOS 用 `kern.proc.pid`，Windows 用创建时间 FILETIME。`stop` 发信号前校验，不匹配按"未运行"处理并删除陈旧文件；旧格式仍可读（token 为空 = 未知，退回存活检查并记警告） | `TestWriteRecordCarriesIdentity`、`TestReadRecordAcceptsLegacyInteger`、`TestVerifyDetectsRecycledPID`、`TestStopRefusesARecycledPid`（真实子进程 + 伪造 token，断言未被杀掉） |
| P2-01 | `5ea0111` | daemon 父进程给子进程一条管道（`ExtraFiles` → fd 3，编号经 `GOFY_DAEMON_NOTIFY` 告知），子进程在 `Runner.Ready()` 之后写 `ready`、`abort()` 之前写 `error: ...`；父进程在 `--startup-timeout`（默认 30s）内等这一行，只有收到 `ready` 才打印成功。超时只报"未确认"，不杀子进程 | `TestDaemonizeWorkerStartsTheWorkerCommand`（断言 argv 且回应 ready）、`TestDaemonizeWorkerReportsChildFailure`、`TestDaemonizeWorkerReportsSilentChildDeath`；实跑：锁被占时父命令带子进程原因失败，`--startup-timeout=1ms` 报未确认且子进程继续运行 |
| P2-02 + P2-04 | `daecdc4` | 命令行先分类再决定读什么：`version` / `--help` 不读任何配置，`stop` / `worker stop` 只读 `config.yaml`（pid 路径），`worker --list` 不读 `nacos.yaml`，`start` / `worker run` / `restart` / `script` 读完整配置。`worker restart` 归为启动形态，同步发生在停旧进程之前，拉取失败时旧实例继续运行。控制命令只用最小 App（不建 DB、模块、中间件、telemetry、日志文件） | `TestPlanCommand`（逐命令断言六个开关）；实跑：`nacos.yaml` 损坏时 `stop` / `worker stop` / `worker --list` 均正常，`config.yaml` 损坏时 `version` 仍可用 |
| P2-05 | `a9f7712` | 租约两侧都改用数据库时钟：写入用 `CURRENT_TIMESTAMP`，过期判断在 SQL 里做（MySQL `INTERVAL ... MICROSECOND`、PostgreSQL 区间乘法、SQLite `datetime()` 归一化）；接管 CAS 里复核一次租约条件。未知方言退回节点时钟并在 README 写明需要 NTP | `TestLedgerLeaseIsWrittenInDatabaseTime`（无时区偏移即数据库写入）、`TestLedgerLeaseDecisionComesFromTheDatabase`（struct 无时间戳不再等于"过期"）、`TestLeasePredicateOnMySQL`（`GOFY_TEST_MYSQL_DSN` 下对真实 MySQL 验证新鲜/过期两种行） |
| P2-07 | `2d9394a` | `orderEventRules` 把目标状态与允许的前置状态放在一起：`order.paid` 只从 `pending`，`order.cancelled` 从 `pending`/`paid`，`cancelled` 是终态。条件写进 UPDATE（`status IN (...)`），0 行时再读一次区分"未落库（重试）/ 已是目标（幂等）/ 过期事件（忽略并告警）" | `TestApplyOrderEventTransitions`（含 `cancelled → order.paid` 不得回退）、`TestApplyOrderEventRetriesUnknownOrder`、`TestApplyOrderEventRejectsUnknownEventType` |
| P2-06 | 合并入 P1-05（`5cffc5d`） | 闸门按 `Identity` 去重，注释与实现一致 | `TestReadyGateIgnoresASecondSignalFromTheSameInstance` |

`make check`（fmt / vet / race / example / 两个 import guard / contract snapshot / CGO-free
build）在执行完全部 P2 后全绿。报告中的 P1 与 P2 至此全部关闭；§5 里需要补的集成用例（Nacos
动作失败、注册失败）仍建议按各自发布节奏补上，它们不在这份报告的缺陷清单内。
