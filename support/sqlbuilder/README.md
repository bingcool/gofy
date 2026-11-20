# support/sqlbuilder

把 SQL 拼出来的构造器：只产出 `(sql, args)`，不执行、不持有连接、不定义模型。

它是 [`entgo.io/ent/dialect/sql`](https://entgo.io/docs/sql-builders) 的转发层，加一个带
error 的 `Build`。构造器与谓词都是包装函数（不是函数值变量），因此只读，并且在 IDE 与
godoc 的函数分组里能看到 `Table`、`Select` 这些名字。**不含** ent 运行时的任何部分：没有 ORM、schema DSL、codegen、migration、
`*ent.Client`、`entsql.Open`。数据访问仍然走 gorm（2026-11-15「不引入 ent client」的决策
不变），本包只解决「语句怎么被拼出来」。

## 为什么需要它

gorm 1.31 能表达子查询、`JOIN (?)`、`clause.Locking`、`OnConflict`，但**没有 CTE，也没有
UNION**：`clause/with.go` 全文只有 `type With struct{}`（没有 `Build` 方法），`clause` 包里
根本不存在 `Union` 类型。这两类语句以前只能整条 `Raw` 手写，`?` 与 args 靠人肉对齐——
改一个条件就要重数一遍，错位时报错位置与真正原因无关。

构造器把这件事变成数据结构：条件可以按 `if` 追加，参数顺序由它维护。

## 快速开始

```go
db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
if err != nil {
    return nil, err
}

orders := sqlbuilder.Table("orders")
sel := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).
    From(orders).
    Where(sqlbuilder.EQ(orders.C("status"), "paid"))

stmt, args, err := sqlbuilder.Build(sel)
if err != nil {
    return nil, err
}
var rows []Row
err = db.Raw(stmt, args...).Scan(&rows).Error // DBConnect 已绑 ctx
```

`Build` 是唯一需要记住的入口：`Query()` 不返回 error，构造期的错误被收在 builder 里，
直接调 `sel.Query()` 会把它们静默丢掉。

执行一律交回 gorm。**不要**从 gorm 取 `*sql.DB` 去 `QueryContext`：`db.DB()` 即使在事务里
返回的也是连接池（它把 `*sql.Tx` 里的字段掏出来），会同时丢掉事务、SQL 日志（脱敏 /
`X-Trace-Id` / 慢 SQL）与连接代际。要流式读大结果集用 `Raw(stmt, args...).Rows()`。

## 五条规则

1. **`Predicate` 有状态，不能并发复用。** `Query()` 会先 `Reset()` 自己再重放回调。同一个
   `*Predicate` 被两个 goroutine 同时 `Query()` 就是 data race，跨请求缓存同样危险。
   谓词在函数内构造、函数内消费，不存字段、不跨 goroutine。
2. **不要把 slice 传给 `EQ`。** 构造器把它当一个参数，gorm 的 `Raw` 又把它当 `IN` 展开，
   两种解释会打架。列表条件用 `In(col, vals...)`。
3. **表名不经过 gorm 的 `NamingStrategy`。** `Table("orders")` 就是字面量。配了表前缀或
   复数化的项目必须从 `model.TableName()` / 列常量取，不要手写。
4. **方言要显式决定，SQLite 不能显式传**（见下表）。
5. **`Build` 出错时语句与参数都为空**，不要执行半成品。

## 方言

| 目标库 | 写法 | 说明 |
| --- | --- | --- |
| MySQL | 默认（不设置） | 默认用反引号，正确 |
| PostgreSQL | `sqlbuilder.Dialect(DialectPostgres)` | 反引号换成双引号 |
| SQLite | **不要传 `dialect.SQLite`** | 列会渲染成 `` `"orders"."id"` ``（反引号套双引号），SQLite 把它当成名为 `"orders"."id"` 的列，报 `no such column`；该方言下 `ForUpdate` 还会记录错误，`Build` 直接失败 |

SQLite 只出现在测试里，用默认方言即可（反引号 SQLite 也接受）。这条不是洁癖：
两种失败方式都很难和「方言传错了」联系起来。

## 多租户

`support/tenant` 只挂 `Query`/`Update`/`Delete`/`Create`，**刻意不挂 `Raw`**。所以：

| 路径 | 租户插件 |
| --- | --- |
| `Where(fragment, args...)` | 部分介入：片段变成 `clause.Expr`，插件能识别其中单独一条 `tenant_id = ?`（仅等值） |
| `Raw(stmt, args...)` | 完全不介入 |

**能用谓词表达的条件就交给 `Where`，不要拼进整条 `Raw`。** 整条语句必须走 `Raw` 且表含
租户列时，自己带上租户条件：

```go
pred, ok := sqlbuilder.TenantScope(ctx, orders) // ctx 里没有租户时返回 false
if !ok {
    return nil, apperr.Unauthorized("tenant required") // 或跳过过滤，由业务决定
}
sel := sqlbuilder.Select(orders.C("id")).From(orders).Where(pred)
```

`TenantScope` 的列名是 `DefaultTenantColumn`（`tenant_id`），与 `support/tenant` 默认列一致
（`sqlbuilder_test.go` 里的 `TestDefaultTenantColumnMatchesTenant` 钉住这一点——这里不能
直接 import `support/tenant`，那会把 gorm 带进一个从不执行 SQL 的包）。

## 什么时候不该用

- query 层一年新增不到 5 条复杂查询：手写 SQL 加 `if` 更直接；
- 目标只是「少写点字符串」：列常量 + `strings.Builder` 就够了；
- 主要诉求是多数据库方言兼容：构造器只解决引号和少量语法，`FILTER (WHERE ...)`、
  `NULLS LAST`、`ILIKE` 仍要自己分支，收益小于预期。

反过来说，出现「3 个以上 CTE 的报表」「有人因为 `?` 与 args 对不齐在生产上踩过一次」
「要把查询构造抽出来做单测而不想起一个数据库」时，就该用它。

## 依赖成本

| 项 | 量 |
| --- | --- |
| `go.mod` | +1 直接依赖（`entgo.io/ent`），+1 间接版本提升（`go.opencensus.io`） |
| `go.sum` | +18 行，多数是 `/go.mod` 哈希 |
| 编进依赖图的 ent 包 | 只有 `entgo.io/ent/dialect` 与 `entgo.io/ent/dialect/sql` |
| 没有的 | `ariga.io/atlas`、gremlin、codegen、`*ent.Client`、gorm |

退出成本是删掉本目录：没有别的包依赖它，import 时不创建任何状态。

## 测试

```bash
go test -count=1 ./support/sqlbuilder/
```

测试直接比对 `(sql, args)`，不需要数据库；`TestDialectQuoting` 里 SQLite 那条是**故意
断言缺陷**的——上游修复后它会失败，届时可以删掉包注释里的警告。
