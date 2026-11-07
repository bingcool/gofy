# support/redis

Redis 客户端包：校验配置、建立连接、`Ping` 校验、关闭连接。基于
[`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis)，默认不保留
全局状态，调用方自己持有并关闭返回的 `*redis.Client`；需要按配置别名取用时，
可以用可选的 `Configure` / `NewIO` 命名实例注册表。

同时提供 `support/ratelimit/redisstore`，把 Redis 用作多实例共享的限流存储。

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

## 命名实例（Configure / NewIO）

按配置别名直接取用实例时，启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`。实例在第一次使用时建连并缓存，后续调用返回同一个
`*redis.Client`：

```go
reg, err := redis.Configure(map[string]redis.Config{
    "default": {Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD")},
    "cache":   {Addr: os.Getenv("CACHE_REDIS_ADDR"), DB: 1},
})
if err != nil {
    return err
}
defer reg.Close()

client, err := redis.NewIO(ctx, "default") // 业务代码：从当前一代取用
```

### 包级 NewIO 与 reg.NewIO 的区别

| 写法 | 从哪一代取用 | 适用场景 |
| --- | --- | --- |
| `redis.NewIO(ctx, "default")` | 包级当前一代，即最近一次 `Configure` / `Install` 的那一代 | 业务代码不持有注册表，任何位置按别名直接取用 |
| `reg.NewIO(ctx, "default")` | `reg` 这一代，与它是否已安装无关 | 装配层、热更新、测试中持有该代引用 |

两者的关系按 `reg` 所处的代区分：

- `reg` 是 `Configure` 返回的当前代时，`reg.NewIO` 和包级 `redis.NewIO`
  共用同一份懒加载缓存，返回同一个客户端，不会建第二条连接；
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
- 建连失败不会缓存，下一次 `NewIO` 会重新尝试；并发调用安全，同一别名只建
  一个客户端。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己打开的
  连接，不影响已经切换到新一代的调用方；demo 热更新依赖这个顺序（新模块先
  启动、旧模块后关闭）。
- `Configure` 等价于 `NewRegistry` + `Install`。热更新时先用 `NewRegistry`
  建好新连接，全部成功后再 `Install` 切换；失败只关闭新代，不影响正在
  服务的旧代。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。

不调用 `Configure` 时，包的行为与 `New`/`Close` 完全一致，不产生全局状态。

## 限流

```go
import (
    "time"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/support"
    "github.com/bingcool/gofy/support/ratelimit/redisstore"
)

store, err := redisstore.New(client, "order-service")
if err != nil {
    return err
}

app, err := gofy.New(
    gofy.WithRateLimit(support.RateLimitConfig{
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
go test ./support/redis/ ./support/ratelimit/redisstore/
```

测试使用 `miniredis` 启动进程内 Redis，不依赖外部服务；覆盖配置校验、默认值、
`Ping`/读写、不可达地址的错误脱敏，以及限流的滑动窗口行为。
