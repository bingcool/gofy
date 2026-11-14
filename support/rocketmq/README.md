# support/rocketmq

在 RocketMQ 之上提供连接、生产与消费，参考 swoolefy 的
`Swoolefy\Support\Rocketmq`。PHP 版本通过 go-sidecar 的 proxy 协议访问
RocketMQ；Go 版本直接使用官方 v5 客户端
[apache/rocketmq-clients/golang/v5](https://github.com/apache/rocketmq-clients/tree/master/golang)
（本仓库锁定 `v5.1.2`），走 **gRPC proxy**（默认端口 8081），不再使用
remoting 协议和 NameServer 9876，也不需要 sidecar。与 `support/redis`、
`support/db`、`support/amqp`、`support/kafka` 一样，本包默认不持有全局状态：

- `New(ctx, Config)` 校验配置、创建 producer，可选地通过 `StartupTopics` 做启动探测；
- `Wrap(rmq.Producer)` 注入已经建好的 producer；
- `NewConsumer(Config, ConsumerConfig)` 按订阅配置单独创建拉取型消费者；
- 需要按配置别名取用时，用可选的 `Configure` / `NewIO` 命名实例注册表；
- 多实例也可以由应用装配层负责，例如 demo 的 `app/appio/rocketmq`：`default`
  发布为 `rocketmq.Default`，其它实例用 `rocketmq.Get("名字")` 取用。

v5 与 v2 的关键差异：

- 连接的是 proxy 的 `host:port`，不是 NameServer 列表；集群需要部署 5.x 的
  proxy（默认 8081）。
- producer 不再注册 producer group，也没有 group 冲突问题；消费者必须显式
  传 group。
- **没有 push consumer**。v5.1.2 只提供 `SimpleConsumer`（拉取），本包在
  `Consume` 里实现长轮询、逐条 ack 和失败重投，API 保持不变。
- 延时消息使用绝对时间（`PublishOptions.DeliverAt`），不再有 1–18 延时级别。

## 连接配置

```go
import (
    "context"
    "time"

    "github.com/bingcool/gofy/support/rocketmq"
)

client, err := rocketmq.New(ctx, rocketmq.Config{
    Endpoint:    "10.0.0.11:8081",
    SendTimeout: 3 * time.Second,
    MaxAttempts: 3,
})
if err != nil {
    return err
}
defer client.Close()
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Endpoint` | 必填 | gRPC proxy 地址 `host:port`，多个地址用 `;` 连接；错误信息里只出现它，不会带出凭据 |
| `Namespace` | 空 | 多租户集群的命名空间前缀，会加到 topic 和 group 上 |
| `AccessKey` / `AccessSecret` | 空 | ACL 认证；两者必须同时设置或同时留空 |
| `SecurityToken` | 空 | 临时凭据的 token；设置时必须同时提供 `AccessKey` / `AccessSecret` |
| `SendTimeout` | `3s` | 单次同步发送的上限，同时用作路由查询超时 |
| `MaxAttempts` | `3` | 单条消息的最大发送次数（含首次）；`0` 落到默认值 `3` |
| `StartupTopics` | 空 | 可选的启动探测 topic；topic 属于业务代码，通常留空，第一次发布时再建连 |
| `SkipStartupCheck` | `false` | 即使配置了 `StartupTopics` 也跳过探测 |

`New` 会调用 `Start`。v5 的 producer 不绑定 topic，只有显式配置
`StartupTopics` 时，`Start` 才会查询这些 topic 的路由：endpoint 不可达或
topic 不存在都会让启动失败。默认不配置 topic，第一次 `Publish` 时才建立连接
并接触 broker；需要尽早发现配置错误时再加 `StartupTopics`。

注意 v5.1.2 的 SDK 会忽略 client 级别的连接选项，建连使用默认的 5 秒超时；
配置了 `StartupTopics` 且 endpoint 不可达时，`New` 至少会等待约 5 秒才返回错误。

## 命名实例（Configure / NewIO）

按配置别名直接取用 producer 时，启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`。producer 在第一次使用时启动并缓存：

```go
reg, err := rocketmq.Configure(map[string]rocketmq.Config{
    "default": {Endpoint: os.Getenv("ROCKETMQ_ENDPOINT")},
    "order":   {Endpoint: os.Getenv("ORDER_ROCKETMQ_ENDPOINT")},
})
if err != nil {
    return err
}
defer reg.Close()

client, err := rocketmq.NewIO(ctx, "default") // 业务代码：从当前一代取用
```

### 包级 NewIO 与 reg.NewIO 的区别

| 写法 | 从哪一代取用 | 适用场景 |
| --- | --- | --- |
| `rocketmq.NewIO(ctx, "default")` | 包级当前一代，即最近一次 `Configure` / `Install` 的那一代 | 业务代码不持有注册表，任何位置按别名直接取用 |
| `reg.NewIO(ctx, "default")` | `reg` 这一代，与它是否已安装无关 | 装配层、热更新、测试中持有该代引用 |

两者的关系按 `reg` 所处的代区分：

- `reg` 是 `Configure` 返回的当前代时，`reg.NewIO` 和包级 `rocketmq.NewIO`
  共用同一份懒加载缓存，返回同一个发布用 `*Client`，不会建第二个 producer；
- `reg` 是用 `NewRegistry` 建好但尚未 `Install` 的新代时，`reg.NewIO`
  只预建这一代的 producer，业务侧包级调用仍然用旧代；全部实例就绪后
  `reg.Install()` 才切换；
- 关闭同样按代区分：`CloseAllIO()` 只关闭当前一代，`reg.Close()` 关闭
  `reg` 这一代（即使它已经不是当前代）。

注册表自身的约定：

- `Configure` 校验每个别名和 `Config`：空别名或非法配置直接返回错误；空 map
  表示禁用全部实例（`NewIO` 返回 `ErrUnknownInstance`）。v5 没有 producer
  group，因此不再做组名去重。
- 未注册的名称返回 `ErrUnknownInstance`；未调用 `Configure`，或当前一代已经
  关闭，返回 `ErrNotConfigured`。
- 启动失败不会缓存，下一次 `NewIO` 会重新尝试。
- 首次启动在注册表锁外执行：不同别名的 producer 并行初始化，互不阻塞；同一
  别名的并发首次调用只建一个 producer，其余调用等待同一次结果。
- `Close` 开始后不再发布新 producer：已经开始、在 `Close` 之后才完成的 producer
  会被立即关闭，调用方得到 `ErrNotConfigured`。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己启动的
  producer，不影响已经切换到新一代的调用方；demo 热更新依赖这个顺序（新模块
  先启动、旧模块后关闭）。
- `Configure` 等价于 `NewRegistry` + `Install`。热更新时先用 `NewRegistry`
  建好新连接，全部成功后再 `Install` 切换；失败只关闭新代，不影响正在
  服务的旧代。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。
- 包级 `NewIO` / `CloseAllIO` 是单 App、单 Registry 的便捷入口：同一进程内多个
  Registry 都 `Install` 时，包级调用只看最后安装的那一代，不承诺多 App 隔离；
  需要代际隔离时用 `reg.NewIO` 与 `reg.Close`。
- 注册表只缓存 producer；消费者由业务用 `NewConsumer` 创建，需要各自
  `Consume` / `Close`。

不调用 `Configure` 时，包的行为与 `New`/`Wrap`/`Close` 完全一致，不产生全局状态。

## 注入已有 producer

```go
import rmq "github.com/apache/rocketmq-clients/golang/v5"

raw, err := rmq.NewProducer(
    &rmq.Config{Endpoint: "127.0.0.1:8081"},
    rmq.WithTopics("order-events"),
)
if err != nil {
    return err
}
if err := raw.Start(); err != nil {
    return err
}

client := rocketmq.Wrap(raw)
defer client.Close()
```

`Wrap` 接管关闭权，但不会修改 producer 配置。注入的客户端没有 `Config`，
`Config()` 返回零值；`Publish` 仍然可用。需要消费时，`NewConsumer` 用的是
`Config`，不是这个客户端。

## 生产

```go
result, err := client.PublishJSON(ctx, order, rocketmq.PublishOptions{
    Topic:        "order-events",
    Tag:          "created",          // 消费端按 Tag 或 SQL92 过滤
    Keys:         []string{order.No}, // broker 建索引，控制台可按 Key 查消息
    Properties:   map[string]string{"region": "cn", "trace-id": traceID},
    DeliverAt:    time.Now().Add(10 * time.Second), // 10 秒后投递
    MessageGroup: order.No,           // FIFO：同一 group 的消息按顺序发送
})
if err != nil {
    return err
}
logger.Info("published",
    zap.String("message_id", result.MessageID),
    zap.String("topic", result.Topic),
    zap.Int64("offset", result.Offset),
)
```

| 字段 | 说明 |
| --- | --- |
| `Topic` | 必填；`Client` 不绑定 topic，每次发布都要指定 |
| `Tag` | 消息分类，消费端按 Tag 订阅过滤 |
| `Keys` | 业务 Key，最多 32 个，每个不超过 128 字节且不含空白；重复的自动去掉 |
| `Properties` | 自定义属性，最多 32 个；Key 只能是字母、数字、`_`、`.`、`-`，不能以 `__` 开头；Value 不超过 1024 字节；合计不超过 16 KiB |
| `DeliverAt` | 延时投递的绝对时间，必须晚于当前时间，否则返回 `ErrInvalidDelay`；零值立即发送 |
| `Delay` | `DeliverAt` 的相对写法（如 `10*time.Second`），内部换算成绝对投递时间；与 `DeliverAt` 同时设置返回 `ErrInvalidDelay` |
| `MessageGroup` | FIFO 消息的分组 key；同一 group 的消息进入同一队列并保持发送顺序 |

- `Publish` 是同步语义：`Send` 等待 broker 确认后返回，等待时间由
  `Config.SendTimeout` 和 `ctx` 中较早结束的一方限制。`SendResult.Offset`
  是第一条回执的队列位点。
- `PublishJSON` 会补一个 `content-type: application/json` 属性（调用方已设置
  时不覆盖）。
- 设置 `MessageGroup` 的消息按 FIFO 类型发送，设置 `DeliverAt` / `Delay` 的消息按延时
  类型发送；两者同时设置时 SDK 以 FIFO 为准。topic 的 `message.type` 必须
  匹配（FIFO 或 DELAY），否则 broker 返回
  `current message type not match with topic accept message types`。本地 broker 建一次
  DELAY topic：

  ```bash
  mqadmin updateTopic -n <namesrv> -t <topic> -c DefaultCluster -a +message.type=DELAY
  ```

- 消费端 `Message.DeliveryTimestamp` 是计划投递时刻，`BornTimestamp` 是生产时刻，
  两者相减可核对实际延迟。
- `Client` 可被多个 goroutine 并发使用；单条消息的发送次数由
  `Config.MaxAttempts` 控制。

## 消费

```go
consumer, err := rocketmq.NewConsumer(clientConfig, rocketmq.ConsumerConfig{
    Topic: "order-events",
    Group: "inventory-service",
    Tags:  []string{"created", "cancelled"},
})
if err != nil {
    return err
}
defer consumer.Close()

err = consumer.Consume(ctx, func(ctx context.Context, message *rocketmq.Message) error {
    var event OrderEvent
    if err := json.Unmarshal(message.Body, &event); err != nil {
        return err
    }
    return handle(ctx, message.MessageID, event) // 按 MessageID 或 Keys 幂等
}, rocketmq.ConsumeOptions{
    OnError: func(err error) {
        logger.Error("rocketmq consumer", zap.Error(err))
    },
})
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Topic` / `Group` | 必填 | 订阅的 topic 与消费者组；v5 的消费者必须显式指定 group |
| `Tags` | 全部 | 订阅的 Tag；空数组或 `["*"]` 表示全部，元素不能含 `\|\|` |
| `SQL` | 空 | SQL92 属性过滤，例如 `eventType IN ('created','cancelled')`；不能和具体 Tag 同时设置 |
| `BatchSize` | `16` | 一次 `Receive` 最多取回的消息数 |
| `Concurrency` | `1` | 拉取 worker 数；大于 1 时批次之间无序 |
| `AwaitDuration` | `5s` | 一次长轮询在 broker 侧的等待时间 |
| `InvisibleDuration` | `30s` | 消息投递后的不可见时间；处理失败未 ack 的消息在这之后重投 |

`Message` 暴露的字段：`MessageID`、`Topic`、`Tag`、`Keys`、`Body`、
`Properties`、`MessageGroup`、`Offset`、`DeliveryAttempt`、
`DeliveryTimestamp`、`BornTimestamp`。

处理与确认语义：

- **默认错误策略是至少一次**：处理函数返回错误时，该消息不 ack；不可见时间
  （`InvisibleDuration`）结束后 broker 重新投递，`DeliveryAttempt` 从 1 开始
  递增。业务按 `MessageID` 或 `Keys` 做幂等。
- 每个 worker 按批次顺序处理消息。一个批次内，处理成功的逐条 ack；失败的
  不 ack，同批其它消息继续处理。
- `DiscardOnError: true` 时上报错误后 ack 失败消息并继续，适合病态消息
  （poison message）需要人工介入的场景。
- 处理函数 panic 会被恢复成错误，并按上面的策略重投，堆栈通过 `OnError`
  上报。
- `ctx` 取消或调用 `Close` 时 `Consume` 返回 `nil`，可以直接作为常驻
  goroutine 的函数体。`Close` 只发出停止信号并立即返回，底层客户端在
  worker 退出后由 `Consume` 关闭。
- `Consumer` 是一次性的：`Consume` 只允许调用一次，结束后用 `Close` 释放。
  未启动就调用 `Close` 是安全的。
- `NewConsumer` 只校验连接配置和订阅配置，不连接；第一次 `Consume` 时才
  `Start` 拉取客户端。消费者不需要 `Client`，可以复用同一份 `Config`。
- 消费者使用集群消费模式（Clustering）。同一个消费组 + Topic 的多个实例
  分摊队列；`DeliveryAttempt` 可用于识别重投。
- 拉取循环遇到长轮询超时会继续等待；其它接收错误通过 `OnError` 上报并退避
  1 秒，不会忙轮询。

### 订阅过滤

Tag 表达式由本包拼成 `paid || cancel` 后交给 broker；被过滤掉的消息不会
投递给这个组，也不进重试。需要按属性过滤时改用 `SQL`：

```go
consumerCfg := rocketmq.ConsumerConfig{
    Topic: "order-events",
    Group: "stock-service",
    SQL:   "eventType IN ('created', 'cancelled')",
}
```

- SQL92 需要 broker 配置 `enablePropertyFilter=true`。
- 消息上没有表达式引用的属性时视为不匹配，不会投递。
- 订阅关系属于消费组。同一个消费组的所有实例必须用同一个过滤条件；条件不同
  就用不同的消费组。
- `SQL` 与具体的 `Tags` 同时设置时，`NewConsumer` 直接返回
  `ErrTagsConflictSQL`。

## API 摘要

| API | 说明 |
| --- | --- |
| `New(context.Context, Config) (*Client, error)` | 校验配置、创建并启动 producer；配置了 `StartupTopics` 时探测路由 |
| `Configure(map[string]Config) (*Registry, error)` | 注册命名配置，生成新一代注册表并安装 |
| `NewRegistry(map[string]Config) (*Registry, error)` | 只生成新一代注册表，不安装 |
| `(*Registry) Install()` | 将该代切换为包级当前代 |
| `NewIO(context.Context, string) (*Client, error)` | 按别名取用当前一代的 producer，首次使用时启动 |
| `(*Registry) NewIO(context.Context, string) (*Client, error)` | 从指定一代取用 producer，与是否安装无关 |
| `(*Registry) Close() error` / `CloseAllIO() error` | 关闭该代 / 当前一代启动的 producer |
| `Wrap(rmq.Producer) *Client` | 注入已有 producer，关闭权归本包 |
| `(*Client) Raw() rmq.Producer` | 取底层 producer，供高级用法 |
| `(*Client) Config() Config` | 有效连接配置；`Wrap` 返回零值 |
| `(*Client) Close() error` / `IsClosed() bool` | 关闭 / 查询状态（幂等） |
| `(*Client) Publish(context.Context, []byte, PublishOptions) (*SendResult, error)` | 同步发送字节消息 |
| `(*Client) PublishJSON(context.Context, any, PublishOptions) (*SendResult, error)` | JSON 序列化后发送 |
| `NewConsumer(Config, ConsumerConfig) (*Consumer, error)` | 创建拉取型消费者，不建连 |
| `(*Consumer) Consume(context.Context, HandlerFunc, ConsumeOptions) error` | 启动拉取循环并处理消息，阻塞到 ctx 取消或 Close |
| `(*Consumer) Config() ConsumerConfig` | 有效订阅配置 |
| `(*Consumer) Close() error` | 发送停止信号（幂等），底层客户端由 `Consume` 释放 |

## 测试

单元测试不需要 broker：用例使用不可达的 `127.0.0.1:1` 并跳过启动探测，覆盖
配置校验、消息构造限制、失败不缓存、注册表分代和消费回调语义。
`TestNewFailsWhenEndpointUnreachable` 是唯一依赖 SDK 建连超时的用例，约 5 秒。

集成测试需要真实的 RocketMQ 5.x proxy（其后的 broker 允许自动创建 topic 与
消费组）：

```bash
GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 \
  go test ./support/rocketmq -run Integration -v
```

集成用例会为每次运行生成唯一的 topic 和消费组，覆盖发布/消费、Tag 过滤、
JSON 属性与 ack 语义；topic 在首次发送时自动创建。

demo 的 `config.yaml` 默认把 `rocketmq` 实例设为 `enabled: false`，本地没有
RocketMQ proxy 时服务照常启动；接上真实集群后改为 `true`，并把
`startup_topics` 换成集群里已存在的 topic。
