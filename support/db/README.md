# support/db

基于 GORM 的 MySQL 协议数据库连接包：打开连接、配置连接池、`Ping` 校验、
关闭连接。驱动使用 `gorm.io/driver/mysql`，因此任何兼容 MySQL 协议的服务器
都可以接入——MySQL、MariaDB、TiDB、OceanBase、SelectDB（Doris）等，实例名
也可以按用途自由命名（如 `selectDb`）。

默认没有全局状态，调用方自己传入 `context`、`Config` 和 `zap.Logger`，
可以直接用于 `gofy.WithDatabase`，也可以单独使用；需要按配置别名取用时，
可以用可选的 `Configure` / `NewIO` 命名实例注册表。

## 快速开始

```go
import (
    "context"

    gofydb "github.com/bingcool/gofy/support/db"
    "go.uber.org/zap"
)

db, err := gofydb.New(ctx, gofydb.Config{
    DSN:          "user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local",
    MaxOpenConns: 50,
    MaxIdleConns: 10,
}, logger)
if err != nil {
    return err
}
defer gofydb.Close(db)

var one int
if err := db.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
    return err
}
```

交给 `gofy.App` 托管时用 `WithDatabase`，连接由 App 创建、热更新时重建、
`Shutdown` 时关闭：

```go
app, err := gofy.New(
    gofy.WithDatabase(cfg.DB), // cfg.DB 是 gofydb.Config
    // ...
)
```

已有连接用 `gofy.WithDB(db)` 注入；该选项不会 `Ping`，也不会关闭连接，
生命周期由调用方负责。两个选项互斥。

## 命名实例（Configure / NewIO）

按配置别名直接取用连接时，启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`。连接池在第一次使用时打开并缓存：

```go
reg, err := gofydb.Configure(map[string]gofydb.Config{
    "order":    {DSN: os.Getenv("ORDER_DB_DSN")},
    "selectDb": {DSN: os.Getenv("SELECT_DB_DSN")}, // 兼容 MySQL 协议的 SelectDB/Doris
}, logger)
if err != nil {
    return err
}
defer reg.Close()

db, err := gofydb.NewIO(ctx, "order") // 业务代码：从当前一代取用
```

### 包级 NewIO 与 reg.NewIO 的区别

| 写法 | 从哪一代取用 | 适用场景 |
| --- | --- | --- |
| `gofydb.NewIO(ctx, "order")` | 包级当前一代，即最近一次 `Configure` / `Install` 的那一代 | 业务代码不持有注册表，任何位置按别名直接取用 |
| `reg.NewIO(ctx, "order")` | `reg` 这一代，与它是否已安装无关 | 装配层、热更新、测试中持有该代引用 |

两者的关系按 `reg` 所处的代区分：

- `reg` 是 `Configure` 返回的当前代时，`reg.NewIO` 和包级 `gofydb.NewIO`
  共用同一份懒加载缓存，返回同一个 `*gorm.DB`（同一连接池），不会打开第二个
  连接池；
- `reg` 是用 `NewRegistry` 建好但尚未 `Install` 的新代时，`reg.NewIO`
  只预建这一代的连接池，业务侧包级调用仍然用旧代；全部实例就绪后
  `reg.Install()` 才切换，并返回被换下的旧代：`Configure` 会关闭旧代，直接调用 `Install` 的装配层自己负责关闭（demo 的按代句柄即走这条路）。
- 关闭同样按代区分：`CloseAllIO()` 只关闭当前一代，`reg.Close()` 关闭
  `reg` 这一代（即使它已经不是当前代）。

注册表自身的约定：

- `Configure` 校验每个别名和 `Config`：空别名或非法配置直接返回错误；空 map
  表示禁用全部实例（`NewIO` 返回 `ErrUnknownInstance`）。`logger` 为 nil 时
  使用 `zap.NewNop()`，所有别名共享同一个 logger。
- 未注册的名称返回 `ErrUnknownInstance`；未调用 `Configure`，或当前一代已经
  关闭，返回 `ErrNotConfigured`。
- 建连失败不会缓存，下一次 `NewIO` 会重新尝试。
- 首次建连在注册表锁外执行：不同别名的连接并行初始化，互不阻塞；同一别名的
  并发首次调用只打开一个连接池，其余调用等待同一次结果。
- `Close` 开始后不再发布新连接：已经开始、在 `Close` 之后才完成的连接会被立即
  释放，调用方得到 `ErrNotConfigured`。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己打开的
  连接池，不影响已经切换到新一代的调用方；demo 热更新依赖这个顺序（新模块先
  启动、旧模块后关闭）。
- `Configure` 等价于 `NewRegistry` + `Install`。热更新时先用 `NewRegistry`
  建好新连接，全部成功后再 `Install` 切换；失败只关闭新代，不影响正在
  服务的旧代。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。
- 包级 `NewIO` / `CloseAllIO` 是单 App、单 Registry 的便捷入口：同一进程内多个
  Registry 都 `Install` 时，包级调用只看最后安装的那一代，不承诺多 App 隔离；
  需要代际隔离时用 `reg.NewIO` 与 `reg.Close`。

`db.default` 由框架的 `WithDatabase` 创建和关闭，业务应继续使用 `Runtime.DB`
或 `App` 注入的 `*gorm.DB`；把同一个别名再放进注册表会得到第二个连接池。

不调用 `Configure` 时，包的行为与 `New`/`Close` 完全一致，不产生全局状态。

## Config

```yaml
db:
  dsn: ${ORDER_DB_DSN}
  max_idle_conns: 10
  max_open_conns: 50
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m
  ping_timeout: 5s
  slow_threshold: 200ms
  log_level: warn        # silent | error | warn | info
  log_parameter_values: false
  prepare_stmt: false
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `DSN` | MySQL 协议 DSN，必填 | — |
| `MaxIdleConns` | 空闲连接数 | `10` |
| `MaxOpenConns` | 最大打开连接数 | `50` |
| `ConnMaxLifetime` | 连接最长存活时间 | `1h` |
| `ConnMaxIdleTime` | 连接最长空闲时间 | `10m` |
| `PingTimeout` | 启动时 `Ping` 的超时 | `5s` |
| `SlowThreshold` | 慢 SQL 阈值 | `200ms` |
| `LogLevel` | GORM 日志级别，`warning` 等同 `warn` | `warn` |
| `LogParameterValues` | 是否在日志中展开 SQL 参数值 | `false` |
| `PrepareStmt` | 是否缓存预编译语句 | `false` |

`Validate()` 在连接前检查：

- `DSN` 不能为空（返回 `ErrDSNRequired`）；
- 连接数不能为负，`MaxIdleConns` 不能大于 `MaxOpenConns`；
- 各时长不能为负；
- `LogLevel` 必须是 `silent` / `error` / `warn` / `warning` / `info` 之一。

零值表示“使用默认值”，因此无法通过配置显式设置 `0` 个连接或 `0` 超时。

## New

`New(ctx, cfg, logger)` 的顺序：

1. `cfg.Validate()`；失败直接返回，不建立连接；
2. 填充默认值；`ctx` 为 nil 时用 `context.Background()`，`logger` 为 nil
   时用 `zap.NewNop()`；
3. `gorm.Open` 打开连接，迁移时禁用外键约束，按 `PrepareStmt` 决定是否缓存
   预编译语句；
4. 设置连接池参数；
5. 在 `PingTimeout` 内 `PingContext`；失败会关闭连接并返回错误。

错误信息中的 DSN 会脱敏为 `user:***@tcp(...)`，不会泄露密码；日志同样不会
输出完整 DSN。

## Close

`Close(db)` 释放底层 `sql.DB` 连接池；`nil` 是空操作。调用后不要继续使用该
`*gorm.DB`。

## 事务上下文

`Transaction(ctx, db, fn)` 在 `db` 上开启事务，并把事务放进传给 `fn` 的
context；repository 用 `WithContext(ctx, fallback)` 取连接，同一次请求内多个
模块的调用就会共享同一个事务：

```go
err := gofydb.Transaction(ctx, rt.DB, func(ctx context.Context) error {
    if _, err := products.DecreaseStock(ctx, skuID, num); err != nil {
        return err
    }
    _, err := orders.Create(ctx, order)
    return err
})
```

```go
func Transaction(ctx context.Context, db *gorm.DB, fn func(ctx context.Context) error) error
func WithContext(ctx context.Context, fallback *gorm.DB) *gorm.DB
```

- 嵌套调用复用外层事务，不创建 savepoint；内层返回错误会冒泡到外层并导致整体
  回滚，内层成功也不会提前提交；
- `WithContext` 优先返回 ctx 中的事务，没有事务时才返回 `fallback`；repository
  的显式连接不会覆盖事务，内层只消费最外层开启的事务；
- `fn` 为 nil 返回 `ErrTxFuncRequired`；ctx 无事务且 `db` 为 nil 返回
  `ErrTxDBRequired`；`ctx` 为 nil 时按 `context.Background()` 处理；
- 事务由调用方 service 开启，repository 不自行提交；context 只承载请求范围
  数据（trace、request id、事务），不要放 repository/service。

## 日志

`LogLevel` 映射到 GORM 日志级别：

| 配置值 | 行为 |
| --- | --- |
| `silent` | 不输出 SQL |
| `error` | 只输出执行失败的 SQL |
| `warn` / `warning` | 输出失败 SQL 和超过 `SlowThreshold` 的慢 SQL |
| `info` | 输出每条 SQL |

SQL 日志使用 `component=gorm` 的 zap 字段，包含 `elapsed`、`rows`、`sql`；
错误和慢 SQL 还会附加 `error` 或 `threshold`。日志通过传入的 `zap.Logger`
输出，便于和应用的访问日志、业务日志统一。

默认不在日志中输出参数值：Logger 实现 `gorm.ParamsFilter`，慢 SQL 和失败 SQL
只记录带 `?` 占位符的模板，手机号、邮箱、Token 等查询条件不会进入日志。
只有本地排查确实需要完整参数时才设置 `log_parameter_values: true`，生产环境
不应开启。

## 配置与安全

- DSN 通常包含数据库密码，推荐用环境变量注入，例如 `dsn: ${ORDER_DB_DSN}`；
- 不要提交带真实密码的 `config.yaml`，示例配置只保留占位符；
- 错误和日志已对 DSN 密码脱敏，但业务代码仍不应主动打印 `Config.DSN`。

## 测试

```bash
go test ./support/db/
```

默认只跑单元测试（校验、空 DSN、错误脱敏、`Close(nil)`，以及基于纯 Go
SQLite 的事务提交 / 回滚 / 嵌套复用）。
连接真实 MySQL 协议服务器的集成测试通过 `GOFY_TEST_MYSQL_DSN` 开启：

```bash
GOFY_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/db -run Integration -v
```

集成测试会验证 `SELECT 1` 和 `MaxOpenConns` 是否生效。
