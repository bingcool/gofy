# support/sqlmodel

模型 → 构造器语句。表名取自 `TableName()`，模型声明了租户列时自动施加租户条件。

```go
sel, err := sqlmodel.Select(ctx, ordermodel.Order{}, "id", "user_id")
if err != nil {
    return nil, err // 表带租户列而 ctx 里没有租户 → tenant.ErrTenantRequired
}

orders := sel.Table() // *sqlbuilder.SelectTable，用于 C()/Join
sel = sel.Where(sqlbuilder.EQ(orders.C("status"), "paid"))

stmt, args, err := sqlbuilder.Build(sel)
```

## 为什么单独一个包

`support/sqlbuilder` 只拼 SQL、不认识模型，而且**必须与 gorm 无关**——`shell/check-imports.sh`
有一条守卫盯着它的依赖图。本包是另一半：它可以依赖 gorm，用 `TableName()` 取表名，用
`schema.LookUpField` 判断表上有没有租户列。后一个判断和 `support/tenant` 改写语句前用的是
**同一个调用**，两条路径因此不会对「这张表是否带租户」给出不同答案。

## 三条规则

1. **失败关闭。** 模型带租户列、ctx 里没有租户时返回 `tenant.ErrTenantRequired`，不会退化成
   不带过滤的查询。要跨租户读（运维脚本、迁移）就显式 `tenant.Without(ctx)`，与插件一致；
   整个部署关闭隔离用 `Options.Disabled`，它是 `tenant.enabled` 的对应项——两条路径必须一起
   开关，否则仓储读到所有租户、构造器还在过滤，配置里看不出这件事。
2. **配置必须与连接一致。** `Options.Handler` 决定租户列名与取值来源，`Options.Namer` 决定
   `TenantID` 映射到哪一列；两者都要传构建 `*gorm.DB` 时用的那一份。传错不会报错，只会让
   两条路径盯不同的列——那正是跨租户读的样子。改过列名或命名策略的项目用 `New` 构造自己的
   `Resolver`，不要用包级 `Select` 的默认值。
3. **`Select` 覆盖不了的语句用 `Table`，谓词自己挂。** JOIN、CTE、手写 UPDATE 都要按表挂：
   一条 join 里每张带租户列的表都需要自己的条件。`Table` 返回的谓词为 `nil` 表示这张表不带
   租户列（或 ctx 被绕过），不需要挂。

## 与连接配置的对应关系

| 连接侧（`tenant` 段） | 构造器侧 |
| --- | --- |
| 整段缺失（不用租户） | `Options.Disabled: true` |
| 段在，不写 `enabled`（开启） | `Options.Disabled: false`（零值） |
| `enabled: false` | `Options.Disabled: true` |
| `column: org_id` | `Options.Handler: tenant.FuncHandler{ColumnName: "org_id", ...}` |
| `*gorm.Config.NamingStrategy` | `Options.Namer` |
| 方言（db 驱动） | `Options.Dialect` |

构造器侧没有"段"的概念，只有一个开关：`Disabled: false` 是开启，也是它的零值。连接侧
"段在而没写 `enabled`"同样落在开启上。这些行不配对的后果最重：`enabled: false` 而构造器开着，会在“租户已关闭”的部署里返回
`ErrTenantRequired`；反过来则是构造器不设防。demo 用一处 `TenantConfig.SQLModelOptions()`
同时喂给两边。

## 方言

`Options.Dialect` 原样交给构造器：为空是 MySQL/SQLite 的反引号，PostgreSQL 传
`sqlbuilder.DialectPostgres`。SQLite 的方言名仍然不能显式传，原因见
[`support/sqlbuilder`](../sqlbuilder/README.md)。

## 测试

```bash
go test -count=1 ./support/sqlmodel/
```

不需要数据库：`schema.Parse` 只读结构体与 tag。测试覆盖无租户列、租户列、ctx 无租户、显式
绕过、自定义列名、自定义命名策略、PostgreSQL 方言与 `*` 选择。
