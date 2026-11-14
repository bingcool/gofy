# support/cache

在注入的 Redis 客户端之上提供统一的键值缓存门面。参考 swoolefy 的
`Swoolefy\Library\Cache`：`Cache` 是业务使用的接口，`Redis` 是默认实现。

本包不持有全局状态，也不接管客户端生命周期：业务把已经连接的
`*redis.Client`（或任意 `goredis.Cmdable`）注入进来，关闭仍由业务或
`app/appio` 负责。

## 快速开始

```go
import (
    "context"
    "time"

    "github.com/bingcool/gofy/support/cache"
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

c, err := cache.NewRedis(client)
if err != nil {
    return err
}

if err := c.Set(ctx, "order:1", "paid", time.Minute); err != nil {
    return err
}
value, ok, err := c.Get(ctx, "order:1") // ok=false 表示 key 不存在
```

也可以直接注入 `app/appio/redis` 发布出来的实例：

```go
c, err := cache.NewRedis(appredis.Cache)
```

## JSON 值

`SetJSON` / `GetJSON` 负责序列化与反序列化，避免业务层重复写
`json.Marshal`：

```go
type order struct {
    ID     uint64 `json:"id"`
    Status string `json:"status"`
}

if err := c.SetJSON(ctx, "order:1", order{ID: 1, Status: "paid"}, time.Minute); err != nil {
    return err
}

var got order
ok, err := c.GetJSON(ctx, "order:1", &got)
```

`GetJSON` 在 key 不存在时返回 `ok=false`，不修改传入的目标值。

## API

| 方法 | 说明 |
| --- | --- |
| `Set(ctx, key, value, ttl)` | 写入字符串；`ttl <= 0` 表示不过期 |
| `SetJSON(ctx, key, value, ttl)` | JSON 序列化后写入 |
| `Get(ctx, key)` | 读取字符串；`ok=false` 表示 key 不存在 |
| `GetJSON(ctx, key, dest)` | 读取并反序列化到 `dest` |
| `Delete(ctx, keys...)` | 删除一个或多个 key，返回删除数量 |
| `Has(ctx, key)` | 判断 key 是否存在 |
| `TTL(ctx, key)` | 返回剩余有效期；`exists=false` 表示 key 不存在 |
| `GetMany(ctx, keys...)` | 批量读取，缺失的 key 不进入结果 map |
| `SetMany(ctx, values, ttl)` | 批量写入，`ttl` 对所有 key 生效 |

批量写入在 `ttl > 0` 时使用事务管道，保证每个 key 同时拿到过期时间；
`ttl <= 0` 时用一次 `MSET` 完成。

## 约定

- `ttl <= 0` 表示写入后不过期，与 swoolefy 的 `RedisCache::set` 一致；
- `TTL` 对不存在的 key 返回 `exists=false`，对没有过期时间的 key 返回 `ttl=0`；
- 缓存错误带 `cache:` 前缀并保留底层 Redis 错误，便于 `errors.Is` 判断；
- 空 key 由 Redis 自行处理，本包不做额外校验；
- `MSET`/`MGET`/事务管道在 Redis Cluster 下要求所有 key 落在同一 slot，
  否则 Redis 返回 `CROSSSLOT`；需要跨 slot 时请逐 key 调用；
- 不支持 `MSETNX`（仅写入不存在的 key）和跨 key 原子脚本，需要这些语义时直接用 Redis 客户端。

## 测试

```bash
go test -race ./support/cache
```

测试使用 `miniredis`，不依赖外部 Redis。
