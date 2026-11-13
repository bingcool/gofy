# support/sqlite

基于 GORM 的 SQLite 连接包：打开连接、配置连接池、`Ping` 校验、关闭连接。

dialector 使用 [`github.com/glebarez/sqlite`](https://github.com/glebarez/sqlite)
（modernc.org/sqlite），是 **纯 Go、无 cgo** 的实现；它与
`gorm.io/driver/sqlite` 的调用方式一致，但后者依赖 `mattn/go-sqlite3`，需要
cgo 和本地 C 工具链。交叉编译或静态镜像部署时本包不需要额外配置。

与 `support/db`、`support/pg` 一样，本包默认没有全局状态，调用方自己传入
`context`、`Config` 和 `zap.Logger`；需要按配置别名取用时，可以用可选的
`Configure` / `NewIO` 命名实例注册表。

## 快速开始

```go
import (
    "context"

    gofysqlite "github.com/bingcool/gofy/support/sqlite"
    "go.uber.org/zap"
)

conn, err := gofysqlite.New(ctx, gofysqlite.Config{
    DSN: "storage/app.db",
}, logger)
if err != nil {
    return err
}
defer gofysqlite.Close(conn)

var one int
if err := conn.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
    return err
}
```

DSN 支持三种写法：

```text
storage/app.db                                        # 文件
:memory:                                              # 单连接内存库
file:storage/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
```

## 连接池与并发

SQLite 是单写多读，多连接同时写会报 `database is locked`。因此默认
**只保留一个连接**（`max_idle_conns: 1`、`max_open_conns: 1`），同进程内不会
出现写锁竞争。

需要更高并发时：

- 用 `_pragma=journal_mode(WAL)` 打开 WAL，读不阻塞写；
- 用 `_pragma=busy_timeout(5000)` 让写锁冲突时等待而不是立刻报错；
- 再按需调大 `max_open_conns`（通常个位数即可，写仍然串行）。

`:memory:` 数据库每个连接都是独立实例；需要多连接共享同一份内存库时用
`file::memory:?cache=shared`，并注意最后一个连接关闭后数据即消失。

## 命名实例（Configure / NewIO）

语义与 `support/db`、`support/pg` 完全一致：启动阶段注册一次，之后任意位置
调用 `NewIO(ctx, name)`，连接池在第一次使用时打开并缓存：

```go
reg, err := gofysqlite.Configure(map[string]gofysqlite.Config{
    "main":  {DSN: "storage/app.db"},
    "cache": {DSN: "file:storage/cache.db?_pragma=journal_mode(WAL)"},
}, logger)
if err != nil {
    return err
}
defer reg.Close()

conn, err := gofysqlite.NewIO(ctx, "main") // 业务代码：从当前一代取用
```

约定：

- `Configure` 校验每个别名和 `Config`：空别名或非法配置直接返回错误；空 map
  表示禁用全部实例（`NewIO` 返回 `ErrUnknownInstance`）。`logger` 为 nil 时
  使用 `zap.NewNop()`。
- 未注册的名称返回 `ErrUnknownInstance`；未调用 `Configure`，或当前一代已经
  关闭，返回 `ErrNotConfigured`。
- 建连失败不会缓存，下一次 `NewIO` 会重新尝试。
- 首次建连在注册表锁外执行：不同别名的连接并行初始化，互不阻塞；同一别名的
  并发首次调用只打开一个连接池，其余调用等待同一次结果。
- `Close` 开始后不再发布新连接：已经开始、在 `Close` 之后才完成的连接会被立即
  释放，调用方得到 `ErrNotConfigured`。
- 每次 `Configure` 生成新一代注册表。旧一代的 `Close` 只关闭它自己打开的
  连接池，不影响已经切换到新一代的调用方。
- `Configure` 等价于 `NewRegistry` + `Install`。需要原子切换时先用
  `NewRegistry` 建好新代、用 `(*Registry).NewIO` 预建连接，全部成功后再
  `Install`；失败只关闭新代，当前代保持不变。
- 包级 `CloseAllIO()` 关闭当前一代并清空；持有 `Configure` 返回值时直接用
  `reg.Close()` 更精确。
- 包级 `NewIO` / `CloseAllIO` 是单 App、单 Registry 的便捷入口：同一进程内多个
  Registry 都 `Install` 时，包级调用只看最后安装的那一代，不承诺多 App 隔离；
  需要代际隔离时用 `reg.NewIO` 与 `reg.Close`。

不调用 `Configure` 时，包的行为与 `New`/`Close` 完全一致，不产生全局状态。

## Config

```yaml
sqlite:
  dsn: storage/app.db
  max_idle_conns: 1
  max_open_conns: 1
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
| `DSN` | 文件路径、`:memory:` 或带 `_pragma` 的 URI，必填 | — |
| `MaxIdleConns` | 空闲连接数 | `1` |
| `MaxOpenConns` | 最大打开连接数 | `1` |
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

SQLite DSN 不含账号密码，因此错误信息直接带出路径（便于定位是哪个库打不开），
不做脱敏。

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

默认不在日志中输出参数值：Logger 实现 `gorm.ParamsFilter`，慢 SQL 和失败 SQL
只记录带 `?` 占位符的模板。只有本地排查确实需要完整参数时才设置
`log_parameter_values: true`，生产环境不应开启。

## 部署注意

- 数据库文件所在目录必须可写；容器部署时把该目录挂成 volume，避免镜像层丢失数据；
- 备份用 SQLite 的在线备份（`VACUUM INTO` 或 sqlite3 CLI），不要在写入过程中
  直接拷贝文件；
- 文件权限默认继承进程 umask，敏感数据建议放在受限目录；
- 主从/多实例部署时不要让多个进程同时写同一个文件，SQLite 不是网络数据库。

## 测试

```bash
go test ./support/sqlite/
```

SQLite 是嵌入式库，测试不需要外部服务：单元测试覆盖校验、注册表代际语义和
建连失败；集成部分在 `t.TempDir()` 中验证文件库、内存库、`SELECT 1` 以及
连接池默认值。