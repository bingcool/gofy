# support/lock

基于 Redis 的**分布式互斥锁**，底层是 [`go-redsync/redsync/v4`](https://github.com/go-redsync/redsync)（DLM / Redlock 实现）。
一把锁就是「一个 key + 一个随机 token + 一个过期时间」：写入 token 的调用方是持有者，
过期时间负责在持有者死掉后释放 key。

与 [`support/lease`](../lease) 的分工：`lease` 表达**长期归属**（选主、单例任务），带
fencing token；`lock` 表达**短临界区互斥**（一次结算、一次导入），靠 TTL 自愈。不要把
`lock` 当成长时间的所有权声明——TTL 到期后它就不互斥了。

## 快速开始

```go
import "github.com/bingcool/gofy/support/lock"

// 1) 显式客户端（推荐：绑定本代连接，Reload 后不会串代）
l, err := lock.New(lock.WithClient(redisClient), lock.WithPrefix("demo:lock"))
if err != nil {
    return err
}

mu, err := l.Lock(ctx, "order:123") // 按配置重试，拿不到返回 ErrAlreadyHeld
if errors.Is(err, lock.ErrAlreadyHeld) {
    return nil // 别人在处理，直接返回
}
if err != nil {
    return err
}
defer mu.Unlock(ctx)

doWork(ctx)
```

```go
// 2) 命名实例：每次调用都经 support/redis 解析，Reload 安装的新实例立即生效
l, err := lock.New(lock.WithInstance("cache"))

// 3) 都不给：使用 support/redis 的 "default" 实例
l, err := lock.New()

// 4) 配置驱动（YAML 解码到 lock.Config）
l, err := lock.Open(cfg.Lock, redisClient, lock.WithErrorHandler(func(err error) {
    logger.Warn("redis lock failed", zap.Error(err))
}))
// cfg.Lock.Enabled 为 false 时返回 (nil, nil)：没有句柄，也没有东西要关闭
```

只有一次机会的场景用 `TryLock`（例如 HTTP handler 防重复提交）：

```go
mu, err := l.TryLock(ctx, "job:"+id) // 只尝试一次，立刻回答"忙"
```

## 核心语义

| 行为 | 说明 |
| --- | --- |
| 互斥范围 | 同一 `前缀:key` 在 Redis 部署内全局唯一。不同前缀互不影响 |
| 非重入 | 同一 key 二次加锁会等到第一次过期，因为第二次看到的值不是它写的 |
| 解锁围栏 | `Unlock` 只在 key 里仍是本锁 token 时删除；过期后被人接管的锁不会被前任释放 |
| 过期自愈 | 持有者崩溃后，key 在 TTL 到期时自动可被抢；不需要清理任务 |
| 续租 | `Extend(ctx)` 把 TTL 重置为获取时的值；锁已丢失返回 `ErrNotHeld` |
| 失败区分 | 锁被占返回 `ErrAlreadyHeld`；Redis 故障返回 `ErrUnavailable`；ctx 取消返回 `ctx.Err()` |

`TryLock` 忽略重试配置，永远只尝试一次——这是契约，不是默认值。

## 使用约束

1. **TTL 必须长于临界区**。锁在你还在干活的时候过期，就不再互斥。耗时不确定的工作要么
   把 `expiry` 配大，要么在临界区里周期性 `Extend`。
2. **不是锁一开就安全**。`Unlock` 失败（`ErrNotHeld`）说明锁已经不属于你了，此时继续
   写共享资源就可能与别人并发——把它当错误处理，不要 `_ = mu.Unlock(ctx)` 了事。
3. **不要 `defer` 里吞错**。`defer mu.Unlock(ctx)` 无法上报失败；对正确性敏感的场景用
   显式调用并检查返回值。
4. **token 必须唯一**。`WithLockToken` 是为可追踪（把 request id 写进 Redis）准备的；
   两次获取用同一个 token 会让双方都以为自己持有 key。
5. **单 Redis 是单点**。`WithClient` 只接受一个客户端，此时 quorum = 1：Redis 不可用
   期间加锁失败（返回 `ErrUnavailable`），不会假装成功。

## Options（`New` / `Open`）

| Option | 说明 | 默认 |
| --- | --- | --- |
| `WithClient(c)` | 固定到显式客户端，与 `WithInstance` 互斥；不接管关闭 | — |
| `WithInstance(name)` | 经 `support/redis` 按别名解析，每次调用生效 | `"default"` |
| `WithPrefix(p)` | 键命名空间，结尾冒号会被规范化掉 | 无 |
| `WithExpiry(d)` | 锁 TTL | `10s` |
| `WithRetry(tries, delay)` | 阻塞式加锁的重试次数与固定间隔 | `3` / `100ms` |
| `WithErrorHandler(fn)` | 接收内部错误（同时也会返回），用于挂日志 | 无 |

单次加锁可覆盖：

| LockOption | 说明 |
| --- | --- |
| `WithLockExpiry(d)` | 单把锁的 TTL |
| `WithLockRetry(tries, delay)` | 单把阻塞式锁的重试策略 |
| `WithLockToken(v)` | 调用方提供的 token（必须唯一） |

## 配置（`lock.Config`）

```yaml
lock:
  enabled: true
  redis_instance_name: default   # 留空即 support/redis 的 default 实例
  prefix: demo:lock              # 键命名空间
  expiry: 10s                    # 必须长于临界区
  tries: 3                       # 阻塞式加锁的重试次数
  retry_delay: 100ms
```

`enabled` 没有默认值：配置里缺这一段时不会悄悄开始写锁键，段名拼错也不会看起来像功能
正常。`WithDefaults()` 只补数值与实例名，不猜 `enabled` 和 `prefix`。

## 可观测性

- 锁键建议带业务标识（`order:123`、`settle:2026-11-16`），出问题时能直接从 Redis 的
  key 看出是谁在持锁；
- `Mutex.Token()` / `Mutex.Until()` 可以写进日志：前者定位持有者，后者是"必须在此之前
  结束"的界线；
- `WithErrorHandler` 里的错误与调用方拿到的是同一个，边界记一次即可，不要两处都记。

## 测试

包本身用 `miniredis` 覆盖加锁/解锁、过期、续租、被占、ctx 取消与 Redis 故障路径。
业务侧要测并发互斥时也建议用 `miniredis`，不要连真实 Redis。
