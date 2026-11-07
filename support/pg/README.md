# support/pg

基于 GORM 的 PostgreSQL 连接包：打开连接、配置连接池、`Ping` 校验、关闭连接。
驱动使用 `gorm.io/driver/postgres`，底层是 `jackc/pgx/v5`（通过 database/sql
接口接入，无需 cgo）。

与 `support/db` 一样，本包默认没有全局状态，调用方自己传入 `context`、
`Config` 和 `zap.Logger`，可以单独使用；需要按配置别名取用时，可以用可选的
`Configure` / `NewIO` 命名实例注册表。

## 快速开始

```go
import (
    "context"

    gofypg "github.com/bingcool/gofy/support/pg"
    "go.uber.org/zap"
)

conn, err := gofypg.New(ctx, gofypg.Config{
    DSN:          "postgres://user:password@127.0.0.1:5432/app?sslmode=disable",
    MaxOpenConns: 50,
    MaxIdleConns: 10,
}, logger)
if err != nil {
    return err
}
defer gofypg.Close(conn)

var one int
if err := conn.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
    return err
}
```

DSN 支持 pgx 的两种形式：

```text
postgres://user:password@127.0.0.1:5432/app?sslmode=disable&connect_timeout=5
host=127.0.0.1 port=5432 user=app password=secret dbname=app sslmode=disable connect_timeout=5
```

`connect_timeout` 单位是秒，建议显式配置，避免网络不通时建连长时间挂起。

## 命名实例（Configure / NewIO）

与 `support/db` 的注册表语义完全一致：启动阶段注册一次，之后任意位置调用
`NewIO(ctx, name)`，连接池在第一次使用时打开并缓存：

```go
reg, err := gofypg.Configure(map[string]gofypg.Config{
    "order":   {DSN: os.Getenv("ORDER_PG_DSN")},
    "report":  {DSN: os.Getenv("REPORT_PG_DSN")},
}, logger)
if err != nil {
    return err
}
defer reg.Close()

conn, err := gofypg.NewIO(ctx, "order") // 业务代码：从当前一代取用
```

约定：

- `Configure` 校验每个别名和 `Config`：空别名或非法配置直接返回错误；空 map
  表示禁用全部实例（`NewIO` 返回 `ErrUnknownInstance`）。`logger` 为 nil 时
  使用 `zap.NewNop()`。
- 未注册的名称返回 `ErrUnknownInstance`；未调用 `Configure`，或当前一代已经
  关闭，返回 `ErrNotConfigured`。
- 建连失败不会缓存，下一次 `NewIO` 会重新尝试；并发调用安全，同一别名只打开
  一个连接池。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己打开的
  连接池，不影响已经切换到新一代的调用方，热更新可以先启动新模块再关闭旧模块。
- `Configure` 等价于 `NewRegistry` + `Install`。需要原子切换时先用
  `NewRegistry` 建好新代、用 `(*Registry).NewIO` 预建连接，全部成功后再
  `Install`；失败只关闭新代，当前代保持不变。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。

不调用 `Configure` 时，包的行为与 `New`/`Close` 完全一致，不产生全局状态。

## Config

```yaml
pg:
  dsn: ${ORDER_PG_DSN}
  max_idle_conns: 10
  max_open_conns: 50
  conn_max_lifetime: 1h
  conn_max_idle_time: 10m
  ping_timeout: 5s
  slow_threshold: 200ms
  log_level: warn        # silent | error | warn | info
  prepare_stmt: false
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `DSN` | pgx DSN（URL 或 keyword/value），必填 | — |
| `MaxIdleConns` | 空闲连接数 | `10` |
| `MaxOpenConns` | 最大打开连接数 | `50` |
| `ConnMaxLifetime` | 连接最长存活时间 | `1h` |
| `ConnMaxIdleTime` | 连接最长空闲时间 | `10m` |
| `PingTimeout` | 启动时 `Ping` 的超时 | `5s` |
| `SlowThreshold` | 慢 SQL 阈值 | `200ms` |
| `LogLevel` | GORM 日志级别，`warning` 等同 `warn` | `warn` |
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

错误信息中的 DSN 会脱敏：URL 形式隐藏 userinfo 中的密码段
（`postgres://user:***@host`），keyword/value 形式把 `password=xxx` 替换为
`password=***`（含单引号包裹的值）；日志同样不会输出完整 DSN。

## Close

`Close(conn)` 释放底层 `sql.DB` 连接池；`nil` 是空操作。调用后不要继续使用该
`*gorm.DB`。

## 日志

`LogLevel` 映射到 GORM 日志级别：

| 配置值 | 行为 |
| --- | --- |
| `silent` | 不输出 SQL |
| `error` | 只输出执行失败的 SQL |
| `warn` / `warning` | 输出失败 SQL 和超过 `SlowThreshold` 的慢 SQL |
| `info` | 输出每条 SQL |

SQL 日志使用 `component=gorm` 的 zap 字段，包含 `elapsed`、`rows`、`sql`；
错误和慢 SQL 还会附加 `error` 或 `threshold`。

## 配置与安全

- DSN 通常包含数据库密码，推荐用环境变量注入，例如 `dsn: ${ORDER_PG_DSN}`；
- 不要提交带真实密码的 `config.yaml`，示例配置只保留占位符；
- 错误和日志已对 DSN 密码脱敏，但业务代码仍不应主动打印 `Config.DSN`；
- 生产环境建议 `sslmode=require`（或 `verify-full`），不要沿用示例中的
  `sslmode=disable`。

## 测试

```bash
go test ./support/pg/
```

默认只跑单元测试（校验、空 DSN、两种 DSN 形式的错误脱敏、`Close(nil)`、
注册表代际语义）。连接真实 PostgreSQL 的集成测试通过 `GOFY_TEST_PG_DSN` 开启：

```bash
GOFY_TEST_PG_DSN='postgres://user:password@127.0.0.1:5432/app?sslmode=disable' \
  go test ./support/pg -run Integration -v
```

集成测试会验证 `SELECT 1` 和 `MaxOpenConns` 是否生效。