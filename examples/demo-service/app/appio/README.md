# app/appio

demo 的 **IO 装配层**：把 `gofy/support/*` 里的库装配成这个应用实际连接的「命名实例」，
再把它们打包成**一代**（generation）发布出去。业务代码不 import `support/redis`、
`support/amqp` 自己建连，也不读任何包级句柄，只从快照取：

```go
db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
cache, err := appio.WithContext(ctx).RedisIO(ctx, appredis.InstanceCache)
mu, err := appio.WithContext(ctx).RedisLock().Lock(ctx, "order:123")
```

`DBConnect` 返回**本次调用应使用的连接**：ctx 里带事务就用事务，否则用本代连接，
并且已经绑定 ctx。要开启事务、或需要一条不被 ctx 中事务接管的连接时用 `DBIO`。

一句话分工：**`support` 回答「一种连接怎么建、怎么用」，`app/appio` 回答「这个应用连哪几个、
什么时候连、当前是哪一代」。**

## 目录

| 名字 | 作用 |
| --- | --- |
| `io_runtime.go` | 模块生命周期：`NewModule` / `Start` / `Stop` / `RequestBinder`，`Name()` 为 `io` |
| `snapshot.go` | 一代句柄的只读视图、`WithContext` / `WithSnapshot` / `Publish`，以及命名实例解析 |
| `db/` | 包装 `support/db` + gorm，暴露 `default` / `order` / `product` |
| `redis/` | 包装 `support/redis` + go-redis v9，暴露 `cache` / `session` / `ratelimit` |
| `amqp/` | 包装 `support/amqp`（RabbitMQ） |
| `kafka/` | 包装 `support/kafka` |
| `rocketmq/` | 包装 `support/rocketmq`（生产者；消费者由 `support/rocketmq.NewConsumer` 单独建） |
| `uuidint/` | 包装 `support/uuidint`，绑定本代 Redis 客户端 |
| `redislock/` | 包装 `support/lock`（redsync），绑定本代 Redis 客户端 |

每个子包的形状相同：`Open`（建连）/`Install`（发布包级句柄）/`Close`（释放）/`Set`
（一代）+ 一个 `Default()` 或按名字取的访问器。它们只做**装配与代际**，不重复实现协议。

## 与 support 的关系

```
业务代码（service / repository / query / 限流中间件）
        │  只认快照，不知道连接从哪来
        ▼
app/appio           命名实例、配置映射、启动顺序、代际发布、tracing 挂载
        │  调用 support 的公开 API 组装
        ▼
support/db   support/redis   support/amqp   support/kafka
support/rocketmq   support/uuidint   support/lock   support/generation
        │  建连
        ▼
MySQL / Redis / RabbitMQ / Kafka / RocketMQ
```

| 关注点 | `support/*` | `app/appio` |
| --- | --- | --- |
| 建连与释放 | `NewRegistry` / `NewIO` / `Close` | 决定连哪几个、什么顺序、失败怎么回滚 |
| 配置结构 | 各包的 `Config`（`dsn`、`addr`、`hosts`…） | 把 `config.yaml` 的段映射进去 |
| 实例命名 | 任意 key，拼错要到解析时才报错 | 白名单在 `Start` 校验，实例名是显式常量 |
| 句柄可见性 | 进程级 Registry | 一代化快照，按请求 / 脚本固定 |
| 可观测 | `InstrumentRedisWith`、`NewGORMPlugin` | 按 `rt.Tracing` 决定是否安装 |
| 失败时机 | 调用时返回错误 | 尽量前移到 `Start`，不留半开连接 |

### 依赖方向是单向的

`app/appio` import `support`；`support` 里没有任何 demo 的名字。demo 是独立 module
（`examples/demo-service/go.mod` + `replace`），`support` 在物理上也 import 不到它。
因此这条规则不是靠约定：**协议能力缺失改 `support`，应用特有的取舍留在 `app/appio`**。
例如「只允许 `cache` / `session` / `ratelimit` 三个 Redis 名字」是 demo 的白名单，
不该塞进 `support/redis`；而「Redis 客户端怎么带 tracing」是通用能力，就该在
`support/otel`。

### 为什么业务不直接调 `support` 的 `NewIO`

三个问题，`support` 自己解决不了：

1. **代际**：`support/redis` 的 Registry 带一个进程级的 `Install`，本包每装配一代就把它
   换一次。业务代码读包级 Registry，就可能在一次请求里拿到上一代的连接、或者上一代
   handler 配上下一代的连接。`app/appio` 把句柄收进不可变快照，由入口固定。
2. **白名单**：拼错的实例名在 `support` 里只是「查不到」，在 `app/appio` 里是启动失败。
3. **生命周期集中**：七个实例的启动顺序、失败回滚、关闭顺序只有一处需要看懂。

## 一代的生命周期

`Module.Start` 的顺序是固定的，后一个依赖前一个：

```
db ──> redis ──> amqp ──> kafka ──> rocketmq ──> uuidint ──> redislock
        │                                             │            │
        └───────────── 绑定本代 Redis 客户端 ─────────┴────────────┘

全部成功 ──> Install 各子包句柄 ──> 组装 Snapshot ──> Publish
任一步失败 ──> 逆序 Close 已建连接 ──> 返回错误（包级句柄不动，旧代继续服务）
```

三个细节值得记住：

- **建连全部在 `Start` 里完成，构造函数不建连**。`NewModule` 只保存配置，因此装配失败
  不会留下半开的连接。
- **先全部就绪，再发布**。`Install` 在最后统一执行，读方看到的要么是整代、要么是上一代，
  不存在「db 是新代、redis 还是旧代」的中间态。
- **`uuidint` / `redislock` 绑定本代 Redis 客户端**（`WithClient`），不走「最后安装的
  Registry」。这样即使 `Reload` 正在换连接，已持有锁的那一代仍然用自己那条连接去解锁。

`Stop` 逐个 `Close` 本代句柄并用 `errors.Join` 合并错误，之后用 `CompareAndSwap(自己, nil)`
撤回快照：Reload 先起新代再停旧代，旧代只能清掉仍指向自己的指针，不会把新连接一起关掉。

### 请求为什么不会串代

`Module` 实现了 `support.RequestBinder`：框架在请求**拿到本代 lease 之后、进入 handler
之前**，把 `Start` 产出的快照固定进 ctx。所以「handler N + 快照 N+1」这种错配不会发生，
在途请求即使后继代已发布，也继续用自己被固定的那一代。

`appio.Middleware()` 只是兜底：给框架之外自建的 engine 用，ctx 已经固定过时不覆盖。

## 取句柄：三种入口，一条路径

| 入口 | 谁固定快照 | 写法 |
| --- | --- | --- |
| HTTP 请求 | 框架（`RequestBinder`） | 业务只写 `appio.WithContext(ctx)` |
| 脚本 | `script/register.go` 的 `withIO`：`Start` → `WithSnapshot` → `Run` → `Stop` | 同一条路径，脚本借到完整一代 |
| 测试 / 临时工具 | 自己 `Publish` / `PublishDB` | 只给不需要模块生命周期的场景 |

`WithContext(ctx)` 的语义是「ctx 有固定值就用它，否则回退到已发布的当前代，两者都没有就
**panic**」。最后这条是刻意的：入口没有发布本代是装配错误，在这里暴露比把 nil 句柄传给
下游、在离原因很远的地方解引用要好。

取句柄只有一条路：

```go
// 命名实例：一律 (句柄, error)
db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.InstanceOrder)
if errors.Is(err, gofydb.ErrUnknownInstance) { ... }  // 别名不在本代配置里
if errors.Is(err, gofydb.ErrNotConfigured)  { ... }  // 快照或 Registry 缺失

// 需要一条不带事务的连接（开启事务、独立提交）时用 DBIO
base, err := appio.WithContext(ctx).DBIO(ctx, appdb.InstanceOrder)

// 不是连接的两个例外
gen := appio.WithContext(ctx).UUID()       // 未启用时为 nil
lock := appio.WithContext(ctx).RedisLock() // 未启用时为 nil
```

## 实例与配置

| 配置段 | 实例 | 建连时机 |
| --- | --- | --- |
| `db.default` | `default` | 框架打开，`app/appio` 只发布引用，从不关闭 |
| `db.order` / `db.product` | `order` / `product` | `Start` 时立即打开 |
| `db.<其他名字>` | 任意（如 `selectDb`） | 首次 `NewIO` 时懒加载 |
| `redis.cache` / `session` / `ratelimit` | 固定三个名字 | `Start` 时打开所有 `enabled: true` 的实例 |
| `amqp.*` / `kafka.*` / `rocketmq.*` | 任意名字，`default` 只是最常用的那个 | `Start` 时打开所有 `enabled: true` 的实例 |
| `uuidint.*` | 单个生成器 | `Start` 时构造并预热池 |
| `lock.*` | 单个 Locker | `Start` 时构造 |

对 `redis` / `amqp` / `kafka` / `rocketmq` 来说，`enabled: false` 的实例不建连、不进本代
Registry，按名字解析返回 `ErrUnknownInstance`；`db` 没有这个开关——配置了就会打开
（`order` / `product` 立即打开，其他名字懒加载）。因此本地没有 RabbitMQ / Kafka / RocketMQ
也能启动，再有一个 MySQL 和一个启用的 Redis，订单链路就能跑通。

两个「必须在启动期失败」的校验：

- `redis` 段启用了 `cache` / `session` / `ratelimit` 之外的名字 → 报错，避免拼写错误悄悄
  留下 nil 句柄；
- `lock.enabled: true` 但 `lock.redis_instance_name` 指向的 Redis 没启用 → 报错。它有
  Registry 可查，缺客户端说明配置自相矛盾，不能拖到第一次加锁。

## `DBConnect` 与 `DBIO` 的分工

| 方法 | 返回什么 | 用在哪 |
| --- | --- | --- |
| `DBConnect(ctx, name)` | ctx 有事务则返回该事务，否则本代连接；已绑定 ctx | 查询与写入，绝大多数场景 |
| `DBIO(ctx, name)` | 本代连接，**不解析事务**，不绑 ctx | 开启事务；必须独立提交的写入（消费者幂等台账、claim 认领） |

两者都按名字从本代 Registry 解析，`default` 对两者都是同一个特例。区别只有一条：
**要不要服从 ctx 里的事务**。

合成一个方法看起来更省事，但「必须独立提交」的写入会因此变成需要绕开的特例
（详见 `support/consumer` 的幂等台账与 `support/claim`），所以保持两个入口。

## 为什么 `UUID()` 和 `RedisLock()` 没有 `IO` 后缀

`DBIO` / `RedisIO` / `AMQPIO` / `KafkaIO` / `RocketMQIO` 返回 `(句柄, error)`，因为它们
要按别名在本代 Registry 里解析，可能失败。生成器和锁不是「连接」，没有 Registry 可按名字
解析，启用与否在 `Start` 时已经决定，所以直接返回句柄、未启用为 nil。

`RedisLock` 不叫 `Lock`：调用方紧接着就要调 Locker 的 `Lock(ctx, key)`，若取用方法也叫
`Lock`，代码会写成 `Lock().Lock(...)`，读起来像笔误。

## 边界

- **不注册路由**：`Register` 是空实现，路由归 `app/module/*`。
- **不含业务**：这里没有查询、没有事务、没有错误码；service / repository 只认快照。
- **包级访问器只留给没有 ctx 的代码**：`appdb.Default()`、`appredis.Cache()` 这些是
  `Install` 的产物，供 script、测试与排障使用；`shell/check-io-access.sh` 禁止
  `service` / `repository` / `query` 调用它们，也禁止自己 `Publish`。共用代码的取法只有
  一种，否则「本代」就没有意义。
- **controller 不得 import `app/appio`**（`shell/check-imports.sh`）：HTTP 契约层只做
  视图映射，连接属于 service 以下。
- **worker 进程不启动这一代**：`worker --group=...` 不跑模块工厂，`appio.Current()` 在
  worker 进程里是 nil。worker 复用这里的 `Config` **类型**读配置（`worker/connections.go`），
  连接由 `support/consumer` 在消费者构造时自建。在 worker 里调 `appio.WithContext` 会
  panic，这是设计而非疏漏。

## 新增一种 IO

以新增一个 `example` 实例为例，七步：

1. 新建 `app/appio/example/example.go`，照现有子包写 `Config`、`Open`、`Set`、
   `Install` / `Close`、`Default()` / `NewIO`，包注释写清「业务经快照取用」。
2. 在 `io_runtime.go` 的 `Module` 加配置字段与 `*Set` 字段，`NewModule` /
   `NewModuleFactory` 各加一个参数。
3. 在 `Start` 里按依赖顺序 `Open`，失败时逆序 `Close` 已建连接；全部成功后再
   `Install`，最后加进 `newSnapshot(...)`。
4. 在 `Stop` 的 `errors.Join` 里加 `Close()`。
5. 在 `snapshot.go` 的 `Snapshot` 加字段，并加取用方法：是连接就 `ExampleIO(ctx, name)`
   返回 `(句柄, error)`，不是连接才直接返回句柄。
6. 加 `config.yaml` 段与 `app/config/config.go` 的字段、`Validate()`，再改
   `reload.go`、`script/register.go`、`app/appio/io_runtime_test.go` 的构造调用。
7. 若要在业务层取用，补 `shell/check-io-access.sh` 的禁用列表（包级访问器），并在
   `app/module/*/README.md` 的 import 矩阵里确认没有越层。

## 延伸阅读

建连细节、配置字段与连接层语义都在各自包里，这里只负责装配：

- [`support/db`](../../../../support/db/README.md) · [`support/redis`](../../../../support/redis/README.md) ·
  [`support/amqp`](../../../../support/amqp/README.md) · [`support/kafka`](../../../../support/kafka/README.md) ·
  [`support/rocketmq`](../../../../support/rocketmq/README.md)
- [`support/uuidint`](../../../../support/uuidint/README.md) · [`support/lock`](../../../../support/lock/README.md)
- [`support/generation`](../../../../support/generation/README.md)：`Set[T]` 的发布 / 固定 / panic 语义
- [`app/module/order/README.md`](../module/order/README.md)：业务层怎么消费快照、import 边界怎么被
  `shell/check-imports.sh` 强制

## 测试

```bash
cd examples/demo-service
go test ./app/appio/...
```

- `io_runtime_test.go`：用 `miniredis` 起 `redis.ratelimit`，验证 `Start` 安装全部句柄、
  快照可取、`Stop` 后句柄清空；同时覆盖 `uuidint` / `redislock` 落在配置的前缀与键上。
  AMQP / Kafka / RocketMQ 的接线测试需要真实服务，未设环境变量时 `t.Skip`：
  `GOFY_TEST_AMQP_ADDR`、`GOFY_TEST_KAFKA_BROKERS`、`GOFY_TEST_ROCKETMQ_ENDPOINT`。
- `snapshot_test.go`：`WithContext` 在没有代时 panic、`Middleware` 按请求固定、
  `db.default` 走特例解析、`NewSet` 快照没有 Registry 时返回 `ErrNotConfigured`。
