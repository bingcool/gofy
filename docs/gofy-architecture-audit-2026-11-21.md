# Gofy 三仓库新一轮代码审计报告

> 审计日期：2026-11-21  
> 审计原则：不新增功能；只识别现有实现中的安全缺口、逻辑错误、边界问题、并发与生命周期问题，以及需要收紧的架构约束。  
> 审计方式：以 GitHub 当前 `main` 分支源码为基线，重新读取三个仓库的目录树、关键实现、配置与测试代码，重点复核本轮新增的租户隔离、SQL Builder / SQL Model，以及三仓库间的契约绑定与客户端调用链。  
> **验证边界：本报告是静态源码审查。没有在本环境执行 `go test ./...`、`go test -race ./...`、数据库集成测试或真实 Nacos / Broker 联调；涉及运行时竞争的结论均标明验证要求。**

## 1. 审计基线

| 仓库 | 分支 | 本次审计基线 |
|---|---|---|
| [bingcool/gofy](https://github.com/bingcool/gofy) | `main` | `068c3e00c1342f5cf728daff12584ffc75556404` |
| [bingcool/gofy-interface-api](https://github.com/bingcool/gofy-interface-api) | `main` | `5394a8121e5f06b66920b4bfe1baded270cbd503` |
| [bingcool/gofy-adapter](https://github.com/bingcool/gofy-adapter) | `main` | `a33d782f3f32b99477de56381d2fe734d80243b2` |

本轮相较上一轮，`gofy` 主仓库已出现新的提交，重点变化集中在：

- `support/tenant`：GORM 行级租户隔离插件；
- `support/sqlbuilder`：基于 ent SQL builder 的 SQL 构造层；
- `support/sqlmodel`：将 GORM Model Schema 与 SQL Builder 连接起来的租户感知 Resolver；
- demo 的租户配置从单一开关调整为“配置段缺失 / 配置段存在但未写 enabled / 显式关闭”的三态语义；
- SQL Builder 的条件构造、批量 IN、Predicate 状态约束及文档补充。

`gofy-interface-api` 与 `gofy-adapter` 的当前 `main` HEAD 仍分别是上一轮审计基线；本轮重新核对了其当前源码，没有将旧报告中的结论直接当成新结论。

## 2. 总体结论

本轮最需要优先处理的是新增租户隔离链路中的两个安全边界：

1. **租户插件在识别“调用方已经写了 tenant_id 条件”时，只检查列名和比较形式，没有检查比较值是否等于当前请求租户。** 这会让一个租户上下文通过手写 `tenant_id = ?` 查询另一个租户的数据。
2. **默认 `OverwriteExplicit=false` 时，INSERT 会保留调用方显式传入的租户值。** 代码注释把它描述为管理员跨租户写入场景，但插件本身没有验证调用方是否具有管理员权限。普通请求如果能控制该字段，就可能写入其他租户的数据。

这两项比本轮其他问题更重要。它们不是“缺少功能”，而是现有隔离策略在特定输入下会被绕过。

其次，三仓库之间仍有几个装配层边界值得修正：

- ~~`AuthRequired` 仍然只是契约声明，默认不强制安装认证中间件~~（复核：该机制已在本地提交中删除，见 §2.1 与 P1-03 复核）；
- `BindAll` 在完成全部校验前就逐条注册路由，遇到错误时会留下部分注册结果；
- SSE 客户端重连没有沿用普通请求的幂等重试判定，对非 GET 的 SSE 路由存在重复提交风险。

## 2.1 复核结论与状态总表（2026-11-21）

复核方式：以三个仓库**本地 `main` 的当前 HEAD** 为准，逐条对照源码与测试，不把审计结论
当成既有事实转述。

**结论：11 项问题中，成立的 10 项已全部修复（2026-11-21），1 项（P1-03）在本地 HEAD 已失效。
修复前逐条验证了回归测试在旧代码下确实失败；三个仓库的全量构建、vet、守卫与测试均通过。**

| 仓库 | 审计基线 | 复核时的本地 HEAD | 差异 |
|---|---|---|---|
| `gofy` | `068c3e0` | `068c3e0` | 一致 |
| `gofy-adapter` | `a33d782`（GitHub main） | `3075cec` | 本地领先 3 个提交，含 `76f6e25 remove AuthValidator` |
| `gofy-interface-api` | `5394a81`（GitHub main） | `c25e272` | 本地领先 3 个提交，含 `dd1cd4a drop the auth declaration from route specs` |

这两个未推送的提交决定了 P1-03 的真伪：它描述的是 `AuthRequired` / `AuthValidator` 存在时的
装配缺口，而这两个东西在本地 HEAD 已被删除、在 GitHub `main` 上仍然存在。所以那条结论是
**"远端成立、本地已失效"**，不是审计读错了代码。

| 编号 | 等级 | 问题 | 复核结论 | 状态（当前 HEAD） | 备注 |
|---|---|---|---|---|---|
| P1-01 | P1 | 已有 `tenant_id = ?` 条件被当作已隔离，不校验绑定值 | 成立 | **已修复** | 原 `constrainsColumn` 只看列名、`isTenantEquality` 只看 SQL 形状，`clause.Eq.Value` 与 `Expr.Vars` 从未与生效租户比较。现 `pinsTenant` 要求值相等才去重，证明不了就追加（见 P1-01 修复记录） |
| P1-02 | P1 | INSERT 中调用方显式填写的租户值被默认保留 | 成立，但性质是"默认值不安全"而非编码错误 | **已修复** | 默认 `OverwriteExplicit=false`；注释把跨租户写入写成了管理员用例，插件却不校验权限。业务池应设 `true`，跨租户写入走单独的特权连接 |
| P1-03 | P1 | `AuthRequired` 声明了认证却可无认证绑定 | **不成立（本地已删除该机制）** | 已失效 | 见 P1-03 复核；GitHub `main` 上仍存在 |
| P2-01 | P2 | 自定义 Handler 返回空列名时隔离静默失效 | 成立 | **已修复** | `NewPlugin` 只 `TrimSpace` 不校验（`plugin.go:61`），`sqlmodel.New` 同样（`sqlmodel.go:151`）；`LookUpField("")` 返回 nil 即跳过 |
| P2-02 | P2 | `TenantScope` 与插件 / `sqlmodel` 的身份优先级不一致 | 成立 | **已修复** | `TenantScope` 直接用 `frameworkcontext.TenantID`（`sqlbuilder.go:657`），另两条路径先看 `OverrideTenant`；`table == nil` 只在 ctx 有租户时 panic |
| P2-03 | P2 | `BindAll` 校验失败时路由已被部分注册 | 成立 | **已修复** | `r.Handle(...)` 在同一个校验循环里（`bind.go:237`），`problems` 循环结束才检查 |
| P2-04 | P2 | SSE 自动重连不走幂等判定 | 成立 | **已修复** | 重连只判 `attempt`、`ctx`、`ErrEventTooLarge`（`sse.go:96-128`）；`openStream` 用 `spec.Method`，POST SSE 合法存在 |
| P2-05 | P2 | 脱敏默认规则是导出可变切片 | 成立 | **已修复** | `DefaultMaskColumns` / `DefaultMaskPatterns` 可被整片替换（`sqlmask.go:41,52`），`defaultMaskSet()` 每次直读 |
| P2-06 | P2 | 批量 INSERT 的列值映射不完整 | 成立 | **已修复** | 行为已在 `maskSQL` 注释里声明为不支持（`sqlmask.go:201`），但 `support/sqllog/README.md` 没有对应边界说明 |
| P2-07 | P2 | AccessLog 默认记录 Query | 成立 | **已修复** | `DefaultAccessLogOptions()` 里 `LogQuery: true`（`accesslog.go:79`） |
| P2-08 | P2 | K8s Lease `Close` 不等待在途 API 操作 | 成立 | **已修复** | `Close()` 只置 `closed` 并清空 `held`（`k8slease.go:294`），无 in-flight 计数 |

**"复核结论"列的口径**：成立＝按当前代码可以复现；不成立＝当前代码里已不存在该机制或结论
与代码相反。**"复核结论"对照审计基线；"状态"列反映本轮修复后的工作区**（三个仓库均未提交、未推送）。
P1-03 是**机制被删除**（不是缺陷被修好）；§5 的"已改善"表属于上一轮，不在本表。

动手前有三个约束值得先定：

1. ~~**P1-01 的修复要能拿到绑定值。**~~ 已落地：`clause.Eq.Value` 与 `clause.Expr.Vars`
   都可读，按字符串形态归一后比较；拿不准时不判"已隔离"——多一个 `AND tenant_id = ?` 只是
   冗余，少一个就是跨租户。
2. ~~**P1-02 改的是默认值，不是逻辑。**~~ 已落地：`OverwriteExplicit` 改成 `*bool`，nil
   表示覆盖；demo 配置与 README 同步。原先的批量填充代码在"覆盖"模式下会整批 `SetColumn`
   （map 切片直接报 `invalid field`），一并改成了按行赋值。
3. ~~**P2-02 受一条守卫约束。**~~ 已落地：覆盖与绕过标记搬到 `support/frameworkcontext`，
   `support/tenant` 转出为 `WithTenant` / `Without`，`sqlbuilder` 直接读同一份标记，
   不引入 gorm 依赖，守卫照旧通过。

## 2.2 本轮修复记录（2026-11-21）

三个仓库的工作区改动，按问题编号列出。**每条都有回归测试，且每条测试都在回退修复后复现了
审计描述的行为**（P2-08 的第三条是防止新实现退化的守护测试，旧代码本来就通过）。

| 编号 | 仓库 | 修复 | 回归测试 |
|---|---|---|---|
| P1-01 | gofy | `pinsTenant` 要求绑定值等于生效租户才去重；证明不了就追加 | `TestExistingConditionMustMatchTheContext`、`TestSameTenant` |
| P1-02 | gofy | `OverwriteExplicit: *bool`，nil 即覆盖；INSERT 按行赋值（修掉批量 map 的 `invalid field`） | `TestInsertOverwritesExplicitTenantByDefault`、`TestBatchInsertFillsPerRow` |
| P2-01 | gofy | `Instrument`/`Initialize` 与 `sqlmodel.New` 拒绝空列名；后者改为返回 error | `TestEmptyColumnFailsAtInstrument`、`TestNewRejectsEmptyColumn` |
| P2-02 | gofy | 覆盖与绕过标记移入 `support/frameworkcontext`，三条路径共用；`TenantScope` 加 nil 防护 | `TestAllTenantPathsResolveTheSameIdentity`、`TestTenantScopeBypassAndNil` |
| P2-03 | gofy-adapter | `BindAll` 两阶段：先校验全部路由，无问题才调用 `r.Handle` | `TestBindAllDoesNotRegisterWhenValidationFails`（旧代码留下 1 条已注册路由） |
| P2-04 | gofy-interface-api | SSE 重连用 `retryFor` 的结果作次数上限；顺带修掉重连失败时 `drainAndClose(nil)` 的 panic | `TestStreamSSENoReconnectForPostWithoutIdempotencyKey`（旧代码 POST 发了 4 次）、`TestStreamSSEReconnectsForPostWithIdempotencyKey` |
| P2-05 | gofy | 默认规则改私有变量，`DefaultMaskColumns()` / `DefaultMaskPatterns()` 返回副本 | `TestDefaultRulesAreCopies` |
| P2-06 | gofy | `maskSQL` 记录 INSERT 列名并按位置映射到所有 VALUES 元组；README 写明扫描器边界 | `TestMaskSQLInsertColumnsByPosition`（单行 / 多行 / 数字 / upsert / 非敏感列） |
| P2-07 | gofy | `DefaultAccessLogOptions().LogQuery = false`，显式打开才记录 query | `TestAccessLogDoesNotRecordQueryByDefault` |
| P2-08 | gofy-adapter | `WaitGroup` 记录在途的 Acquire/Renew/Release，`Close` 最多等待 `RequestTimeout` | `TestCloseWaitsForInFlightRenew`、`TestCloseWaitIsBoundedByRequestTimeout` |

**语义变化需要部署侧注意**（不只是修 bug）：

1. `tenant.Options.OverwriteExplicit` 从 `bool` 改为 `*bool`；`TenantConfig.overwrite_explicit`
   同样改三态，不写即覆盖。需要保留调用方显式租户值的特权连接必须写 `false`，或改用
   `tenant.Without(ctx)`。
2. `sqlmodel.New` 现在返回 `(*Resolver, error)`；装配代码必须处理错误（demo 容器在装配期
   panic）。`sqlmodel.Default` 保持为值，内部用 must 风格构造。
3. AccessLog 默认不再写 `query` 字段；需要它的应用用 `WithAccessLogOptions` 显式打开
   `LogQuery`，并自行确认参数内容或改用白名单。
4. `sqllog.DefaultMaskColumns` / `DefaultMaskPatterns` 现在是函数而非切片变量。

验证：gofy `go build ./...`、`go vet ./...`、`check-imports.sh`、`go test -count=1 ./...`，
demo `make check`；gofy-adapter 与 gofy-interface-api 各自 `go build` / `go vet` /
`go test -count=1 ./...`，全部通过。三个仓库均**未提交、未推送**。

# 3. P1：需要优先修复

## P1-01：租户插件把“任意 tenant_id 等值条件”误判为“已经按当前租户隔离”

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/tenant/plugin.go`  
**相关函数：** `scope()`、`constrainsColumn()`、`exprsConstrainColumn()`、`isTenantEquality()`

### 当前实现

插件会检查已有 WHERE 条件。如果发现条件树中存在针对租户列的等值比较，就认为调用方已经完成租户过滤，不再追加自己的租户条件。

目前检查的重点是：

- 是否是 `tenant_id = ?` 形式；
- 列名是否等于配置的租户列；
- 表达式是否位于能够限制结果集的 AND 路径中；
- 不把 OR / NOT 中的租户列引用当成有效隔离条件。

这比简单的字符串包含判断严谨很多，但仍缺少一个决定性检查：**等值比较右侧的参数值是否等于当前上下文的 tenant ID。**

### 可复现风险

假设数据库有两个租户：

```text
tenant_id = tenant_a
tenant_id = tenant_b
```

当前请求上下文属于 `tenant_a`，业务代码误写或恶意构造：

```go
db.WithContext(ctxForTenantA).
    Where("tenant_id = ?", "tenant_b").
    Find(&rows)
```

插件识别到 `tenant_id = ?`，就认为调用方已经加过租户条件，不再追加：

```sql
WHERE tenant_id = 'tenant_b'
```

最终 `tenant_a` 的请求读取到了 `tenant_b` 的数据。

同样的问题也存在于结构化 `clause.Eq` 条件：目前只根据列名判定，没有比较其 Value 与当前租户 ID。

### 风险判断

这是租户隔离的直接绕过路径，属于 P1 安全问题。

当前测试覆盖了：

- `tenant_id <> ?`；
- OR；
- NOT；
- 列名只出现在普通值中；
- `tenant_id = ? OR ...`；
- 同租户值的重复条件不再追加。

但没有覆盖“上下文租户为 A，调用方显式等值条件却是 B”的场景。

### 修复建议

不要仅凭“WHERE 中存在租户列等值条件”就认定已经隔离。

建议调整为：

1. 插件始终掌握当前上下文的 tenant ID；
2. 对已有的租户等值条件，只有能够证明其绑定值与当前 tenant ID 一致时，才允许去重；
3. 无法可靠提取或证明参数值时，追加插件自己的租户条件；
4. 不要为了避免重复条件而牺牲隔离正确性。

新增回归测试：

- ctx 为 A，`tenant_id = A`：不重复追加；
- ctx 为 A，`tenant_id = B`：最终结果仍只能是 A；
- ctx 为 A，结构化 `clause.Eq` 的 Value 为 B：最终结果仍只能是 A；
- ctx 为 A，复杂 AND / OR / NOT 组合中出现 B：不得跨租户。

### 修复记录（2026-11-21）

`constrainsColumn` / `exprsConstrainColumn` 改为 `pinsTenant` / `exprsPinTenant`：除了原来的
形状判定（合取路径、单独一条等值、非 OR / NOT），还要求绑定值等于本次生效租户。取值只有
两处：`clause.Eq.Value`（map / struct 写法）与 `clause.Expr.Vars[0]`（手写 SQL）；比较用
字符串形态（`string`、`[]byte`、`driver.Valuer`，其余 `fmt.Sprint`），所以 `"42"`、`42`、
`int64(42)` 与命名 string 类型算同一个租户。

证明不了相等就追加，得到 `tenant_id = 'b' AND tenant_id = 'a'`——零行，不是跨租户读取。
失败方向是单边的：认不出的值只会多一个谓词，不会少数据。

管理端路径不变：`tenant.WithTenant(ctx, id)` 改变生效租户，显式条件与之相等时仍然只绑定
一次（`TestWithTenantKeepsAnotherTenantReachable`）。

新增回归测试，三条用例在原实现下都能读出另一个租户的数据：

- `TestExistingConditionMustMatchTheContext`：raw SQL / map / struct 三种写法，断言返回
  0 行且租户绑定从 1 个变成 2 个；
- `TestWithTenantKeepsAnotherTenantReachable`：覆盖仍然可用、绑定数仍为 1；
- `TestSameTenant`：值归一化，包括 `nil`、无效 `sql.NullString`、列引用一律判否。

## P1-02：默认保留 INSERT 显式 tenant_id，普通请求可能写入其他租户

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/tenant/plugin.go`  
**相关函数：** `fill()`  
**配置：** `tenant.Options.OverwriteExplicit`

### 当前实现

`OverwriteExplicit` 默认是 `false`。

当 INSERT 的目标对象已经显式填写租户列时，插件保留调用方传入的值；只有字段不存在或为零值时，才使用当前上下文租户。

代码注释说明，这样可以让管理员有意写入其他租户；如果设置为 `true`，则强制覆盖为当前上下文租户。

### 问题

插件本身并不知道当前调用方是不是管理员，也没有管理员权限校验。

因此，“允许管理员跨租户写入”实际上被实现成了：

> 任何能够构造带有非空 tenant_id 的 INSERT 调用方，都可以保留这个租户值。

如果业务 Request 结构体中包含租户字段，或者业务层将请求字段直接映射到 Model，就可能出现：

```go
// 当前请求身份属于 tenant_a
order := Order{
    TenantID: "tenant_b",
    Amount:   100,
}

db.WithContext(ctxForTenantA).Create(&order)
```

在默认配置下，插件会保留 `tenant_b`。

这会造成跨租户写入。即使该记录不会被当前租户查询到，也可能污染其他租户的数据、触发错误业务关系或形成越权数据注入。

### 风险判断

这是 P1。默认策略不应把“调用方显式填写租户列”视为可信授权。

### 修复建议

在不增加新功能的前提下，先明确现有插件的安全默认值：

- 普通业务连接应强制使用上下文租户，即启用 `OverwriteExplicit`；
- 如果确实需要跨租户写入，应由受控的管理入口显式使用特权路径，而不是让普通 INSERT 自动信任调用方字段；
- 对外部 Request 不应直接暴露可写入的租户字段；
- 增加测试：ctx=A、INSERT 显式 tenant=B，普通配置下必须写入 A。

如果保留 `OverwriteExplicit=false` 作为通用默认值，必须明确它不是安全默认值，不能将其用于普通请求数据库连接。

## P1-03：~~AuthRequired 仍可能在没有认证中间件时正常绑定~~（复核：本地已失效）

> **复核结论（2026-11-21）：对审计基线成立，对当前代码不成立。**
>
> 基线 `a33d782` 上确有 `BindOptions.AuthValidator`、`validateAuth()`，契约里也有
> `Spec.Auth`，报告列出的三条路径（声明了没装中间件、`BindAll` 未配 validator、直接
> `Bind()`）都能对上。但整套机制已被有意删除：
>
> - `gofy-adapter` `76f6e25 refactor(bind): remove AuthValidator`；
> - `gofy-interface-api` `dd1cd4a refactor(contracts)!: drop the auth declaration from route specs`。
>
> 两个提交目前只在本地 `main`，GitHub 上仍停在 `a33d782` / `5394a81`——审计读到的是真实
> 存在的旧代码，不是误读，只是基线没包含这两个提交。
>
> 删除理由（2026-11-19 决策）：`AuthValidator` 只比对中间件的**名字**，一个放行实现也能
> 通过校验，等于给出一种不存在的保证；生产契约也从未写过 `Spec.Auth`，校验器一直空转。
> 现在"需要登录"= 分组或路由上 `Use("auth")`，没装即公开；契约不再声明认证，也就不存在
> "声明与实现不一致"。
>
> 证据：当前代码 `grep -rn "AuthRequired|AuthValidator|validateAuth"` 在两个仓库里只剩
> 测试夹具的同名字段（一个配置响应样例），与路由认证无关。
>
> **仍然存在的风险（性质不同）**：装配时忘了 `Use("auth")` 的路由就是公开的，adapter 无从
> 校验——它拿到的是中间件实例，没有名字可比对。这属于启动清单与 review 范畴，不是本报告
> 描述的那条缺陷，也无法在 adapter 内部修掉。

**原报告主张（摘要，便于追溯）**：`AuthRequired` 只是元数据，不自动安装认证中间件；
`AuthValidator == nil` 时 `validateAuth()` 直接返回 nil；直接 `Bind()` 不解析
`Spec.Middleware`，因此"声明需要认证、实际没有保护"可以静默存在。

**复核后的正确表述**：以上只在含 `AuthValidator` 的历史版本上成立（`a33d782` 及更早）。
当前版本没有"认证声明"这一层，风险退化为"路由漏装认证中间件"，与框架其它分组一致，
不再是本条描述的装配缺口。

# 4. P2：建议修复或收紧

## P2-01：自定义 Tenant Handler 返回空列名时，租户隔离会静默失效

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/tenant/plugin.go`、`support/sqlmodel/sqlmodel.go`  
**相关函数：** `NewPlugin()`、`sqlmodel.New()`

### 当前实现

`NewPlugin()` 直接读取：

```go
column := strings.TrimSpace(handler.Column())
```

随后在模型 Schema 中执行：

```go
stmt.Schema.LookUpField(p.column)
```

`sqlmodel.New()` 也直接将 `Handler.Column()` 保存为 Resolver 的列名。

默认 `ContextHandler` 与 `FuncHandler` 会提供默认列名，因此正常内置实现没有问题。

但 `Handler` 是公开接口，外部自定义实现可以返回空字符串或错误列名。

### 风险

如果列名为空，`LookUpField("")` 找不到字段，插件会把模型当作“不需要租户隔离”，直接跳过过滤。

这属于配置错误导致的 fail-open，而不是启动期失败。

### 修复建议

- 在插件和 Resolver 构造阶段校验列名非空；
- 对非法列名不要静默跳过；
- 最好让构造过程能够返回错误；如果现有 API 暂时不能改变，则至少在应用装配阶段显式校验；
- 增加自定义 Handler 返回空列名的测试。

## P2-02：`sqlbuilder.TenantScope()` 与租户插件 / SQL Model 的身份覆盖语义不一致

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/sqlbuilder/sqlbuilder.go`  
**函数：** `TenantScope()`

### 当前实现

`TenantScope()` 直接使用：

```go
frameworkcontext.TenantID(ctx)
```

而 `support/tenant` 和 `support/sqlmodel` 都会优先检查：

```go
tenant.OverrideTenant(ctx)
```

也就是 `tenant.WithTenant(ctx, id)` 设置的显式租户覆盖值。

### 问题

同一个 ctx 如果同时存在：

- frameworkcontext 中的租户 A；
- `tenant.WithTenant(ctx, B)` 显式覆盖租户 B；

那么：

- GORM tenant 插件使用 B；
- `sqlmodel.Resolver` 使用 B；
- `sqlbuilder.TenantScope()` 使用 A。

同一请求的不同 SQL 构造路径因此可能按不同租户过滤。

### 修复建议

`TenantScope()` 应复用 `tenant.OverrideTenant()` 的优先级语义，或将租户 ID 的解析逻辑统一到一个不会产生循环依赖的公共位置。

同时应增加一致性测试，验证三条路径对同一 ctx 得到相同租户。

另外，`TenantScope()` 当前没有校验 `table == nil`，调用方误传 nil 会 panic。建议增加 nil 防护，至少避免公共辅助函数因无效参数直接崩溃。

## P2-03：`BindAll()` 校验失败时，Router 已经被部分注册

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**仓库：** `bingcool/gofy-adapter`  
**文件：** `adapter/gofy/bind.go`  
**函数：** `BindAll()`

### 当前实现

`BindAll()` 会遍历所有路由：

1. 校验 OperationID、HTTP Method、Path；
2. 检查实现方法与签名；
3. 解析中间件；
4. 校验 Auth；
5. 对当前通过的路由立即调用 `r.Handle()`。

直到循环结束，才检查 `problems` 并返回聚合错误。

### 问题

如果路由列表中：

- 前 5 条路由合法；
- 第 6 条路由存在签名错误；

那么前 5 条路由已经注册，第 6 条导致函数最终返回错误。

调用方看到的是“绑定失败”，但 Router 内部已经存在部分路由。

这会带来：

- 启动失败后的 Router 状态不完整；
- 如果调用方尝试修复配置并复用 Router，可能触发重复注册；
- 测试和 Reload 路径难以保证失败原子性；
- `RateLimit` 等装配回调也可能在整个路由集合尚未验证完时已经执行。

### 修复建议

把 `BindAll()` 改为两个阶段：

**阶段一：纯校验与构建待绑定列表**

- 校验所有路由；
- 解析所有中间件；
- 执行认证校验；
- 收集完整错误；
- 不调用 `r.Handle()`。

**阶段二：全部通过后统一注册**

只有第一阶段没有错误时，才真正注册路由。

不需要引入新的注册机制，只需避免“校验与副作用混在同一个循环里”。

## P2-04：SSE 自动重连没有执行普通请求的幂等重试判定

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**仓库：** `bingcool/gofy-interface-api`  
**文件：** `client/sse.go`  
**函数：** `StreamSSE()`、`openStream()`

### 当前实现

普通请求通过 `retryFor()` 控制重试：

- GET / HEAD / OPTIONS / DELETE 可以按配置重试；
- POST / PUT / PATCH 只有携带非空 `Idempotency-Key` 才允许重试。

但 SSE 路径在断连或 EOF 后，会直接重新调用 `openStream()`，没有复用 `retryFor()` 的判定。

### 风险

如果某个 SSE 契约使用 POST（当前契约层没有在 `StreamSSE()` 入口强制 GET），那么：

- 第一次 POST 已经到达服务端；
- 服务端可能已经执行了业务动作；
- SSE 连接随后断开；
- 客户端自动重连，再次发送相同 POST；
- 业务动作可能重复执行。

标准 SSE 通常使用 GET，但当前 API 并未将这一点作为强制约束。

### 修复建议

最小修复有两种，建议优先选择约束更明确的一种：

- `StreamSSE()` 只接受 GET；
- 或者如果允许写方法，则必须像普通请求一样检查 Idempotency-Key，缺少幂等键时禁止自动重放。

增加 POST SSE 断连重连测试，确保不会无幂等保护地重复提交。

## P2-05：SQL 脱敏默认规则仍是导出可变切片

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/sqllog/sqlmask.go`  
**变量：** `DefaultMaskColumns`、`DefaultMaskPatterns`

### 当前实现

两个默认安全规则均以导出变量形式暴露：

```go
var DefaultMaskColumns = []string{...}
var DefaultMaskPatterns = []string{...}
```

`defaultMaskSet()` 每次构造规则时会读取这两个切片。

### 问题

外部包可以直接修改：

```go
sqllog.DefaultMaskColumns = nil
```

也可以修改其中的元素。

结果是框架内置脱敏规则可能被业务代码、测试代码或初始化代码意外改变。

如果并发修改，还可能造成 data race。

这与“内置规则不可被项目配置删除”的安全设计目标不一致。

### 修复建议

- 内部默认规则改成私有变量；
- 如确实需要对外查看，提供返回副本的函数；
- 内部构造 maskSet 时只读取私有默认规则；
- 增加测试：调用方修改返回副本，不得影响框架内部规则。

## P2-06：SQL 脱敏对批量 INSERT 的列和值映射仍不完整

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/sqllog/sqlmask.go`  
**函数：** `maskSQL()`

### 当前实现

扫描器主要依靠最近出现的标识符跟踪字段和值的关系。

代码注释已经明确说明：

> 批量 INSERT 的按列位置映射属于当前不支持的 SQL 形态。

例如：

```sql
INSERT INTO users (employee_no, remark)
VALUES ('sensitive-number', 'text')
```

扫描器没有完整建立 INSERT 列表与 VALUES 各位置之间的映射关系。

如果 `employee_no` 是业务自定义敏感字段，且值没有命中内置正则模式，日志中可能保留原始值。

### 风险

这不是一个未说明的 Bug：当前实现已明确承认该形态不支持。

但 SQL Logger 是安全相关组件，调用方容易把“启用了 SQL 脱敏”理解为所有 SQL 都会被完整脱敏。因此应避免在生产使用中形成错误安全预期。

### 修复建议

- 将当前支持边界写入 SQL Logger 的配置说明与生产使用说明；
- 增加普通 INSERT、批量 INSERT、自定义敏感列的测试；
- 在完整支持之前，不要宣称批量 INSERT 的自定义敏感列也能可靠脱敏；
- 如果无法确认某种 SQL 形态是否安全，优先避免输出其原始参数值。

本项属于 P2 安全边界，不建议为了追求 SQL 解析器完整性而扩展成一个大型 SQL Parser。

## P2-07：AccessLog 默认记录 Query，敏感参数黑名单无法覆盖所有业务参数

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**文件：** `support/middleware/accesslog.go`  
**相关配置：** `DefaultAccessLogOptions()`、`SensitiveQuery`

### 当前实现

默认配置：

```go
LogQuery: true
```

同时提供了一组默认敏感参数黑名单，例如 password、token、signature、api_key 等。

### 问题

黑名单只能覆盖已知常见字段，不能覆盖业务自定义参数，例如：

```text
?customer_identity=...
?bank_account=...
?reset_code=...
?private_business_key=...
```

只要业务没有主动把参数名加入黑名单，Query 就可能被写入访问日志。

### 修复建议

生产默认建议关闭 Query 记录。

如果确实需要记录 Query，应使用业务白名单，而不是依赖一个不断扩充的敏感参数黑名单。

不需要删除现有 Query 记录能力，只需调整默认安全策略和配置说明。

## P2-08：K8s Lease Close 不等待已在途的 API 操作完成

> **状态（2026-11-21）：已修复，见 §2.2。** 以下保留审计时的原始分析。


**仓库：** `bingcool/gofy-adapter`  
**文件：** `adapter/lease/k8slease/k8slease.go`  
**相关函数：** `Locker.Close()`、`Acquire()`、`Renew()`、`Release()`

### 当前实现

`Close()` 会：

- 设置 `closed=true`；
- 清空本地 `held` reservation；
- 阻止后续 Acquire。

但它不会等待已经开始的 Acquire / Renew / Release 操作结束，也不会取消这些操作。

单次 API 请求有 `RequestTimeout`，因此不会无限挂起；但 `Close()` 返回时，已经在途的请求仍可能继续访问 Kubernetes API。

### 风险

如果应用把 `Close()` 理解为“Locker 已完全停止，不会再访问 API”，这个语义并不成立。

在模块退出或 Reload 时，可能出现：

- Close 已返回；
- 旧的 Renew 仍在执行；
- 底层 client 正在被其他组件释放；
- 退出日志已经显示 Locker 关闭，但仍有在途请求。

### 修复建议

先明确 `Close()` 的真实语义。

如果现有架构要求 Close 返回后不再有 API 操作，应增加在途操作的生命周期协调；如果不要求，则至少在文档中明确：

- Close 只阻止新 Acquire；
- 已经取得的 handle 仍可 Renew / Release；
- Close 不等待已在途的 API 调用。

此项属于生命周期边界，建议结合真实 Shutdown 顺序测试后再确定修复范围，不应直接改变 Lease 的释放语义。

# 5. 本轮确认已改善 / 暂不列为新问题的内容

以下项目重新核对后，未发现需要重复列为本轮新增 Bug 的证据：

| 模块 | 本轮确认 |
|---|---|
| `support/consumer` | Schema 初始化已使用调用方生命周期 Context，并加独立超时；上一轮 Background Context 问题已修复 |
| `support/amqp` / `support/kafka` | Trace Header 合并已复制调用方 Map，避免直接修改调用方数据 |
| `examples/demo-service/app/appio/io_runtime.go` | 资源按打开顺序逆序关闭；启动失败回滚错误已与原始错误聚合 |
| `support/uuidint` | StopPoolContext 超时后不提前关闭发送 goroutine 正在使用的 channel；缓冲关闭由后台 goroutine 退出后完成 |
| `support/sqllog/sqlpolicy.go` | 程序化 Config 与 YAML Config 的启用语义已通过 `configured()` 补齐 |
| `gofy-interface-api/client/encode.go` | 指针显式零值与路径参数零值处理已有对应修复 |
| `gofy-interface-api/client/sse.go` | 单行与单事件累计字节数均有限制，超限后不再无意义重连 |
| `gofy-adapter/client/nacos/resolver.go` | 零值 Resolver、nil receiver 的初始化错误处理仍有效；失败刷新、权重为零、fallback 错误身份等处理已存在 |
| `gofy-adapter/adapter/lease/k8slease` | API 请求超时、TTL int32 上限、同一 handle 的 Renew / Release 串行化、Close 后拒绝新 Acquire 均已实现 |

# 6. 三个仓库的整体复核结论

## 6.1 gofy 主仓库

本轮主要风险集中在新加入的租户隔离链路，而不是 SQL Builder 本身。

### 租户隔离

整体架构方向是合理的：

- 租户 ID 从 Context 获取，不放进共享插件字段；
- GORM 插件按 Model Schema 判断是否有租户列；
- 缺少租户 ID 时 fail-closed；
- `tenant.Without()` 明确表达特权绕过；
- `sqlmodel` 与 GORM 插件共享 Handler；
- 配置开关同时控制插件和 SQL Model Resolver。

但目前有两个关键边界没有闭合：

1. 已有租户等值条件没有校验其参数值；
2. INSERT 显式租户值默认可信。

这两项应先修复，再考虑将租户隔离作为生产安全边界使用。

### SQL Builder

本轮没有发现足够证据支持把已在 README 明确写出的使用约束重新认定为 Bug：

- Predicate 是有状态对象，不能跨请求复用；
- SelectTable 会被写入 Dialect 状态，不能跨方言复用；
- PostgreSQL 的占位符不能与手写 `?` 混用；
- `OnConflict` 必须明确方言；
- `Build()` 必须用于取回 builder 内部错误；
- JOIN 中每张租户表都要分别挂租户条件。

这些限制本身可以接受，但业务代码必须遵守。

### 配置与资源生命周期

本轮抽查到的 Consumer 初始化、IO 回滚、资源逆序关闭、UUID Pool 停止逻辑，上一轮已指出的问题基本都有对应修复。

## 6.2 gofy-interface-api

本轮重点复核：

- 请求构造与 Header 合并；
- 指针零值编码；
- Idempotency-Key 识别；
- 普通请求重试；
- SSE 重连与事件大小限制；
- Endpoint Auth / ResponseKind 元数据。

整体实现较上一轮更完整。

本轮最值得修正的是：

- SSE 自动重连应当与普通请求保持相同的幂等保护原则；
- 非 GET SSE 路由目前没有在客户端重连入口被明确禁止。

~~契约层的 `AuthRequired` 只是元数据，不会自动安装认证中间件，这一点需要由 adapter 的生产启动校验兜底。~~ 复核：本地提交已删除 `AuthRequired` 与 `AuthValidator`，认证改为分组/路由显式 `Use("auth")`，见 P1-03 复核。

## 6.3 gofy-adapter

本轮重点复核：

- Bind / BindAll / BindGroup；
- Middleware Registry；
- ~~AuthValidator~~（复核：本地已删除）；
- Nacos Resolver；
- Kubernetes Lease。

本轮发现：

- `BindAll()` 的验证与路由注册没有分成两个阶段，失败时存在部分注册；
- ~~AuthRequired 仍依赖可选的 AuthValidator~~（复核：机制已删除，本地不成立）；
- K8s Lease Close 的语义不等于等待所有在途 API 操作结束。

Nacos Resolver 上一轮针对零值、失败刷新、权重摘流、fallback 错误身份等修正仍然存在，没有发现新的确定性高优先级问题。

# 7. 建议修复顺序

建议不要同时大面积调整三个仓库，按风险顺序处理。

| 顺序 | 问题 | 优先级 | 原因 |
|---:|---|---|---|
| 1 | 租户等值条件必须校验参数值与当前租户一致 | P1 | 直接影响跨租户读取 |
| 2 | 普通 INSERT 不应信任调用方显式 tenant_id | P1 | 直接影响跨租户写入 |
| 3 | ~~生产环境必须校验 AuthRequired 对应的实际认证策略~~ | — | 复核：本地已删除该机制，本条不再适用（§2.1） |
| 4 | Tenant Handler 的空列名必须启动失败 | P2 | 防止错误配置静默关闭隔离 |
| 5 | TenantScope 与 WithTenant 的身份优先级统一 | P2 | 避免不同 SQL 路径按不同租户执行 |
| 6 | BindAll 改为先完整校验、后统一注册 | P2 | 保证装配失败不留下部分状态 |
| 7 | SSE 重连增加幂等保护或限定 GET | P2 | 避免非幂等请求重复执行 |
| 8 | SQL 脱敏默认规则改为内部不可变 | P2 | 防止安全基线被外部修改 |
| 9 | 明确 SQL 批量 INSERT 脱敏边界 | P2 | 避免敏感参数进入日志 |
| 10 | AccessLog 默认关闭 Query 或改为白名单 | P2 | 避免业务自定义敏感参数泄漏 |
| 11 | 明确 K8s Lease Close 的在途请求语义 | P2 | 避免 Shutdown 生命周期误判 |

# 8. 修复后必须补充的回归测试

## 8.1 Tenant

- [ ] ctx=A，调用方 `tenant_id=A`，插件不重复追加条件；
- [ ] ctx=A，调用方 `tenant_id=B`，最终只能读取 A；
- [ ] ctx=A，结构化 `clause.Eq` 指定 B，最终只能读取 A；
- [ ] ctx=A，INSERT 显式指定 B，普通业务配置最终写入 A；
- [ ] 自定义 Handler 返回空列名，插件装配失败；
- [ ] `tenant.WithTenant(ctx, B)` 与 frameworkcontext 中的 A 同时存在时，GORM / SQL Model / TenantScope 均使用 B；
- [ ] `TenantScope(ctx, nil)` 不 panic。

## 8.2 Adapter Bind

- [ ] 路由集合中前几条合法、后面一条非法时，Router 不留下任何部分注册；
- [ ] BindAll 全部通过后才执行实际 Handle；
- [x] ~~AuthRequired 没有实际认证策略时，生产启动失败~~ 不适用：该声明与校验器已删除；
- [x] ~~直接 Bind 的认证边界有明确测试~~ 不适用：`Bind` 只装显式传入的中间件，没有隐式认证可测。

## 8.3 Interface API Client

- [ ] GET SSE 断连后按预期重连；
- [ ] POST SSE 没有 Idempotency-Key 时不自动重放；
- [ ] POST SSE 带 Idempotency-Key 时按既定策略重试；
- [ ] 重连过程中取消 Context，事件 goroutine 能及时退出；
- [ ] 重连失败后 Body 被关闭，不残留连接。

## 8.4 SQL Log

- [ ] 修改默认规则返回副本，不影响内部默认规则；
- [ ] 普通 INSERT 敏感列脱敏；
- [ ] 批量 INSERT 的敏感字段边界测试；
- [ ] AccessLog 默认配置不会输出 Query；
- [ ] Query 畸形编码时不输出原始内容。

## 8.5 Lease

- [ ] Acquire 在途时 Close；
- [ ] Renew 在途时 Close；
- [ ] Release 在途时 Close；
- [ ] Close 返回后是否仍允许已持有 handle 操作，行为与文档一致；
- [ ] `go test -race` 覆盖并发 Acquire / Renew / Release / Close。

# 9. 最终判断

Gofy 当前的主要问题不是功能不够，而是新能力加入后，少数关键边界还没有达到框架应有的安全确定性。

其中租户隔离最需要谨慎：

- 不能把“调用方已经写了租户条件”当成“调用方写的是当前租户”；
- 不能把“调用方显式指定租户”当成“调用方有权写入该租户”；
- 不能让错误的 Handler 配置静默跳过租户过滤；
- 不能让 GORM、SQL Model、TenantScope 三条路径使用不同的租户身份优先级。

这几项修复不需要新增任何框架功能，也不需要重做架构。截至 2026-11-21，租户隔离语义、Adapter 装配原子性、SSE 重试边界与日志默认值均已在本地工作区修复（§2.2）；认证校验一项因机制删除而失效（§2.1）。

**建议本轮只做上述修复，不继续扩展 SQL Builder、租户插件或客户端能力。**
