# worker 取连接的现状盘点与迁移方案

- 状态：**已被修订**（2026-11-19 晚）。第 0～4 批曾全部落地（见 §10），但 §10 引入的 `workerio` 与
  `tenantguard` 是"用 `Runtime.DB` 伪造一代"的补丁；改由进程装配入口承担之后两者已删除，见 **§11**。
  本文保留原始盘点与分批依据，便于回溯"为什么先这么改、后来为什么换掉"。
- 起点：`core.Runtime` 的 `DB` 字段是框架启动期持有的 `db.default`，worker 进程没有装配代，
  业务只能从 `Runtime` 拿连接；HTTP 与脚本走的是 `appio.WithContext(ctx).DBIO(...)`。
  两套取法并存，且 worker 拿不到命名实例。
- 结论：**业务查询共 6 个逻辑取用点、8 处真实语句、0 处需要命名实例**；
  盘点顺带发现一个既有缺陷——开启 `tenant.enabled` 后认领生产者每轮失败（见 §4.1）。

## 1. 口径与计数

`grep -rn "rt\.DB" worker/`（不含 `_test`）共 **15 处文本引用**：

| 口径 | 数量 | 说明 |
|---|---|---|
| 文本引用 | 15 | 含 nil 检查与传参 |
| nil 检查 | 6 | `if !ok \|\| rt.DB == nil` |
| 真实取用 | 9 | 发起语句或把句柄交给 Runner |
| 业务侧文本 | 13 | 扣除 `tenantguard` 的 2 处（装插件，不是查询） |
| 业务侧真实取用 | 8 | §3 表格逐条列出 |
| 逻辑取用点 | 6 | handler、scan、claim 生产者、claim 消费者、summary、tenantguard |

此前口头估算的"17 处"偏高，以本文的 15/13/8 为准。

## 2. 两级取法的现状

| 入口 | 取连接方式 | 是否随代际变化 | 能取命名实例 |
|---|---|---|---|
| HTTP 请求 | `appio.WithContext(ctx).DBIO(ctx, "default")` | 是（Reload 换 Set） | 是 |
| 脚本 | 同上（`script/withIO` 先起 module 再发布） | 是（每次脚本一代） | 是 |
| worker | `rt.DB` | 否（进程级单例） | **否** |
| 框架内部 | 健康检查、`instrumentDB`、module factory 判空 | — | — |

`app/module/**` 里没有一处直接用 `rt.DB`；worker 的 8 处取用是唯一的业务旁路。

关键事实：`appdb.NewSet(db)` 建出的 Set **没有 registry**（`app/appio/db/db.go:64,244-249`），
`DBIO(ctx, "order")` 会返回 `ErrNotConfigured`。所以"改成 DBIO"本身不会让 worker 拿到命名实例，
要拿得走 `appdb.Open`（见 §6 决策 3）。

## 3. 逐处清单

| # | 位置 | 用途 | 语句形状 | 需要命名实例 | 租户归类 |
|---|---|---|---|---|---|
| G1-1 | `worker/group1/handler_order.go:104,107`（语句见 `:144`、`:161`） | 消费状态迁移：条件 UPDATE，0 行时回读区分"未落库 / 已达成 / 过期" | `Model(&model.Order{})` 具名模型 | 否 | 按事件租户；`orders` 无 `tenant_id` 列，插件不改写 |
| G1-2 | `worker/group1/scan.go:59,88` | 每 5s 统计待处理订单数 | `Table("orders").Count(&int64)` | 否 | **跨租户**，已 `tenant.Without`（`scan.go:85`） |
| G1-3 | `worker/group1/scan_claim.go:174,182` | 生产者启动时校验任务表存在 | `Migrator().HasTable` | 否 | 不走回调 |
| G1-4 | `worker/group1/scan_claim.go:232`（写入见 `:289`） | 每轮把一批任务行写进任务表（单事务） | `Table(cfg.Table).Create(map[string]any)` | 否 | **跨租户，但未声明** → §4.1 |
| G1-5 | `worker/group1/scan_claim.go:306,314,326` | 消费者校验表 + 把池交给 `claim.NewRunner` | `claim` 包内部全是 `Raw` / `Exec` | 否 | 不改写（不经 clause builder） |
| G1-6 | `worker/group1/scan_claim.go:364`（查询见 `:464`） | 结算：按用户聚合订单 | `Table("orders").Scan(&匿名 struct)` | 否 | 实测不改写（Schema 无租户列，见 §4.2） |
| G2-1 | `worker/group2/summary.go:59,83` | 每小时按状态聚合订单 | `Table("orders").Scan(&[]statusCount)` | 否 | **跨租户**，已 `tenant.Without`（`summary.go:80`） |
| TG | `worker/tenantguard/tenantguard.go:72,75` | 在框架池上安装租户插件 | 非查询 | — | 不改写 |

**需要命名实例的业务点：0 处。** `config.yaml` 的 `db` 段配了 `default` / `order` / `product`
三个实例，但 worker 进程只拿得到 `default`，另外两个从不打开——这是能力缺口，不是当前需求。

## 4. 盘点发现

### 4.1 认领生产者的 map INSERT 在开租户后失败关闭（既有缺陷）

`enqueueClaimJob` 用 `Table(...).Create(map[string]any)` 写入，map 没有 schema，
插件无法判定租户列，于是走 `support/tenant/plugin.go:149-157` 的 fail-closed 分支：
`resolve` 返回 `tenantID(...)`，ctx 无租户即在 `:176` 写入 `ErrTenantRequired`。

实测（临时程序，见 §9）四组形状：

```
insert(map, no tenant)  => tenant: tenant id is required: table job_task
scan(anon struct)       => no such table: orders        ← 无租户错误
count(&int64)           => tenant: tenant id is required
insert(map, Without)    => no such table: job_task      ← 通过租户检查
```

后果：把 `tenant.enabled` 置为 true 后，`order-settle-producer` 每轮报错，
认领示例链路（生产者 + 消费者 + Runner）整体不可用。**与迁移无关，现在就有。**
修法：在该 INSERT 上用 `tenant.Without` 声明跨租户特权形状，或给任务表补具名模型。

### 4.2 两处"看起来有问题、实际不受影响"的形状

- `settleOrderJob` 的 `Scan(&匿名 struct)`：GORM 能解析出 Schema，但其中没有 `tenant_id` 字段，
  插件按"表不含租户列"放行（`plugin.go:161-163`）。实测只有"表不存在"错误。
- `claim` 包的 `Raw` / `Exec`：不经 clause builder，插件不介入（`support/claim/claim.go:505-749`）。

迁移时不要"顺手"给这两处加 `Without`——它们没有失败关闭的问题，加了反而掩盖真实语义。

### 4.3 worker 没有代，是"取不到"的根因

框架在 `worker_host.go:103-128` 建完 `core.Runtime` 就立刻构造 Runner，
中间没有 app 可见的就绪钩子；worker 也不跑 `ModuleFactories`（`start.go:584` 的
`startModules` 只在 HTTP 路径调用）。因此 worker 进程里 `appio.Current()` 始终为 nil，
`appio.WithContext(ctx)` 会在第一次取用时 panic（panic 语义见 `app/appio/snapshot.go:176-182`）。

要统一取法，必须先让 worker 拥有一个"只有数据库"的装配代。

## 5. 目标形态

三级所有权，各归其位：

```
App（打开 / instrument / 关闭 db.default，生命周期由框架持有）
  └─ 装配代 Snapshot（worker 进程一个，只含 db 子集）
       └─ 业务只写 appio.WithContext(ctx).DBIO(ctx, "default")
```

`Runtime.DB` 保留给装配层与框架内部（健康检查、`instrumentDB`、nil 判定），
不再作为业务取连接的入口。

## 6. 分批迁移清单

每批独立可验证、可回滚；前两批不改业务行为。

### 第 0 批：修 §4.1 的失败关闭

- 改动：`enqueueClaimJob`（`scan_claim.go:289`）的 INSERT 加 `tenant.Without`，并写清
  "任务表是跨租户的特权形状"；新增用例"开启租户时插入成功"。
- 验证点：
  1. 新用例在 `tenant.enabled=true` 下通过，关闭时语句与现在一致；
  2. 起 `worker --group=group1`，`claim jobs enqueued` 日志恢复，无 `ErrTenantRequired`；
  3. `go test ./worker/group1/` 全绿。
- 回滚：单行改动，删掉 `Without` 即恢复（但缺陷会回来）。

### 第 1 批：worker 装配代（基础设施，不改业务取法）

- 新增 `worker/workerio`：
  - `Ensure(rt *core.Runtime) (*appio.Snapshot, error)`：`appdb.NewSet(rt.DB)` → 发布 Snapshot，
    `sync.Once` 幂等；**每个 worker 的 `Run` 入口先调它**，因为分组并发启动，
    不能只在"第一个分组"发布；
  - `Reset()`：`appio.Publish(nil)`，供进程退出时收回。
- 需要 `appio` 补一个只带 db 的构造导出（当前 `newSnapshot` 未导出，`PublishDB` 只用于测试语义）。
- 验证点：
  1. `Ensure` 连续调用两次返回同一 Snapshot，`appio.Current()` 非 nil；
  2. `appio.WithContext(ctx).DBIO(ctx, "default")` 返回非 nil，且与 `rt.DB` 是同一个池；
  3. `Reset()` 后 `Current() == nil`；
  4. `Set.Close()` 不会关闭 `rt.DB`（`NewSet` 的 registry 为 nil，`db.go:275-283`）——
     用一个 ping 断言钉住；
  5. `worker --list` 与 `worker --group=group1` 启动正常。
- 回滚：新增包 + 每个 Run 顶部一行调用，删除即恢复。

### 第 2 批：group1 四个取用点改 DBIO

- 改动：
  - `handler_order.go:107`：`db, err := appio.WithContext(ctx).DBIO(ctx, appdb.DefaultInstance)`；
  - `scan.go:88`、`scan_claim.go:182,232,314`：同样在入口解析一次，后续语句用该句柄；
  - `scan_claim.go:326,364`：`NewRunner` 与 `settleOrderJob` 的入参由调用方解析后传入，
    不改这两个函数的签名。
- 验证点：
  1. 现有 4 条 tenant 用例改造后仍通过；
  2. 起一轮 worker，`order scan tick` / `claim batch` / `order settled` 日志与迁移前一致；
  3. `grep -rn "rt\.DB" worker/` 只剩 `tenantguard`（2 处）；
  4. 开租户跑 60s，无 `ErrTenantRequired`。
- 回滚：按文件回退，行为等价。

### 第 3 批：group2 summary 改 DBIO

- 改动：`summary.go:83` 同上；跨租户聚合的 `Without` 保持不变。
- 验证点：报表日志 9 个状态字段不变；开租户时无 `ErrTenantRequired`；
  `worker --group=group2` 能跑满一个周期（或临时把 `IntervalMS` 调小验证一次）。
- 回滚：同第 2 批。

### 第 4 批：守卫与文档

- 改动：
  - `shell/check-imports.sh` 加 worker 规则：禁止 `rt.DB`，白名单 `tenantguard` / `workerio`；
    现有守卫只扫 `./app/...`，worker 完全没有约束；
  - `worker/README.md:13` 更新"worker 进程不运行 app/appio"的表述；
  - `docs/gorm-tenant-design.md` §11.1 补一句取连接路径。
- 验证点：
  1. 故意在 `worker/group1/scan.go` 写一行 `rt.DB`，守卫应失败并指出文件与规则；
  2. 恢复后 `make check` 全绿（当前 28 包）。
- 回滚：删规则即可，守卫本身不影响运行。

## 7. 三个决策点

1. **发布时机**：框架在 `worker_host.go:103-128` 建完 rt 立刻起 Runner，没有 app 可见的就绪钩子，
   所以只能在每个 Run 入口做幂等 `Ensure`。不引入新的框架钩子——那是另一件事。
2. **谁负责收回**：`NewSet` 不拥有 default 池，不 Reset 也不泄漏连接；但不 Reset 会让
   `appio.Current()` 指向进程退出后已关闭的池。建议 `main.go` 的 worker 分支
   `defer workerio.Reset()`（`plan.Kind` 已能区分 worker 与 http-start）。
3. **命名实例现在开不开**：`appdb.Open(ctx, rt.DB, cfg, rt)` 能建带 registry 的 Set，但会把
   `order` / `product` **急切建连**（`db.go:224-231`，每个池 2~5 条连接）。现在 0 处需要，
   建议第 1 批只统一 default；等出现真实消费方再传过滤后的 `cfg` 开命名实例。

## 8. 明确不动的三处

- **不删 `Runtime.DB`**：健康检查、`instrumentDB`、module factory 判空都依赖它；
  删掉之后 worker 无路可走，问题会从"取法不统一"变成"取不到"。
- **不改 `claim.NewRunner` 签名**：它按进程持池，与"每 tick 取连接"不冲突；
  改签名属于另一件事，且会牵动 `support/claim` 的公开 API。
- **不把 `tenantguard` 改成 DBIO**：它需要在"框架持有的那个池"上装插件；
  走快照会让"插件装在哪一代"变含糊，而 worker 本来就没有换代。

## 9. 附：实测方法与输出

临时程序（`/tmp/tenantprobe`，`replace` 指向本仓库，未改动仓库文件）用 SQLite 建空库，
`tenant.Instrument` 后依次执行四种语句形状，输出见 §4.1。
`scan(anon struct)` 一组返回的是"表不存在"，说明租户检查没有介入；
其余两组无 `Without` 时在构造阶段就被 `ErrTenantRequired` 短路，SQL 未发出。

## 10. 落地记录（2026-11-19）

五批全部完成。逐批对照 §6：

| 批次 | 实际改动 | 验证 |
|---|---|---|
| 0 | `enqueueClaimJob` 的 map INSERT 加 `tenant.Without`（`worker/group1/scan_claim.go`） | 新增 `TestClaimEnqueueIsCrossTenant`：租户开启时入队成功、行数正确 |
| 1 | 新增 `worker/workerio`（`Ensure` / `Use` / `Reset` / `Published`）；`appio.NewSnapshotDB` 导出 | 4 条用例：发布一次、`Use` 固定代际、缺库报 `ErrDatabaseRequired`、`Reset` 幂等且撤回 |
| 2 | group1 四个取用点（handler、scan、claim 生产者、claim 消费者）改走 `workerio.Use` | `grep rt.DB worker/` 只剩 `tenantguard` 与 `workerio`；原有 handler 状态机与 tenant 用例全过 |
| 3 | group2 `summary` 同上 | 报表用例与 `make check` 通过 |
| 4 | 守卫加到 `shell/check-io-access.sh`；`worker/README.md`、`docs/gorm-tenant-design.md` §11.1 更新 | 注入一行 `rt.DB` 时守卫失败并打印文件行号；删除后恢复通过 |

### 与计划的差异

1. **多了一个 `workerio.Use`**。计划里各入口写 `Ensure` + `DBIO` 两步；实际把它合成一个入口辅助：
   发布代际、`appio.WithSnapshot` 固定到 ctx、再经 `DBIO` 解析。固定的必要性在于 tick 可能活过一次
   `Reset`——固定后仍解析启动时那个句柄，而不是跟随全局。下游代码仍是
   `appio.WithContext(ctx).DBIO(...)`，没有引入第二种取法。
2. **守卫落在 `check-io-access.sh`，不是 `check-imports.sh`**。它检查的是"怎么取句柄"，与那份
   已有的 service/repository/query 规则同属一类；`check-imports.sh` 只看 import 关系，看不出 `rt.DB`。
   同时加了一条：`worker/` 内除 `workerio` 外不得调用 `appio.Publish/PublishDB/Current`，避免第二个发布点。
3. **`Reset` 接到了 `main.go`**：`app.Execute` 返回后、仅 `plan.Kind` 以 `worker` 开头时调用。
   HTTP、脚本与控制类命令走各自的模块生命周期，不需要它。

### 仍未做

**命名实例**（§7 决策 3）保持未开：`workerio` 用 `appdb.NewSet(rt.DB)`，只有 `db.default`，
`DBIO("order")` 返回 `ErrNotConfigured`。当前 0 处需要；要用时按决策 3 改成 `appdb.Open` 并传过滤后的
配置，代价是 `order` / `product` 会急切建连。

### 验证中发现并修复的并发缺陷

第一次带着 `tenant.enabled=true` 真跑 `worker --group=group1` 时，GORM 打出
`duplicated callback gofy:tenant:before_delete/before_create`：`installTenant` 的
"先查 inspector、再 Instrument" 不是原子的，三个 worker 同时首用数据库时有两个都看到了
"尚未安装"。已在 `app/appio/db/db.go` 加包级 `installMu` 把检查与安装串行化（安装每池一次，
不在热路径）；重跑后告警消失，入队、认领、扫描都正常。

### 一处测试约束

`workerio` 的代际是进程级，而 `worker/group1` 的测试辅助为每个用例新开 SQLite。`newOrderHandlerContext`
因此注册了 `t.Cleanup(workerio.Reset)`：不撤回的话，下一个用例会解析到上一个（已关闭）的池，
报 `sql: database is closed`。新增的 worker 测试若自己造 Runtime，也需要同样的清理。

## 11. 修订：由装配入口取代 workerio（2026-11-19 晚）

### 11.1 为什么推翻 §10

§10 的做法是"每个 worker 入口用 `Runtime.DB` 发布一份只带 default 的代际"。它有三处说不通：

1. **它证明了缺的是装配入口，而不是缺一条取法。** worker 复用 service 与 repository，
   而那两层只认 `appio.WithContext(ctx).DBIO(...)`；它们不需要"Runtime 上有个 DB 字段"，
   需要的是"进程里有一代已发布的句柄"。
2. **租户插件装错了位置。** 插件装在池上，而 `tenantguard.Ensure(rt)` 只能在拿到 `rt.DB`
   之后装；一个早于消费者启动的扫描循环、或者一条都没收到的消费者，都要靠"每个入口各装一次"
   来兜底。装配点是唯一能看到这一代全部池的位置，`appdb.WithTenant` 本来就为此存在。
3. **命名实例拿不到。** `appdb.NewSet(rt.DB)` 没有 registry，`DBIO("order")` 报
   `ErrNotConfigured`。而 worker 一旦复用依赖 `db.order` 的 service，就必须有 registry。

### 11.2 新形态

| 层 | 职责 | 代码 |
|---|---|---|
| 框架 | 提供 worker 装配钩子：Runtime 就绪后 `factory(rt)` → `Start`，分组 drain 后由 cleanup `Stop`（早于关池） | `option.go` `WithWorkerModules`、`worker_host.go` `startWorkerModules` |
| 应用装配 | 声明"worker 进程需要哪几段 IO"，本例只有 db | `appio.NewDBOnlyModuleFactory(cfg.DB, cfg.Tenant.OpenOptions()...)` |
| 业务 | 与 HTTP 完全一致 | `appio.WithContext(ctx).DBIO(ctx, "default")` |

要点：

- **`Register` 从不被调用**：worker 没有 Router，钩子复用 `ModuleFactory` 类型但不走路由阶段。
- **不需要代际固定**：worker 进程不参与 Reload，`Publish` 一次即恒定，`WithSnapshot` 与
  `workerio.Reset` 都没有存在理由。
- **只装需要的那一段**：`appredis.Open` / `appkafka.Open` / `appamqp.Open` 对每个 enabled 实例
  同步建连，全量装配会和消费者自建的 broker 连接重复。裁剪由应用声明，框架不猜。
- **default 池仍归框架**：`Runtime.DB` 是"框架托管池的引用"，只在装配层出现；业务侧读到它就是接错，
  守卫脚本直接拦（无白名单）。

### 11.3 删除与保留

删除：`worker/workerio`（4 文件）、`worker/tenantguard`（2 文件 + 测试）、6 个入口的 `Use`/`Ensure` 调用、
`main.go` 的 `workerio.Reset`、守卫脚本的两处豁免。

保留：`appio.NewSnapshotDB` 与 `PublishDB`（脚本与单测仍用它们构造只带 default 的一代）、
`appdb.EnsureTenant`（测试模拟装配、以及未来自建池的入口）。

`withOrderTenant` 收窄成"只把事件租户带进 ctx"：插件由装配安装，它不再需要 `rt`，也因此不会
把某条路径绑死在一个池上。

### 11.4 命名实例：从"未做"变为"已具备"

§10「仍未做」的那条不再成立。`NewDBOnlyModuleFactory` 走 `appdb.Open`，`db.order` / `db.product`
与 HTTP 路径一样打开并进 registry。实测启动日志：

```
{"msg":"db instance connected","instance":"order"}
{"msg":"db instance connected","instance":"product"}
```

### 11.5 验证

| 场景 | 结果 |
|---|---|
| `make check`（29 包，含 `-race`） | 全绿 |
| 框架 `go test ./...` | 全绿（新增 5 条 `worker_module_test.go` 用例） |
| `worker --group=group1 --name=order-scan`（tenant off） | 装配先于分组，tick 正常，无 `runtime database is required` |
| `worker --group=group1 --name=order-scan`（tenant on） | 插件在装配期已装，tick 正常 |
| `worker --group=group1 --name=order-settle-producer`（tenant on） | 入队 6 行成功（跨租户 map INSERT） |
| `worker --group=group1 --name=order-settle-consumer`（tenant on） | 认领、结算、租约全部正常 |
| 失败路径 | factory 报错 → 进程不启动、worker 从未运行；A 成功 B 失败 → A 被 Stop |

### 11.6 行为变化的两处细节

1. **报错时机**：以前没有 db 时是每个 tick 报一次 `ErrNotConfigured` 并无限退避；现在
   装配失败会让进程启动失败。但"没有 `db` 段"仍不是装配失败——`db` 是可选段，
   `NewDBOnlyModuleFactory` 照样装配，失败推迟到首次取用，与请求一致。
2. **关停顺序**：`Stop` 由 `a.started` 驱动，排在分组 drain 之后、`gofydb.Close` 之前。
   worker 装配走的正是 HTTP 模块同一套 cleanup 逆序停止逻辑，没有第二条关闭路径。
