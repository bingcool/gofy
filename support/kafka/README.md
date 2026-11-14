# support/kafka

在 Kafka 之上提供连接、生产与消费，参考 swoolefy 的 `Swoolefy\Library\Kafka`
（librdkafka 封装）。底层使用纯 Go 的
[franz-go](https://github.com/twmb/franz-go)，不需要 cgo。与 `support/redis`、
`support/db`、`support/amqp` 一样，本包默认不持有全局状态，也不替业务决定
实例数量：

- `New(ctx, Config)` 校验配置并发送一次 metadata 请求确认 broker 可达；
- `Wrap(*kgo.Client)` 注入已经建好的 franz-go 客户端；
- `NewProducer` / `NewConsumer` 在客户端之上创建生产者和消费者；
- 需要按配置别名取用时，用可选的 `Configure` / `NewIO` 命名实例注册表；
- 多实例也可以由应用装配层负责，例如 demo 的 `app/appio/kafka`：`default` 发布为
  `kafka.Default`，其它实例用 `kafka.Get("名字")` 取用。

与 AMQP 的一个关键差异：franz-go 把消费者组配置放在客户端级别，因此
**Producer 共享 `New` 创建的客户端，每个 Consumer 用同一份连接配置单独建一个
客户端**。这样可以同时运行多个消费者组，而不会互相覆盖 group 设置。

## 连接配置

```go
import (
    "context"
    "os"
    "time"

    "github.com/bingcool/gofy/support/kafka"
)

client, err := kafka.New(ctx, kafka.Config{
    Brokers:       []string{"10.0.0.11:9092", "10.0.0.12:9092"},
    ClientID:      "order-service",
    DialTimeout:   5 * time.Second,
    Idempotent:    true,
    RecordRetries: 10,
})
if err != nil {
    return err
}
defer client.Close()
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Brokers` | 必填 | seed broker 列表，其余节点由 metadata 自动发现 |
| `ClientID` | `gofy` | Kafka client id |
| `DialTimeout` | `5s` | 每个 broker 的 TCP 建连超时 |
| `Idempotent` | `false` | 幂等生产；开启后由 broker 去重，需要集群允许幂等写入 |
| `RecordRetries` | `10` | 单条消息的最大生产重试次数；`-1` 表示不限（franz-go 默认） |

`New` 会跳过连不上的 seed broker，全部失败时返回聚合错误；错误中只出现
`host:port`。本包不创建 topic：生产环境通常由基础设施预先建好 topic，开发环境
可以开启 broker 的 `auto.create.topics.enable`。

## 命名实例（Configure / NewIO）

按配置别名直接取用客户端时，启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`。客户端在第一次使用时建连并缓存：

```go
reg, err := kafka.Configure(map[string]kafka.Config{
    "default": {Brokers: []string{os.Getenv("KAFKA_BROKERS")}, ClientID: "order-service"},
    "order":   {Brokers: []string{os.Getenv("ORDER_KAFKA_BROKERS")}, ClientID: "order-service"},
})
if err != nil {
    return err
}
defer reg.Close()

client, err := kafka.NewIO(ctx, "default") // 业务代码：从当前一代取用
```

### 包级 NewIO 与 reg.NewIO 的区别

| 写法 | 从哪一代取用 | 适用场景 |
| --- | --- | --- |
| `kafka.NewIO(ctx, "default")` | 包级当前一代，即最近一次 `Configure` / `Install` 的那一代 | 业务代码不持有注册表，任何位置按别名直接取用 |
| `reg.NewIO(ctx, "default")` | `reg` 这一代，与它是否已安装无关 | 装配层、热更新、测试中持有该代引用 |

两者的关系按 `reg` 所处的代区分：

- `reg` 是 `Configure` 返回的当前代时，`reg.NewIO` 和包级 `kafka.NewIO`
  共用同一份懒加载缓存，返回同一个生产用 `*Client`，不会建第二条连接；
- `reg` 是用 `NewRegistry` 建好但尚未 `Install` 的新代时，`reg.NewIO`
  只预建这一代的连接，业务侧包级调用仍然用旧代；全部实例就绪后
  `reg.Install()` 才切换；
- 关闭同样按代区分：`CloseAllIO()` 只关闭当前一代，`reg.Close()` 关闭
  `reg` 这一代（即使它已经不是当前代）。

注册表自身的约定：

- `Configure` 校验每个别名和 `Config`：空别名或非法配置直接返回错误；空 map
  表示禁用全部实例（`NewIO` 返回 `ErrUnknownInstance`）。
- 未注册的名称返回 `ErrUnknownInstance`；未调用 `Configure`，或当前一代已经
  关闭，返回 `ErrNotConfigured`。
- 建连失败不会缓存，下一次 `NewIO` 会重新尝试。
- 首次建连在注册表锁外执行：不同别名的连接并行初始化，互不阻塞；同一别名的
  并发首次调用只建一个客户端，其余调用等待同一次结果。
- `Close` 开始后不再发布新连接：已经开始、在 `Close` 之后才完成的客户端会被
  立即关闭，调用方得到 `ErrNotConfigured`。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己打开的
  连接，不影响已经切换到新一代的调用方；demo 热更新依赖这个顺序（新模块先
  启动、旧模块后关闭）。
- `Configure` 等价于 `NewRegistry` + `Install`。热更新时先用 `NewRegistry`
  建好新连接，全部成功后再 `Install` 切换；失败只关闭新代，不影响正在
  服务的旧代。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。
- 包级 `NewIO` / `CloseAllIO` 是单 App、单 Registry 的便捷入口：同一进程内多个
  Registry 都 `Install` 时，包级调用只看最后安装的那一代，不承诺多 App 隔离；
  需要代际隔离时用 `reg.NewIO` 与 `reg.Close`。
- 注册表只缓存生产用的 `Client`；`NewConsumer` 仍然按消费者组单独建客户端，
  这些消费者需要各自 `Close`。

不调用 `Configure` 时，包的行为与 `New`/`Wrap`/`Close` 完全一致，不产生全局状态。

## 注入已有客户端

```go
raw, err := kgo.NewClient(
    kgo.SeedBrokers("127.0.0.1:9092"),
    kgo.ConsumerGroup("order-group"),
)
if err != nil {
    return err
}
client := kafka.Wrap(raw)
defer client.Close()
```

`Wrap` 接管关闭权，但不会修改客户端的配置。用它创建 Consumer 时，
`ConsumerConfig.Group` 等组配置不会生效，**注入的客户端必须已经配置好
`kgo.ConsumerGroup`、offset reset 和 rebalance 回调**；`NewConsumer` 只会把
topic 加入订阅。

## 生产

```go
producer, err := kafka.NewProducer(client, kafka.ProducerConfig{
    Topic:        "order.created",
    FlushTimeout: 5 * time.Second,
})
if err != nil {
    return err
}

err = producer.PublishJSON(ctx, order, kafka.PublishOptions{
    Key:     orderID, // 同 key 进入同一分区，保证同一订单的事件有序
    Headers: map[string]string{"x-trace-id": traceID},
})
```

- `Publish` 是同步语义：内部使用 `ProduceSync`，等待 broker 确认后返回；等待
  时间由 `FlushTimeout` 和 `ctx` 中较早结束的一方限制。
- 默认使用 Kafka 的分区器，`Key` 决定分区；需要严格顺序时使用稳定的业务 id。
- `PublishJSON` 会补一个 `content-type: application/json` header（调用方已设置
  时不覆盖）。
- 单条消息失败不会自动重试到无限；重试次数由 `Config.RecordRetries` 控制。

## 消费

```go
consumer, err := kafka.NewConsumer(client, kafka.ConsumerConfig{
    Topic:           "order.created",
    Group:           "order-group",
    // 默认 commit_mode: manual，处理成功后才提交位点
    AutoOffsetReset: "earliest",
    SessionTimeout:  45 * time.Second,
    OnPartitionsAssigned: func(ctx context.Context, assigned map[string][]int32) {
        logger.Info("partitions assigned", zap.Any("partitions", assigned))
    },
})
if err != nil {
    return err
}
defer consumer.Close()

err = consumer.Consume(ctx, func(ctx context.Context, record *kgo.Record) error {
    var order Order
    if err := json.Unmarshal(record.Value, &order); err != nil {
        return err
    }
    return handle(ctx, order)
}, kafka.ConsumeOptions{
    OnError: func(err error) {
        logger.Error("kafka consumer", zap.Error(err))
    },
})
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Topic` / `Group` | 必填 | 订阅的 topic 与消费者组 |
| `CommitMode` | `manual` | `manual` 只提交处理成功的位点；`auto` 由 franz-go 周期提交已拉取位点 |
| `DisableAutoCommit` | `false` | 已弃用；`true` 等价于 `commit_mode: manual`，`false` 不再表示自动提交 |
| `AutoCommitInterval` | `200ms` | 自动提交间隔，仅 `commit_mode: auto` 生效 |
| `AutoOffsetReset` | `earliest` | 无位点时从最早或最新开始，可选 `latest` |
| `SessionTimeout` | `45s` | 消费者组会话超时 |
| `BlockRebalanceOnPoll` | `false` | 当前批次处理并提交完再放行重平衡；franz-go 没有 `max.poll.interval.ms`，用它替代 |
| `OnPartitionsAssigned` / `OnPartitionsRevoked` | 无 | rebalance 钩子，仅对受管客户端生效 |

Java 客户端的 `max.poll.interval.ms` 在这里没有对应项：它存在是因为应用线程处理期间心跳会
停，而 franz-go 的心跳在独立 goroutine 上，慢 handler 不会把成员踢出组。真正还会发生的是
重平衡落在 handler 与提交之间，导致提交失败、整批重放；`BlockRebalanceOnPoll: true` 把这段
窗口关掉——当前批次跑完、位点提交后才放行重平衡。

处理与位点语义：

- **默认 `commit_mode: manual`，投递语义是 at-least-once**：处理函数返回错误时，
  先提交此前成功的位点，再让 `Consume` 返回该错误。失败消息没有提交，重启并重新
  加入组后会从最后提交的位点重新投递。适合“宁可重复、不可丢失”的业务。
- `DiscardOnError: true` 时上报错误后跳过该条消息，并提交其位点，用于病态消息
  （poison message）需要人工介入的场景；该条消息按 at-most-once 处理。
- 处理函数 panic 会被恢复成错误，并按上面的策略处理，堆栈通过 `OnError`
  上报。
- `commit_mode: auto` 必须显式配置：franz-go 会周期性提交**已拉取**的位点，
  处理失败的消息也可能已被提交。只适合副作用可重复执行、丢失单条消息可接受的
  场景（缓存刷新、配置同步等），订单、支付等业务保持默认 `manual`。
- `AutoCommitReset` 只在组内没有已提交位点时生效；`earliest` 适合首次上线，
  `latest` 适合只处理新消息。
- franz-go 在后台维护心跳和 rebalance，不需要像 librdkafka 那样调大
  `max.poll.interval.ms`；处理慢的业务通过增加分区和消费者实例提升吞吐。
- `ctx` 取消时 `Consume` 返回 `nil`，可以直接作为常驻 goroutine 的函数体。
- 一个 `Consumer` 只应由一个 goroutine 调用 `Consume`。`Close` 会退出消费者组；
  由 `Wrap` 创建的 Consumer 不会关闭共享客户端。

## 手动提交与重平衡

默认的 `commit_mode: manual` 下，本包按分区批次提交最后一个成功处理的位点。
如果希望在失去分区前提交当前进度，可以使用 `OnPartitionsRevoked`：

```go
OnPartitionsRevoked: func(ctx context.Context, revoked map[string][]int32) {
    // 这里的 ctx 在 rebalance 结束前有效；本包已经同步提交了成功位点，
    // 业务可以在这里记录日志或刷新本地状态。
},
```

franz-go 允许 poll 与 rebalance 并发，极端情况下同一条消息可能被两个成员处理；
这是 at-least-once 的正常表现，业务处理需幂等。

## API 摘要

| API | 说明 |
| --- | --- |
| `New(context.Context, Config) (*Client, error)` | 校验配置并确认 broker 可达 |
| `Configure(map[string]Config) (*Registry, error)` | 注册命名配置，生成新一代注册表并安装 |
| `NewRegistry(map[string]Config) (*Registry, error)` | 只生成新一代注册表，不安装 |
| `(*Registry) Install()` | 将该代切换为包级当前代 |
| `NewIO(context.Context, string) (*Client, error)` | 按别名取用当前一代的客户端，首次使用时建连 |
| `(*Registry) NewIO(context.Context, string) (*Client, error)` | 从指定一代取用客户端，与是否安装无关 |
| `(*Registry) Close() error` / `CloseAllIO() error` | 关闭该代 / 当前一代打开的客户端 |
| `Wrap(*kgo.Client) *Client` | 注入已有客户端，关闭权归本包 |
| `(*Client) Raw() *kgo.Client` | 取底层客户端，供高级用法 |
| `(*Client) Config() Config` | 有效连接配置；`Wrap` 返回零值 |
| `(*Client) Ping(context.Context) error` | 发送 metadata 请求 |
| `(*Client) Close() error` / `IsClosed() bool` | 关闭 / 查询状态（幂等） |
| `NewProducer(*Client, ProducerConfig) (*Producer, error)` | 创建生产者，共享连接 |
| `(*Producer) Publish(context.Context, []byte, PublishOptions) error` | 同步发布字节消息 |
| `(*Producer) PublishJSON(context.Context, any, PublishOptions) error` | JSON 序列化后发布 |
| `NewConsumer(*Client, ConsumerConfig) (*Consumer, error)` | 创建消费者，受管客户端会单独建连 |
| `(*Consumer) Consume(context.Context, HandlerFunc, ConsumeOptions) error` | 拉取并处理消息 |
| `(*Consumer) Close() error` | 退出消费者组并释放客户端（仅受管消费者） |

## 集成测试

单元测试不需要 broker。集成测试通过环境变量开启，本机 docker 的 `dev-kafka`
可以用下面的方式运行：

```bash
GOFY_TEST_KAFKA_BROKERS=127.0.0.1:9092 \
  go test ./support/kafka -run Integration -v
```

集成用例会为每次运行创建唯一的 topic 和消费者组，覆盖同 key 顺序生产/消费、
手动提交、`Wrap` 注入、seed broker 回退和不可达 broker 的快速失败。
