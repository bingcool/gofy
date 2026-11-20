# support/tenant

GORM 行级租户隔离插件。语义对齐 swoolefy 的 `TenantLineInterceptor`：自动拼接
`tenant_id`、按模型判定是否参与、缺租户失败关闭、可显式绕过；实现上用 GORM 的
结构化 clause，而不是改写 SQL 字符串。完整设计见
[docs/gorm-tenant-design.md](../../docs/gorm-tenant-design.md)。

## 快速开始

```go
gormDB, err := db.New(ctx, cfg, logger)   // support/db
_ = otel.InstrumentGORM(gormDB)           // 先装链路（它会替换 Statement.Context）
err = tenant.Instrument(gormDB, tenant.Options{}) // 后装租户
```

顺序要紧：otel 插件的 before 回调会替换 `Statement.Context`，租户插件必须读到替换后的
context，所以**租户插件在 otel 之后安装**。

模型只要声明租户列就会被自动隔离，业务代码无需手写 `Where`：

```go
type Order struct {
    ID       uint
    TenantID string `gorm:"column:tenant_id"`
}

// SELECT ... WHERE tenant_id = <ctx 里的租户>
db.WithContext(ctx).Find(&orders)

// INSERT 自动填 tenant_id
db.WithContext(ctx).Create(&Order{...})
```

租户 ID 默认取 `frameworkcontext.TenantID(ctx)`：本地 JWT 身份优先，其次可信上游的
`X-Tenant-Id` 透传头。与框架其余部分共用同一身份来源。

## 开关：装上才生效

本包没有 `enabled` 这种字段：`Instrument` 被调用就是开启，不调用就是**完全没装**——四个
回调一个都没注册，SQL 一条都不会被改写。部署侧的 `enabled` 表达的只是"要不要调用
`Instrument`"，不是插件里的一个分支。

部署侧有两层"关闭"，含义不同：

- **整段不配置**：这套部署完全不用租户隔离，插件不装、构造器也不加条件；
- **段在，但 `enabled: false`**：同样不装，只是把"有租户列、先不隔离"写明。

**段在而没写 `enabled` 是开启。** 写了 `tenant` 段就是在用租户，少写一个键不该把过滤
去掉——那是本包唯一会静默失效的方向：带租户列的表会不加过滤地返回所有租户的数据，而且
没有报错。

| 部署配置 | 模型有 `tenant_id` | GORM 仓储 | 构造器（`support/sqlmodel`） |
| --- | --- | --- | --- |
| 整段缺失 | 有 / 无 | 不改写 | `Disabled: true`，不加条件、不报错 |
| 段在，不写 `enabled` | 有 | 过滤 + 填充，缺租户失败关闭 | 同上（`WHERE` 或 `ErrTenantRequired`） |
| 段在，不写 `enabled` | 无 | 原样 | 原样 |
| `enabled: false` | 有 / 无 | 不改写 | `Disabled: true`，不加条件、不报错 |

两个"原样"原因不同：开着的插件是按 `LookUpField` 判定后主动跳过，关着是因为根本没有回调。

最后一行要和"安全兜底"区分开：关掉插件后，带租户列的表就是**全租户可见**，没有过滤也没有
报错。安全来自开启的时候——那时的失败关闭才是"缺租户即拒绝"。

**两条路径必须一起开关。** `support/sqlmodel` 走的是构造器（按模型取表名再拼 SQL），插件
装不装它并不知道，所以它有自己的 `Options.Disabled`。只关一处会得到"仓储读到所有租户、
构造器还在过滤"（或反过来），而配置文件里看不出这种半开状态。demo 里
`TenantConfig.SQLModelOptions()` 用同一段配置同时喂给两边。

## 配置

| `tenant.Options` 字段 | 默认 | 说明 |
|---|---|---|
| `Handler` | `ContextHandler{}` | 提供租户 ID 与列名；自定义来源时实现 `Handler` 或用 `FuncHandler` |
| `OverwriteExplicit` | 覆盖（`nil`） | `*bool`；`nil` / `true` 时 INSERT 强制用当前租户，忽略调用方已填的值；只有特权连接才写 `false` |
| `OnBypass` | `nil` | `Without(ctx)` 生效时回调，用于给特权绕过留痕 |
| `OnUnresolved` | `nil` | GORM 解析不出 schema 时回调，对应 swoolefy 的 `warnNotRewritten` |

`Handler` 三方法：`TenantID(ctx) string`、`Column() string`、`IgnoreTable(table) bool`。
实现**必须无状态**——插件单例被所有请求共享，租户值只能来自 `ctx`。

`Column()` 返回空串（或全空白）会在 `Instrument` / `Initialize` 阶段返回
`ErrTenantColumnRequired`，不会退化成"每张表都不带租户"。`FuncHandler` 对空列名会回退默认列，
要实测这条拒绝路径需自己实现 `Handler`。

`OverwriteExplicit` 的零值是"覆盖"：插件分不清管理员与请求 DTO 里带进来的 `tenant_id`，
默认信任调用方就是一条跨租户写入通路。INSERT 会**按行**设置租户列——批量里即使某一行显式
写了别的租户，默认也会被改成上下文租户；整批 `SetColumn` 在 map 切片上还会报 `invalid field`。
确实要跨租户写入用 `tenant.Without(ctx)`（整条跳过插件），而不是把这个开关关掉。

## 表是否参与隔离

看模型的 schema，不查 information_schema：

- `stmt.Schema.LookUpField(column) != nil` → 参与（自动加条件/填充列）。
- 返回 `nil` → 跳过，普通 GORM 行为。
- `stmt.Schema == nil`（如 `Table("x").Find(&[]map[string]any{})`）→ 交给
  `Handler.IgnoreTable` 兜底；默认 `false`，即**仍然失败关闭**，避免静默返回全表数据。

注意：`Table("orders").Find(&[]Order{})` 的 Dest 是模型切片，GORM 仍会解析出 Schema，
所以按模型字段判定，不走兜底。只有 map 类 Dest 才会让 Schema 为 nil。

## 失败关闭

目标语句需要隔离但 ctx 里没有租户 ID 时，`Instrument` 装的插件调用
`stmt.AddError(ErrTenantRequired)` 短路 GORM 的 SQL 构造。**不会发出不带过滤的 SQL**。
错误带表名，便于定位：

```
tenant: tenant id is required: table orders
```

需要「无租户也能跑」的入口（健康检查、迁移）必须显式用 `Without(ctx)`，而不是靠空租户
自动放行。

## 绕过与显式指定

```go
ctx = tenant.Without(ctx)              // 本次调用跳过租户改写（特权操作）
ctx = tenant.WithTenant(ctx, "t-42")   // 脚本/worker/测试直接指定租户
```

绕过标记在 `context.Context` 上，作用范围是一条语句链，不污染池化连接。
`Without` 优先于一切判定，包括无 Schema 的语句。
只应给运维脚本、数据迁移、跨租户报表使用；请求 handler 禁止调用。

这两个标记也以只读形式导出，供别的隔离路径（`support/sqlmodel`）判断同一件事：

```go
tenant.Bypassed(ctx)          // ctx 是否被 Without 标记
tenant.OverrideTenant(ctx)    // WithTenant 设置的显式租户，以及是否存在
```

插件内部也走这两个函数，因此"构造器认为该绕过、插件认为不该"这种分歧不会出现。

## 语句覆盖

| 语句 | 行为 |
|---|---|
| SELECT / UPDATE / DELETE | 追加 `clause.Eq`，并把调用方已有的 WHERE 包成一个分组后 AND 组合 |
| INSERT（struct / map / 切片 / 批量） | `SetColumn` 填充租户列；已有显式值时按 `OverwriteExplicit` 处理 |
| 已含租户条件的 WHERE | **只在「合取路径上的等值 + 绑定值等于生效租户」同时成立时才不追加**：顶层 AND 链里的 `clause.Eq`（`Value` 相等），或单条 `tenant_id = ?`（`Expr.Vars[0]` 相等）。`<>`、`NOT`、`OR`、`LIKE`、`IN`、右侧字面量，以及绑定了别的租户 ID 的条件，一律照常追加 |
| 不含租户列的模型 | 不改写 |
| 原生 `Exec` / `Raw` / `Row` | **不改写**——没有 schema 可判定；需手写租户条件 |

"已含租户条件"要同时看**形状**和**值**，缺一条都照常追加。

**形状**必须是合取的：`Where("tenant_id <> ?")`、`Where("tenant_id = ? OR is_public = 1")`、
`Where("NOT (tenant_id = ?)")` 都提到了租户列，却都不限制结果集。把它们当作"已过滤"
会让跨租户数据直接可见，且不报错、不留日志。

**值**必须是本次生效的租户。`Where("tenant_id = ?", otherTenant)` 的形状完全正确——
单独一条等值、在合取路径上——但绑定的是别人的 ID：

```go
// ctx 属于租户 a，参数来自请求
db.WithContext(ctxForA).Where("tenant_id = ?", req.TenantID).Find(&orders)
// 旧版：WHERE tenant_id = 'b'          ← 读到租户 b 的数据，零报错
// 现在：WHERE tenant_id = 'b' AND tenant_id = 'a'  ← 零行
```

只有能从 `clause.Eq.Value`（map / struct 写法）或 `Expr.Vars[0]`（手写 SQL）证明与生效
租户相等时才不追加。认不出的值——无法归一的类型、无效的 `sql.NullString`、列引用——一律
追加；追加只会多一个谓词，而放行是跨租户读取，两者代价不对称。比较在字符串形态上做，
所以 `"42"`、`42`、`int64(42)` 与命名 string 类型都被当作同一个租户。

要按别的租户查询（管理端），用 `tenant.WithTenant(ctx, id)`：它设定生效租户，因此
`WithTenant(ctx, "b")` 配 `Where("tenant_id = ?", "b")` 仍然只绑定一次、照常返回 b 的
数据。整条绕过则是 `tenant.Without`——两者都是显式表达意图，而不是写一个租户条件指望
插件让路。

## 边界

- **JOIN**：只给主表追加条件。JOIN 的表要隔离，在 `Joins` 字符串里手写或用 scope。
- **别名**：条件用列名而非 `alias.column`，避免猜测别名；单表无歧义。多表 JOIN 时若列名
  冲突，需要业务侧显式限定。
- **软删除**：GORM 自己的 `deleted_at` clause 与本条件用 AND 组合，互不干扰。
- 本方案是「共享表 + tenant_id 列」的行级隔离，不解决分库分表。
- 插件只持有不可变配置，可安全地被所有 goroutine 共享；租户值只在 ctx 上流动。

## 测试

`go test ./support/tenant -race` 覆盖 SELECT/UPDATE/DELETE/INSERT 四种语句、struct/map/
切片/批量插入、失败关闭、不含租户列的模型、`Without`/`WithTenant`、
「已有租户条件但绑定了别的租户」、去重的绑定计数、值归一化、`IgnoreTable`
兜底、自定义列名、与「替换 context 的前置插件」共存、重复安装被拒绝，以及 100
goroutine 并发不同租户不串扰。

demo 侧的装配接线测试在 `examples/demo-service/app/appio/db/tenant_test.go`：
验证配置开启后插件确实装到连接池，以及 `db.default` 跨 Reload 代复用时安装是幂等的。
