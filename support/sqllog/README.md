# support/sqllog

SQL 语句日志的整层实现：文本档位、脱敏、采样与限流，以及 GORM 适配器。
`support/db`、`support/sqlite` 通过它输出 SQL 条目，框架的应用入口用
`gofy.WithSQLPolicy` 把一份策略交给每个连接池。

## 为什么不在 logger

`logger` 必须保持叶子包——只依赖 `zap` 与日志轮转，不得依赖任何 gofy 包。
SQL 条目需要从请求上下文取两类链路 id：`X-Trace-Id`（跨服务业务 id，来自
`support/headerpropagation`）与 `trace_id` / `span_id`（OTel Span，来自
`support/otel`）。把这一层放在 `logger` 里，会让日志包依赖整个框架；而
`support/db`、`support/sqlite` 为了拿 `*Policy` 反过来 import `logger`，又把
日志装配拖进了每个数据库包。三条导入守卫同时被破，因此整层搬到 `support/sqllog`。

## 快速开始

一份策略，进程内共享；每个池接收同一个指针。

```go
import (
    gofysqllog "github.com/bingcool/gofy/support/sqllog"
)

policy, err := gofysqllog.NewPolicy(cfg.SQLLog) // 一段配置一个策略，每进程一次
if err != nil {
    return err
}
gormDB, err := gofydb.New(ctx, dbCfg, logger,
    gofydb.WithSQLPolicy(policy),   // 采样、限流、脱敏、档位
    gofydb.WithSQLLogger(sqlLogger), // 可选：SQL 条目走独立通道
)
```

`NewPolicy` 返回的 `*Policy` 并发安全，缺省与校验都在构造时完成；`Disabled()`
为真时整层不产生条目（失败与慢语句也没有）。策略为 `nil` 时连接保持旧行为：
按 `log_level` 输出、不采样、不脱敏。

## 三个档位

| 档位 | 内容 |
|---|---|
| `ModeOff` | 不写语句文本，只留 `elapsed` / `rows` / 错误 |
| `ModePlaceholder`（默认） | 带占位符的模板，值不外泄 |
| `ModeFull` | 展开后的语句，仅限本地排障 |

## 什么算"启用了本节"

`ResolveEnabled` 按顺序看三件事，YAML 与代码构造走同一套判定：

1. 写了 `enabled` 就听它的，`false` 压过其它所有字段；
2. 否则 YAML 段里出现任何其它键就算启用——写了 `mode: full` 却忘了 `enabled`，
   不能静默失效；
3. 否则结构体里有任一非零字段就算启用，`sqllog.Config{Mode: "full"}` 因此与
   `sql_log: {mode: full}` 行为一致。

第 3 条只在代码里构造 `Config` 时起作用（YAML 路径由第 2 条先命中）。零值字段与
未提及的字段无法区分，所以 `Config{LogSchemaQueries: false}` 表达的仍是"不启用"；
想明确关闭就用 `Enabled: &false`。

## 两条容易踩的语义

- **限流是进程级的**：`error_rate_limit` 的含义是"本进程每秒最多写 N 条"。
  每个池各造一份策略会把这个数乘以池数量，所以策略要在装配点构造一次再传下去。
- **脱敏默认表由框架维护**：`MaskConfig.Columns` / `Patterns` 是追加，不是替换。
  升级框架会带来新的敏感列；复制到各项目里的清单做不到这一点。

## 脱敏支持边界

脱敏作用于**已展开的 SQL 文本**（值已内联），实现是扫描器而不是 SQL 解析器。它能稳定处理：

- `WHERE col = 'v'` / `col IN (...)` / `UPDATE ... SET col = ?`：列名与随后的字面量配对；
- `INSERT INTO t (c1, c2) VALUES (v1, v2), (v3, v4)`：先记列名，再按**位置**映射到每一组
  值，所以自定义敏感列在多行插入里也能覆盖；
- 不依赖列名的内置值模式（手机号、邮箱、证件号、银行卡、长 token）：任何字符串或数字字面量
  命中都会被替换，包括列名看不出敏感的自由文本。

边界同样要清楚：

- 认不出的语句形态**原样输出**。CTE、CASE、方言特有语法、子查询里的字面量等没有可靠的
  列名配对时，扫描器不会猜。启用脱敏不等于"任何值都已脱敏"；如果某类语句的值不能确认安全，
  宁可不要记录它（调低档位或在调用点跳过日志），不要靠脱敏兜底。
- `MaskConfig.Columns` / `Patterns` 是追加。内置基线由框架维护，改动返回副本不会影响它：
  `DefaultMaskColumns()` / `DefaultMaskPatterns()` 都返回拷贝。
- 脱敏只作用于 SQL 日志，不作用于访问日志；访问日志的 query 记录默认关闭，见
  `support/middleware`。

## 与 logger 的分工

`log:` 决定日志去哪里（输出、轮转、编码），`sql_log:` 决定一条 SQL 条目记什么
（档位、脱敏、采样、限流）。前者留在 `logger`，后者在这里。
