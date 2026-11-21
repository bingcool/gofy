# gofy 架构与代码审计报告（2026-11-21 Round 3，Rev.5）

> 本版是对同日 Round 3 初版的**二次代码复核**：逐条回到源码验证，修正了 3 处分级、
> 2 处影响描述和若干机制细节，未新增发现。Rev.3 落地两条 P1，Rev.4 落地八条 P2，
> Rev.5 落地六条 P3；P3-05 是取舍问题，已确认「保持 int32 并补说明」。初版的问题编号
> 在本版有调整，对照见 §0。

## 0. 修订记录（Rev.2，2026-11-21 16:xx）

复核方式：对初版 17 条发现 + 2 条排除项逐条打开源码重读，重点检查「结论是否成立」
「触发条件是否完整」「影响是否被高估」。结论：**17 条全部成立，但 3 条分级偏高、
2 条影响描述不准确**，已修正；2 条排除维持。

| 初版编号 | 本版编号 | 变化 | 原因 |
|---|---|---|---|
| P1-02 worker `/healthz` 回退 | **P2-06** | P1 → P2 | 只在 `ready` 尚未置位、且 `setStopping` 先于 `setReady` 执行时发生；窗口是 Ready 信号已入缓冲、主循环尚未消费的瞬间。一旦 `ready` 已置位，`if !allReady \|\| ready` 会阻止二次 `setReady`，不会回退 |
| P1-04 Nacos 回滚后文件停留失败版本 | **P2-08** | P1 → P2 | 影响描述被高估：进程重启时 `main` 会先执行 `nacos.Sync`，Nacos 可达时用远端 D1 覆盖文件，不会加载失败版本 D2；Nacos 不可达时启动直接失败。真实影响是运行期磁盘与内存不一致、外部工具读到失败版本 |
| P1-05 SSE 心跳不重置计数 | **P2-07** | P1 → P2 | 默认 `MaxEventBytes=1MiB` 下，纯心跳块约 6 字节/次，需要约 17 万次心跳才触发；有业务事件时每次 `dispatch` 都会重置。只有「长期无业务事件的纯心跳流」或「显式调小上限」才命中 |
| P1-03 SQL 脱敏 | **P1-02** | 维持 P1 | 复核确认 `LIKE`/`BETWEEN` 命中 `keywordSet`，`REGEXP` 不在表里但会被当作标识符覆盖列名，净效果相同 |
| 其余 13 条 | 编号见 §3 | 维持 | 复核确认代码路径与触发条件 |

**没有撤销任何一条**：初版 17 条在二次复核后均有源码证据。另新增了两处描述修正
（`--force` 的语义、SQL mask 的 `REGEXP` 机制），已写入对应条目。

## 0.1 修复记录（Rev.3–Rev.5，2026-11-21）

Rev.3 处理 P1，Rev.4 处理 P2，Rev.5 处理 P3；原则都是不新增功能、不做顺带重构。
P3-05 经确认保持 `int32`，只补说明，行为不变。

| 编号 | 修复文件 | 变更要点 | 验证 |
|---|---|---|---|
| P1-01 | `restart.go` | `isRestartIgnorableError` 递归遍历 `Unwrap() []error`，仅当全部叶子可忽略时才忽略；单链按内层判断 | `TestIsRestartIgnorableError` 增加 join 组合；`TestStopWorkerWildcardReportsPartialFailure` 用真实 helper 进程复现「一个已退出 + 一个拒绝退出」 |
| P1-02 | `support/sqllog/sqlmask.go` | 比较类关键字保留列上下文；`BETWEEN` 的两个边界都归列；只有 INSERT 语句的 `表 (` 才当列清单 | `TestMaskSQLComparisonKeepsColumnContext`（8 形态）与 `TestMaskSQLComparisonLeavesOtherColumnsAlone`（4 反向） |
| P2-01 | `start.go` | diagnostics 用独立 `min(2s, timeout)` 预算，主 server 与模块 Stop 保留完整预算 | `TestDiagnosticsShutdownKeepsTheBusinessBudget`：长 pprof 在途 + 业务请求在途，旧实现 EOF、修复后 200 |
| P2-02 | `cmd/gofy/main.go` | `go.mod` 始终走 `mergeGoMod`，`--force` 只重写其它文件 | `TestGenerateForceKeepsGoModRequirements` + 真实目录 `--force` 重跑 |
| P2-03 | 模板与 demo `main.go` | `envFromProcess` 在 `Setenv` 之前取值，reloader 复用同一变量 | `TestGenerateDefaults` 断言 `GOFY_ENV` 只读一次 |
| P2-04 | `docgen/schema.go` | `gt`/`lt` 生成排他约束；字符串/数组平移闭区间 | `TestApplyConstraintsExclusiveAndPointers` |
| P2-05 | `docgen/schema.go` | `applyConstraints` 入口解引用指针 | 同上，三个指针子用例 |
| P2-06 | `worker_host.go`、`worker_health.go` | 置 ready 前检查 `runCtx`；`setReady` 改为从 starting 的 CAS | `TestWorkerHealthStateIsMonotonic` |
| P2-07 | `client/sse.go` | `dispatch` 早退分支也重置 `eventBytes` | `TestStreamSSEKeepsHeartbeatsOutOfTheEventBudget` |
| P2-08 | `support/nacos/watch.go` | 记录 `written`，无变更分支发现文件领先时重新 Sync | `TestWatcherRestoresTheAppliedContentAfterRollback` |
| P3-01 | `start.go` | 最终 `cleanup` 把 `a.sqlLogger` 一起 Sync | `TestCleanupSyncsSQLLogger` 用计数 Syncer 断言 |
| P3-02 | `cmd/gofy/main.go` | 目标检查用全部模板输出路径，不受当前开关影响 | `TestGenerateAcceptsItsOwnOptionalDirectories` |
| P3-03 | `support/otel/config.go` | `BatchConfig.Validate` 按生效值校验，抽公共默认常量 | `TestConfigValidate` 两条新用例 + `TestBatchConfigAcceptsEqualEffectiveBounds` |
| P3-04 | `support/diagnostics/diagnostics.go` | 改正 pprof import 的注释，写明真实代价 | 注释修正，无需测试 |
| P3-06 | `client/sse.go` | `readLine` 比较时排除 `\r\n`，与事件级口径一致 | `TestStreamSSEAcceptsLineAtTheExactLimit`（LF/CRLF） |
| P3-07 | `adapter/gofy/docs.go` | 注册阶段拒绝 `DocsPath="/"`，错误信息给出替代 | `TestDocsModuleRejectsRootPath` |
| P3-05 | `docgen/schema.go`、README | 确认保持 `int32`：在类型映射处与 README 写明「`int`/`uint` 按 32 位表示，需要 64 位时显式声明」 | 无行为变化，不需测试 |

回归：三个仓库均通过 `go build ./...`、`go vet ./...`、`go test ./... -count=1`；
除 P2-06 与 P3-04 外，每条新用例都先在旧实现上确认失败（P3-04 是注释修正）。脚手架另做了
「新建模块 → 生成 → tidy → 编译 → 启动 → /healthz → worker --list → stop」的端到端验证。
详见各条目末尾的「修复记录」。

## 1. 基线与范围

| 仓库 | 本地 HEAD | 审计范围 |
|---|---|---|
| `gofy` | `7f163bb` | 根目录核心生命周期（gofy/start/stop/restart/reload/generation/worker_host 等）、`support/` 全量、`config/`、`logger/`、`command/`、`cmd/gofy` 脚手架、`examples/demo-service` 接线 |
| `gofy-interface-api` | `c722e00` | 契约类型、client（含 SSE）、docgen、docsui、OpenAPI 生成 |
| `gofy-adapter` | `0e81668` | adapter/gofy 全量、client/nacos 等 |

**不纳入**：`gofy-library`；第三方依赖源码（仅在确认 SDK 行为时查阅）；纯风格问题；
已有测试固定为「预期行为」的设计取舍。

**分级口径**

| 等级 | 含义 |
|---|---|
| P1 | 有现实触发路径，造成数据泄漏或重复消费，且现有机制不能兜底 |
| P2 | 明确缺陷，但需特定配置/命令/时序，或影响集中在运行期状态一致性、可观测性、工具链 |
| P3 | 边界一致性、资源收尾、文档措辞或极端配置下的行为偏差 |

## 2. 方法与复核

- 初版由六个分片独立扫描（核心生命周期、数据与消息、支撑设施、入口与脚手架、
  契约仓库、适配器仓库），再由三名复核员逐条验证。
- Rev.2 由主审逐条重读源码，重点复核 P1/P2 与两条排除项；下面每条都标注了
  「复核结论」，并给出实际代码路径而不是转述初版。
- 复核中特别检查了三类容易误报的情况：
  1. 代码事实成立但调用方每次新建实例（一次性契约）——见 §7；
  2. 代码事实成立但有测试把行为固定为预期（singleflight）——见 §7；
  3. 影响被高估（启动路径会在重启时自我修正）——见 P2-08。

## 3. 结论摘要

本版收录 **17 项**：P1 2 项、P2 8 项、P3 7 项。另有 2 项候选经复核属于刻意设计，
列在 §7「已排除」。Rev.3–Rev.5 已修复 P1 两项、P2 八项与 P3 六项；P3-05 经确认保持
`int32` 并补充说明。

| 编号 | 等级 | 仓库 | 一句话 | 复核结论 | 状态 |
|---|---|---|---|---|---|
| P1-01 | P1 | gofy | `worker restart` 把 joined error 里的 `ErrNotRunning` 当成整体可忽略，旧 worker 未停就启动新进程 | 成立，确定性逻辑缺陷 | ✅ 已修复 |
| P1-02 | P1 | gofy | SQL 脱敏在 LIKE/BETWEEN/函数包裹列上丢失列上下文，自定义敏感列明文入日志 | 成立，仅 `full` 档有字面量 | ✅ 已修复 |
| P2-01 | P2 | gofy | diagnostics 关闭与主服务、模块 Stop 共用停机预算，长 pprof 请求可截断优雅停机 | 成立 | ✅ 已修复 |
| P2-02 | P2 | gofy | 脚手架 `--force` 整体覆写 `go.mod`，与注释承诺的合并语义不一致 | 成立 | ✅ 已修复 |
| P2-03 | P2 | gofy / demo | `envFromProcess` 在 `Setenv` 之后重算，恒为 true，热更新不再让环境跟随配置 | 成立 | ✅ 已修复 |
| P2-04 | P2 | interface-api | docgen 把 `gt`/`lt` 映射成闭区间，文档放行校验层会拒绝的值 | 成立 | ✅ 已修复 |
| P2-05 | P2 | interface-api | docgen 未对指针类型解引用，`*string`/`*[]T` 生成错误的约束关键字 | 成立 | ✅ 已修复 |
| P2-06 | P2 | gofy | worker `/healthz` 可在停机开始后被翻回 ready | 成立，窗口窄（初版 P1-02） | ✅ 已修复 |
| P2-07 | P2 | interface-api | SSE 心跳/注释块不重置 `eventBytes`，长期纯心跳流被误杀 | 成立，默认上限下触发周期长（初版 P1-05） | ✅ 已修复 |
| P2-08 | P2 | gofy | Nacos apply 失败后远端回滚，本地文件停留在失败版本 | 成立，启动 Sync 会修正（初版 P1-04） | ✅ 已修复 |
| P3-01 | P3 | gofy | 最终 `cleanup()` 未 Sync 独立 SQL 日志通道 | 成立 | ✅ 已修复 |
| P3-02 | P3 | gofy | 关闭可选模块后重跑脚手架，自己生成的 `script/`/`worker/` 被误判为外来目录 | 成立 | ✅ 已修复 |
| P3-03 | P3 | gofy | OTel batch 校验在 `MaxQueueSize` 未显式设置时被跳过 | 成立 | ✅ 已修复 |
| P3-04 | P3 | gofy | `support/diagnostics` 对 `net/http/pprof` 的 import 形式注释与 Go 语义不符 | 事实成立，当前无暴露路径 | ✅ 已修复 |
| P3-05 | P3 | interface-api | docgen 把平台相关的 `int`/`uint` 固定映射为 `int32` | 取舍问题，非确定性错误 | ✅ 已决策 |
| P3-06 | P3 | interface-api | SSE 单行上限把换行计入，净内容恰好等于上限时被误判超限 | 成立 | ✅ 已修复 |
| P3-07 | P3 | adapter | `DocsPath="/"` 时只注册 `GET /`，Swagger UI 静态资源全部 404 | 成立 | ✅ 已修复 |

> 行号以各仓库当前 HEAD 为准；重构后请按函数名定位，不要只依赖行号。

---

## 4. P1：需要优先处理

### P1-01 `worker restart` 误判部分停止失败，旧 worker 未停就启动新进程

**仓库/位置**：`gofy`
- `stop.go:61-75` — `stopAllWorkers` 用 `errors.Join` 合并逐 pid 文件的错误
- `restart.go:32-36` — `isRestartIgnorableError` 对**整个 join** 调 `errors.Is`
- `worker_host.go:522-547` — `restartWorker` 的判定分支

**复核结论**：成立，确定性逻辑缺陷，无测试规避。

**触发条件**
执行 `worker restart` 的通配形态（`--group` 为空 / `all`），本机存在至少两个 worker pid
文件：一个进程已退出（`stopPIDFile` 返回 `ErrNotRunning`），另一个存活但 SIGTERM 后在
`--timeout` 内未退出且未给 `--force`（`stopPIDFile` 返回 `ErrStopTimeout`）。

**实际 vs 预期**
`errors.Join` 产生的错误实现了 `Unwrap() []error`，`errors.Is` 会遍历所有叶子；只要其中
一个叶子是 `ErrNotRunning` 就返回 true。于是「部分失败」被整判为「没有实例可停」，
`restartWorker` 打印「未发现运行中的 worker，直接启动」并调用 `runWorker`。
预期：只要存在任一非可忽略错误（`ErrStopTimeout`、`ErrIdentityUnverified` 等）就中止。

**影响**
同一 group 两个进程并存，重复消费同一队列；启用 `workerHealth` 时新进程还会因端口占用
启动失败，最终旧的没停、新的没起。

**复现思路**
参考 `worker_internal_test.go:166-214` 构造两个 pid 文件：一个写已退出 pid，另一个写忽略
SIGTERM 的 helper 进程；以 `--timeout=100ms`、`--force=false` 调
`app.ExecuteArgs(ctx, []string{"worker","restart","--group=all","--timeout=100ms"})`。
期望返回 `ErrStopTimeout`。

**修复建议（最小改动）**
`isRestartIgnorableError` 增加 `interface{ Unwrap() []error }` 递归分支：仅当**所有**叶子
都属于 `ErrNotRunning` / `ErrPIDFileDisabled` / `ErrUnsupported` 时才忽略。补 join 组合测试。

**修复记录（2026-11-21，已落地）**
- `restart.go`：`isRestartIgnorableError` 改为先判 `Unwrap() []error` 并逐叶子递归，全部
  可忽略才算可忽略；单链 `Unwrap() error` 按被包装的错误判断；`nil` 明确返回 false。
- 测试：`restart_internal_test.go` 增加 3 组 join 用例（全可忽略 / 混合失败 / 嵌套 join）；
  `worker_internal_test.go` 新增 `TestStopWorkerWildcardReportsPartialFailure`，构造「一个
  过期 pid 文件 + 一个忽略 SIGTERM 的 helper 进程」，以 `--timeout=150ms` 调通配符 stop，
  断言返回 `ErrStopTimeout` 且 `isRestartIgnorableError` 为 false。
- 验证：该集成用例在旧实现下失败（被整判为可忽略），修复后通过。

---

### P1-02 SQL 脱敏在 LIKE / BETWEEN / 函数包裹列上丢失列上下文

**仓库/位置**：`gofy/support/sqllog/sqlmask.go`
- `:398-400` — 关键字分支 `if keyword != "IN" && keyword != "NOT" { lastIdent = "" }`
- `:325-328` — 标识符后紧跟 `(` 即进入「INSERT 列清单」收集
- `:248-272` — `isSensitive` 完全依赖 `lastIdent` / `inListColumn` / `insertColumns`

**复核结论**：成立。次轮复核确认了三条具体机制：
1. `LIKE`、`BETWEEN` 在 `keywordSet`（`:489-499`）中，命中 `:398` 后 `lastIdent` 被清空；
2. `REGEXP` **不在** `keywordSet` 里，会被当作普通标识符把 `lastIdent` 覆盖成 `regexp`，
   净效果同样是丢列关联；
3. 函数名后紧跟 `(` 时，`:325-328` 把括号内的标识符收进 `insertColumns` 并清空
   `lastIdent`，`LOWER(employee_no)` 因此也失去列上下文。

**触发条件**
`sql_log.mask.enabled: true`、`sql_log.mode: full`（只有该档 GORM 会插值出字面量）、
`mask.columns` 配置了自定义敏感列（如 `employee_no`），且 SQL 形态为：
- `WHERE employee_no LIKE 'SF-778899'`、`BETWEEN 'a' AND 'b'`、`REGEXP '...'`；
- `WHERE LOWER(employee_no) = 'SF-778899'` 等函数包裹。

**实际 vs 预期**
列上下文丢失后，字面量只能靠值模式兜底；`SF-778899` 不匹配内置 phone/email/id_card 等
正则，被明文写入日志。扫描器已经为 INSERT 与 IN 维护了「列名—值」配对，比较谓词没有
理由不保留。`mask.enabled` 默认 false，`mode` 默认 placeholder（无字面量），因此默认配置
不触发；触发的是「启用脱敏 + full 档 + 自定义列 + 特定查询形态」的组合。

**影响**
合规基线被绕过，且只在特定查询形态下泄漏，排障时难以发现。内置列因值模式兜底通常仍被
遮住，问题集中在自定义列。

**复现思路**
`mustSet(t, MaskConfig{Columns: []string{"employee_no"}})` 后调用
`maskSQL("SELECT * FROM users WHERE employee_no LIKE 'SF-778899'", set)`，
断言结果不含 `SF-778899`。现有测试只覆盖 `=`、`IN`、INSERT 与模式兜底
（`sqlmask_test.go:393-401` 的 LIKE 用例用的是手机号，被 pattern 兜住）。

**修复建议（最小改动）**
1. `:398-400` 的关键字清空规则排除比较类关键字（`LIKE`、`BETWEEN`、`REGEXP`、`RLIKE`、
   `IS`），`AND`/`OR` 等连接词继续清空；
2. `:325-328` 仅在确认处于 INSERT 列清单上下文时才进入收集，避免把函数括号误判；
3. 补 LIKE / BETWEEN / REGEXP / 函数包裹的回归用例。

**修复记录（2026-11-21，已落地）**
- `support/sqllog/sqlmask.go`：
  1. 新增 `comparisonKeywords`（`LIKE` / `BETWEEN` / `REGEXP` / `RLIKE` / `IS`），关键字
     分支清空 `lastIdent` 时排除它们；`REGEXP` / `RLIKE` 同时补进 `keywordSet`，不再被当作
     普通标识符覆盖列名。
  2. 新增 `betweenColumn`：`BETWEEN` 记下被比较的列，随后的 `AND` 把它还回 `lastIdent`，
     使上下两个边界都归属同一列（`BETWEEN 'a' AND 'b'` 不再只遮前一个）。
  3. 新增 `inInsertStatement`：只有 `INSERT` 语句、且不在 `VALUES` 子句里时，`名字 (` 才
     进入列清单收集；`SELECT` / `UPDATE` / `DELETE` / `ON DUPLICATE KEY UPDATE` 关闭该状态，
     `LOWER(employee_no)` 因此保留列上下文。
- 测试：`TestMaskSQLComparisonKeepsColumnContext` 覆盖 LIKE、NOT LIKE、BETWEEN、NOT
  BETWEEN、REGEXP、RLIKE、`LOWER(col) =`、`UPPER(col) LIKE` 八种形态；
  `TestMaskSQLComparisonLeavesOtherColumnsAlone` 覆盖四个反向用例，确认无关列不被误遮、
  SELECT 列表里的函数调用不被当成列清单。
- 验证：旧实现下前六种形态均失败（值原样出现在结果里），修复后全部通过；`support/sqllog`
  原有测试（INSERT 按位置映射、IN 列表、转义字面量、模式兜底）无回归。

---

## 5. P2：需要排期处理

### P2-01 diagnostics 与主服务、模块 Stop 共用停机预算

**位置**：`gofy/start.go:971-1029`；`gofy/support/diagnostics/diagnostics.go:305-327`

**复核结论**：成立。单一 `ctx, cancel := context.WithTimeout(context.Background(), timeout)`
（默认 10s）依次用于 `a.diagnostics.Shutdown(ctx)`（`:1002`）、`a.srv.Shutdown(ctx)`
（`:1008`）和模块 `stopModule(..., ctx)`（`:1025`）。diagnostics 的
`Shutdown` 是 `srv.Shutdown(ctx)`，会等在途请求；pprof 的
`/debug/pprof/profile?seconds=30` 默认 30s，足以耗光整个预算。随后主 server 拿到已过期
ctx，`srv.Shutdown` 立即失败并走 `srv.Close()` 强制断开业务请求，模块 Stop 也拿到过期 ctx。
`start.go:995-1000` 的注释明确说「运维端点不该决定进程能否停下」，实际相反。

**修复建议**：给 diagnostics 独立短预算（如 `min(2s, timeout)`），或将其关闭移到主服务与
模块 Stop 之后。补「长 pprof 在途 + 正常业务在途」的停机用例。

**修复记录（2026-11-21，已落地）**
- `start.go`：新增常量 `diagnosticsShutdownBudget = 2s`；`cleanup()` 为 diagnostics 单独
  建 `context.WithTimeout`（取 `min(2s, timeout)`），主 server 与模块 Stop 继续用完整预算。
- 测试：`TestDiagnosticsShutdownKeepsTheBusinessBudget`。起一个 10s 的 pprof profile 与一个
  在途业务请求，等诊断监听超时切断 profile 后才放行业务请求，断言业务请求返回 200。
- 验证：旧实现 5.3s 后业务请求收到 `EOF`（共享预算耗尽触发 `srv.Close`），修复后 2.3s 内
  正常排空。

### P2-02 `--force` 整体覆写 `go.mod`

**位置**：`gofy/cmd/gofy/main.go:164-169`（注释承诺）vs `:194`、`:210`（实现）

**复核结论**：成立。`Generate` 的注释承诺 `go.mod` 始终走合并、只有其它文件受 `force`
控制；但合并分支被 `err == nil && !force` 挡住，`force=true` 时直接以模板内容写盘。
调用方自己 `go get` 的依赖与 `toolchain` 指令丢失，`go` 指令被重置为 `--go` 默认值。

**边界说明**：`go.mod` 确实是脚手架生成的文件，`--force` 的字面语义「覆盖生成文件」可以
覆盖它；问题在于**注释承诺与实现不一致**，且丢失是静默的（命令照常打印成功）。这是缺陷，
不是设计取舍。

**复现**：在带 `require github.com/google/uuid v1.6.0`、`go 1.24` 的 `go.mod` 目录上带
`--force` 重跑，依赖消失且 `go` 变成 1.26。`TestGenerateIsIdempotent` 只断言 `main.go`
被重写，未覆盖 `go.mod`。

**修复建议**：只要 `go.mod` 存在就调用 `mergeGoMod`，`force` 仅控制其它文件。补 force +
已有 extra require 的用例。

**修复记录（2026-11-21，已落地）**
- `cmd/gofy/main.go`：把 `go.mod` 分支移到 `!force` 判断之外。`go.mod` 存在时始终走
  `mergeGoMod`，`--force` 只重写其它生成文件；`Generate` 的注释同步改为「始终合并」。
- 测试：`TestGenerateForceKeepsGoModRequirements`。go.mod 预置 `go 1.24`、`toolchain
  go1.24.3`、`github.com/google/uuid`，以 `--force` 重跑后逐一断言仍在，且框架 require 与
  replace 已追加。
- 端到端：在真实生成目录里加 require 后 `--force` 重跑，go 指令与依赖均保留。

### P2-03 `envFromProcess` 恒为 true

**位置**：`gofy/cmd/gofy/templates/main.go.tmpl:74-78,95`；
`gofy/cmd/gofy/templates/reload.go.tmpl:118-123`；
`gofy/examples/demo-service/main.go:83-88,134`

**复核结论**：成立。启动逻辑先在 `GOFY_ENV` 为空时 `os.Setenv("GOFY_ENV", cfg.Env)`，
随后才用 `os.Getenv("GOFY_ENV") != ""` 计算 `envFromProcess` —— 此时必然非空，恒为 true。
demo 在 `:83` 已正确保存了 Setenv 前的局部变量并在 `:84` 使用，但 `:134` 又重算一遍，
形成回归；模板则从未保存过原值。

结果：reload 中 `if !r.envFromProcess` 成为死分支，进程环境停留在启动值，`Runtime.Env`
却取新 `cfg.Env`。依赖 `systemenv` 的日志分目录、worker/script 判定继续使用旧环境。

**边界**：`plan.LoadConfig=false`（help/version）时不会走到这里；`cfg.Env` 为空时也不成立。
实际受影响的是「HTTP 启动 + 未显式设置 GOFY_ENV」这一最常见形态。

**修复建议**：在 `Setenv` 之前取值并传给 reloader；模板与 demo 都改为复用该局部变量。

**修复记录（2026-11-21，已落地）**
- `cmd/gofy/templates/main.go.tmpl` 与 `examples/demo-service/main.go`：把 `envFromProcess`
  提到 `plan.LoadConfig` 块之前取值，回落判断改为 `if !envFromProcess`，`reloader` 直接复用
  该局部变量；demo 原先在块内保存、块外重算的回归一并消除。
- 测试：`TestGenerateDefaults` 断言生成文件里 `os.Getenv("GOFY_ENV")` 只出现一次，且
  reloader 赋值是 `envFromProcess: envFromProcess,`。

### P2-04 docgen 把 `gt` / `lt` 映射为闭区间

**位置**：`gofy-interface-api/docgen/schema.go:302-303,315,325`

**复核结论**：成立。`:302-303` 把 `min`/`gte`/`gt` 归为同一分支并写 `minimum`；`:315`
把 `max`/`lte`/`lt` 写成 `maximum`。OpenAPI 的 `minimum`/`maximum` 是闭区间，而
go-playground validator 的 `gt=N`/`lt=N` 是严格排他。生成的文档放行服务端会拒绝的值，
客户端生成器随之出错。`min`/`max`/`gte`/`lte` 的映射本身正确。

**修复建议**：把 `gt`/`lt` 拆出，生成 `exclusiveMinimum`/`exclusiveMaximum`（配合
`minimum`/`maximum`）；字符串/数组分支同样需要排他语义。

**修复记录（2026-11-21，已落地）**
- `docgen/schema.go`：`gt`/`lt` 从 `min`/`gte`、`max`/`lte` 中拆出。数字写成
  `minimum` + `exclusiveMinimum: true` / `maximum` + `exclusiveMaximum: true`（OpenAPI
  3.0.3 的排他形式）；字符串与数组没有排他关键字，改为平移闭区间：`gt=N` →
  `minLength/minItems = N+1`，`lt=N` → `maxLength/maxItems = N-1`，`lt=0` 这类不可能约束
  不生成（负的上限非法）。
- 测试：`TestApplyConstraintsExclusiveAndPointers` 覆盖 `gt/lt`、`gte/lte`、字符串、数组与
  数值四种组合。

### P2-05 docgen 未对指针类型解引用

**位置**：`gofy-interface-api/docgen/schema.go:240,258-261,285-291`

**复核结论**：成立。`applyConstraints` 收到原始 `field.Type`，直接用 `t.Kind()` 判断
`String`/`Slice`/`Array`。`*string` 的 `min=2` 因此生成 `minimum: 2` 而非 `minLength: 2`；
`*[]T` 生成 `minimum` 而非 `minItems`。同文件的 `enumFor`（`:258-261`）与 `parseScalar`
（`:365-367`）都做了指针解引用，唯独这里是遗漏。

**修复建议**：`applyConstraints` 入口循环 `for t.Kind() == reflect.Ptr { t = t.Elem() }`。

**修复记录（2026-11-21，已落地）**
- `docgen/schema.go`：`applyConstraints` 入口对指针解引用，`isString`/`isArray` 基于元素
  类型判断，与 `enumFor`、`parseScalar` 的做法一致。
- 测试：`TestApplyConstraintsExclusiveAndPointers` 的 `PointerString`、`PointerSlice`、
  `PointerExclusive` 三个子用例断言生成 `minLength` / `minItems` / `minimum`。

### P2-06 worker `/healthz` 在停机开始后仍可能被翻回 ready

**位置**：`gofy/worker_host.go:223-228,242-253,270-289`；`gofy/worker_health.go:64-65`

**复核结论**：成立，但触发窗口比初版描述的窄，本版由 P1 降为 P2。

**触发条件（完整版）**
必须同时满足：
1. 所有 runner 已上报 ready，`readyAll <- true` 已写入容量为 1 的 channel；
2. 主循环**尚未**消费该值，因此局部变量 `ready` 仍为 false；
3. 停机先发生：`stopCh` 被关闭或某个 runner 返回错误，主循环处理停机分支并
   `cancelRunners`，`runCtx.Done()` 触发 `health.setStopping()`；
4. 下一轮 `select` 选中仍带缓存值的 `readyAll`，且 `setStopping` 已先于 `setReady` 执行。

此时 `if !allReady || ready` 为 false，`health.setReady()` 把状态从 stopping 覆盖回
ready。若 `ready` 已经置位（正常运行时早已消费过 readyAll），该分支会 `continue`，
不会二次 `setReady`，因此不会回退。

**影响**
`worker_health.go:64-65` 是裸 `atomic.Store`，没有单调性；回退后 drain 窗口的探针返回
200，调度器继续投流量。窗口是「Ready 信号已入缓冲、主循环尚未消费」的瞬间，概率低，
但一旦命中就与「停机立刻 503」的设计目标相反。

**复现思路**
让某 runner 在 `worker.Ready(ctx)` 后由另一个 goroutine 调 `app.Shutdown`，配合 `-race`
多次运行；或在单元层直接验证 `setStopping(); setReady()` 后 `handle` 返回 200。

**修复建议（最小改动）**
二选一，建议同时做：
1. `worker_host.go:282-284` 调 `health.setReady()` 前检查 `runCtx.Err() == nil`；
2. `workerHealth.setReady()` 改为单调状态，仅当当前为 starting 时才 CAS 到 ready。

**修复记录（2026-11-21，已落地）**
- 两条都做了。`worker_host.go` 在消费 `readyAll` 后、置 ready 前检查 `runCtx.Err() != nil`，
  已取消则 `continue`（`readyAll` 已置 nil，不会重入）。
- `worker_health.go` 的 `setReady` 改为 `CompareAndSwap(workerHealthStarting,
  workerHealthReady)`：stopping 是终态，迟到的 Ready 信号不能把探针翻绿；`setStopping`
  仍是 Store，与 CAS 并发时二者都收敛到 stopping。
- 测试：`TestWorkerHealthStateIsMonotonic` 依次断言 starting 503 → ready 200 → stopping
  503 → 再 `setReady` 仍为 503。

### P2-07 SSE 心跳/注释块不重置 `eventBytes`

**位置**：`gofy-interface-api/client/sse.go:284-292,301-314,320-322,119-122`

**复核结论**：成立，但默认配置下触发周期长，本版由 P1 降为 P2。

**触发条件**
SSE 流中反复出现不产生事件的块：以 `:` 开头的注释/心跳行，或只有 `id:` / `retry:` 而没有
`data:` / `event:` 的块。每个块以空行结束。

**实际 vs 预期**
`dispatch()` 在 `!hasData && event == ""` 时于 `:286` 直接 return，唯一的重置语句在 `:292`
不能被走到；心跳行本身也被计入 `eventBytes`。累计超过 `MaxEventBytes` 后
`StreamSSE` 在 `:119-122` 直接上报并 return，不再重连。

**触发量级（降级依据）**
默认 `MaxEventBytes = 1 MiB`。一个 `: ping` 块约 6 字节，且空行不计入；纯心跳流需要约
17 万次心跳才越限（15s 一次约 30 天）。只要流中夹杂真实事件，每次 `dispatch` 都会重置。
真正容易命中的是显式调小 `MaxEventBytes`（限制内存）的部署，或服务端持续发送较长注释行。

**影响**
长期无业务事件的纯心跳流会在某个时刻被误判为「超大事件」并静默中断；错误信息有误导性。

**复现思路**
`Config{MaxEventBytes: 64}`，服务端循环发 `: x\n\n` 共 30 次后发一个正常
`data: ok\n\n`。预期收到事件，实际约第 22 个心跳时 error channel 收到 `ErrEventTooLarge`。

**修复建议（最小改动）**
把状态重置移到 `dispatch` 最前面，早退判断之前：
`event, data, retry, hasData, eventBytes = "", nil, 0, false, 0`。

**修复记录（2026-11-21，已落地）**
- `client/sse.go`：`dispatch` 的早退分支（无 `data` 且无 `event`）在返回前同样重置
  `event`、`data`、`retry`、`hasData`、`eventBytes`，心跳/注释块的字节不再跨块累计。
- 测试：`TestStreamSSEKeepsHeartbeatsOutOfTheEventBudget`，`MaxEventBytes: 64`、30 个
  `: ping` 块后接 `data: ok`，断言收到事件而不是 `ErrEventTooLarge`。
- 验证：旧实现第 11 个心跳即报 "event over 64 bytes"，修复后正常收事件。

### P2-08 Nacos apply 失败后远端回滚，本地文件停留在失败版本

**位置**：`gofy/support/nacos/watch.go:193,203,214-229,249-265`

**复核结论**：成立，但初版对影响的描述不准确，本版由 P1 降为 P2。

**触发条件**
1. prime 成功：文件 = 远端 D1，`current = applied = D1`；
2. 远端改为 D2 → `Listen` 报变更 → `Sync` 把 D2 **写入本地文件** → `apply(D2)` 失败 →
   两个摘要仍是 D1，但磁盘已是 D2；
3. 远端回滚到 D1。

下一轮 `Listen(ctx, current=D1)` 与服务端当前摘要相同，`changed=false`，不再调用 `Sync`。
本地文件永远是 D2，而进程内生效配置是 D1。

**机制澄清**
游标是 `current` 而不是 `applied`（`watch.go:203`）；问题出在该场景下 `current == applied`，
使 `Listen` 回答「无变化」。这与上一轮已修的「apply 失败后不重放」是两条独立路径：
远端保持 D2 时会正常重放，只有远端恰好回滚到 `applied` 摘要时才命中本问题。

**影响（修正后）**
真实影响是**运行期磁盘与内存不一致**：config.yaml 是失败版本 D2，进程实际跑 D1，
外部工具、运维查看文件、或基于文件的诊断都会误判。初版称「重启后会加载 D2」并不准确：
`main` 在启动时会先执行 `nacos.Sync`，Nacos 可达时用远端 D1 覆盖文件；Nacos 不可达时
`Sync` 失败并 `fatalf`，进程不会启动。只有在有人把 `nacos.enabled` 改为 false 后重启
（此时确认过程序已不再依赖 Nacos）才会加载 D2。

**复现思路**
可切换内容的假 Nacos server：prime=D1 → 推 D2 且 `WithOnChange` 返回错误 → 推回 D1；
断言本地文件仍为 D2、onChange 未再次调用。

**修复建议（最小改动）**
把「已写入文件的摘要」与「已应用的摘要」分开记录；`Listen` 游标使用文件摘要，或在
`result.Digest == applied` 分支额外校验磁盘文件摘要，不一致则重写文件再推进游标。

**修复记录（2026-11-21，已落地）**
- `support/nacos/watch.go`：新增 `written`（磁盘内容摘要），在 `Sync` 成功后更新；游标
  仍用 `applied`（远端未回滚时靠它重放 apply），但在 `Listen` 回答「无变更」的分支检查
  `written != current`——一旦不相等说明文件停在被拒绝的版本上，立即 `Sync` 一次把已应用
  内容写回磁盘，然后继续。
- 测试：`TestWatcherRestoresTheAppliedContentAfterRollback`。prime=D1 → 推 D2 且 handler
  失败 → 文件为 D2 → 回滚 D1（fake server 按摘要比对，回答「无变更」）→ 断言文件恢复 D1，
  且 handler 不再被调用。
- 验证：旧实现 5s 超时（文件永远停在 D2），修复后通过。

---

## 6. P3：边界一致性与收尾

### P3-01 最终 `cleanup()` 未 Sync 独立 SQL 日志通道

**复核结论**：成立。`gofy/start.go:1063-1065` 只 Sync `logger`、`systemLogger`、
`accessLogger`；`generation.go:297-299` 的退役代包含 `gen.sqlLogger`。默认
`a.sqlLogger = a.logger` 时无影响，配置 `sql_outputs` 后它是独立文件 logger，退出时缺少
最后一次 Sync，Reload 后退出与直接退出行为不一致。修法：
`syncLoggers(..., a.sqlLogger)`。

**修复记录（2026-11-21，已落地）**
- `start.go`：最终 `cleanup()` 的 `syncLoggers` 加入 `a.sqlLogger`（nil 与重复指针由
  `syncLoggers` 自身跳过）。
- 测试：`TestCleanupSyncsSQLLogger` 用记录 Sync 次数的 `zapcore.WriteSyncer` 作为 SQL
  通道，断言 `cleanup()` 后计数非零。
- 验证：旧实现下 `cleanup did not sync the SQL logger`，修复后通过。

### P3-02 关闭可选模块后重跑脚手架被误判为外来目录

**复核结论**：成立。`gofy/cmd/gofy/main.go:245-301` 的 `files()` 按 `enabled()` 过滤输出，
`:371-383` 的 `checkAppTarget` 只用过滤后的 `outs` 构造 allowed 集合。默认生成后再带
`--with-scripts=false` / `--with-worker=false` 重跑，上次自己生成的 `script/`、`worker/`
被当作外来内容拒绝，并提示 `use --force`（而 force 会触发 P2-02）。修法：allowed 基于
全部模板输出路径，而非过滤后的集合。

**修复记录（2026-11-21，已落地）**
- `cmd/gofy/main.go`：新增 `templateOutputs()`，遍历模板只取输出路径、不渲染也不看开关；
  `Generate` 把「本次文件 + 模板全集」一起交给 `checkAppTarget`。
- 测试：`TestGenerateAcceptsItsOwnOptionalDirectories`——默认生成后带
  `--with-scripts=false --with-worker=false` 重跑，断言不报错且 `script/`、`worker/`
  仍在（关闭模块只是不再写，不删除代码）。
- 验证：旧实现报 `already contains script (use --force to write anyway)`，修复后通过。

### P3-03 OTel batch 校验在默认值下被跳过

**复核结论**：成立。`gofy/support/otel/config.go:268` 用 `b.MaxQueueSize > 0` 作守卫；
手写 `Config{Enabled:true, Batch:{MaxExportBatchSize:3000}}` 时校验跳过，随后
`withDefaults` 把队列补成 2048，得到被规则禁止的 2048/3000 组合。`provider.go:139-142`
先 Validate 后 withDefaults；OTel SDK 自身的纠偏在应用 gofy 的 option 之前，救不回来。
经 YAML 加载（`default:"2048"` 已填值）不触发，只影响手写 Config 的公开 API 路径。
修法：按生效值校验，或把 `withDefaults()` 提到 `Validate()` 之前。

**修复记录（2026-11-21，已落地）**
- `support/otel/config.go`：抽常量 `defaultMaxQueueSize` / `defaultMaxExportBatchSize`，
  `withDefaults` 与 `Validate` 共用；`Validate` 先把 0 替换成默认值再比较，不再用
  `MaxQueueSize > 0` 作守卫。
- 测试：`TestConfigValidate` 增两条（只设 3000 的 batch；只设 10 的 queue），
  `TestBatchConfigAcceptsEqualEffectiveBounds` 固定 `batch == queue` 与默认值合法。
- 验证：旧实现两条新用例都返回 nil（该拒未拒），修复后正确报错。
- 行为变化：`max_queue_size` 小于默认批大小（512）而 batch 未显式配置的写法现在会在
  `New` 阶段报错——那本来就是会被 SDK 拒绝的组合。

### P3-04 pprof import 形式注释与 Go 语义不符

**复核结论**：事实成立，但当前无实际暴露路径。`gofy/support/diagnostics/diagnostics.go:26`
普通 import `net/http/pprof`，`:330-336` 注释称「刻意不用空导入」以避免注册到
`http.DefaultServeMux`。Go 规范中普通 import 同样执行 `init()`，注释前提不成立。
复核确认项目内没有 server 使用 DefaultServeMux（诊断服务、业务 server、worker 健康端均
自建 mux），因此这是**注释错误**，不是运行时漏洞。修法：改正措辞，或改用不触发 init 的
实现并说明代价。

**修复记录（2026-11-21，已落地）**
- `support/diagnostics/diagnostics.go`：重写 `registerPprof` 的注释，明确「普通 import 与
  空导入一样会执行 init，没有任何 import 形式能跳过」；说明真正起隔离作用的是本进程没有
  监听使用 DefaultServeMux，并把「将来若用 DefaultServeMux 会暴露 pprof」写成约束；
  同时记下另一条路（自行实现 handler）的代价。代码未动——它是注释缺陷，不是运行时缺陷。

### P3-05 docgen 把 `int` / `uint` 固定映射为 `int32`

**复核结论**：部分成立，属取舍。`gofy-interface-api/docgen/schema.go:90-97` 按宽度分组：
`int`/`int8`/`int16`/`int32` → `int32`，`int64` → `int64`；`uint` 同理。
64 位平台上原生 `int`/`uint` 是 64 位，按文档生成的客户端用 32 位解码可能截断。
也有 `openAPITyper` 可自定义覆盖，但内置类型未启用。建议确认后决定是改成 `int64`，
还是在文档中写明约束；不建议直接按 bug 修。

**决策记录（2026-11-21）：保持 `int32`，补说明。**
- 依据：现有契约里使用裸 `int`/`uint` 的字段只有 `envelope.Code`、`paging.Page`、
  `PageSize` 等 7 处，取值都远小于 32 位上限，改成 `int64` 不会带来收益。
- 落地：`docgen/schema.go` 的类型映射处加注释说明该口径，README 增加「整数宽度」一节；
  代码行为不变，契约需要完整范围时显式声明 `int64` / `uint64`。

### P3-06 SSE 单行上限的 off-by-one

**复核结论**：成立。`gofy-interface-api/client/sse.go:379` 的 `readLine` 用含 `\n` 的
`frag` 比较，`:311-314` 的事件级累加却只算净内容。净内容恰好等于 `MaxEventBytes` 的单行
会在 `readLine` 被判超限，与 `client/call.go:311-317`「恰好等于 max 接受」的口径不一致。
修法：比较时排除末尾换行符。

**修复记录（2026-11-21，已落地）**
- `client/sse.go`：`readLine` 在比较前剥掉片段末尾的 `\n`（以及紧随的 `\r`），只累计
  内容字节；拼进 `line` 时仍保留原片段，返回值语义不变。
- 测试：`TestStreamSSEAcceptsLineAtTheExactLimit`，`MaxEventBytes=64`、内容恰好 64 字节，
  分别用 LF 与 CRLF 结束，断言收到事件。
- 验证：旧实现两种换行都在 `readLine` 报 `line over 64 bytes`，修复后通过。

### P3-07 `DocsPath="/"` 时 Swagger UI 静态资源 404

**复核结论**：成立。`gofy-adapter/adapter/gofy/docs.go:41-47` 在 `docsPath != "/"` 时才注册
`r.GET(docsPath+"/*any", ...)`。`docsui.Handler` 支持 `DocsPath="/"`（把
`InternalBasePath` 设为 `/`），但 gin 对 `/` 是精确匹配，`/swagger-ui.css` 等资源无路由，
HTML 打开后 CSS/JS 全 404。默认 `/docs` 不受影响。修法：根路径下也注册 catch-all
（注意与 `/openapi.json` 的路由冲突），或在 `Register` 中明确拒绝 `DocsPath="/"`。

**修复记录（2026-11-21，已落地）**
- `adapter/gofy/docs.go`：选择「明确拒绝」而不是 catch-all——gin 的 `/` 是精确匹配，
  catch-all（`/*any`）又与 spec 路由在同一 engine 上冲突，根路径无法同时满足两者。
  `Register` 现在返回
  `gofy: docs DocsPath must not be "/"; use a prefix such as /docs`。
- 测试：`TestDocsModuleRejectsRootPath` 断言注册失败且错误点名 `DocsPath`。
- 验证：旧实现注册成功（随后静态资源 404），修复后启动阶段即报错。
- 行为变化：配置 `docs_path: /` 的部署会启动失败并得到明确提示，需要改成 `/docs` 之类
  的前缀；这是有意的，静默的坏页面更难排查。

---

## 7. 已排除的候选

| 候选 | 排除理由 | 复核 |
|---|---|---|
| RocketMQ `Consumer` 的 `consuming` 标志在 `start()` 失败后不复位 | `support/rocketmq/consumer.go:382-391` 的事实成立，但 `support/worker/consumer/rocketmqconsumer` 的包注释明确把该类型定义为一次性；`NewFactory` 与 `support/consumer/rocketmq.go:160` 每次 Run 都新建实例，生产路径没有同实例重试 | Rev.2 复核维持 |
| Nacos Resolver leader 用 `context.WithoutCancel` 不响应调用方取消 | `gofy-adapter/client/nacos/resolver.go:591-601` 的注释明确说明刷新要为所有等待者发布结果；`TestResolveCancelDoesNotAbortTheSharedRefresh`（`resolver_test.go:467-503`）把该行为固定为预期；刷新有 `cfg.Timeout` 上界（默认 2s）。等待者路径本身尊重 ctx | Rev.2 复核维持 |

P3-04 曾以「pprof 污染 DefaultServeMux」的形式出现，本版保留但明确标注无实际暴露路径，
不按运行时漏洞处理。

## 8. 历史问题状态确认

本轮按当前 HEAD 复核了前两轮报告的全部结论，确认均已修复，不重复列入发现：

- `gofy-architecture-audit-2026-11-21.md`：P1-01 ~ P1-03、P2-01 ~ P2-08 共 11 项
  （租户隔离、契约 auth、BindAll、SSE 重连、脱敏默认规则、K8s Lease 等）。
- `gofy-architecture-audit-2026-11-20-round2.md`：§3 的 8 项 P0/P1 与 §4 的 6 项 P2
  （队列停机、租户三条、message_id、Nacos 重放、Reload SQL policy、worker Shutdown、
  silent 日志、claim DDL、AccessLog 负数、JSON null、测试可重复性、死代码、OTEL ctx）。

其中 P2-08（Nacos 回滚）与上一轮「apply 失败后不重放」是**不同路径**：前者是远端回滚
导致 `changed=false`，后者是远端保持新摘要导致重放；两者不互相覆盖。

## 9. 建议处理顺序

1. ~~**P1-01、P1-02**：确定性逻辑缺陷与合规泄漏~~ — ✅ **已于 2026-11-21 修复**，
   见 §0.1 与 §4 各自的修复记录。
2. ~~**P2-06、P2-08**：状态一致性~~ / ~~**P2-02、P2-03**：工具链与模板~~ /
   ~~**P2-01、P2-04、P2-05、P2-07**~~ — ✅ **已于 2026-11-21 全部修复**。
3. ~~**P3-01 ~ P3-04、P3-06、P3-07**~~ — ✅ **已于 2026-11-21 修复**。
4. ~~**P3-05（待决策）**：`int`/`uint` 固定映射为 `int32`~~ — ✅ **已决策：保持
   `int32` 并补说明**。现有契约里使用裸 `int` 的字段只有 `envelope.Code`、
   `paging.Page` / `PageSize` 等 7 处，都在 32 位内；已在 `docgen/schema.go` 的类型
   映射处与 README 写明该口径，需要完整范围时由契约显式声明 `int64`。

**验证命令（每次改动前后都应重跑）**

```bash
# 三个仓库各自
go build ./... && go vet ./... && go test -count=1 ./...

# gofy
./shell/check-imports.sh
go test -race -count=1 ./...
# 脚手架三种形态：默认 / --with-scripts=false --with-worker=false / --with-db --with-docker
# 各自 go mod tidy && go build ./...
```

## 10. 方法与局限

- 初版六个分片独立扫描；Rev.2 由主审逐条重读源码，重点复核 P1/P2 与排除项，
  修正了 3 处分级、2 处影响描述、若干机制细节。
- 每条发现都给出精确文件位置和触发条件；无法定位到代码路径的怀疑未写入正文。
- 局限：未连接真实 MySQL / Nacos / broker / OTLP collector，涉及外部服务时序的结论
  基于源码与现有测试替身推演；Windows 信号处理与 daemon fork 只做源码核对，未实测。
  修复阶段的复核方式：每条新用例先在旧实现上运行确认失败，再在修复后确认通过；
  例外是 P2-06（窗口依赖 goroutine 调度顺序，只做状态机单元验证）与 P3-04（注释修正，
  无行为变化）。
- Rev.3–Rev.5 起，已修复条目在末尾附「修复记录」；正文不展开完整 diff，也不引入新功能，
  所有建议均为最小改动。
