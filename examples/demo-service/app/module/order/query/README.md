# app/module/order/query

order 模块的**只读查询层**。每个模块可以有自己的 `query` 子包，只服务本模块的
读取需求：原生 SQL、跨表 JOIN、`EXISTS` / `NOT EXISTS`，以及为列表页、报表、
导出定制的读模型。

## 与 repository 的分工

| | `repository` | `query` |
| --- | --- | --- |
| 面向 | 单个聚合 | 本模块表的跨表读，可 JOIN 其他模块的表 |
| 操作 | 写 + 按主键/索引的简单读 | 只读 |
| 返回 | 本模块 `model` | 本包的 row 结构 |
| 事务 | 由 service 开启，仓库消费 | 与写操作共享同一个 ctx 事务 |
| 迁移 | 表结构归 `model` + `migrations/` | 不参与迁移 |

写路径永远回到对应模块的 repository；这里不提供任何 `Create` / `Update` /
`Delete`，避免同一张表出现两个写入口。

## 层级边界

`query` 属于 order 模块，只允许 import：

- 本模块 `model`（需要复用表结构时）；
- `app/appio`（解析连接）；
- `support` 与框架包；
- 标准库与第三方库（gorm）。

不允许 import 本模块的 `service` / `controller` / `innerapi` / `repository`，
也不允许 import 其他模块的任何包、HTTP 契约与 adapter。**其他模块的表只能在
SQL 里写表名**：表结构是数据库契约，跨模块的 Go 依赖才需要 innerapi。

## 样例

| 方法 | 演示的写法 |
| --- | --- |
| `ListOrderLines` | `orders ⋈ order_items ⋈ products` 三表内连接、行金额计算 |
| `ListUnsoldProducts` | `NOT EXISTS` 反半连接（从未被下单的产品） |
| `UserHasOrder` | `EXISTS`，命中一条即返回，状态由调用方传入 |
| `ListUserOrderStats` | 原生 SQL：`GROUP BY` + 聚合 + `HAVING` + 多列排序 |
| `GetUserOrderStat` | 单行聚合，未命中翻译成 `ErrNotFound` |

配套表 `order_items` 见 `migrations/0004_order_items.sql`（含演示数据）。

## 连接与事务

`Queries` 不持有连接，每次调用都从 ctx 解析本代连接：

```go
db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
return db, nil
```

- `appio.WithContext(ctx)` 取请求入口固定的一代快照，Reload 期间的旧请求继续用旧代；
- `DBConnect` 优先返回 ctx 中的事务。因此 service 里

  ```go
  gofydb.Transaction(ctx, rt.DB, func(ctx context.Context) error {
      // 写：repository
      // 读：queries.ListOrderLines(ctx, id) —— 看到同一事务内未提交的数据
  })
  ```

  读写落在同一个事务、同一条连接上。已由 `TestQueriesShareAmbientTransaction`
  覆盖（回滚后查询结果消失）。

## 写查询的约定

1. **只写 SELECT，参数一律 `?` 占位**；表名、列名只能是常量，禁止拼接用户输入。
2. **列别名与 row 结构的 `gorm:"column:..."` 一一对应**。读模型不是表结构，
   改表时编译器不会报错，测试会先失败，所以每个方法都要有真实 SQL 测试。
3. **`LIMIT` 走 `clampLimit`**：零值取默认 20，上限 200，避免一次拖走整张表。
4. **分页要两条 SQL**（`COUNT(*)` + 列表），并且都从同一个 ctx 连接发出，
   保证计数与列表看到同一份数据。
5. **优先让索引吃满谓词**：`order_items.order_id`、`order_items.product_id`、
   `orders.user_id` 已有索引；新增查询前先看 `EXPLAIN`，慢查询会进
   `component=gorm` 的 `slow sql` 日志。
6. **只读账号可选**：如果报表查询压力大，可给 query 层单独配一个只读实例
   （`db.<name>`），用 `NewDBIO(ctx, "report")` 解析，不影响写库。这里必须用 `NewDBIO`：
   报表要落在只读库上，不能被 ctx 里 `db.default` 的事务接管——`DBConnect` 只按 ctx
   判断有没有事务，不区分实例。

## 调用方式

`Queries` 无状态，可以按需构造：

```go
queries := orderquery.New()
lines, err := queries.ListOrderLines(ctx, orderID)
```

需要注入到 service 时，在装配根构造一次并随依赖传入即可；它不持有连接，
不需要跟随 Reload 重建，也不会跨代读到旧连接。

## 测试

```bash
cd examples/demo-service
go test -count=1 ./app/module/order/query/
```

测试用纯 Go SQLite 建三张表并发布为 `app/appio` 快照，跑真实 SQL；不 mock
gorm，也不依赖外部数据库。
