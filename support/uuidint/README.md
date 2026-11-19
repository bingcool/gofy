# support/uuidint

基于 Redis 的**分布式自增 ID**生成器（返回 `int64`，不是 RFC UUID 字符串），
移植自 swoolefy 的 PHP `Uuid` 库：Lua 原子脚本把「Redis 时间前缀 + 段内序号」
拼成大整数 ID，同一毫秒冲突自动重试，支持重试与备用连接降级。

Go 与 PHP 使用同一套 Lua 和 ID 布局，**可以共享同一个 Redis key 混合发号**。

## 快速开始

```go
import (
    "context"

    "github.com/bingcool/gofy/support/uuidint"
)

// 使用 support/redis 的命名实例（推荐：热更新后自动切到新连接）
gen, err := uuidint.New("uuid-key", uuidint.WithInstance("cache"))
if err != nil {
    return err
}

id, err := gen.Get(ctx)          // 单个
ids, err := gen.GetBatch(ctx, 10) // 批量 10 个
```

直接注入已有客户端：

```go
gen, err := uuidint.New("uuid-key", uuidint.WithClient(redisClient))
```

不传 `WithClient` / `WithInstance` 时使用 `support/redis` 的 `default` 实例。

## ID 结构与生成原理

Lua 在单次 `EVAL` 内完成：

1. `INCRBY incrKey step`。`step` 钳到 `1 ~ MaxIncr(999990)`；段内序号超过上限
   时删 key 重新自增，回绕后仍超限则本段作废（返回空）；
2. 用 Redis `TIME` 取秒 + 微秒（补零到 6 位后取前 3 位）组成毫秒级时间串；
3. 新段首次写入时设置 TTL（默认 15s），并与上一毫秒戳比对，同一毫秒冲突
   返回空，由 Go 侧重试；
4. 返回 `{prefixNumber, incrId}`，其中
   `prefixNumber = second + msecond + "000000"`。

Go 侧最终 ID：`id = prefixNumber + incrId`（19 位数字，`int64` 可容纳到约
2286 年）。

批量取号取闭区间 `[max-n+1, max]`，`n` 是实际发给 Redis 的步长；一次申请
超过 `MaxIncr` 时会被钳制，返回数量会少于请求数量。

特点：趋势递增、跨节点靠 Redis 保证唯一、毫秒级冲突自动重试。**不保证跨毫秒
连续**：同一毫秒内严格 +1，跨毫秒时前缀增加、段序号重置。

## 配置项（Options）

| Option | 说明 | 默认 |
| --- | --- | --- |
| `WithClient(*goredis.Client)` | 直接用已有客户端作主连接 | — |
| `WithInstance(name)` | 用 `support/redis` 命名实例作主连接；每次取号经包级 `redis.NewIO` 懒解析，跟随进程内最后安装的一代 Registry（单 App 兼容路径） | `default` |
| `WithFallbackClients(...)` | 主连接重试耗尽后依次尝试的客户端 | 无 |
| `WithFallbackInstances(...)` | 主连接重试耗尽后依次尝试的命名实例 | 无 |
| `WithFallbackDelay(time.Duration)` | 尝试第一个备用前的停顿；无备用时不等待，ctx 取消立即中断 | `10ms` |
| `WithTTL(time.Duration)` | 计数 key 的 TTL，至少 1s（Redis EXPIRE 只接受整秒） | `15s` |
| `WithRetry(times, delay)` | 主连接尝试次数与间隔；同毫秒冲突也走重试；`0` 表示默认次数 | `3` / `150ms` |
| `WithErrorHandler(func(error))` | 主连接重试耗尽时回调一次，再尝试备用连接 | 无 |
| `WithPool(size, interval)` | 启用本地预取池：启动预热 `size` 个号，之后每 `interval` 补一批 | 不启用 |

`WithClient` 与 `WithInstance` 互斥；`New` 会校验 key 非空、TTL/重试参数，
以及注入的客户端不能为 nil。启用池时校验 `size ∈ [1, MaxIncr]`、
`interval ≥ 10ms`（建议 1s~2s）。

## 本地预取池

池化语义参考 PHP `UuidManager` 的 Channel 池，但每个 Generator 拥有独立池
（PHP 的 Channel 是静态共享的，不同业务 key 会混用）。

```go
gen, err := uuidint.New("uuid-key",
    uuidint.WithInstance("cache"),
    uuidint.WithPool(200, time.Second), // 预热 200 个，每秒补货
)
if err != nil {
    return err
}
if err := gen.StartPool(ctx); err != nil { // 同步预热一批，启动后立即可用
    return err
}
defer gen.StopPool()

id, err := gen.Get(ctx)          // 先从本地池取，取空才访问 Redis
ids, err := gen.GetBatch(ctx, 10)
```

行为：

- `StartPool` 同步分配一批号再把余量交给后台 ticker；重复调用无副作用，
  未启用 `WithPool` 时是空操作。
- `Get`/`GetBatch` 先消费本地池，不够的部分才向 Redis 申请；批量结果由
  「池中旧号 + 新申请区间」组成，仍保持递增。
- 池满时跳过本轮补货；连续 3 个 `interval` 无人消费时清空旧池再补，
  避免长时间闲置堆积（与 PHP 一致）。
- 池里已分配但未使用的号在清池/`StopPool` 后成为空洞，**不影响唯一性**；
  单次申请量仍受 `MaxIncr` 钳制。
- `GetBatch` 取号失败但已从池中取出部分 ID 时，这些 ID 随错误一起返回，
  不会静默丢弃。
- `StopPool` 幂等；停止后 `Get` 直接回落到 Redis 单次取号。池不可重启，
  需要重启时重新 `New` 一个 Generator。
- `StopPoolContext(ctx)` 是带预算的版本，供 `Module.Stop` 这类不能无限等待的
  调用方使用：取消 ticker 不会打断它已经进入的那次 Redis 调用（池的 ctx 没有
  deadline，go-redis 只按 ctx 里的 deadline 缩短命令），因此到截止时间返回
  `ctx.Err()` 并留下一个预取 goroutine，而不是把调用方挂住。此时缓冲**不关闭**
  （关闭会让被放弃的 goroutine 下一次发送 panic）；ticker 真退出后再调一次可以
  完成收尾并关闭缓冲。

## 失败与降级

1. 主连接最多尝试 `retryTimes` 次（含第一次），失败间隔 `retryDelay`；
   `WithRetry(0, …)` 按默认 3 次处理，负数报错；
2. 仍失败 → 调用 `WithErrorHandler`（若有）；
3. 有备用时停顿 `WithFallbackDelay` 的时长（默认 10ms，可被 ctx 取消）；
4. 依次尝试每个备用客户端/实例各一次；
5. 全部失败返回包裹 `ErrUnavailable` 的错误，可用 `errors.Is` 判断；
   没有任何可报原因时返回裸 `ErrUnavailable`，`WithErrorHandler` 不会被 nil 触发；
   `ctx` 被取消时返回 `context.Canceled` / `context.DeadlineExceeded`。

请保证 Redis 高可用，或配置可写的备用实例。

备用可以是另一台独立 Redis。ID 由「Redis TIME 毫秒前缀 + 最多 `MaxIncr` 的段内序号」
组成，序号不会进位到前缀，因此只有落在同一毫秒才可能重复。框架在主连接重试耗尽、
告警之后、尝试备用之前默认停顿 10ms（`WithFallbackDelay` 可调，设为 0 关闭，没有
备用时不等待），让切换进入新的毫秒，两边各自从 1 开始的计数器就不会撞。

停顿不修正时钟偏差：若备用 Redis 的 `TIME` 比主慢超过这个时长，两边仍可能落在同一
毫秒，跨机房部署时可适当调大。它也不保证趋势递增——独立计数器的序号会回退，但 ID
本身仍然唯一。

## Redis Key

| Key | 说明 |
| --- | --- |
| `{key}` | 段内自增计数，带 TTL |
| `{key}_time_stamp` | 上一成功发放的毫秒时间戳，用于同毫秒冲突检测，TTL 与计数 key 相同 |

## 与 PHP 版的差异

- 池按 Generator 隔离，`StartPool` 同步预热一批；PHP 的 Channel 池是静态共享的；
- 重试使用 `time.Timer` 并响应 `ctx` 取消，不会阻塞 goroutine；
- 返回 `(id, error)`，不再用 `null` 表示失败；
- 命名实例别名由 `support/redis` 注册表提供，对应 PHP 的组件容器取 redis
  对象。

## 测试

```bash
go test ./support/uuidint/
```

测试用 miniredis（支持 EVAL/TIME），不需要真实 Redis；覆盖单号/批量、
TTL、同段连续性、降级、全部失败、命名实例别名和参数校验。

## demo 示例

`examples/demo-service` 已经接入：

- `app/appio/uuidint` 在启动时用配置的 `uuidint.redis_instance_name`（默认
  `ratelimit`）构建生成器并发布 `uuidint.Default`，`Stop` 时清空；
- `gen-uuid` 声明 `WithoutDatabase`，不走 `app/appio` 装配代，自己按同一份配置注册
  Redis 实例后取号：

```bash
cd examples/demo-service
go run . script gen-uuid --count=5
# 走本地预取池（依次用 uuidint.pool_size / pool_interval，等待一个补货周期）
go run . script gen-uuid --count=5 --pool --wait=1200ms
```

输出为 19 位趋势递增 ID；连续执行时同段内序号接续。
