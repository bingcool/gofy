# support/mysql

基于 GORM 的 MySQL 连接包：打开连接、配置连接池、`Ping` 校验、关闭连接。
包内没有全局状态，调用方自己传入 `context`、`Config` 和 `zap.Logger`，
可以直接用于 `gofy.WithDatabase`，也可以单独使用。

## 快速开始

```go
import (
    "context"

    "github.com/bingcool/gofy/support/mysql"
    "go.uber.org/zap"
)

db, err := mysql.New(ctx, mysql.Config{
    DSN:          "user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local",
    MaxOpenConns: 50,
    MaxIdleConns: 10,
}, logger)
if err != nil {
    return err
}
defer mysql.Close(db)

var one int
if err := db.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
    return err
}
```

交给 `gofy.App` 托管时用 `WithDatabase`，连接由 App 创建、热更新时重建、
`Shutdown` 时关闭：

```go
app, err := gofy.New(
    gofy.WithDatabase(cfg.DB), // cfg.DB 是 mysql.Config
    // ...
)
```

已有连接用 `gofy.WithDB(db)` 注入；该选项不会 `Ping`，也不会关闭连接，
生命周期由调用方负责。两个选项互斥。

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
  prepare_stmt: false
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `DSN` | MySQL DSN，必填 | — |
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

错误信息中的 DSN 会脱敏为 `user:***@tcp(...)`，不会泄露密码；日志同样不会
输出完整 DSN。

## Close

`Close(db)` 释放底层 `sql.DB` 连接池；`nil` 是空操作。调用后不要继续使用该
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
错误和慢 SQL 还会附加 `error` 或 `threshold`。日志通过传入的 `zap.Logger`
输出，便于和应用的访问日志、业务日志统一。

## 配置与安全

- DSN 通常包含数据库密码，推荐用环境变量注入，例如 `dsn: ${ORDER_DB_DSN}`；
- 不要提交带真实密码的 `config.yaml`，示例配置只保留占位符；
- 错误和日志已对 DSN 密码脱敏，但业务代码仍不应主动打印 `Config.DSN`。

## 测试

```bash
go test ./support/mysql/
```

默认只跑单元测试（校验、空 DSN、错误脱敏、`Close(nil)`）。
连接真实 MySQL 的集成测试通过 `GOFY_TEST_MYSQL_DSN` 开启：

```bash
GOFY_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/mysql -run Integration -v
```

集成测试会验证 `SELECT 1` 和 `MaxOpenConns` 是否生效。
