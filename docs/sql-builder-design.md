# SQL 构造器：引入 `entgo.io/ent/dialect/sql` 的方案

**状态**：方案，尚未编码
**日期**：2026-11-20
**关联**：`docs/module-layering-design.md`（query / repository 分工）、`support/db`（连接与事务）

---

## 一、结论

1. **引入 `entgo.io/ent/dialect/sql`，只用它的 SQL 构造能力。** 不用 ent 的 ORM、schema DSL、codegen、migration、`*ent.Client`。这是与 2026-11-15「数据访问只用 gorm」决策不冲突的一步：引进的是构造器，不是第二套运行时。
2. **定位：`query` 只读层的可选构造器。** 产出 `(sql string, args []any)`，交给 **gorm 的 `Raw`/`Where`** 执行。事务仍由 ctx 携带，连接仍从 `appio` 快照取，SQL 日志、脱敏、trace-id、慢 SQL 全部继续生效。
3. **不封装成新 DSL。** 新增 `support/sqlbuilder`，只做 re-export 加一个 `Build` 辅助，API 就是 ent 的 API，文档指向上游。

一句话：**只借它的拼装能力，不借它的执行栈。**

---

## 二、gorm 的真实能力边界

方案要站得住，先把 gorm 1.31.2 能做与不能做说清楚。以下逐条查过源码，不是凭印象：

| 能力 | gorm 的写法 | 结论 |
|---|---|---|
| 子查询 | `Where("EXISTS (?)", sub)` | ✅ 可用，括号由 gorm 补 |
| 子查询 JOIN | `Joins("JOIN (?) AS t ON ...", sub)` | ✅ 可用 |
| CTE | 期望是 `Clauses(clause.With{})` | ❌ **不可用**。`clause/with.go` 全文只有 `type With struct{}`，没有 `Build` 方法，是个空壳——gorm 至今没有实现 CTE，只能 `Raw("WITH ...")` 手写 |
| UNION | 期望是 `Clauses(clause.Union{})` | ❌ **不可用**。`clause` 包里没有 `Union` 类型，只有 `Raw("? UNION ?", a, b)` |
| 递归 CTE | — | ❌ 同上，只能 Raw |
| 行锁 | `Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})` | ✅ 可用 |
| Upsert | `Clauses(clause.OnConflict{...})` | ✅ 可用，方言差异由 Dialector 处理 |
| 窗口函数 | `Select("ROW_NUMBER() OVER (...)")` | ⚠️ 只能写字符串，且表达式无法复用 |
| 谓词树 | `db.Where(db.Where(a).Or(b))` | ⚠️ 能表达，但正确性靠阅读 |

所以缺口分两类：

- **真的做不到**：CTE、递归 CTE、UNION、EXCEPT、INTERSECT。这些只能整条 `Raw` 手写，参数与 `?` 靠人肉对齐。
- **做得到但成本高**：谓词的组合、窗口表达式复用、生成 SQL 的单元测试。

这个区分很重要：它决定了引入 builder 之后**哪些代码值得改、哪些不值得**。第三节分别说这两类的代价。

---

## 三、两类缺口的具体代价

### 3.1 第一类：CTE / UNION 只能整条手写，参数顺序靠人肉对齐

gorm 没有 `clause.With` 的实现，也没有 `Union`，所以一条含 CTE 的语句只能这样写：

```go
db.Raw(`
WITH paid AS (SELECT id, user_id FROM orders WHERE status = ? AND created_at >= ?)
SELECT user_id, COUNT(*) FROM paid
WHERE user_id IN (SELECT id FROM users WHERE level = ?)
GROUP BY user_id HAVING COUNT(*) > ?
`, "paid", from, 3, 2).Scan(&rows)
```

这不是"不够优雅"，是三个具体风险：

1. **改一个条件就要重数一遍**。在上面加一个 `AND amount > ?`，得把后面三个参数整体后移。
2. **错位的报错位置与真正原因无关**。MySQL 报 `Incorrect arguments to mysqld_stmt_execute`，Postgres 报 `bind message supplies N parameters`，都不告诉你哪个条件少了参数。
3. **无法按分支拼装**。`if from != nil` 就要写成两套完整字符串，或者用 `strings.Builder` 手工拼 `WHERE`，拼到第三个条件时通常会出现 `WHERE ... AND` 尾巴或漏 `AND`。

用 builder 之后，同一段是：

```go
paid := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).
	From(orders).
	Where(sqlbuilder.EQ(orders.C("status"), "paid")).
	Where(sqlbuilder.GTE(orders.C("created_at"), from))

with := sqlbuilder.With("paid_orders").As(paid)
sel := sqlbuilder.Select(with.C("user_id"), sqlbuilder.Count("*")).
	From(with).GroupBy(with.C("user_id")).
	Having(sqlbuilder.GT(sqlbuilder.Count("*"), minCount)).
	Prefix(with)
```

参数顺序由 builder 维护，条件可以按 `if` 追加。

### 3.2 第二类：谓词树无法用数据结构表达

gorm 的动态条件是 `if` 链，一旦出现 `A AND (B OR C)` 这种结构，就必须靠嵌套 `db.Where(db.Where(b).Or(c))` 来保证括号：

```go
q := db.Where("status = ?", "paid")
q = q.Where(db.Where("amount > ?", 100).Or("refunded_at IS NULL"))
```

深一层就要停下来确认括号对不对，而正确性靠的是阅读，不是类型。ent 的 `Predicate` 把这件事变成数据结构：

```go
p := entsql.And(
    entsql.EQ("status", "paid"),
    entsql.Or(entsql.GT("amount", 100), entsql.IsNull("refunded_at")),
) // → `status` = ? AND (`amount` > ? OR `refunded_at` IS NULL)
```

### 3.3 生成的 SQL 不可断言

`gorm.Session{DryRun: true}` 能拿到 SQL，但要构造一次 `*gorm.DB`（需要 dialector），且组合出来的语句形态随 gorm 版本变化。builder 的 `Query()` 是纯函数：`go test` 里直接比对字符串和 args，不需要数据库、不需要 gorm。

这三条要分开算：3.1 是**能力缺失**（gorm 没有 CTE/UNION 的结构化构造，只能裸写），3.2 和 3.3 是**成本问题**。方案的收益也分两档——CTE/UNION 是"从不能到能"，谓词树和可测性是"从容易错到不容易错"。后者的价值只有在查询数量足够多时才成立，这也是第十四节判断标准的由来。

---

## 四、选型对比

| 方案 | 依赖 | CTE / UNION / 窗口 | 谓词树 | 与 gorm 共存 | 判断 |
|---|---|---|---|---|---|
| 继续裸 SQL | 0 | 手工拼 | 无 | — | 现状，够用但易错 |
| `Masterminds/squirrel` | 小 | UNION 有，CTE 无类型支持 | Placeholder 拼接 | 可以 | 否，能力不足 |
| `doug-martin/goqu` | 中 | 支持 | 有 | 可以 | 能力强但 DSL 重，学习成本高于收益 |
| **`entgo.io/ent/dialect/sql`** | ent 模块，实际只编译 2 个包 | 全部支持 | `Predicate` | 输出 `?` + `[]any`，天然兼容 | **选** |
| 自写 helper | 0 | 只解决一部分 | 弱 | 可以 | 备选，长期会重写成半个 builder |

选 ent 的决定性理由不是"功能最多"，而是**它的输出形态正好是 gorm 需要的**：`Query() (string, []any)`，占位符是 `?`，args 是平铺的 `[]any`。不需要任何适配层。

---

## 五、依赖成本（实测）

从当前干净状态临时 import `entgo.io/ent/dialect/sql`，跑 `go mod tidy`，记录变化后回滚。结果：

**go.mod 新增 2 行**

```
+	entgo.io/ent v0.14.6
+	go.opencensus.io v0.24.0 // indirect
```

`go.opencensus.io` 是 ent 的 module graph 带进来的版本提升，不是运行期依赖。

**go.sum 新增 18 行**，涉及 12 个模块（多数是 `/go.mod` 哈希，不是源码）：

```
entgo.io/ent  github.com/DATA-DOG/go-sqlmock  github.com/golang/groupcache
github.com/golang/protobuf  github.com/google/go-cmp  github.com/mattn/go-sqlite3
github.com/stretchr/testify  go.opencensus.io  golang.org/x/net
golang.org/x/sys  google.golang.org/grpc  google.golang.org/protobuf
```

**实际编译进依赖图的 ent 包只有两个**

```
$ go list -deps entgo.io/ent/dialect/sql | grep entgo
entgo.io/ent/dialect
entgo.io/ent/dialect/sql
```

关键结论：

- **没有 `ariga.io/atlas`**。那是 `entc` 和 migration 的依赖，`dialect/sql` 不需要。
- **没有 gremlin、没有 codegen、没有 `*ent.Client`**。
- `dialect` 包 208 行，唯一外部依赖是 `github.com/google/uuid`，gofy 已经在用。
- 许可证 Apache-2.0，与现有依赖一致。

代价主要是 **go.sum 的 18 行噪音** 和一次 `go mod tidy` 带来的 `go.opencensus.io` 版本提升。这两项需要在那次提交里单独说明，避免被误认为引入了一个大依赖。

---

## 六、四条集成路线

| 路线 | 做法 | 判断 |
|---|---|---|
| **A. 替换 gorm** | 用 ent 做 ORM，全量改写 repository | **否**。`support/claim`、`support/consumer` 幂等台账、`support/otel`、`logger.GormLogger` 都深度绑定 gorm；2026-11-15 已论证过理由 |
| **B. 只借构造器** | builder 生成 `(sql, args)`，交给 `db.Raw()` / `db.Where()` 执行 | **选** |
| **C. 用 ent 自己的 Driver** | `entsql.Open(driver, dsn)` 建第二套连接 | **否**。会出现第二套连接池、第二套事务、第二套日志，且绕过 appio 的代际管理 |
| **D. 从 gorm 取 `*sql.DB` 执行** | builder 给出 `(sql, args)`，`db.DB().QueryContext()` 执行 | **否**。`db.DB()` 在事务上返回的是连接池而非事务，同时丢掉日志与代际，见 9.6 |

路线 B 的具体收益，逐项对齐现有机制：

| 机制 | 走 B 之后 |
|---|---|
| 事务 | `db.Raw()` 作用在 `gofydb.WithContext(ctx, db)` 上，ctx 里有 tx 就用 tx，跨模块同事务照旧 |
| 连接代 | 句柄仍从 `appio.WithContext(ctx).DBIO(...)` 取，Reload 后不会跨代 |
| SQL 日志 / 脱敏 / trace-id / 慢 SQL | 最终由 gorm 发出，`logger.GormLogger` 全链路生效，配置项一个都不用改 |
| 多租户 | ⚠️ 见第十一节，Raw 不经过插件回调 |
| 脚本 / worker / controller | 三种入口平权，都能用；不新增 import 边界 |

**这是本方案最重要的部分**：不改变任何运行时机制，只改变"SQL 字符串怎么被拼出来"。

---

## 七、放在哪

新增 `support/sqlbuilder`，与 `support/db` 平级。

```
support/
├── db/           # 连接、事务、registry
├── sqlbuilder/   # SQL 构造器（本方案）
└── ...
```

理由：

- `support/db` 的职责是「连哪个库、什么时候连、事务怎么传」；构造 SQL 是另一件事，塞进去会让那个包的文档继续变长。
- 与 `support/claim`、`support/consumer`、`support/kafka` 的组织方式一致，都是平级的能力包。
- `query` 层已经允许 import `support`，不需要改守卫脚本。

**不要**放进 `app/appio`：那是应用装配层，构造器和连接代际无关。

---

## 八、API 形态

不造新 DSL。`support/sqlbuilder` 的内容是 re-export 加一个 `Build`：

```go
// Package sqlbuilder 只做两件事：把 ent 的 SQL 构造器引到一处，
// 以及提供带 error 的 Build。它不是一套新的 DSL。
package sqlbuilder

import (
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
)

type (
	Selector    = entsql.Selector
	SelectTable = entsql.SelectTable
	Predicate   = entsql.Predicate
	Builder     = entsql.Builder
	Querier     = entsql.Querier
	LockOption  = entsql.LockOption
)

// 构造
var (
	Select        = entsql.Select
	SelectExpr    = entsql.SelectExpr
	Table         = entsql.Table
	Expr          = entsql.Expr
	Count         = entsql.Count
	With          = entsql.With
	WithRecursive = entsql.WithRecursive
	Dialect       = entsql.Dialect
)

// 谓词
var (
	P         = entsql.P
	And       = entsql.And
	Or        = entsql.Or
	Not       = entsql.Not
	EQ        = entsql.EQ
	NEQ       = entsql.NEQ
	GT        = entsql.GT
	GTE       = entsql.GTE
	LT        = entsql.LT
	LTE       = entsql.LTE
	In        = entsql.In
	NotIn     = entsql.NotIn
	IsNull    = entsql.IsNull
	NotNull   = entsql.NotNull
	Like      = entsql.Like
	Exists    = entsql.Exists
	NotExists = entsql.NotExists
	ColumnsEQ = entsql.ColumnsEQ
	ColumnsNEQ = entsql.ColumnsNEQ
)

// 行锁
var (
	WithLockAction = entsql.WithLockAction
	WithLockTables = entsql.WithLockTables
	SkipLocked     = entsql.SkipLocked
	NoWait         = entsql.NoWait
)

// Build 返回 SQL 与参数，并把构造期的错误显式带出来。
// ent 的 Query() 只返回 (string, []any)，错误记在内部，容易被静默丢弃。
func Build(q Querier) (string, []any, error) {
	stmt, args := q.Query()
	if e, ok := q.(interface{ Err() error }); ok {
		if err := e.Err(); err != nil {
			return "", nil, fmt.Errorf("sqlbuilder: %w", err)
		}
	}
	return stmt, args, nil
}
```

这样做的取舍：

- **好处**：调用方只 import `support/sqlbuilder`，未来若换构造器（goqu 或自研），只需要改一个文件；`entgo.io/ent` 的名字不会散落到业务代码里。
- **代价**：需要维护这份 var 清单；上游新增 API 不会自动出现。清单只覆盖实际用到的部分，缺了就在用的时候加——不追求完整镜像。

---

## 九、用法示例（均为实测输出）

### 9.1 动态谓词，交给 gorm 的 `Where`

```go
func (q *Queries) listFiltered(ctx context.Context, f Filter) ([]Row, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}
	orders := sqlbuilder.Table("orders")

	preds := []*sqlbuilder.Predicate{sqlbuilder.EQ(orders.C("user_id"), f.UserID)}
	if f.Status != "" {
		preds = append(preds, sqlbuilder.EQ(orders.C("status"), f.Status))
	}
	if f.MinAmount > 0 {
		preds = append(preds, sqlbuilder.GT(orders.C("amount"), f.MinAmount))
	}
	if f.IncludePublic {
		preds = append(preds, sqlbuilder.Or(
			sqlbuilder.NotNull(orders.C("owner_id")),
			sqlbuilder.EQ(orders.C("is_public"), 1),
		))
	}

	where, args, err := sqlbuilder.Build(sqlbuilder.And(preds...))
	if err != nil {
		return nil, err
	}
	var rows []Row
	// 片段交给 gorm，事务、日志、代际全部照旧
	if err := db.WithContext(ctx).Model(&ordermodel.Order{}).Where(where, args...).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
```

生成：`` `orders`.`user_id` = ? AND `orders`.`status` = ? AND (`orders`.`owner_id` IS NOT NULL OR `orders`.`is_public` = ?) ``

### 9.2 CTE

```go
orders := sqlbuilder.Table("orders")

paid := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).
	From(orders).
	Where(sqlbuilder.EQ(orders.C("status"), "paid"))

with := sqlbuilder.With("paid_orders").As(paid)

stmt, args, err := sqlbuilder.Build(
	sqlbuilder.Select(with.C("user_id"), sqlbuilder.Count("*")).
		From(with).
		GroupBy(with.C("user_id")).
		Having(sqlbuilder.GT(sqlbuilder.Count("*"), 2)).
		Prefix(with),
)
```

生成：

```sql
WITH `paid_orders` AS (
  SELECT `orders`.`id`, `orders`.`user_id` FROM `orders` WHERE `orders`.`status` = ?
)
SELECT `paid_orders`.`user_id`, COUNT(*) FROM `paid_orders`
GROUP BY `paid_orders`.`user_id` HAVING COUNT(*) > ?
```

args：`[]any{"paid", 2}` —— 顺序由 builder 维护，调用方不用数。

### 9.3 UNION

```go
stmt, args, _ := sqlbuilder.Build(
	sqlbuilder.Select(orders.C("id")).From(orders).
		Where(sqlbuilder.EQ(orders.C("status"), "paid")).
		Union(sqlbuilder.Select(items.C("order_id")).From(items)),
)
```

```sql
SELECT `orders`.`id` FROM `orders` WHERE `orders`.`status` = ?
UNION SELECT `order_items`.`order_id` FROM `order_items`
```

### 9.4 窗口函数

窗口函数没有专用 API，走 `AppendSelectExprAs`：

```go
rank := sqlbuilder.Expr("ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC)")

stmt, _, _ := sqlbuilder.Build(
	sqlbuilder.Select(orders.C("id")).
		AppendSelectExprAs(rank, "rn").
		From(orders),
)
```

```sql
SELECT `orders`.`id`, (ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC)) AS `rn` FROM `orders`
```

注意不要同时用 `SelectExpr(rank)` 和 `AppendSelectExprAs(rank, "rn")`，会输出两列。

### 9.5 整条语句交给 `Raw`

```go
stmt, args, err := sqlbuilder.Build(sel)
if err != nil {
	return nil, err
}
if err := db.WithContext(ctx).Raw(stmt, args...).Scan(&rows).Error; err != nil {
	return nil, fmt.Errorf("query order lines: %w", err)
}
```

### 9.6 为什么不从 gorm 取 `*sql.DB` 执行

既然 builder 已经给出 `(query, args)`，一个自然的想法是：从 gorm 拿到底层 `*sql.DB`，直接 `QueryContext` 更"原生"：

```go
pool, _ := db.DB()
rows, _ := pool.QueryContext(ctx, query, args...)
```

**不要这样做。** `db.DB()` 返回的不是当前事务：即便 `db` 实际是一个 `*sql.Tx` 包装，gorm 也会用 unsafe 反射把里面的 `db` 字段抠出来，返回连接池本身（`gorm.go:431-433`）：

```go
if tx, ok := connPool.(*sql.Tx); ok && tx != nil {
    return (*sql.DB)(reflect.ValueOf(tx).Elem().FieldByName("db").UnsafePointer()), nil
}
```

实测 `tx.DB() == db.DB()` 为 `true`——两者是同一个池指针，事务状态完全丢失。

四个后果，按严重程度排：

| 后果 | 说明 |
|---|---|
| **丢事务** | 事务内的查询跑到另一条连接上：读不到未提交数据，失败也不会随事务回滚。单测（无事务）看不出来，生产上才暴露 |
| **丢日志** | `logger.GormLogger` 的 Trace 只挂在 gorm 的 callback 上。SQL 日志、脱敏、`X-Trace-Id`、慢 SQL 统计全部不记录 |
| **丢代际** | `db.DB()` 返回当前代的 `*sql.DB`；Reload 调用 `gofydb.Close` 之后指针失效，报 `sql: database is closed` |
| **要自己扫** | `*sql.Rows` → struct 需要反射或 sqlx，gorm 的 `Scan` 已经做了这件事 |

如果目的是**流式读大结果集**，不需要绕开 gorm：

```go
rows, err := db.WithContext(ctx).Raw(stmt, args...).Rows() // *sql.Rows，逐行 Scan
```

它同样返回 `*sql.Rows`、同样不把结果全部载入内存，但事务、日志、代际都还在。

### 9.7 行锁

```go
stmt, args, _ := sqlbuilder.Build(
	sqlbuilder.Select(orders.C("id")).From(orders).
		Where(sqlbuilder.EQ(orders.C("status"), "queued")).
		ForUpdate(sqlbuilder.WithLockAction(sqlbuilder.SkipLocked)),
)
// SELECT `orders`.`id` FROM `orders` WHERE `orders`.`status` = ? FOR UPDATE SKIP LOCKED
```

注意：这条语句必须在显式事务里执行，且 `db.Raw` 要走 tx 句柄，否则锁立刻释放。`support/claim` 已有更完整的认领语义，业务不应自己写这条。

### 9.8 连接从哪来：`appio` 取连接要两步

`appio.WithContext(ctx).DBIO(ctx, name)` 只做一件事：从当前代快照取连接，**不解析事务**。事务在 ctx 里，要用 `gofydb.WithContext(ctx, db)` 提出来，两步都不能省：

```go
raw, err := appio.WithContext(ctx).DBIO(ctx, appdb.DefaultInstance) // ① 当代连接
if err != nil {
	return nil, err
}
db := gofydb.WithContext(ctx, raw) // ② ctx 里有 tx 就用 tx，否则用 raw

query, args, err := sqlbuilder.Build(sel)
if err != nil {
	return nil, err
}
if err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
	return nil, err
}
```

三个容易漏的点：

1. **`DBIO` 需要实例名**（`appdb.DefaultInstance` 或 `"order"`），不是无参调用。
2. **`gofydb.WithContext` 只选连接，不绑定 ctx**。事务分支拿到的 tx 自带 ctx（`Transaction` 用 `conn.WithContext(ctx)` 创建），但 fallback 分支（registry 里的当代连接）没有，所以执行前统一补 `.WithContext(ctx)`；漏掉就没有超时、不能取消。
3. **直接 `DBIO(...).Raw(...)` 会绕过事务**。它取到的是当代连接池上的句柄，在事务内调用时不会自动切到 tx——这与 9.6 的 `db.DB()` 是同一类错误，只是更隐蔽，因为代码看起来完全正常。

`repository` 与 `query` 的 `conn(ctx)` 已经把这两步封在一起（见 `order_repository.go` 的 `conn`），所以业务代码只需要：

```go
db, err := q.conn(ctx)
```

**推荐走 `conn(ctx)`**，不要在每个查询里重复取连接。理由不是省代码，而是"漏一步就在事务外执行"这种错误没法靠读代码发现。

### 9.9 需要开启事务时用 `gofydb.Transaction`

9.8 的两步解决的是「有事务就加入」，开启事务另有专门函数：

| 场景 | 写法 |
|---|---|
| 已有事务就加入，没有就直接执行（大多数读写） | `db := gofydb.WithContext(ctx, raw)` |
| 由当前调用开启事务，未提交前不让别人看见 | `gofydb.Transaction(ctx, raw, fn)` |

标准形态（`order_service.go:72-96`）：

```go
db, err := appio.WithContext(ctx).DBIO(ctx, appdb.DefaultInstance)
if err != nil {
	return nil, fmt.Errorf("resolve default database: %w", err)
}
err = gofydb.Transaction(ctx, db, func(ctx context.Context) error {
	// 这里的 ctx 已经是携带事务的 ctx
	for _, it := range items {
		if err := s.productAPI.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
			return err
		}
	}
	_, err := s.orderRepository.Create(ctx, &ordermodel.Order{...})
	return err
})
```

三个设计点，决定了它能不能用好：

1. **事务通过 ctx 向下传**。product 模块的 repository 与 order 的 repository 都用 `gofydb.WithContext(ctx, ...)` 取连接，于是跨模块的两次写落在同一个事务里，同提交同回滚——调用方不需要把 `*gorm.DB` 当参数一路传下去。
2. **嵌套调用复用外层，不建 savepoint**。内层 `Transaction` 看到 ctx 里已有事务就直接执行 `fn`，内层返回的错误冒泡到最外层，由最外层统一回滚。
3. **`conn` 参数直接传 `DBIO` 的返回值**，不要先经过 `gofydb.WithContext`。`Transaction` 内部第一件事就是检查 ctx：有事务则复用（`conn` 完全不参与），没有才在 `conn` 上开启。提前解析反而会掩盖"我以为在开事务，其实在复用别人的"这种情况。

sqlbuilder 与这三条没有冲突：事务内用 builder，只要 `db` 来自 `conn(ctx)` 或 `gofydb.WithContext`，`Raw` 就在事务里执行，`ForUpdate` 的锁也才真正持有到提交。

---

## 十、五个必须写进包注释的约束

前三条是实测发现的，不是理论风险。

**1. `Predicate` 有状态，不能并发复用。** `Query()` 会 `Reset()` 自己再重跑回调。同一个 `*Predicate` 被两个 goroutine 同时 `Query()` 是 data race；跨请求缓存谓词同样危险。规则：**谓词在函数内构造、函数内消费，不存字段、不跨 goroutine**。

**2. 不要把 slice 塞进 `EQ`。** builder 会把它当一个参数，gorm 的 `Raw` 会把它当 `IN` 展开——两次解释不一致。`In` 要用 `sqlbuilder.In(col, vals...)` 显式构造。

**3. 表名不经过 gorm 的 `NamingStrategy`。** `sqlbuilder.Table("orders")` 就是字面量 `orders`。如果项目配了表前缀或复数化，必须从 `model.TableName()` 取，不要手写。建议在 `query` 包里统一用 `xxxmodel.TableName()` 或列常量。

**4. 方言要显式决定，且 SQLite 不能显式传。**

| 目标库 | 建议 | 原因 |
|---|---|---|
| MySQL | 默认（不设置）或 `Dialect(dialect.MySQL)` | 默认用反引号，正确 |
| PostgreSQL | `Dialect(dialect.Postgres)` | `Quote` 会把反引号替换成双引号 |
| SQLite | **不要传 `dialect.SQLite`** | 两个问题，见下 |

SQLite 的两个问题都是实测出来的：

1. 生成 `` `"orders"."id"` ``——反引号套双引号。SQLite 反引号是合法的标识符引号，于是它去找一个名为 `"orders"."id"` 的列，报 `no such column: "orders"."id"`。列明明存在，报错却说没有，这类信息很难和"方言传错了"联系起来。
2. `Selector.For` / `ForUpdate` 在 SQLite 方言下直接 `AddError("sql: SELECT .. FOR UPDATE/SHARE not supported in SQLite")`，于是 `Build` 返回错误。

SQLite 只出现在测试里，用默认方言即可（SQLite 也接受反引号）。这一条必须写进包注释：否则下一个人照着 `dialect.SQLite` 写，测试要么静默空结果，要么报一个看起来与 SQL 无关的错误。

**5. `Query()` 不返回 error。** 构造期的错误记在 `Builder.errs` 里，必须用 `Build()` 取。直接调 `sel.Query()` 会静默吞掉错误。

---

## 十一、租户隔离：一个必须正视的削弱

`support/tenant` 的插件只挂 `Query`/`Update`/`Delete`/`Create` 四个 processor，`Raw` 刻意不挂（`plugin.go:73`，注释写明"Raw/Row 刻意不挂，因为它们没有可判定的结构化 schema"）。

这意味着：**走 `db.Raw(stmt, args...)` 执行的语句，不会自动补 `tenant_id` 条件。**

两条路径的风险等级不同，要分开记：

| 路径 | 租户插件是否介入 |
|---|---|
| `Where(fragment, args...)` | **部分介入**。片段变成 `clause.Expr`，插件的 `constrainsColumn` 会识别其中单独一条 `tenant_id = ?`（`plugin.go:343` 注释："手写 SQL 同样处理"）。但只认等值——`tenant_id IN (...)`、`tenant_id <> ?`、`OR` 连接的一律不算作已过滤 |
| `Raw(stmt, args...)` | **完全不介入**。整条语句直接写进 `Statement.SQL`，四个 processor 都不触发 |

所以在第十一节的对策之外，还有一条使用建议：**能用 `Where(fragment)` 表达的条件，就不要拼进整条 `Raw`**。谓词树正好适合这条路径——`Build(pred)` 的结果交给 `Where`，既拿到 builder 的组合能力，又保留了插件的一次兜底检查。

`app/module/*/query` 现在的手写 SQL 已经是这个状态，所以不是新问题；但引入 builder 会让这种写法变多。三条对策，方案建议同时做前两条：

1. **包文档强制约定**：`support/sqlbuilder` 的包注释写死——「若目标表含租户列，谓词里必须显式包含 `tenant_id`，Raw 不经过 tenant 插件」。
2. **提供 helper**：

```go
// TenantScope 返回当前请求的租户等值谓词；ctx 里没有租户时返回 (nil, false)，
// 由调用方决定是报错还是跳过。列名固定为 tenant_id，与 support/tenant 的默认列一致。
func TenantScope(ctx context.Context, table *entsql.SelectTable) (*Predicate, bool) {
	tid := frameworkcontext.TenantID(ctx)
	if tid == "" {
		return nil, false
	}
	return EQ(table.C("tenant_id"), tid), true
}
```

3. （可选）在 `query` 层的测试里加一条断言：对含租户列的表，生成的 SQL 必须包含 `tenant_id`。这需要一张表名到租户列的映射，成本较高，先不做。

如果项目实际没有启用 `support/tenant`，这一节可以降级为注释，但代码里的约定要留着——插件是可选能力，随时可能被打开。

---

## 十二、明确不做的事

| 不做 | 原因 |
|---|---|
| ent 的 schema DSL / `ent generate` | 会引入 codegen 和 atlas，与 gorm 的 AutoMigrate 形成两套模型定义 |
| `*ent.Client` / `entsql.Open` | 第二套连接与事务，绕过 appio 代际 |
| 用 builder 做 INSERT / UPDATE 的默认路径 | 写路径继续走 gorm，`clause.OnConflict` 已经够用；只在批量 upsert 场景评估 |
| 封装完整 DSL（`sqlbuilder.Q().Where(...).OrderBy(...)`） | 增加一层要维护的 API，收益只是换个名字 |
| 替换 `support/claim` 的裸 SQL | claim 的 SQL 依赖方言细节（`FOR UPDATE SKIP LOCKED` / `NOW(3)`），已经稳定，改动无收益 |

---

## 十三、落地步骤（批准后执行）

| 步骤 | 内容 | 验证 |
|---|---|---|
| 1 | 新增 `support/sqlbuilder/sqlbuilder.go`（re-export + `Build`）与包注释 | `go build ./support/sqlbuilder` |
| 2 | `go get entgo.io/ent@v0.14.6` + `go mod tidy`，确认 go.mod/go.sum 变化与第五节一致 | `git diff go.mod go.sum` 与本文档数字比对 |
| 3 | 新增 `support/sqlbuilder/README.md`：三条使用规则、方言表、租户约定 | — |
| 4 | 在 `app/module/order/query` 增加一个 builder 版本的示例（建议 `ListUnsoldProducts`，与现有 Raw 版本并列，测试同时跑两条，断言结果一致） | `go test ./app/module/order/query/` |
| 5 | 更新 `docs/module-layering-design.md`：query 层可选构造器 | — |
| 6 | 检查 `shell/check-imports.sh`：controller 不引入 `sqlbuilder`（构造 SQL 不属于入口层） | `make check` |

第 4 步的"两条实现跑同一组断言"是这个方案的关键验证：如果 builder 版本与 Raw 版本在 SQLite 上返回不同结果，就说明方言或参数展开有问题，宁可放弃方案也不能带着不确定性上线。

---

## 十四、什么情况下不值得做

诚实地说，这个方案不是必选项。以下任一情况成立时，建议维持现状：

1. **query 层一年新增不到 5 条复杂查询。** 手写 SQL 加一层 `if` 拼条件更直接，引入一个 2000 行的 builder API 只为了每年用几次，不划算。
2. **团队里没人愿意读 ent 的 builder 文档。** 这套 API 的能力很强，但 `With`/`Prefix`/`AppendSelectExprAs` 这些用法不看文档写不出来。写不对的时候，它比裸 SQL 更难排查——因为 SQL 不是你写的。
3. **主要目标只是"少写点字符串"。** 那用 `ordermodel` 的列常量加 `strings.Builder` 就够了，不需要新依赖。
4. **近期要支持多数据库（MySQL + PostgreSQL）并且查询里有大量方言差异。** builder 只解决引号和少量语法，`FILTER (WHERE ...)`、`NULLS LAST`、`ILIKE` 这些仍然要自己分支；这种情况下收益小于预期。

反过来说，如果出现下面这些信号，就值得做：

- query 层开始出现 3 个以上 CTE、5 个以上子查询的报表查询；
- 有人因为 `?` 与 args 对不齐，在生产上踩过一次参数错位；
- 需要把查询构造逻辑抽出来做单元测试，而不想为每条 SQL 起一个数据库。

---

## 附：与 2026-11-15「不引入 ent」决策的关系

那次决策拒绝的是 `*ent.Client`：它带来的是一套新的模型定义、连接管理、事务桥接和 codegen，会与 gorm 争夺同一块地盘。本次引入的 `dialect/sql` 是一个无状态的 SQL 字符串构造器，不持有连接、不定义模型、不参与迁移，退出成本是删掉 `support/sqlbuilder` 一个目录。

两者不矛盾，但要把这条边界写进 `support/sqlbuilder` 的包注释，否则下一个人会以为"既然引了 ent，不如把 repository 也换掉"。
