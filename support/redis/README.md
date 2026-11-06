# support/redis

Redis 客户端包：校验配置、建立连接、`Ping` 校验、关闭连接。基于
[`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis)，包内没有全局状态，
调用方自己持有并关闭返回的 `*redis.Client`。

同时提供 `transport/ratelimit/redisstore`，把 Redis 用作多实例共享的限流存储。

## 快速开始

```go
import (
    "context"

    "github.com/bingcool/gofy/support/redis"
)

client, err := redis.New(ctx, redis.Config{
    Addr:     "127.0.0.1:6379",
    Password: os.Getenv("REDIS_PASSWORD"),
    DB:       0,
})
if err != nil {
    return err
}
defer redis.Close(client)
```

`New` 会在 `PingTimeout` 内执行 `PING`，失败时关闭连接并返回错误，便于在启动阶段
快速失败。

## 限流

```go
import (
    "time"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/transport"
    "github.com/bingcool/gofy/transport/ratelimit/redisstore"
)

store, err := redisstore.New(client, "order-service")
if err != nil {
    return err
}

app, err := gofy.New(
    gofy.WithRateLimit(transport.RateLimitConfig{
        Store:  store,
        Limit:  100,
        Window: time.Minute,
    }),
    // ...
)
```

`redisstore` 用 ZSET + Lua 实现滑动窗口，取 Redis 服务器时间，多个实例共享同一份计数。
Redis 错误原样返回，由中间件的 `FailOpen` 决定放行还是返回 503。

## Config

```yaml
redis:
  network: tcp
  addr: ${REDIS_ADDR}          # host:port 或 unix socket 路径
  username: ''
  password: ${REDIS_PASSWORD}
  db: 0
  pool_size: 10
  min_idle_conns: 2
  dial_timeout: 5s
  read_timeout: 3s
  write_timeout: 3s
  ping_timeout: 5s
  max_retries: 0               # 0 使用 go-redis 默认值 3；-1 关闭重试
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `Network` | `tcp` 或 `unix` | `tcp` |
| `Addr` | 必填，`host:port` 或 socket 路径 | — |
| `Username` | ACL 用户名（Redis 6+） | 空 |
| `Password` | 密码 | 空 |
| `DB` | 数据库序号 | `0` |
| `PoolSize` | 连接池上限 | `10` |
| `MinIdleConns` | 最小空闲连接 | `2` |
| `DialTimeout` | 建连超时 | `5s` |
| `ReadTimeout` | 读超时 | `3s` |
| `WriteTimeout` | 写超时 | `3s` |
| `PingTimeout` | 启动 `Ping` 超时 | `5s` |
| `MaxRetries` | 命令重试次数 | go-redis 默认 `3` |

`Validate()` 在连接前检查：`Addr` 非空（`ErrAddrRequired`）、`Network` 合法、
`DB`/连接数/时长不为负、`MinIdleConns` 不超过 `PoolSize`、`MaxRetries >= -1`。

零值表示“使用默认值”。

## 错误与日志

- 错误信息包含 network、地址、DB 序号和超时，不包含密码；
- go-redis 的连接池日志默认写入标准日志；需要统一到 zap 时由业务调用
  `go-redis` 的日志接口自行接管，本包不修改全局 logger；
- 密码建议通过环境变量注入，不要提交到仓库。

## 测试

```bash
go test ./support/redis/ ./transport/ratelimit/redisstore/
```

测试使用 `miniredis` 启动进程内 Redis，不依赖外部服务；覆盖配置校验、默认值、
`Ping`/读写、不可达地址的错误脱敏，以及限流的滑动窗口行为。
