# support/queues

在注入的 Redis 客户端之上提供两类队列，参考 swoolefy 的
`Swoolefy\Library\Queues`：

- `Queue`：即时队列，底层是 Redis List，支持优先队列和失败重试；
- `DelayQueue`：延迟队列，底层是 Redis ZSET，到期后才能被取出。

与 `support/cache`、`support/redis` 一样，本包不持有全局状态，也不接管客户端
生命周期：业务把已经连接的 `*redis.Client`（或任意 `goredis.Cmdable`）注入进来，
关闭仍由业务或 `app/appio` 负责。

## 键布局

即时队列使用三个键，与 swoolefy 保持一致：

| 键 | 类型 | 用途 |
| --- | --- | --- |
| `{key}` | List | 主队列，`LPUSH` 入队、`RPOP` 出队 |
| `{key}:priority_queue` | List | 优先队列，`Pop` 时优先消费 |
| `{key}:retry_queue_sort` | ZSET | 重试队列，score 为下次可投递的 Unix 秒 |

延迟队列只使用一个 ZSET，`{key}` 既是队列名也是 ZSET 键，score 为到期时间。

## 快速开始

```go
import (
    "context"
    "time"

    "github.com/bingcool/gofy/support/queues"
    "github.com/bingcool/gofy/support/redis"
)

client, err := redis.New(ctx, redis.Config{
    Addr:     "127.0.0.1:6379",
    Password: os.Getenv("REDIS_PASSWORD"),
})
if err != nil {
    return err
}
defer redis.Close(client)

q, err := queues.NewQueue(client, queues.Config{
    Key:        "orders",
    MaxRetries: 3, // 0 表示使用 queues.DefaultMaxRetries
})
if err != nil {
    return err
}
```

也可以直接注入 `app/appio/redis` 发布出来的实例：

```go
q, err := queues.NewQueue(appredis.Cache, queues.Config{Key: "orders"})
```

### 生产与消费

```go
if _, err := q.Push(ctx, order{ID: 1, Status: "created"}); err != nil {
    return err
}
// 需要插队时写入优先队列
if _, err := q.PushPriority(ctx, order{ID: 2, Status: "vip"}); err != nil {
    return err
}

msg, ok, err := q.Pop(ctx, 2*time.Second)
if err != nil {
    return err
}
if !ok {
    // 2 秒内没有消息
    return nil
}

var job order
if err := msg.Decode(&job); err != nil {
    return err
}
if err := handle(job); err != nil {
    // 消费失败：delay 之后重新投递；返回 false 表示已达重试上限
    scheduled, retryErr := q.Retry(ctx, msg, 10*time.Second)
    _ = scheduled
    return retryErr
}
```

### 延迟队列

```go
dq, err := queues.NewDelayQueue(client, queues.Config{Key: "orders:delay"})
if err != nil {
    return err
}

if err := dq.Add(ctx, order{ID: 1}, 30*time.Second); err != nil {
    return err
}

// 一到期的消息会被原子取出并删除，limit <= 0 时默认取 10 条
messages, err := dq.Pop(ctx, 10)
for _, msg := range messages {
    var job order
    if err := msg.Decode(&job); err != nil {
        return err
    }
    if err := handle(job); err != nil {
        // 失败后按新的延迟时间重新写回同一个 ZSET
        _, _ = dq.Retry(ctx, msg, time.Minute)
    }
}
```

## 消息结构

`Push`、`PushPriority`、`Add` 都会把业务数据包进统一信封：

```json
{
  "__id": "0b4c9f0a-...",
  "__retry_num": 0,
  "__timestamp": 1793512800,
  "payload": {"id": 1, "status": "created"}
}
```

- `__id`、`__retry_num`、`__timestamp` 是保留字段，由本包维护；
- 业务数据放在 `payload` 内，消费时用 `msg.Decode(&dest)` 还原；
- 与 swoolefy 的扁平 JSON 不同，gofy 使用嵌套 `payload`。键名虽然一致，
  但消息格式不互通，不要让两种实现的消费者混跑同一个队列。

`Message` 也可以直接序列化后交给其他系统，字段名保持稳定。

## API

### `Queue`

| 方法 | 说明 |
| --- | --- |
| `NewQueue(client, Config)` | 构造即时队列；`client == nil` 返回 `ErrClientRequired` |
| `Push(ctx, payloads...)` | 写入主队列尾部，返回入队条数 |
| `PushPriority(ctx, payloads...)` | 写入优先队列，`Pop` 时优先取出 |
| `Pop(ctx, timeout)` | 取出下一条消息；`timeout <= 0` 立即返回 |
| `Retry(ctx, msg, delay)` | 失败消息写入重试 ZSET；返回 false 表示已达上限 |
| `Len(ctx)` | 主队列长度（不含优先队列和重试队列） |
| `Key()` / `PriorityKey()` / `RetryKey()` / `MaxRetries()` | 键与配置 |

### `DelayQueue`

| 方法 | 说明 |
| --- | --- |
| `NewDelayQueue(client, Config)` | 构造延迟队列 |
| `Add(ctx, payload, delay)` | 写入延迟消息，`delay` 之后才可取出 |
| `Pop(ctx, limit)` | 原子取出并删除至多 `limit` 条到期消息 |
| `Retry(ctx, msg, delay)` | 以新的到期时间重新写回 ZSET |
| `Len(ctx)` | 队列中的消息总数（包含未到期消息） |
| `Key()` / `MaxRetries()` | 键与配置 |

## 约定

- **先删除后消费**：`Pop` 返回前消息已从 Redis 删除。消费失败必须显式调用
  `Retry`；如果进程在 `Pop` 与 `Retry` 之间崩溃，这条消息会丢失。
  需要 at-least-once 语义时应改用 Redis Streams，本包不提供消费确认。
- **重试次数**：`MaxRetries` 是失败后额外投递的次数，因此一条消息最多被投递
  `MaxRetries + 1` 次；`Retry` 返回 `false` 时消息被丢弃，由业务决定是否记录。
- **重试回流发生在 `Pop` 时**：每次 `Pop` 前用 Lua 原子地把最多 100 条到期重试
  消息搬回主队列，没有独立的后台定时器。
- **延迟弹出是原子的**：`DelayQueue.Pop` 用 Lua 一次完成 `ZRANGEBYSCORE` +
  `ZREM`，多消费者并发时同一条消息只会被一个消费者拿到。
- **阻塞精度为秒**：`Queue.Pop` 的 `timeout` 非整秒会向上取整（Redis 阻塞命令
  以秒为超时单位），例如 50ms 会阻塞约 1 秒；`timeout <= 0` 不阻塞。
- **cluster 注意**：即时队列的重试回流脚本同时操作 `{key}` 和
  `{key}:retry_queue_sort`。Redis Cluster 要求一次 `EVAL` 的所有键在同一 slot，
  请把 `Config.Key` 写成 hash tag（例如 `{orders}`），派生出的键会落在同一 slot。
- `payload` 必须可被 `encoding/json` 序列化；错误带 `queues:` 前缀并保留底层
  Redis 错误，便于 `errors.Is` 判断。
- `Config.Key` 会去掉首尾空白；空键返回 `ErrKeyRequired`，负数重试次数返回错误。

## 测试

```bash
go test -race ./support/queues
```

测试使用 `miniredis`，不依赖外部 Redis。
