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

## 覆盖范围

| 类别 | 内容 |
| --- | --- |
| 语句 | `Select` / `SelectExpr` / `With` / `WithRecursive` / `Union`（方法）/ `Window` / `RowNumber` / `Raw` / `Expr` |
| 写语句 | `Insert` / `Update` / `Delete`，upsert 的 `ConflictColumns` / `ConflictConstraint` / `ConflictWhere` / `UpdateWhere` / `DoNothing` / `ResolveWithIgnore` / `ResolveWithNewValues` / `ResolveWith` |
| 谓词 | `And` / `Or` / `Not` / `EQ` / `NEQ` / `GT` / `GTE` / `LT` / `LTE` / `In` / `InSlice` / `NotIn` / `NotInSlice` / `IsNull` / `NotNull` / `Like` / `HasPrefix` / `HasSuffix` / `Contains` / `EqualFold` / `ContainsFold` / `Exists` / `NotExists` / `ColumnsEQ` / `ColumnsNEQ` / `ColumnsLT` / `ColumnsLTE` / `ColumnsGT` / `ColumnsGTE` / `CompositeGT` / `CompositeLT` / `IsTrue` / `IsFalse` / `False` |
| 逃生口 | `P`（直接写 builder）、`ExprP`（片段 + 参数）、`Raw`（无参数片段） |
| 聚合与列表达式 | `Count` / `Sum` / `Max` / `Min` / `Avg` / `Lower` / `As` / `Distinct` / `Asc` / `Desc` / `DescExpr` |
| 行锁 | `WithLockAction` / `WithLockTables` / `WithLockClause`，动作 `SkipLocked` / `NoWait` |

不做：DDL、`CreateView`、schema、migration、任何 ORM 能力。

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

## 条件拼接

条件加不加取决于参数有没有值：先建语句，再按需 `Where`。**`Where` 可以调用多次，每次之间
都是 AND**，不需要自己套一层 `And`，更不要在无条件时挂一个空 `And()`。

```go
sel := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).From(orders)

if len(status) > 0 {
    sel = sel.Where(sqlbuilder.InSlice(orders.C("status"), status))
}
if userID > 0 {
    sel = sel.Where(sqlbuilder.GT(orders.C("user_id"), userID))
}
if keyword != "" {
    sel = sel.Where(sqlbuilder.Contains(orders.C("name"), keyword))
}

stmt, args, err := sqlbuilder.Build(sel)
```

条件多到一个函数里放不下时，先攒谓词再一次性挂上（`And` 只有一个参数时不加括号，多个时
加括号，语义一致）：

```go
preds := make([]*sqlbuilder.Predicate, 0, 4)
if len(status) > 0 {
    preds = append(preds, sqlbuilder.InSlice(orders.C("status"), status))
}
if userID > 0 {
    preds = append(preds, sqlbuilder.GT(orders.C("user_id"), userID))
}
if len(preds) > 0 {
    sel = sel.Where(sqlbuilder.And(preds...))
}
```

两条规则：

1. **`[]string` 传不进 `In`。** `In` 收 `...any`，`In(col, status...)` 要求 `status` 是
   `[]any`。整个传 `In(col, status)` 虽然能编译，但语句里只有一个装着 slice 的参数，只在
   gorm 之后又把它展开时才成立，PostgreSQL 方言下还会打乱占位符编号。用 `InSlice` /
   `NotInSlice`。
2. **空列表的语义要自己定。** `InSlice` 传空 slice 得到 `FALSE`（与 `In` 不带参数一致），
   意思是「匹配零行」；要「不过滤」就在 `len > 0` 里跳过谓词。`Where(And())` 不属于任何
   一种：它留下一个空的 `WHERE`，语句直接无效——`TestConditionalWhere` 钉住了这个行为。

## 谓词挂到哪里

`*Predicate` 只是条件树，不构成语句，落点只有下面几处。**位置不会报错**——挂错地方通常只是
条件没生效：

| 落点 | 语义 |
| --- | --- |
| `Select(...).Where(p)` | WHERE，多次调用之间 AND |
| `Select(...).Having(p)` | HAVING，聚合之后的过滤 |
| `Join(t).OnP(p)` / `.On(c1, c2)` | JOIN 的 ON；`On` 只做列等值，复合条件用 `OnP(And(...))` |
| `Update(t).Where(p)` / `Delete(t).Where(p)` | 写语句的 WHERE |
| `Insert(t).ConflictWhere(p)` / `UpdateWhere(p)` | upsert 的冲突范围与 DO UPDATE 条件 |
| `Build(p)` → `db.Where(stmt, args...)` | 单独渲染，塞回 gorm 的链式语句复用 |

`Or()` / `Not()` 装饰**下一条** `Where`：`Where(a).Or().Where(b).Where(c)` 得到
`(a OR b) AND c`，只影响紧随其后的那一次。

谓词里要套语句时，`Exists(sel)` / `NotExists(sel)` 收整条 `*Selector`，`In(col, sel)` 的第一个
参数是 `*Selector` 时按子查询渲染（不是列表）：

```go
sub := sqlbuilder.Select(items.C("order_id")).From(items).
    Where(sqlbuilder.EQ(items.C("status"), "ok"))

sel := sqlbuilder.Select(orders.C("id")).From(orders).
    Where(sqlbuilder.In(orders.C("id"), sub))
// WHERE `o`.`id` IN (SELECT `i`.`order_id` FROM `order_items` AS `i` WHERE `i`.`status` = ?)
```

**JOIN 的别名在 `C()` 调用那一刻就固定了，而实参先于 `Join` 求值。** `Join` 会给没有别名的
表自动分配 `t1`、`t2`…，`C()` 把当时的别名烘进字符串；函数实参在 `Join` 运行之前就求完了，
所以连下面这条「一条链」的写法都是错的：

```go
users := sqlbuilder.Table("users")

sel := sqlbuilder.Select(users.C("id")).From(orders).Join(users)
// SELECT `users`.`id` FROM `orders` JOIN `users` AS `t1`
//          ^^^^^^^^^^ FROM 里这张表已经叫 t1 了，运行时报「未知列」
```

`Join` 跑完之后同一个 `users.C("id")` 才变成 `` `t1`.`id` ``——同一张表前后取到不同字符串，
所以重跑一遍偶然而已。显式起别名不受求值顺序影响，这是唯一稳的写法
（`TestJoinAliasIsFixedWhenCIsCalled` 钉住这两段行为）：

```go
orders := sqlbuilder.Table("orders").As("o")
users := sqlbuilder.Table("users").As("u")

sqlbuilder.Select(orders.C("id"), users.C("name")).
    From(orders).
    Join(users).On(orders.C("user_id"), users.C("id"))
// SELECT `o`.`id`, `u`.`name` FROM `orders` AS `o` JOIN `users` AS `u` ON `o`.`user_id` = `u`.`id`
```

## 写语句与 upsert

`Insert` / `Update` / `Delete` 是为 gorm 链式表达不了的语句准备的（批量 upsert 且新值要计算、
SET 里读本行、带关联子查询的 DELETE），**不是默认写路径**：仓储仍走 gorm。用的时候执行方式与
SELECT 一致——`Build` 之后 `db.Raw` / `db.Exec`，句柄来自 `DBConnect`，事务与日志照旧。

**upsert 必须显式带方言**：

```go
stmt, args, err := sqlbuilder.Build(
    sqlbuilder.Dialect(sqlbuilder.DialectMySQL).
        Insert("orders").
        Columns("id", "status", "amount").
        Values(1, "paid", 100).
        OnConflict(sqlbuilder.ConflictColumns("id"), sqlbuilder.ResolveWithNewValues()),
)
// INSERT INTO `orders` (`id`, `status`, `amount`) VALUES (?, ?, ?)
//   ON DUPLICATE KEY UPDATE `id` = VALUES(`id`), ...
```

写成 `sqlbuilder.Insert(...).OnConflict(...)`（不带方言）会得到**一条没有冲突子句的无效 SQL，
且不报错**——上游按方言分支渲染，没有 default 分支。`TestUpsertWithoutDialectIsInvalid` 钉住了
这个行为。

## 八条规则

1. **`Predicate` 有状态，不能并发复用。** 它内部是 `{Builder, depth, fns}`：`Query()` 往自己的
   缓冲区里写、写之前先 `Reset()`，`And`/`Or` 渲染时还会回写子谓词的 `depth`。同一个
   `*Predicate` 被两个 goroutine 同时 `Query()`，`-race` 会在 `mayWrap` 的 `preds[i].depth = …`
   处报 data race。跨请求缓存同样危险，而且**单线程也会错**。谓词在函数内构造、函数内消费，
   不存字段、不跨 goroutine。
2. **`Predicate` 自身不带运算符，不是可复用的模板。** 组合只发生在 `And`/`Or` 里（它们负责在
   操作数之间写 `AND`/`OR`）。在已有谓词上调 `p.In(...)` / `p.EQ(...)` 是往同一个输出流里追加
   片段，没有分隔符：`` `status` = ?`id` IN (?, ?) ``，参数还会累积
   （`TestSharedPredicateIsNotATemplate`）。要加条件就重新 `And(...)` 一个新的。
3. **不要把 slice 传给 `EQ`。** 构造器把它当一个参数，gorm 的 `Raw` 又把它当 `IN` 展开，
   两种解释会打架。列表条件用 `InSlice(col, vals)`（`[]string` 也摊不进 `In` 的 `...any`）。
4. **表名不经过 gorm 的 `NamingStrategy`。** `Table("orders")` 就是字面量。配了表前缀或
   复数化的项目必须从 `model.TableName()` / 列常量取，不要手写。
5. **方言要显式决定，SQLite 不能显式传**（见下表）。
6. **`Build` 出错时语句与参数都为空**，不要执行半成品。
7. **`*SelectTable` 有状态**：与语句一起构造，不缓存、不跨方言复用（见「方言」一节）；
   别名也会被 `Join` 自动分配并被 `C()` 固化（见「谓词挂到哪里」）。
8. **upsert 走 `Dialect(...).Insert(...)`**：不带方言时冲突子句不会渲染，也不报错。

## 方言

| 目标库 | 写法 | 说明 |
| --- | --- | --- |
| MySQL | 默认（不设置） | 默认用反引号，正确 |
| PostgreSQL | `sqlbuilder.Dialect(DialectPostgres)` | 反引号换成双引号，**占位符变成 `$1, $2`** |
| SQLite | **不要传 `dialect.SQLite`** | 列会渲染成 `` `"orders"."id"` ``（反引号套双引号），SQLite 把它当成名为 `"orders"."id"` 的列，报 `no such column`；该方言下 `ForUpdate` 还会记录错误，`Build` 直接失败 |

SQLite 只出现在测试里，用默认方言即可（反引号 SQLite 也接受）。这条不是洁癖：
两种失败方式都很难和「方言传错了」联系起来。

**占位符随方言变化，因此不要混用。** PostgreSQL 方言下构造器写 `$1, $2`，而 `Expr` /
`ExprP` / `Raw` 里写死的 `?` 会原样保留。两者混在一条语句里，gorm 会把 `?` 从 1 重新编号，
与已有的 `$1` 撞车，绑定错位且不报错。一条语句要么整体按 PostgreSQL 形态构造，要么整体不。

**表对象带状态。** `From` / `Join` 会把语句的方言写回 `*SelectTable`，所以同一个表值不能跨方言
复用：先用 PostgreSQL 方言跑一次，再拿同一个表构造 MySQL 语句，会渲染出
`` `"orders"."id"` ``（反引号套双引号）。表对象与使用它的语句一起构造，不要存字段。

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
