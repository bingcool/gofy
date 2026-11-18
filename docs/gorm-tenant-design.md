# gofy 基于 GORM 的租户隔离设计

- 状态：**已实现**（2026-11-18）。实现包 `support/tenant`，使用说明见
  [support/tenant/README.md](../support/tenant/README.md)；demo 装配接线见 §11。
- 目标：把 swoolefy `TenantLineInterceptor` 的语义搬到 gofy，让业务 SQL **在数据访问层自动带上
  `tenant_id`**，而不是每个 repository 手写 `Where("tenant_id = ?", id)`。
- 参照实现：[TenantLineDemoHandler.php](../swoolefy/vendor/bingcool/library/src/Db/Interceptor/TenantLineDemoHandler.php)
  与其 `TenantLineInterceptor`（同目录，约 35KB）；对应接口 `TenantLineHandlerInterface`。
- 相关基础设施：`support/db`（gorm 连接装配）、`support/otel/gorm.go`（已有 gorm callback 插件范式）、
  `support/frameworkcontext.TenantID`（租户上下文读取）、`support/headerpropagation`（`X-Tenant-Id` 透传）。

## 1. 为什么要做，以及为什么不能直接照搬 PHP 实现

swoolefy 的做法是**在 PDO 预处理前改写 SQL 字符串**：`beforeExecute()` 用正则判断语句类型
（SELECT/UPDATE/DELETE/INSERT/REPLACE），解析出表名与别名，判断该表是否含租户字段，然后
把 SQL 字符串改写（在 `GROUP BY`/`LIMIT` 等尾部子句前插入 `WHERE tenant_id = :tenant_id`，
或给 INSERT 补列与占位符），并同步往绑定参数数组追加值。它是一个 1000+ 行的正则解析器，
还要处理 `hasTenantCondition` 去重、动态表名、JOIN UPDATE、INSERT SELECT 等大量边界。

**gofy 不应照搬字符串改写**，原因是两边的数据访问栈根本不同：

| 维度 | swoolefy | gofy |
|---|---|---|
| SQL 来源 | 应用拼好的 SQL 字符串 | GORM 用 `clause` 结构化构造 SQL |
| 介入点 | PDO 预处理前，只有字符串和绑定数组 | GORM callback，能拿到 `*gorm.Statement` 的 `Schema`/`Clauses`/`Context` |
| 表字段元信息 | 查 information_schema / PDO getColumnMeta | `Statement.Schema.LookUpField("tenant_id")`（模型已解析） |
| 值绑定 | 手工维护 `:tenant_id` 与 bindParams 对应 | `clause.Eq` / `SetColumn` 交给 GORM 占位符系统 |
| 别名/JOIN | 全靠正则猜 | 明确知道 model、table、clause，不需要猜 |

在 GORM 里解析 SQL 字符串既是重复劳动，又必然漏掉 GORM 支持的方言与构造方式。正确做法是
**在 GORM 的 callback 阶段，对 `Statement` 追加结构化条件**。这也与项目里已有的
`support/otel.GORMPlugin`（注册 `Before("gorm:query")` 等六个回调）完全一致，可以直接复用这套
机制与装配位置。

## 2. 目标与非目标

**目标**

1. 业务代码只要走 `support/db` 拿到的 `*gorm.DB`，对**带 `tenant_id` 字段的模型**做 CRUD，
   SELECT/UPDATE/DELETE 自动追加 `tenant_id = ?`，INSERT/REPLACE 自动填充该列。
2. 租户 ID 从 `support/frameworkcontext.TenantID(ctx)` 读取，与 JWT 认证和上游 `X-Tenant-Id`
   透传保持同源，业务层不需要把它一层层传进 repository。
3. 失败关闭（fail-closed）：目标表需要隔离但当前上下文没有租户 ID 时，**拒绝执行**并返回错误，
   绝不退化成「不过滤」。这与 swoolefy `assertTenantId()` 的语义一致。
4. 有明确的绕过手段，供运维脚本、迁移、跨租户统计使用。
5. 不侵入 GORM 生成的 SQL 字符串，不引入 SQL 解析依赖，不访问 information_schema。

**非目标**

- 不做「数据库表结构自动加列」——那是迁移工具的事。
- 不做字段级/行级权限的其余部分（owner、部门等），本期只做租户。
- 不改写原生 `db.Exec`/`db.Raw` 的原始 SQL（见 §8 边界）。

## 3. 核心设计：一个 GORM Plugin

新增包 `support/tenant`（名字待定，也可放 `support/db/tenant`），导出一个实现 `gorm.Plugin`
的插件，安装方式与 otel 插件相同：`db.Use(tenant.NewPlugin(opts...))`。

### 3.1 Handler 接口（对齐 swoolefy 的三件事）

```go
// Handler 提供租户元信息。实现必须是无状态的：插件单例被所有请求并发共享，
// 租户值只能来自入参 ctx，不能存在 Handler 的字段里。
type Handler interface {
    // TenantID 从 ctx 取当前租户 ID。空串表示无租户，触发失败关闭。
    TenantID(ctx context.Context) string
    // Column 返回租户字段名，默认 tenant_id。
    Column() string
    // IgnoreTable 在无法从模型元信息判断时，是否跳过该表。
    // 与 swoolefy 的 ignoreTable 兜底语义一致。
    IgnoreTable(table string) bool
}
```

默认实现 `ContextHandler`：`TenantID` 直接委托 `frameworkcontext.TenantID(ctx)`，
`Column` 返回 `"tenant_id"`，`IgnoreTable` 返回 `false`（即默认所有模型都参与隔离）。
业务若要改字段名或自定义来源，实现自己的 `Handler` 即可，与 swoolefy 换
`TenantLineDemoHandler` 的位置对应。

**关键差异与理由**：PHP 的 handler 从协程上下文取租户（`SwooleContext::get('tenant_id')`），
Go 里协程不是上下文容器，租户必须显式挂在 `context.Context` 上。gofy 已有现成的
`frameworkcontext`（本地 JWT 身份优先、上游透传兜底），所以直接复用，不另起一套 goroutine-local
存储。这也让 worker、script 等非 HTTP 入口可以用 `frameworkcontext.WithUser` 或自己的
`Handler` 注入租户，行为一致。

### 3.2 判定「模型是否参与隔离」——用 Schema 而不是元数据查询

swoolefy 在 `shouldIgnore()` 里查表字段元信息：能读到且含租户列就隔离，能读到但不含就跳过，
读不到才回退到 `handler.ignoreTable()`。GORM 里这件事更简单也更可靠：

```
field := stmt.Schema.LookUpField(handler.Column())
if field == nil { 跳过 }   // 模型没有该列，等价于「表不含租户字段」
```

- `stmt.Schema` 由 GORM 从 model/Dest 解析，含全部字段元信息，无需查询数据库。
- 没有 Schema 的场景（如 `Table("x").Where(...)` 无 model、`Exec`/`Raw`）无法判定，
  这时回退到 `Handler.IgnoreTable(table)`，**默认不跳过**，并记一条告警——对应 swoolefy
  `warnNotRewritten()` 的「无法识别目标表时告警」语义。
- 这里刻意**不提供**「按表名白名单静态配置」作为首选：白名单会随 migrate 漂移，
  用模型字段是否声明为准更不容易漏。白名单只作为 `IgnoreTable` 的业务兜底。

### 3.3 各语句的改写（全部用 clause / SetColumn，不碰 SQL 字符串）

插件在 GORM 的五个 processor 上注册 before 回调（row/raw 见 §8）：

**SELECT / DELETE** — 在 `Before("gorm:query")` / `Before("gorm:delete")` 里：

```go
stmt.AddClause(clause.Where{Exprs: []clause.Expression{
    clause.Eq{Column: clause.Column{Table: alias, Name: column}, Value: tenantID},
}})
```

GORM 会把条件合并进已有 WHERE（自动处理 AND 与括号），并正确生成占位符与绑定值。
`alias` 从 `stmt.Table` / join 信息推导；无法确定时用列名本身（单表无歧义）。

**UPDATE** — `Before("gorm:update")`，同样追加 WHERE 条件。注意 GORM 的 update 若没有 model
（`Model(&T{})` 缺失）时 `Schema` 可能为 nil，此时按 §3.2 回退。

**INSERT / REPLACE** — `Before("gorm:create")`：

```go
stmt.SetColumn(column, tenantID, true) // 第三个参数表示来自 callback，避免被视为用户显式赋值
```

- `SetColumn` 支持 struct、`map[string]any`、`[]map` 三种 Dest，单行与批量插入都能覆盖。
- 若模型声明了该字段但调用方**已经显式赋值**，应尊重显式值还是强制覆盖？建议策略：
  **显式值优先**——先检查 `stmt.Dest` 里该字段是否零值，非零则不覆盖。这样管理后台可以
  合法地跨租户造数据。若项目要求「一律强制当前租户」，把该行为做成插件选项
  `OverwriteExplicit bool`（默认 false）。
- 这与 swoolefy 的 `rewriteInsert` 一致：它检测字段列表/SET 列表中是否已有租户列，有则不动。

### 3.4 失败关闭（fail-closed）

在确认「模型含租户列」之后、追加条件之前调用：

```go
if tenantID == "" {
    stmt.AddError(fmt.Errorf("%w: tenant id required for %s", tenant.ErrTenantRequired, stmt.Table))
    return
}
```

- `stmt.AddError` 会让整条操作直接失败（GORM 遇到 `Statement.Error` 会中止回调链），
  不会发出「无租户条件」的 SQL。
- 与 swoolefy `assertTenantId()` 抛 `DbException` 等价，错误信息带表名便于定位。
- 需要「无租户也能跑」的入口（如启动期的健康检查脚本）必须用 §4 的绕过机制显式声明，
  而不是靠空租户自动放行。

### 3.5 幂等：避免重复追加

同一条语句可能因为嵌套 scope 或重复注册被多次处理。追加前先检查 WHERE 里是否已有租户条件：
遍历 `stmt.Clauses["WHERE"]` 的表达式，若存在列名等于 `column` 的 `clause.Eq` 则跳过。
GORM 没有现成 API，但表达式是结构化对象，做一次浅层类型判断即可（比 PHP 的正则
`hasTenantCondition` 简单得多）。插件注册本身也应防重复：`Name()` 固定，
`db.Use` 两次会由 GORM 报重复错误，与 otel 插件行为一致。

## 4. 绕过机制（对齐 swoolefy 的 `withoutTenantScope`）

swoolefy 用 connection 上的 `shouldIgnoreTenantInterceptor()` 跳过。Go 里用 **context 标记**，
作用范围精确到一次调用，不污染连接（连接是池化共享的，不能把状态放连接上）：

```go
ctx = tenant.Without(ctx)          // 本次调用跳过租户改写
db.WithContext(ctx).Find(&orders) // 返回全部租户数据
```

实现：插件在 `TenantID(ctx)` 之前先看 ctx 里有没有 `bypassKey`。`Without` 用
`context.WithValue`，是标准做法，与项目里 `frameworkcontext`/`headerpropagation` 一致。
再加一个 `tenant.WithTenant(ctx, id)` 供脚本/测试直接指定租户（不依赖请求身份）。

**绕过是特权操作**：文档要明确「只有运维脚本、数据迁移、跨租户报表可用」，业务 handler
禁止调用。可选加一个审计钩子 `OnBypass func(ctx, table)`，让绕过留痕。

## 5. 装配位置（与代际、多实例共存）

项目约定（见 `MEMORY.md`）：连接统一走 `support/db`，多实例按「装配代」管理。插件应：
1. **在每代创建连接后安装**，与 `otel.InstrumentGORM` 并列：

```go
gormDB, err := db.New(ctx, cfg, logger)      // support/db 打开连接
_ = otel.InstrumentGORM(gormDB)              // 已有：链路
_ = tenant.Instrument(gormDB, tenant.Options{Handler: myHandler}) // 新增：租户
```

2. 不改 `support/db.New` 的默认行为——**默认不装租户插件**。是否启用由应用决定，
   避免给不用多租户的项目强加语义（与 `llm_ai.enabled` 的思路一致）。
3. 顺序：otel 插件的 before 会替换 `Statement.Context`（注入 span），租户插件必须读到最终
   context，因此**租户插件要在 otel 之后安装**（回调按注册顺序执行），或租户插件从
   `stmt.Context` 取值时确保拿到的是 otel 更新后的那个。文档与测试要把这个顺序钉住。

## 6. 与项目现有约定的兼容

- **只读查询层 `query`**（每模块 `query` 子包）：它只读、用 gorm、跨模块表靠 SQL 写表名。
  插件对它同样生效；因为 `query` 里常见 `Table("other_table")`（无 model、Schema 为 nil），
  会走 §3.2 的兜底路径，需要业务用 `Handler.IgnoreTable` 或 `Without` 明确处理，避免告警噪音。
- **claim / 消费台账**：`support/claim`、`support/consumer` 的表不含 `tenant_id`（模型字段为准），
  插件自动跳过，不受影响。
- **导入守卫**：新增 `support/tenant` 属 support 层，只能 import `support/frameworkcontext`、
  `support/headerpropagation` 与 gorm，不得 import 任何 `app/`。要在
  `shell/check-imports.sh` 的 support 规则里确认这条边界。
- **不写进程环境、不用全局状态**：租户值只在 ctx 上流动；插件实例无可变字段，可被所有
  请求并发共享（与 `Registrar`/`MemoryStore` 的并发教训一致，字段要么是配置要么加锁）。

## 7. 测试策略

用 gorm 的 sqlite driver（项目已有 `support/sqlite`）做集成测试，建立含/不含 `tenant_id` 的模型：

| 场景 | 断言 |
|---|---|
| SELECT 带租户列模型 | SQL 含 `tenant_id = ?`，绑定值为当前租户 |
| UPDATE / DELETE | WHERE 追加租户条件，且原有条件用 AND 正确组合 |
| INSERT（struct / map / 批量） | 租户列被填充；调用方已显式赋值时按策略保留或覆盖 |
| 无租户 ID | 操作返回 `ErrTenantRequired`，不发出 SQL（失败关闭） |
| 不含租户列的模型 | 不改写，正常执行 |
| `tenant.Without(ctx)` | 不加条件，返回全部数据 |
| `tenant.WithTenant(ctx, id)` | 用指定租户，覆盖上下文来源 |
| 重复追加 | 同一语句多次处理只出现一次条件 |
| 无 model 的 `Table("x")` | 走 IgnoreTable 兜底 / 告警，行为可预期 |
| 与 otel 插件共存 | 安装顺序正确，span 与租户都生效 |
| 并发不同租户 | 100 个 goroutine 各带不同租户，结果互不串扰（race 测试） |

最关键的是最后一条：swoolefy 用协程上下文天然隔离租户，Go 里必须靠「值只在 ctx、插件无状态」
来保证并发安全，这条测试是这条架构约束的守门员。

## 8. 边界与已知限制

1. **原生 `db.Exec` / `db.Raw`**：GORM 的 raw/row callback 里没有结构化 Schema，无法可靠判定
   是否含租户列。建议：**不改写**，并在文档里明确「原生 SQL 不走租户隔离，需手写 tenant_id
   或使用 `session` 显式 scope」。这与 swoolefy「无法识别目标表时告警跳过」是同一取舍，
   但 Go 这边更保守（不猜字符串）。
2. **JOIN / 子查询**：GORM 的 `Joins` 会带多张表。插件只给主表追加租户条件；JOIN 的表若要隔离，
   需在 `Joins` 字符串里手写或用 scoped 的 gorm scope。不要试图在插件里解析 join 字符串。
3. **聚合与 UNION**：`clause.Where` 追加对聚合查询有效；UNION 的两半各自是独立 Statement，
   各自追加，符合预期。
4. **跨库/多租户分库**：本方案是「共享表 + tenant_id 列」的行级隔离，不解决分库分表。
5. **软删除**：`deleted_at` 由 GORM 自己的 clause 处理，与租户条件通过 AND 组合，互不干扰。

## 9. 落地步骤（已完成，见 §11）

1. 实现 `support/tenant`：Handler 接口、默认 ContextHandler、Plugin、Without/WithTenant、
   ErrTenantRequired。
2. 写 §7 的集成测试（sqlite）与并发 race 测试。
3. 在 demo 的 appio 装配代里接入（与 otel 插件并列，默认关闭），并在
   `support/tenant` 用 `frameworkcontext` 的 `X-Tenant-Id` 透传路径验证请求身份确实是
   租户来源（`TestUpstreamHeaderIsIdentitySource`）。
4. 更新 `support/tenant/README.md` 与本文档状态，写明「默认不启用、如何启用、绕过是特权」。

## 10. 一句话总结

swoolefy 在 PDO 层**改写 SQL 字符串**来实现租户隔离；gofy 应以 GORM **结构化 clause** 的
方式在 callback 阶段追加租户条件——语义对齐（自动拼接、字段判定、失败关闭、可绕过），
实现路径完全不同（不解析 SQL、不查 information_schema、租户值走 context 而非协程上下文）。
这样既复用了项目已有的 gorm 插件范式与租户上下文，又避免了 1000 行正则解析器带来的
方言适配与边界处理负担。

## 11. 落地结果与方案偏差（2026-11-18）

实现文件：

- `support/tenant/tenant.go`：`ErrTenantRequired`、`ErrPluginNil`、`Without`、`WithTenant`。
- `support/tenant/handler.go`：`Handler` 接口、`ContextHandler`（默认）、`FuncHandler`。
- `support/tenant/plugin.go`：`Plugin`（gorm.Plugin）、`Options`、`Instrument`、四个 before
  回调、结构化去重与显式值检测。
- `support/tenant/README.md`：使用说明。
- 测试：`support/tenant/plugin_test.go`（20 个用例，含 race 与 100 goroutine 并发隔离）、
  `examples/demo-service/app/appio/db/tenant_test.go`（装配层接线与幂等）、
  `examples/demo-service/app/config/config_test.go`（配置开关）。

### 11.1 与方案的偏差

1. **§3.2 的「无 model 时 Schema 为 nil」需要修正。** 实测 GORM v1.31.2：
   `Table("t").Find(&[]T{})` 的 Dest 是模型切片，`processor.Execute` 仍会解析出
   `Schema`，因此按模型字段判定；只有 `Dest` 是 `[]map[string]any` 这类无类型值时
   `Schema` 才为 nil，`IgnoreTable` 兜底才生效。这实际上更好——凡能用模型表达的查询
   都有可靠判定。README 与测试都写明了这一点。

2. **§3.3 的 alias 限定没有实现。** 方案打算从 `stmt.Table`/join 推导别名，生成
   `alias.tenant_id`。但 GORM 的 `Statement` 没有可靠的「当前主表别名」来源，猜错会
   造出错误 SQL。实现改为 `clause.Column{Name: column}`（只限定列名、不加表前缀）——
   单表无歧义，这是绝大多数场景；多表 JOIN 且列名冲突时由业务显式限定。见 §8.2。

3. **§5 的装配位置落在应用装配代，而非框架 `gofy.go`。** 框架在 `start.go` 给自持的
   `db.default` 装 otel，之后模块 `Start` 才打开额外实例；demo 在
   `app/appio/db.Open` 里用可选参数 `WithTenant(...)` 于 otel **之后**安装，同时覆盖
   `db.default` 与 eager 实例。这保持了方案「默认不启用」的要求：
   `tenant.enabled: false`（或整段缺失）时完全不装插件。

4. **`installTenant` 是幂等的。** 方案只说重复 `db.Use` 应报错；但 `db.default` 由框架
   持有、跨 Reload 代复用，`Open` 会对同一个 `*gorm.DB` 调用多次。因此装配层先用
   `Callback().Query().Get("gofy:tenant:before_query")` 判断是否已装，避免 Reload 失败；
   `tenant.Instrument` 本身仍保持「重复安装报错」的库级语义。

5. **绕过检查提到最前。** 实现中发现若把 `isBypassed` 放在 schema 判定之后，
   `Without(ctx)` 在无 Schema 的语句上会失效并报 `ErrTenantRequired`。已改为在所有
   判定之前检查，并加了回归测试 `TestWithoutWinsWithoutSchema`。

6. **`OnUnresolved` / `OnBypass` 做成了回调而非日志。** `support/tenant` 是 support 层，
   按项目约定不 import `logger`（接收 `*zap.Logger` 或回调）；把输出决策留给应用，
   包本身无日志依赖。

7. **worker 进程未接线。** worker 不走 `ModuleFactories`（见 §6 与项目约定），其连接由
   worker 宿主自行装配。需要租户隔离的 worker 应在其装配点显式调用
   `tenant.Instrument`；脚本走 `script/register.go`，已与 HTTP 入口共用同一开关。

### 11.2 验证

- `go test ./support/tenant -race`：20 个用例全过，含 100 goroutine 并发不同租户不串扰。
- demo 装配测试：`installTenant` 装上后 `WithTenant` 查询只返回本租户行；连续三次安装
  不报重复回调。
- `make check` 全绿（含导入守卫、race、cgo-free、Windows cross）。
