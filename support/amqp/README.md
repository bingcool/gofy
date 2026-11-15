# support/amqp

在 AMQP 0-9-1（RabbitMQ）之上提供连接、拓扑声明、发布与消费，参考 swoolefy 的
`Swoolefy\Library\Amqp`。与 `support/redis`、`support/db` 一样，本包默认不持有
全局状态，也不替业务决定实例数量：

- `New(ctx, Config)` 按配置建连，连接断开后可以自动重连；
- `Wrap(conn)` 接管一个已经建好的 `*amqp091.Connection`，适合连接由外部创建
  或复用的场景；
- 需要按配置别名取用时，用可选的 `Configure` / `NewIO` 命名实例注册表；
- 多实例也可以由应用装配层负责，例如 demo 的 `app/appio/amqp`：每个实例一个
  `Client`，`default` 发布为业务句柄，其它实例按名字取用。

## 连接配置

```go
import (
    "context"
    "os"
    "time"

    "github.com/bingcool/gofy/support/amqp"
)

client, err := amqp.New(ctx, amqp.Config{
    Hosts: []amqp.Host{
        {Host: "10.0.0.11", Port: 5672, Username: "app", Password: os.Getenv("AMQP_PASSWORD"), Vhost: "orders"},
        {Host: "10.0.0.12", Port: 5672, Username: "app", Password: os.Getenv("AMQP_PASSWORD"), Vhost: "orders"},
    },
    Heartbeat:         10 * time.Second,
    ConnectionTimeout: 5 * time.Second,
})
if err != nil {
    return err
}
defer client.Close()
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Host.Port` | `5672` | broker 端口 |
| `Host.Vhost` | `/` | 虚拟主机 |
| `Host.Scheme` | `amqp` | 可选 `amqps`，使用系统根证书走 TLS |
| `Heartbeat` | `10s` | 与 broker 协商的保活间隔 |
| `ConnectionTimeout` | `5s` | 每个 host 的建连与握手超时 |
| `Locale` | `en_US` | AMQP 握手 locale |

`New` 会跳过连不上的 host，按顺序尝试下一个；全部失败时返回聚合错误，错误里
只出现 `host:port`，不会带出用户名或密码。`Host.uri()` 用 `net/url` 编码凭据，
密码中包含 `@`、`:`、`/` 等保留字符也能正确往返。

## 注入已有连接

```go
conn, err := amqp091.Dial(uri)
if err != nil {
    return err
}
client := amqp.Wrap(conn)
defer client.Close()
```

`Wrap` 接管连接的关闭权，但没有配置可用于重连：连接失效后 `Channel()` 返回
`amqp.ErrNotConfigured`。需要自动重连时请用 `New`。

## 命名实例（Configure / NewIO）

按配置别名直接取用连接时，启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`。客户端在第一次使用时建连并缓存：

```go
reg, err := amqp.Configure(map[string]amqp.Config{
    "default": {
        Hosts: []amqp.Host{{Host: os.Getenv("AMQP_HOST"), Port: 5672, Username: "app", Password: os.Getenv("AMQP_PASSWORD")}},
    },
    "order": {
        Hosts: []amqp.Host{{Host: os.Getenv("ORDER_AMQP_HOST"), Port: 5672, Username: "app", Password: os.Getenv("ORDER_AMQP_PASSWORD")}},
    },
})
if err != nil {
    return err
}
defer reg.Close()

client, err := amqp.NewIO(ctx, "default") // 业务代码：从当前一代取用
```

### 包级 NewIO 与 reg.NewIO 的区别

| 写法 | 从哪一代取用 | 适用场景 |
| --- | --- | --- |
| `amqp.NewIO(ctx, "default")` | 包级当前一代，即最近一次 `Configure` / `Install` 的那一代 | 业务代码不持有注册表，任何位置按别名直接取用 |
| `reg.NewIO(ctx, "default")` | `reg` 这一代，与它是否已安装无关 | 装配层、热更新、测试中持有该代引用 |

两者的关系按 `reg` 所处的代区分：

- `reg` 是 `Configure` 返回的当前代时，`reg.NewIO` 和包级 `amqp.NewIO`
  共用同一份懒加载缓存，返回同一个 `*Client`，不会建第二条连接；
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
  并发首次调用只建一个 `Client`，其余调用等待同一次结果。
- `Close` 开始后不再发布新连接：已经开始、在 `Close` 之后才完成的连接会被立即
  关闭，调用方得到 `ErrNotConfigured`。
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

不调用 `Configure` 时，包的行为与 `New`/`Wrap`/`Close` 完全一致，不产生全局状态。

## 声明、发布与消费

```go
queue, err := amqp.NewQueue(client, amqp.QueueConfig{
    Topology: amqp.Topology{
        Exchange:     "orders",
        ExchangeType: amqp.ExchangeDirect, // direct（默认）/fanout/topic/headers
        Queue:        "order.created",
        RoutingKey:   "created",
        BindingKey:   "created", // direct 必须与 RoutingKey 一致，可省略其中一个
        Durable:      true,
    },
    Confirm: true, // 发布确认：等 broker ack，nack 返回错误
})
if err != nil {
    return err
}
```

发布：

```go
err = queue.PublishJSON(ctx, order, amqp.PublishOptions{
    Persistent:    true, // delivery mode 2；默认 transient
    CorrelationID: orderID,
    Headers:       amqp091.Table{"x-trace-id": traceID},
})
```

消费：

```go
err = queue.Consume(ctx, func(ctx context.Context, msg amqp091.Delivery) error {
    var order Order
    if err := json.Unmarshal(msg.Body, &order); err != nil {
        return err // 默认 nack 并 requeue，可用 DiscardOnError 关闭重入
    }
    return handle(ctx, order) // 返回 nil 自动 ack
}, amqp.ConsumeOptions{
    PrefetchCount: 10,
    OnError: func(err error) {
        logger.Error("amqp consumer", zap.Error(err))
    },
})
```

- 处理函数返回 `nil` 自动 `Ack`；返回错误自动 `Nack`，默认重新入队，设置
  `DiscardOnError: true` 则丢弃或转死信交换机。
- 处理函数 panic 会被恢复成错误，仍按上面的 ack/nack 规则处理，并通过
  `OnError` 上报堆栈。
- `Consume` 在连接或 channel 断开后按 `ReconnectDelay`（默认 `3s`）重连，
  重连时重新声明拓扑；`ctx` 取消时排空投递并返回 `nil`，可直接作为常驻
  goroutine 的函数体。
- 发布使用一个共享 channel（`Queue` 内加锁串行化）；每次 `Consume` 使用独立
  channel，长连接消费者不会阻塞发布者。
- 手动 ack 意味着 at-least-once：处理失败重入或连接中断后，消息可能被再次
  投递，业务处理需幂等。

`PublishOptions` 支持 `RoutingKey`（topic 每次发布单独指定）、`ContentType`、
`Persistent`、`Priority`、`Headers`、`Expiration`、`MessageID`、
`CorrelationID`、`ReplyTo`、`Type`、`AppID`、`Timestamp` 和 `Mandatory`。
`Mandatory` 会原样传给 broker；本包未订阅 `basic.return`，不可路由的消息不会
在客户端报错。

## 延迟队列（TTL + 死信）

设置 `Delay` 后，`Queue` 的主拓扑是暂存队列，消息在里面等到单条消息过期，再通过
死信交换机转发到 `Target`：

```text
Publish ──> [Delay.Exchange] ──> [Delay.Queue] ── 消息过期 ──> [Target.Exchange]
                                                                      │
Consume <──────────────────────────── [Target.Queue] <───────────────┘
```

**延迟由发布端决定（推荐）**：`DelayConfig.TTL` 留零，队列不声明 `x-message-ttl`，
每条消息等多久来自 `PublishOptions.Delay`。延迟因此只有一种写法，与 RocketMQ、Redis
的 sink 一致，也不会被 broker 截断：

```go
queue, err := amqp.NewQueue(client, amqp.QueueConfig{
    Topology: amqp.Topology{
        Exchange:   "orders.delay",
        Queue:      "orders.delay.staging",
        RoutingKey: "created",
        Durable:    true,
    },
    Delay: &amqp.DelayConfig{
        Target: amqp.Topology{
            Exchange:   "orders",
            Queue:      "order.created",
            RoutingKey: "created",
            Durable:    true,
        },
    },
    Confirm: true,
})

err = queue.PublishJSON(ctx, event, amqp.PublishOptions{Delay: 5 * time.Second})
```

零 TTL 时 `Delay` **必填**：没有队列 TTL，未定延迟的消息永远不过期，会一直留在暂存
队列里，所以 `Publish` 在调用点返回 `ErrDelayRequired`。这比"用队列 TTL 兜底"更
安全——有队列 TTL 时漏写 `Delay` 并不报错，消息只是悄悄等满队列 TTL 后迟到，把
bug 藏了起来。

**经典固定延迟队列（兼容）**：给 `DelayConfig.TTL` 一个至少 `1ms` 的正值，队列会
声明 `x-message-ttl`。此时 `Delay` 可选（不传就继承队列 TTL），且不得超过它——
broker 取队列 TTL 与单条消息 TTL 中较小值，超出部分会被静默截断，框架因此提前报
`ErrInvalidDelay`：

```go
Delay: &amqp.DelayConfig{TTL: 30 * time.Second, Target: /* ... */}
```

- 发布时先声明 `Target`，再声明带 `x-dead-letter-exchange` 的暂存队列，目标拓扑
  缺失也不会丢消息。默认还会写 `x-dead-letter-routing-key`，值为 `Target` 的
  routing key；`PreserveRoutingKey` 开启时不写这个参数。
- 消费读取的是 `Target.Queue`，暂存队列只负责等待。
- `PublishOptions.Delay` 是相对延迟入口（"5 秒后投递"），内部转成单条消息 TTL，
  最小 `1ms`；与底层字段 `Expiration` 互斥。用在没有 `Delay` 的普通队列上会返回
  `ErrDelayRequiresWorkflow`——消息到期后没有死信目的地，会直接消失。
- **队头阻塞**：RabbitMQ 只在队头判定消息是否过期。同一个暂存队列混用不同延迟时，
  前面的长等待会挡住后面的短等待。按延迟分档建多个暂存队列，或让一档队列只承载
  一种延迟。
- 暂存队列的 `Topology.Arguments` 会被保留，但上述死信参数由 `Delay` 覆盖；不要
  手工设置同名参数。零 TTL 模式下手工写入的 `x-message-ttl` 也会被删除，否则会
  悄悄把队列级 TTL 带回来。已存在队列的参数不能修改，切换模式要换队列名或先删除
  队列，否则 broker 返回 `PRECONDITION_FAILED`。

### topic 类型的延迟队列

topic exchange 靠 routing key 选绑定，所以**必须**开 `PreserveRoutingKey`。
默认行为会把死信 routing key 固定成 `Target` 的那一个值：所有过期消息都被改成同一个
键，`order.paid` 与 `order.cancelled` 再也分不开；若目标绑定写的是具体键，字面的
`order.*` 甚至匹配不上，消息直接丢失。开启后队列不设置
`x-dead-letter-routing-key`，RabbitMQ 死信时沿用消息原始的 routing key：

```text
Publish(order.paid) ──> [topic: orders.delay] ── binding order.* ──> [暂存队列]
                              │ 消息过期，保留 order.paid
                              v
                     [topic: orders.ready] ── binding order.* ──> [目标队列]
```

```go
queue, err := amqp.NewQueue(client, amqp.QueueConfig{
    Topology: amqp.Topology{
        Exchange:     "orders.delay",
        ExchangeType: "topic",
        Queue:        "orders.delay.staging",
        BindingKey:   "order.*", // 绑定 pattern，不是发布用的 key
        Durable:      true,
    },
    Delay: &amqp.DelayConfig{
        Target: amqp.Topology{
            Exchange:     "orders.ready",
            ExchangeType: "topic",
            Queue:        "orders.ready",
            BindingKey:   "order.*",
            Durable:      true,
        },
        PreserveRoutingKey: true,
    },
})

// 每条消息给出具体键；它会一路带到目标 exchange。
err = queue.PublishJSON(ctx, event, amqp.PublishOptions{
    RoutingKey: "order.paid",
    Delay:      5 * time.Second,
})
```

- `BindingKey` 是匹配用的 pattern，`RoutingKey` 是发布用的字面键，**不要混用**：
  暂存拓扑只写 `BindingKey`，具体键由 `PublishOptions.RoutingKey` 逐条给出。
- topic 拓扑必须至少有一个 key。空 binding key 只匹配空 routing key，队列会一直空闲。
- 开启 `PreserveRoutingKey` 时，即使 `Topology.Arguments` 里手工写了
  `x-dead-letter-routing-key` 也会被删除——只要该参数存在就会覆盖原始键。
- direct exchange 保持默认行为即可：固定死信键本来就是它想要的。
- 示例沿用推荐的零 TTL 模式：延迟由 `PublishOptions.Delay` 决定，队列不声明
  `x-message-ttl`。想改成固定延迟队列时同样要换队列名，避免参数冲突。

## API 摘要

| API | 说明 |
| --- | --- |
| `New(context.Context, Config) (*Client, error)` | 校验配置并连接第一个可用 host |
| `Configure(map[string]Config) (*Registry, error)` | 注册命名配置，生成新一代注册表并安装 |
| `NewRegistry(map[string]Config) (*Registry, error)` | 只生成新一代注册表，不安装 |
| `(*Registry) Install()` | 将该代切换为包级当前代 |
| `NewIO(context.Context, string) (*Client, error)` | 按别名取用当前一代的客户端，首次使用时建连 |
| `(*Registry) NewIO(context.Context, string) (*Client, error)` | 从指定一代取用客户端，与是否安装无关 |
| `(*Registry) Close() error` / `CloseAllIO() error` | 关闭该代 / 当前一代打开的客户端 |
| `Wrap(*amqp091.Connection) *Client` | 注入已有连接，不可重连 |
| `(*Client) Channel() (*amqp091.Channel, error)` | 取 channel；受管连接失效时自动重连 |
| `(*Client) Close() error` / `IsClosed() bool` | 关闭连接 / 查询状态（幂等） |
| `NewQueue(*Client, QueueConfig) (*Queue, error)` | 校验并绑定拓扑 |
| `(*Queue) Publish(context.Context, []byte, PublishOptions) error` | 发布字节消息 |
| `(*Queue) PublishJSON(context.Context, any, PublishOptions) error` | JSON 序列化后发布 |
| `(*Queue) Consume(context.Context, HandlerFunc, ConsumeOptions) error` | 声明、消费、自动 ack/nack 与重连 |
| `(*Queue) Close() error` | 关闭发布 channel（下次发布自动重开） |

## 集成测试

单元测试不需要 broker。集成测试通过环境变量开启，本机 docker 的
`dev-rabbitmq` 可用下面的方式运行：

```bash
GOFY_TEST_AMQP_ADDR=127.0.0.1:5672 \
GOFY_TEST_AMQP_USERNAME=rabbitmq \
GOFY_TEST_AMQP_PASSWORD=... \
GOFY_TEST_AMQP_VHOST=my_vhost \
  go test ./support/amqp -run Integration -v
```

集成用例覆盖 direct 发布/消费、发布确认、handler 失败重入、延迟队列、
多 host 回退和 `Wrap` 注入。
