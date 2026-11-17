# Gofy 三仓库全量架构审计报告（2026-11-18）

> 审计日期：2026-11-18
> 审计范围：`bingcool/gofy`、`bingcool/gofy-interface-api`、`bingcool/gofy-adapter`，以及 gofy 仓库内的 `examples/demo-service` 与 `examples/interface-service`。共 502 个 Go 文件（gofy 447 / interface-api 43 / adapter 12），另含三个仓库的 `go.mod`、`Makefile`、`shell/` 守卫与示例 YAML。
> 代码基线：gofy `5fabded`；gofy-adapter `3b11e5e`；gofy-interface-api `6556e10`（均为各自 `main` HEAD）。
> 审计原则：不新增功能，只识别 Bug、边界与临界条件、并发与生命周期缺陷、常驻内存/资源所有权问题、架构契约不一致。
> 方法：按六个方向分派只读探索，要求每条结论必须带 `文件:行号` 与代码事实；主审计员对 P0/P1 结论逐条回读源码复核（下称「已复核」），其余为静态取证、未运行复现。
> 与历史报告的关系：历史四轮（`2026-11-15`、`11-15-review`、`11-16`、`11-16-round2`、`11-17-round3`）已标记修复；本报告**从头**重审当前代码，不重复核对旧结论，只在旧问题仍可见时注明「已知遗留」。
> 路径锚定：`gofy/xxx.go` = gofy 仓库根；`interface-api/xxx.go` = gofy-interface-api 仓库根；`adapter/xxx.go` = gofy-adapter 仓库根；`demo/xxx.go` = `gofy/examples/demo-service/xxx.go`。

---

## 0. 结论摘要

| 严重度 | 条数 | 定义 |
|---|---|---|
| P0 | 1 | 静默数据丢失，或可用性被无条件破坏，无外部征兆 |
| P1 | 12 | 常见触发条件下产生错误行为（错误 ACK、连接泄漏、误判、崩溃） |
| P2 | 32 | 特定配置/边界/并发下出错，或资源单调增长、契约不一致 |
| P3 | 16 | 局部健壮性、可观测性、文档与实现偏差 |

> 上表是**逐条回读复核后**的结果（初稿为 1/13/34/14）：1 条不成立已移入 §1，2 条因触发前提过窄降为 P3，另有 17 条修正了表述或前提。复核方法与结论见 §1。

总体判断：

1. **架构骨架是稳的**：分代装配（`support/generation` + Snapshot）、pid 身份记录、claim 的 `owner+attempts` 围栏、worker 就绪闸门、消费终态落库才 ACK、Nacos 双时间戳节流、类型化客户端唯一 URL 构造点，这些都经过前几轮修复，本轮未发现结构性反转。
2. **系统性风险集中在三条链路**：① 消费台账与 broker 重投语义的错配（B1/B6）；② 「Close 只关连接、不清状态」在多个包重复出现（C1/C2/E3/D8）；③ 出厂配置/守卫/注释与实现之间的三处偏差，让部分能力实际不可达（F1/F2/F3）。
3. **常驻内存不是普遍问题，但有几个明确点**：resolver 的 `cache/rr` 两张 map、限流 `hits` map、台账表只增不删、派生 transport 缓存满后不回收、换代时旧代连接池不可达。均为「随运行时间单调增长」，见 §4。
4. **本轮逐条回读后撤销 1 条、降级 6 条**（§1 X-1~X-7），它们是刻意设计、框架内不会触发或前提写错，不应作为 bug 排期；另有 17 条修正了表述与前提（如 B5 只影响 MySQL 路径、D2 的「当数据」只适用于下载/分块流、D1 限于非 JSON 编码、C6 需 `RequireExpiry=false`、C4 的 XFF 伪造只在放宽信任代理后成立）。

---

## 1. 复核后撤销或降级的结论

写在这里是为了避免后续按「疑似问题」重复排查。

| 编号 | 原始判断 | 复核结论 |
|---|---|---|
| X-1 | `consume.go:468-485` 中 `succeed` 写库失败只 Warn 就 ACK，判为 P0「终态未落库仍确认」 | **降级为 P2（可观测性）**。源码注释明确：副作用已发生，不 ACK 让 broker 重投反而扩大重复；`park`（dead 终态）仍严格「落库失败即返回可重试错误」。这是「done 与 dead 不同语义」的刻意取舍，不是遗漏。剩下的真实缺口只有：失败仅一行 Warn，没有指标/告警，且依赖业务幂等。 |
| X-2 | `k8slease.Renew` 无锁，判为 P1「并发续约自竞争丢租约」 | **降级为 P2**。`support/lease/lease.go:236-244` 的 `keep()` 是串行 ticker，框架内不会产生并发 Renew；只有调用方直接并发调用公开的 `heldLease.Renew` 时才会自竞争。问题是「并发契约未声明且字段无保护」，不是框架路径上必然发生。 |
| X-3 | `resolver.go:475-491` leader 用调用方 ctx，判为 P1「一个超时作废整批」 | **降级为 P2（设计取舍）**。注释已说明：调用方取消时该次尝试不能代表 Nacos 状态，因此既不节流也不覆盖缓存；StaleTTL 内已有缓存的等待者仍由旧缓存服务，「整批作废」并不发生。代价是需要刷新的等待者回退 stale 且刷新失败无声，属于可改进项，不是 bug。 |
| X-4 | `demo/config.yaml:268` 空 `inner_external_base_uri` 使降级成死代码，判为 P1 | **降级为 P2（示例配置）**。留空即「不写 metadata、不降级」是文档化语义；真实缺口是示例注释把降级描述成默认行为，且 group3 的 5s deadline 与「最坏 10s」冲突（见 F1）。 |
| X-5 | `claim.Runner` 全程用 runCtx、不用 `DrainContext`，判为 P1 | **不成立，删除**。Runner 只透传调用方给的 ctx；唯一的项目内调用方 `demo/worker/group1/scan_claim.go:420` 传的正是 `worker.DrainContext(ctx)`，与 `support/worker/ticker.go:22-33` 的约定一致。库不强制 drain 属于 API 契约，不是实现缺陷；若担心新调用方遗漏，可另立一条「文档/示例约束」，不计入问题清单。 |
| X-6 | `Runner.Run` 的 `fatal` 变量「稳定数据竞争」，判为 P2 | **降级为 P3**。写入走 `once.Do`，读取点 `runner.go:259` 经 `fatalCh` 关闭或 `wg.Wait` 到达时有 happens-before；只有 select 选中 `ctx.Done()` 且 fail 同时写入的窄窗口无同步。是真竞态但窗口很窄，`-race` 未必复现。 |
| X-7 | 限流键可伪造（`ClientIP` 信任 XFF），判为 P2 | **只保留一半，降为 P3**。`gofy/start.go:275` 默认 `SetTrustedProxies([]string{})`，XFF 默认不可用，只有显式放宽信任代理后才可伪造；成立的是 `hits` map 在 GC 周期内没有硬上限（`ratelimit.go:97,135-150` 有周期 sweep，不是「永不回收」）。 |

另有一条**已知遗留**：`interface-api` 的 `validate:"required"` 与编码器 `isZero` 判定漂移（D1），即 2026-11-17 复核中的 P2-01，当前仍未实现。本报告保留它，但标注为已知项，不计入新增工作量。

---

## 2. 问题清单

### 2.1 P0

#### B1 · Redis 队列把 `ErrInFlight` 当成普通失败，耗尽重试后永久丢消息（P0 · 已复核 · 已修复）

- **证据**：`gofy/support/consumer/consume.go:423-435` 在另一实例持有有效 `processing` 租约时返回 `ErrInFlight` 且明确「不 ACK，等待稍后重投」；`gofy/support/queues/consume.go:145-161` 对任意非 nil 错误都调 `q.Retry`；`gofy/support/queues/message.go:75-81` 在 `RetryNum >= maxRetries` 返回 `false` 且不再入队；`gofy/support/consumer/queues.go:208-216` 默认 `MaxRetries=3`、`RetryDelay=3s`、租约 5min。
- **风险**：租约有效期内，本副本每 3s 重投一次、约 9s 耗尽 3 次预算后队列直接丢弃消息。若首个 handler 随后失败，broker 已无副本、台账停在 `processing`，消息彻底消失——幂等台账最该防的场景反而丢了消息。
- **建议**：`ErrInFlight` 单独识别：重新入队但**不递增 RetryNum**，且延迟不小于 `ProcessingLease`；或交给延迟队列并冻结重试预算。
- **修复（2026-11-18）**：`consumer.ErrInFlight` 改为实现 `queues.InFlightError` 标记（结构化接口，不引入反向依赖）；`Queue.Consume` 在 `DiscardOnError` 与普通重试之前识别该标记，调用新增的 `Queue.RetryInFlight` 按 `ConsumeOptions.InFlightDelay` 重新入队。该路径不改 `RetryNum`、不受 `MaxRetries` 限制；`consumer/queues.go` 把延迟设为 `DefaultProcessingLease`（5 分钟），确保副本不会与仍持有租约的 handler 抢跑。回归测试覆盖两条规则：`MaxRetries=1` 下两次在途重投后仍能成功、`DiscardOnError=true` 时在途消息不被丢弃。

### 2.2 P1

#### A1 · `start --daemon` 没有父子握手，父进程无条件报成功（P1 · 已复核）

- **证据**：`gofy/daemon.go:31-37` 用 `process.Detach(executable, args)`（不带 NotifyEnv/ExtraFiles），随即 `c.Success("已在后台启动")`；`gofy/start.go:222` 的 `markReady()` 从不调用 `notifier.ready()`（唯一调用在 `gofy/worker_host.go:247`）；`gofy/start.go:875` 的 `a.notifier.failed(err)` 因 `notifierFromEnv()` 为 nil 而 no-op。
- **风险**：端口被占、pid 判重失败、Nacos 注册失败或模块 Start 失败时子进程静默退出，父进程仍打印成功；worker 的 daemon 路径却有完整 fd 3 握手（`worker_host.go:409-416`），两条路径不对称。
- **建议**：`start` 复用 worker 的 `DetachWithNotify` + `awaitWorkerStartup`，并在 HTTP `run()` 就绪后发 `notifier.ready()`。

#### A2 · `Shutdown()` 停机时 runCtx 取消排在 DB 关闭之后（P1 · 已复核）

- **证据**：`gofy/start.go:101` `run` 用 `context.WithCancelCause(parent)` 建 runCtx 并存入 `a.cancel`；`gofy/start.go:846-850` 的 `Shutdown()` 只 `close(a.stopCh)`；`gofy/start.go:930-956` 的 cleanup 顺序为模块 Stop → `gofydb.Close` → telemetry Shutdown → `a.cancel(nil)`。
- **风险**：凡按 `core/runtime.go:115-117` 监听 `rt.Context().Done()` 的后台 goroutine，直到 DB/logger 释放后才收到信号，期间可能 use-after-close；模块 Stop 若等待该 ctx，会卡满 shutdown 预算。
- **建议**：把 `a.cancel(nil)` 提到 cleanup 开头（registrar.Stop 之前），保留末尾的幂等取消。

#### B4 · 消费台账列类型硬编码 MySQL `datetime(3)`，PG/SQLite 方言分支形同虚设（P1 · 已复核）

- **证据**：`gofy/support/consumer/consume.go:634-635` `CreatedAt/UpdatedAt` 带 `gorm:"type:datetime(3)"`；`:887-906` 却为 postgres/sqlite 生成过期判断 SQL；`:668-677` 用 `AutoMigrate` 建表。
- **风险**：PostgreSQL 无 `datetime` 类型，`AutoMigrate` 建表即失败 → `NewProcessor` 报错 → 消费者按 RestartOnError 无限重启、永不就绪。
- **建议**：去掉 `type` tag 交给 gorm 按 dialector 映射，或按 `Dialector.Name()` 选列类型。

#### B5 · MySQL 路径写库用秒精度、过期比较用毫秒精度（P1）

- **证据**：`gofy/support/consumer/consume.go:875-877` `dbNow()` 为 `CURRENT_TIMESTAMP`（无小数）；`:894` MySQL 用 `CURRENT_TIMESTAMP(3) - INTERVAL n MICROSECOND`；`:634` 列声明 `datetime(3)`。PG（`:896` 微秒）与 SQLite（`:903` 两侧都归一化到秒）不存在这一错配。
- **风险**：MySQL 下写入被截断到整秒，`updated_at` 最多比真实时刻早 0.999s；短 `ProcessingLease` 下数据库提前判定过期，另一副本 takeover，与仍在运行的 handler 并发。
- **建议**：MySQL 写入同样用 `CURRENT_TIMESTAMP(3)`（或写入与判断同截断到秒并对租约向上取整）；PG/SQLite 保持现状。

#### B6 · `ErrInFlight` 与四种 broker 的重投语义都不匹配（P1）

- **证据**：`gofy/support/amqp/queue.go:385` 对错误 `Nack(requeue=true)` 立即重投；`gofy/support/kafka/consumer.go:428-449` handler 错误会 `commit(last)` 并退出整个 `Consume`；`gofy/support/consumer/kafka.go`、`amqp.go` 均未特判 `ErrInFlight`。
- **风险**：AMQP 在 5min 租约内零延迟紧循环打满 DB/CPU；Kafka 则整体退出循环、触发 Runner 退避重启与重新入组，而不是单条稍后重投。
- **建议**：定义统一 in-flight 错误类型，各 broker 走「延迟重投且不推进正常重试预算」分支。

#### C1 · Nacos `Sync` / `Watcher` 从不关闭自建 Client（P1 · 已复核）

- **证据**：`gofy/support/nacos/sync.go:33-39` 顶层 `Sync` 每次 `NewClient(cfg)` 后直接返回 `client.Sync(ctx)`；`gofy/support/nacos/watch.go:116-207` 的 `NewWatcher/Watch/Run` 全程无 `client.Close()`，Watcher 也没有 Close；`gofy/support/nacos/client.go:83` 定义了 `Close()`，包内无调用点。
- **风险**：`client.go:69` 每次 `http.DefaultTransport.Clone()` 生成独立连接池，频繁 Sync 或 Watch 退出后，空闲连接与读 goroutine 滞留累积。
- **建议**：`Sync` 内 `defer client.Close()`；Watcher 加 `Close()`，或在 `Run` 开头 `defer w.client.Close()`。

#### D1 · `validate:required` 零值被编码器静默丢弃，与 docgen required 漂移（P1 · 已知遗留 P2-01）

- **证据**：`interface-api/client/encode.go:395,424,450` 的 collectForm/collectHeaders/collectCookies 均忽略 tag options，靠 `isZero` 跳过；`interface-api/docgen/schema.go:243-260` `fieldRequired` 对 `required` / 非指针字段返回 true，`docgen/operation.go:140,155,169` 直接写进 required。另 `docgen/operation.go:105-107` 跳过 uri+form 双 tag 的 form 字段，而 `client/encode.go:393` 不跳过（`ItemRequest` 会多发 `?id=7`）。
- **风险**：文档标必填，GET/上传在零值时漏发 query/header/cookie/form；JSON body 走 `json.Marshal` 不受影响，同一字段跨方法表现不一。
- **建议**：抽 `endpoint.FieldRequired` 作为 docgen 与编码器的唯一判定源；顺带对齐双 tag 跳过规则。该项在 2026-11-17 复核中列为暂缓，本轮不实现。

#### D2 · 流式响应不校验 Content-Type / ResponseKind，下载与分块流会把错误信封当数据（P1）

- **证据**：`interface-api/client/sse.go:138-146` `openStream` 仅判 StatusCode，客户端从不读 `spec.ResponseKind`；`:186-203` 未知行忽略。DownloadFile / StreamChunked（`:254-259,293`）同样只判状态码。
- **风险**：2xx + `application/json` 的 gofy 错误体，在下载/分块流上会被当作文件内容或流数据交给业务；在 SSE 上则是「静默零事件结束」——两者都不报错，但只有前两者算「当数据」。
- **建议**：2xx 时校验 `Content-Type` 与 `spec.ResponseKind` 是否匹配所用 helper，不符则限长读体并走 `responseError`；SSE 至少把非 `text/event-stream` 视为协议错误。

#### D3 · SSE 服务端正常关闭（EOF）不触发重连（P1 · 已复核）

- **证据**：`interface-api/client/sse.go:75-81` `err == nil` 即 return；`sse.go:206-210` `readEvents` 遇 `io.EOF` 且 dispatch 成功返回 `(lastID, nil)`；`client/sse_reconnect_test.go:62-64` 注释也确认「clean return reads as EOF, stream ends without a reconnect」，测试靠 `panic(http.ErrAbortHandler)` 才触发重连。
- **风险**：断连最常见的形态恰是 EOF，长连接退化为一次性请求，调用方既拿不到事件也收不到错误。
- **建议**：EOF 返回可重试哨兵错误走 `c.base(ctx)` 重连，仅 ctx 取消/显式结束才终止。

#### D4 · Group Prefix 在文档与请求之间没有一致性保证（P1）

- **证据**：`interface-api/docgen/openapi.go:59` 用 `cfg.prefixFor(spec.Tags)+spec.Path`，`Build` 读不到 `Group.Prefix`；`interface-api/client/encode.go:49` 只拼 `opts.pathPrefix`（默认空），`client/options.go:71-74` 把责任交给调用方。
- **风险**：文档路径带部署前缀、client 默认不带 → 404；两套来源（Tags / Config.Prefixes vs 手传）之间没有守卫。
- **建议**：让描述符自身携带前缀，client 自动拼接，docgen 复用同一来源；过渡期加启动期一致性断言。

#### E3 · Resolver 的 `cache` / `rr` 无上限，`Close` 只关连接不清状态（P1 · 已复核）

- **证据**：`adapter/client/nacos/resolver.go:194-195` `cache/rr` 按 `service@group` 永驻（`inflight` 每次刷新结束即删，见 `:488,514`，不在此列）；`:296-301` `Close()` 仅 `r.client.Close()`，不清 map、不置 closed；`:362` 键拼接无转义。
- **风险**：高基数服务名内存单调增长；Close 后 `Resolve` 仍会打已关闭的连接且缓存不释放；服务名含 `@` 时键可能碰撞。
- **建议**：Close 加锁清 `cache/rr` 并置 `closed`，入口拒绝已关闭调用；键改用结构化分隔（含长度或 JSON 编码）。

#### F2 · 分层守卫只覆盖 `app/module`，worker 直写 SQL 绕开快照（P1）

- **证据**：`demo/shell/check-imports.sh:33,36` 只 `go list ./app/...` 且 case 仅匹配 `app/module/`；`demo/shell/check-io-access.sh:17` dirs 仅 service/repository/query；`demo/worker/group1/scan.go:73`、`scan_claim.go:284,452` 直接 `rt.DB.Table("orders")` / `Create(row)`；`demo/worker/worker.go:19-23` 却写「复用与 HTTP 处理器相同的数据访问」。
- **风险**：worker / script / serviceclients / container 是门禁盲区，新增越界 import 或跨模块写表不会被拦；注释与实际不符，误导审查。
- **建议**：守卫 dirs 扩到 `worker/`，或让 worker 改走 query/repository + `appio` 快照，并同步注释。

### 2.3 P2

#### A3 · SIGKILL / 进程无法执行清理时 pid 文件残留

`gofy/stop.go:146-158` 发出 SIGKILL 后直接返回，不删文件。正常 `SIGTERM` 停止时被停进程自己的 cleanup（`gofy/start.go:961-965` 的 `RemoveIfOwner`）会删除 pid 文件，`stop.go:134-138` 不重复删是正确的；残留只发生在进程来不及执行清理的场景（SIGKILL、OOM kill、`-9` 后立刻断电）。→ `stop` 在确认退出后统一 `pidfile.RemoveIfOwner`。

#### A4 · 自定义命令保留字漏 `restart` / `status`

`gofy/command/command.go:31-39` 的 `reservedNames` 缺这两个；`gofy/internal/commandline/commandline.go:134-143` 先注册内建命令再追加自定义，cobra 同名取先加入者。→ 补齐保留字，或改为从 commandline 常量表生成。

#### A5 · 去重路径把 `IdentityUnknown` 当成「确有其它实例」

`gofy/start.go:667-670`、`gofy/worker_host.go:360-364` 只在 `IdentityMismatch` 时放行，其余（含 Unknown）按 `process.Alive` 判重——token 校验失败但 pid 存活就拒绝启动，而本机 pid 复用是常态。`stop.go:107-116` 对 Unknown 只 Warn 后照常发信号，不做判重，不属此问题。→ start/worker 两处：Unknown 视为无效记录直接覆盖。

#### A6 · 模块 Start/Stop 缺 panic 恢复

`gofy/start.go:492-499,535-545` 的 factory/register 有 recover，但 `startModules`（`:553`）、`reload.go:358` 的 stopModuleList、cleanup（`:934`）没有。→ 每处 Start/Stop 包一层 recover 转 error，避免 cleanup 半途中断漏清 pid/DB/logger。

#### B2 · `succeed` 台账失败只有一行 Warn（X-1 的真实残留）

`gofy/support/consumer/consume.go:468-485`。→ 加指标/告警，并在文档里明确「done 路径依赖业务幂等，dead 路径才保证落库」。

#### B8 · `processing` 租约不续期，且 `NewProcessor` 不约束 handler 耗时

`gofy/support/consumer/consume.go:727-828` 进入时写一次 `updated_at`；`:324-363` 不校验 `ProcessingLease` 与 handler 超时（对比 `support/claim/runner.go:220-223` 有强制校验）。→ 执行期按 attempts CAS 刷新 `updated_at`，或在构造期强制 lease ≥ handler 超时。

#### B9 · 台账只增不删；AMQP 无稳定 ID 时幂等形同虚设

`gofy/support/consumer/consume.go:929-992` 全是 `Updates`，无 Delete/归档；`support/claim/claim.go:773-793` 的 done/dead 行同样保留；`consume.go:377-382` 空 ID 补 `uuid.NewString()`，`support/consumer/amqp.go:298-338` 无 MessageId 时返回空串并自承「disables deduplication」。→ 加 `Prune`/TTL 分区；无稳定 ID 时启动告警或用 body hash 兜底键。

#### C2 · `Configure` 换代泄漏旧代连接池

`gofy/support/db/io.go:57-64,101-106` 的 `Install()` 无条件 `current.Store(r)`；`redis/io.go:97-102`、`sqlite/io.go:98-103`、`pg/io.go` 同构，`Close()` 只在 `current==r` 时 CAS 清 nil。→ `Install` 返回 `Swap` 出来的旧代并由 Configure 关闭，或 API 强制调用方持有并 Close。

#### C3 · Nacos 长轮询两端都缺保护

`gofy/support/nacos/client.go:131-141` 把 ctx 超时抬到 `hold=max(PollTimeout,30s)+Timeout`，但 `Long-Pulling-Timeout` 头仍发未抬升的 `PollTimeout`；`watch.go:186-188` 在 `changed==false` 时直接 `continue`，无退避（Listen 本身阻塞，仅服务端异常立即返回时才接近忙循环）。→ header 改用 `hold`，并对过快返回加 `RetryDelay` 休眠。

#### C5 · `uuidint.WithRetry(0)` 让主连接零次尝试

`gofy/support/uuidint/uuidint.go:163-175` 只拒绝负数；`:367` 使 `for attempt < 0` 一次不执行（主源零次尝试；有 fallback 时仍会试一次 fallback）；无 fallback 且全程无错误时 `errors.Join()` 空参返回 nil，`:401` 末尾拼出 `%!w(<nil>)` 且 `onError(nil)`。→ `times==0` 回落默认 3，且无 errs 时返回干净的 `ErrUnavailable`。

#### C6 · JWT 非数字 `exp` 被当作「无过期」放行（默认配置下）

`gofy/support/jwtauth/jwt.go:225-232` 仅当 `numericDate` 返回 ok 才校验过期；`:230-231` 在 `RequireExpiry=true` 时缺失会报 `ErrMissingExpiry`，但默认 `RequireExpiry=false`，且 `:268-285` 只接受 `json.Number`/`float64`——字符串/null/bool 形式的 `exp` 存在却按「无过期」放行。→ `exp` 存在但不可解析时报 `ErrMalformed`，并考虑默认要求 expiry。

#### D5 · `data` 缺失或为 `null` 一律返回 `(nil, nil)`

`interface-api/client/call.go:173-175,205-208`。普通 JSON 路由服务端漏 data 也算成功，调用方解引用 panic（demo 的 group3 已因此补过判空）。→ 仅 `spec.NoContent` / 204 返回 `(nil,nil)`，其余对非指针 Res 报 `ProtocolError`。

#### D6 · `Retry:0` / `Timeout:0` 无法表达「显式禁用」

`interface-api/client/client.go:185-187,206-208` 把 0 当未设置改回默认值；`call.go:74-77` 的 `WithTimeout(0)` 同样回退。→ 用指针或 `-1` 哨兵区分零值与未设置。

#### D7 · BaseURL 带 query 时路径被拼进 query 串

`interface-api/client/encode.go:50-56` `target := base; target += path`，`http://h?token=x` 变 `http://h?token=x/orders`；`:74-76` `u.RawQuery = query.Encode()` 无条件覆盖；`client.go:160-171` 校验只看 scheme/host。→ 解析 base 为 URL 后设置 Path，query 合并而非覆盖。

#### D8 · `WithConnectTimeout` 缓存满后派生的 transport 无人回收

`interface-api/client/client.go:308-321` 满时不入 map 直接返回；`:329-341` Close 只遍历 map——缓存满后新建的池关闭时机不受 Close 控制（缓解：空闲连接最终会由 Transport 的 `IdleConnTimeout` 自动回收）。→ 满时淘汰并 `CloseIdleConnections` 最旧条目，或复用共享溢出池。

#### E1 · k8slease 并发 Renew 自竞争（X-2 的残留契约问题）

`adapter/lease/k8slease/k8slease.go:315-352` Get→Update 无互斥，`h.version` 无锁写且全仓从未读取。→ `heldLease` 加 Mutex 包住 Get→Update，删死字段 `version`；文档声明 Renew 非并发安全。

#### E2 · leader 用调用方 ctx，取消时本轮刷新静默失败（X-3 的可观测性缺口）

`adapter/client/nacos/resolver.go:475,482-491`。StaleTTL 内已有缓存的等待者仍会被 `serveLocked` 用旧数据服务，「整批作废」并不发生；真正受影响的是必须刷新的那批等待者——刷新失败回退 stale 且收不到一次可区分的信号。→ leader 用 `WithoutCancel(ctx)+WithTimeout(Timeout)` 独立 ctx；waiter 至少收到一次刷新失败事件。

#### E4 · Locker 无 Close，held reservation 可永久残留

`adapter/lease/k8slease/k8slease.go:191-210,248,293` held 仅在 Acquire 失败/Release/Renew 失败时删；一旦 `heldLease` 被丢弃，该 key 永远 `ErrHeld`，map 随 key 增长，coordination transport 无释放口。→ 加 `Locker.Close` 清 held，并把「必须 Release」写进接口契约。

#### E5 · `random` / `round_robin` 仍会选中 weight=0 实例

`adapter/client/nacos/resolver.go:587-596` 仅 weight 分支过滤，默认策略是 random（`:681`）；`gofy/support/nacos/naming.go:26` 的 `Weight float64` 无法区分字段缺省与显式 0。→ 在 `filter` 里统一剔除 `Weight<=0`，并把「摘流只对 weight 策略生效」的现有说明改为全策略一致；若要区分缺省，依赖层改 `*float64`。

#### E6 · k8slease 无内置超时，TTL 极端值可溢出成 1s

`adapter/lease/k8slease/k8slease.go:104-120` 未设 `rest.Config.Timeout`；`:162,315,359` 全用调用方 ctx；`:415 int32(Ceil(ttl))` 在 `lease.ValidateTTL` 无上限的前提下，TTL 超过约 68 年才会溢出回退成 1s——理论性边界，分量较低。→ 每次调用加超时预算并设默认 Timeout，校验 TTL 上界。

#### E7 · `DocsModule` 强制开启，无环境门控

`adapter/gofy/docs.go:26` 直接 `m.cfg.Enabled = true` 覆盖入参，`:32-38` 无条件挂 UI/spec，仅注释要求「非生产」。→ 尊重 `Enabled` 或加显式环境开关。

#### E8 · 兜底分组的 Nacos 故障被包成 `ErrNoInstances`

`adapter/client/nacos/resolver.go:410-414` 把 `fbErr` 用外层 `%w=ErrNoInstances` 包裹。→ 换独立哨兵或让 `fbErr` 作外层 `%w`，否则白名单降级（B6 相关）会把「注册中心故障」误判成「无实例」。

#### F1 · 示例出厂配置使降级链路不可达，group3 预算自相矛盾（X-4 的残留）

`demo/config.yaml:268` `inner_external_base_uri: ''` → `demo/serviceclients/serviceclients.go:168` 不包 `degradable`、不写 metadata；`demo/worker/group3/discovery_check.go:59` 给唯一调用方 `WithTimeout(...,5s)`，而 `config.yaml:269` 单次预算也是 5s、注释写「最坏 10s」。primary 一旦吃满 5s，`shouldDegrade` 因 `ctx.Err()!=nil` 直接放弃。→ fallback 用独立子 ctx（仍受外层总 deadline 约束），group3 deadline 改 ≥2×timeout，或提供一个可用的示例 URI。

#### F3 · 配置变更策略整段被常量关闭

`demo/main.go:28` `const enableNacosSyncConfig=false` → `:59` Sync 被门控、`:68` watchEnabled=false、`demo/reload.go:67` 不加 nacosWatchModule；而 `demo/application.yaml:44,51,56-59` 已配 `enable_nacos_config_change_restart` / `config_change_strategy:restart` / `restart_grace_period:5`。→ 改为读该键或环境变量，或删掉这些键并注明「示例默认关闭」。

#### F4 · 两个幽灵键被非严格 YAML 静默吞掉

`demo/nacos.yaml:7` `authorization_bearer`、`demo/application.yaml:22` `ephemeral`；`gofy/support/nacos/config.go:732-747,580-590` 的 rawConfig/rawApplication 无对应字段，`:518,618` 用普通 `yaml.Unmarshal`（非 KnownFields）；`support/nacos/README.md:155` 也承认 ephemeral 不读、固定 true。→ 删两键或补字段接上，并对关键配置启用严格解码。

#### F5 · 分层守卫按「类别」放行，注释声明未落地

`demo/shell/check-imports.sh:146-159` query case 只 fail `module`/`contract`，注释却称「也不碰 adapter」；`adapter/client/framework/other` 静默放行；service/controller/repository 同病，仅 innerapi 是严格白名单。→ 各层改显式白名单 + `*)` fail。

#### F6 · 限流 Responder 未注入，429/503 错误信封不一致

`demo/app/container/container.go:79` `RateLimiter{}.Callback()` 零值；`demo/app/middleware/rate_limit.go:43-45` 注释要求装配根传 Responder；`adapter/gofy/bind.go:252-255` 不注入；`gofy/support/middleware/ratelimit.go:47-48` nil 时退化默认。→ `BindOptions` 收 `reg.Responder` 并填入。

#### F7 · 契约过滤参数被静默忽略

`examples/interface-service/appinterface/.../orderrequest/request.go:25-30` 的 `Status`(`oneof`)、`CreatedFrom/To`(`datetime`) 带校验；`demo/app/module/order/controller/order_controller.go:67` 只透传 `UserID/Page/PageSize`；`order_service.go:145` 的 `ListByUser` 只按 user_id 分页。→ 补 WHERE 条件，或从契约移除字段（否则客户端以为过滤生效）。

#### A7 · `retireGeneration` 用 Background 且同步阻塞

`gofy/reload.go:315` 发布新代后同步调用 `retireGeneration(context.Background(),...)`，内部 wait/stopCtx（`generation.go:274`）均基于 Background。→ 透传 Reload 的 ctx / `a.rt.Context()`，否则慢 Reload 会拖长停机。

#### C9 · `LoadConfig` 缺 Discovery 校验

`gofy/support/nacos/config.go:410-422` 只调 `ParseChangeStrategy`；`Load:504-506` 却补了 `cfg.Discovery.Validate()`。→ 单文件入口同样调用 Validate，避免拼错配置到首次寻址才暴露。

#### D11 · refcheck 的 `adapterModule` 定义了却没进 deny

`interface-api/cmd/refcheck/main.go:54` 定义后未加入任何规则；`:139` cmd 为 `allowAll`，`:146-149` deny 仅 gofy/gin；`main_test.go:68` 还断言 adapter 不被拒绝。→ 把 adapterModule 并入 library 各规则 deny，否则「全库框架无关」在 cmd 下不成立。

#### E10 · adapter import 守卫的禁用前缀是占位符（冗余，非放行漏洞）

`adapter/shell/check-imports.sh:10` 禁 `github.com/yourorg/`，而 `go.mod:1` 是 `github.com/bingcool/gofy-adapter`；`:14-26` 的白名单本身已拦住一切未列出的外部包，该行目前不构成实际放行，但守卫意图失效。→ 换成真实前缀，并把 client-go 白名单限定到 k8slease 目录。

### 2.4 P3

| 编号 | 问题 | 证据 | 建议 |
|---|---|---|---|
| A8 | `IdentityUnknown` 时 status 仍报 running、stop 仍发信号 | `gofy/status.go:262-269`、`stop.go:100-116`；`internal/process/token_darwin.go:18-28` sysctl 失败返回空 token | Unknown 且非本进程时判 stale，或要求 `--force` |
| B10 | Reap 三条 UPDATE 不在事务内、无分批 | `gofy/support/claim/claim.go:692-714` | 包进 Transaction 并 `ORDER BY id LIMIT n` 循环 |
| B11 | RocketMQ 停机 `wg.Wait()` 无超时，handler 忽略 ctx 时永久阻塞 | `gofy/support/rocketmq/consumer.go:400-405`（`ticker.go:53` 为同步调用、不产生泄漏；`runner.go:256` 有 DrainTimeout 兜底） | 收尾等待用 select + 超时包裹 |
| B7 | `Runner.Run` 的 `fatal` 存在窄窗口数据竞争（X-6 降级：经 fatalCh / `wg.Wait` 有 happens-before，仅 `ctx.Done()` 分支与 `once.Do` 写入并发时无同步） | `gofy/support/worker/runner.go:165-176,244-259` | 用缓冲 `chan error` 或 `atomic.Value` |
| C4 | 限流 `hits` map 在 GC 周期内无硬上限（X-7 降级：默认 `SetTrustedProxies([]string{})` 下 XFF 不可伪造，「键可伪造」只在显式放宽信任代理后成立） | `gofy/start.go:275`、`gofy/support/ratelimit/ratelimit.go:97,135-150` | 设最大 key 数，sweep 周期与 window 解耦 |
| C7 | `uuidint.WithTTL(0)` 覆盖默认值 | `support/uuidint/uuidint.go:145-155,284-289,519-521` | `ttl==0` 不覆盖 settings |
| C8 | `SetInnerExternalBaseURI` 不校验 URL | `gofy/support/nacos/register.go:113-129` | `url.ParseRequestURI` 校验 scheme/host |
| C10 | 访问日志默认记录 query，仅黑名单脱敏 | `gofy/support/middleware/accesslog.go:70-80,229-240` | 默认 `LogQuery=false` 或改白名单 |
| D9 | 流式调用忽略 `WithTimeout`（非静默：`options.go:79-85` 已声明该行为；缺口是想要超时的调用方没有替代方案） | `interface-api/client/sse.go:56,241,280` | 提供流式整体 deadline，或在文档显式标注 |
| D10 | `PageRequest` Offset 可为负（`default` tag 由 creasty/defaults 在 `gofy/support/bind.go:19` 应用、服务端生效；client 侧零值不发送与 D1 同源） | `interface-api/contracts/common/paging/paging.go:18-20`、`client/encode.go:394-395` | Offset 下界钳制；client 按 default tag 填充 |
| E9 | 零值 `Resolver` 部分路径 panic | `adapter/client/nacos/resolver.go:472,561`（`:394` 只返回 service 必填错误，不 panic） | Resolve 入口加就绪检查，返回错误而非崩溃 |
| F8 | service 自算 offset，直调路径无下界保护 | `demo/app/module/order/service/order_service.go:145`、`product_service.go:71` | 改用 `req.Offset()` 或入口 clamp |
| E9b | rr 键拼接无转义（并入 E3） | `adapter/client/nacos/resolver.go:362` | 结构化键 |
| A9 | `start --daemon` 在 Windows 直接 panic | `gofy/daemon.go:20-22` | 返回明确错误而非 panic |
| C11 | `otel` 导出失败只记录日志（设计取舍，记录备查） | `gofy/start.go:948-952` | 保持现状，但补一条计数器 |
| F9 | demo 注释称 worker 复用 HTTP 数据访问（与 F2 同源） | `demo/worker/worker.go:19-23` | 与 F2 一并改 |

---

## 3. 按主题复核「已做对」的部分

以下经源码复核确认成立，后续审计不应重复报为缺陷：

- **分代可见性**：`generation.Set.Middleware` 只做兜底、不覆盖已有 pin；`support/generation` 的 gate + lease + defer release 保证请求入口固定一代。
- **claim 围栏**：`owner + attempts` CAS、`FOR UPDATE SKIP LOCKED` 事务内取行、`park` 失败不 ACK、dead/done 终态、`ceilSeconds` 避免 DATETIME 舍入成 0。
- **worker 就绪**：`ReadyReporter` 由各传输层 `OnReady` 回调、`Runner.Ready()` 汇总、按 Identity 去重、drain 立刻 503、启动失败永不就绪。
- **Nacos resolver 主链路**：`lastAttempt` 节流 / `lastSuccess` stale 双时间戳、负缓存、单飞按 `service@group` 隔离、本地分组门控、metadata URL 主校验、weight 策略排除 0 权重并 fail-closed。
- **类型化客户端**：`WithPathPrefix` 只在唯一构造点拼接，Call/SSE/download/chunked 覆盖；`Proxy:nil` 避免内网地址走环境代理；`cappedBuffer` 让 multipart 峰值等于上限。
- **demo 分层与迁移**：service 唯一可 import `app/config` 且只收本模块配置值；query 跨模块只写 SQL 表名；`migrations/0005_job_task.sql` 与 `claim.DDL` 逐字段一致；`Create` 在同一事务扣库存+建单。
- **客户端跨 reload**：`serviceclients` 进程级构造一次、Reload 不重建，`DemoService/Source/SetLogger/Close` 全 nil 安全，`worker --list` 传 nil 不 panic。
- **三仓库 replace 与 broker 实例名**：目标存在且 module 名匹配，group1 订阅引用的 `default/ratelimit` 与配置一一对应。

---

## 4. 常驻内存与资源生命周期专题（横向）

| 位置 | 增长/泄漏形态 | 触发 | 关联条目 |
|---|---|---|---|
| `adapter/client/nacos/resolver.go:194-206` | `cache/rr` 按 service@group 永驻（`inflight` 每次刷新结束即删），Close 不清 | 服务名高基数、动态服务发现 | E3 |
| `gofy/support/ratelimit/ratelimit.go:81-150` | `hits` map 有周期 sweep（过期键会清），但 GC 周期内无 key 总数硬上限 | 大量不同来源 IP / 大 window | C4 |
| `gofy/support/consumer/consume.go:929-992` | 台账行只 UPDATE 不删/归档，唯一索引单调膨胀 | 长跑消费者 | B9 |
| `interface-api/client/client.go:308-341` | 派生 transport 缓存满后新建池不入 map，Close 回收不到 | 调用方传多种 connectTimeout | D8 |
| `gofy/support/db/io.go:57-106` 等四个 io 包 | `Install` 覆盖旧代，旧连接池不可达 | 反复 Reload | C2 |
| `gofy/support/nacos/sync.go:33-39`、`watch.go` | 每次 Sync/Watch 自建 Transport，退出不 Close | 频繁 Sync、Watcher 重启 | C1 |
| `adapter/lease/k8slease/k8slease.go:191-210` | `held` map 在句柄丢失后永久残留 | 调用方忘记 Release | E4 |
| `gofy/reload.go:315` | 旧代排空用 Background，无取消路径 | 停机时慢 Reload | A7 |

共同模式：**「创建时登记、关闭时不反登记」**。建议统一给这类包定义 `Close()` 语义（清状态 + 关连接 + 置 closed），并在 `App` 的 cleanup 里按代收口。

---

## 5. 修复优先级建议（B1 已于 2026-11-18 修复，其余不在本轮实现）

1. **第一批（数据正确性）**：~~B1~~ → B6 → B4/B5 → D2/D3。先堵住「消息被丢」和「流被当成功」，再统一租约精度。（B1 已修复，见 §2.1；B3 原列于此，复核后撤销，见 §1 X-5。）
2. **第二批（生命周期与资源）**：A1/A2 → C1/C2 → E3/D8 → A7。这一批都是「Close/取消顺序」，改动小但回归面广，需要配套停机测试。
3. **第三批（契约一致性）**：D1（已知遗留）、D4、F2/F3/F4/F5、E5/E8、D11/E10。以守卫与断言为主，避免行为突变。
4. **第四批（P3）**：按模块顺手清理，不单独排期。

验证建议：B1 用 Redis + 双副本注入 in-flight；A1/A2 用 daemon 启停与 `SIGTERM` 时序；C1/C2/E3 用连接数/内存曲线；D2/D3 用假后端返回错误信封与 EOF；F1 用 dev-nacos 真机 e2e（本轮已有脚本与方法，见 `docs/nacos-service-discovery-client-design.md` §0.1）。

---

## 6. 限制与未验证项

- 本报告是**源码级静态审计**：除已标注「已复核」的条目为回读源码二次确认外，未运行故障注入、压测、真实 Kubernetes Lease 冲突、PostgreSQL/SQLite 上的台账迁移，也没有对全部 502 个文件逐行走查（按模块分派覆盖主要路径与全部支撑包）。
- P2/P3 中若干条是「特定配置下才触发」的边界判断，落地前建议先写一个最小复现测试确认，避免把设计取舍当 bug 改。
- 历史报告已标「已修复」，本报告未对历史修复做回归测试，只观察当前代码是否仍可见同类形态。

---

---

---

## 7. 问题索引（按严重度排序）

按 P0 → P1 → P2 → P3 排列，同级别保持第 2 节顺序。「是否已修复」是 **2026-11-18 生成本报告时** 的快照：B1 已在同日修复并更新该列，其余条目本轮只做审计、未改动业务代码，因此仍为「否」；后续修复可直接在表中更新该列。§1 的 X-1 ~ X-7 是复核后撤销/降级/修正的结论，不计入本索引。

| 编号 | 严重度 | 问题 | 是否已修复 |
|---|---|---|---|
| B1 | P0 | Redis 队列把 `ErrInFlight` 当成普通失败，耗尽重试后永久丢消息 | 是（2026-11-18） |
| A1 | P1 | `start --daemon` 没有父子握手，父进程无条件报成功 | 否 |
| A2 | P1 | `Shutdown()` 停机时 runCtx 取消排在 DB 关闭之后 | 否 |
| B4 | P1 | 消费台账列类型硬编码 MySQL `datetime(3)`，PG/SQLite 方言分支形同虚设 | 否 |
| B5 | P1 | MySQL 路径写库用秒精度、过期比较用毫秒精度 | 否 |
| B6 | P1 | `ErrInFlight` 与四种 broker 的重投语义都不匹配 | 否 |
| C1 | P1 | Nacos `Sync` / `Watcher` 从不关闭自建 Client | 否 |
| D1 | P1 | `validate:required` 零值被编码器静默丢弃，与 docgen required 漂移 | 否（已知遗留） |
| D2 | P1 | 流式响应不校验 Content-Type / ResponseKind，下载与分块流会把错误信封当数据 | 否 |
| D3 | P1 | SSE 服务端正常关闭（EOF）不触发重连 | 否 |
| D4 | P1 | Group Prefix 在文档与请求之间没有一致性保证 | 否 |
| E3 | P1 | Resolver 的 `cache` / `rr` 无上限，`Close` 只关连接不清状态 | 否 |
| F2 | P1 | 分层守卫只覆盖 `app/module`，worker 直写 SQL 绕开快照 | 否 |
| A3 | P2 | SIGKILL / 进程无法执行清理时 pid 文件残留 | 否 |
| A4 | P2 | 自定义命令保留字漏 `restart` / `status` | 否 |
| A5 | P2 | 去重路径把 `IdentityUnknown` 当成「确有其它实例」 | 否 |
| A6 | P2 | 模块 Start/Stop 缺 panic 恢复 | 否 |
| B2 | P2 | `succeed` 台账失败只有一行 Warn（X-1 的真实残留） | 否 |
| B8 | P2 | `processing` 租约不续期，且 `NewProcessor` 不约束 handler 耗时 | 否 |
| B9 | P2 | 台账只增不删；AMQP 无稳定 ID 时幂等形同虚设 | 否 |
| C2 | P2 | `Configure` 换代泄漏旧代连接池 | 否 |
| C3 | P2 | Nacos 长轮询两端都缺保护 | 否 |
| C5 | P2 | `uuidint.WithRetry(0)` 让主连接零次尝试 | 否 |
| C6 | P2 | JWT 非数字 `exp` 被当作「无过期」放行（默认配置下） | 否 |
| D5 | P2 | `data` 缺失或为 `null` 一律返回 `(nil, nil)` | 否 |
| D6 | P2 | `Retry:0` / `Timeout:0` 无法表达「显式禁用」 | 否 |
| D7 | P2 | BaseURL 带 query 时路径被拼进 query 串 | 否 |
| D8 | P2 | `WithConnectTimeout` 缓存满后派生的 transport 无人回收 | 否 |
| E1 | P2 | k8slease 并发 Renew 自竞争（X-2 的残留契约问题） | 否 |
| E2 | P2 | leader 用调用方 ctx，取消时本轮刷新静默失败（X-3 的可观测性缺口） | 否 |
| E4 | P2 | Locker 无 Close，held reservation 可永久残留 | 否 |
| E5 | P2 | `random` / `round_robin` 仍会选中 weight=0 实例 | 否 |
| E6 | P2 | k8slease 无内置超时，TTL 极端值可溢出成 1s | 否 |
| E7 | P2 | `DocsModule` 强制开启，无环境门控 | 否 |
| E8 | P2 | 兜底分组的 Nacos 故障被包成 `ErrNoInstances` | 否 |
| F1 | P2 | 示例出厂配置使降级链路不可达，group3 预算自相矛盾（X-4 的残留） | 否 |
| F3 | P2 | 配置变更策略整段被常量关闭 | 否 |
| F4 | P2 | 两个幽灵键被非严格 YAML 静默吞掉 | 否 |
| F5 | P2 | 分层守卫按「类别」放行，注释声明未落地 | 否 |
| F6 | P2 | 限流 Responder 未注入，429/503 错误信封不一致 | 否 |
| F7 | P2 | 契约过滤参数被静默忽略 | 否 |
| A7 | P2 | `retireGeneration` 用 Background 且同步阻塞 | 否 |
| C9 | P2 | `LoadConfig` 缺 Discovery 校验 | 否 |
| D11 | P2 | refcheck 的 `adapterModule` 定义了却没进 deny | 否 |
| E10 | P2 | adapter import 守卫的禁用前缀是占位符（冗余，非放行漏洞） | 否 |
| A8 | P3 | `IdentityUnknown` 时 status 仍报 running、stop 仍发信号 | 否 |
| B10 | P3 | Reap 三条 UPDATE 不在事务内、无分批 | 否 |
| B11 | P3 | RocketMQ 停机 `wg.Wait()` 无超时，handler 忽略 ctx 时永久阻塞 | 否 |
| B7 | P3 | `Runner.Run` 的 `fatal` 存在窄窗口数据竞争（X-6 降级：经 fatalCh / `wg.Wait` 有 happens-before，仅 `ctx.Done()` 分支与 `once.Do` 写入并发时无同步） | 否 |
| C4 | P3 | 限流 `hits` map 在 GC 周期内无硬上限（X-7 降级：默认 `SetTrustedProxies([]string{})` 下 XFF 不可伪造，「键可伪造」只在显式放宽信任代理后成立） | 否 |
| C7 | P3 | `uuidint.WithTTL(0)` 覆盖默认值 | 否 |
| C8 | P3 | `SetInnerExternalBaseURI` 不校验 URL | 否 |
| C10 | P3 | 访问日志默认记录 query，仅黑名单脱敏 | 否 |
| D9 | P3 | 流式调用忽略 `WithTimeout`（非静默：`options.go:79-85` 已声明该行为；缺口是想要超时的调用方没有替代方案） | 否 |
| D10 | P3 | `PageRequest` Offset 可为负（`default` tag 由 creasty/defaults 在 `gofy/support/bind.go:19` 应用、服务端生效；client 侧零值不发送与 D1 同源） | 否 |
| E9 | P3 | 零值 `Resolver` 部分路径 panic | 否 |
| F8 | P3 | service 自算 offset，直调路径无下界保护 | 否 |
| E9b | P3 | rr 键拼接无转义（并入 E3） | 否 |
| A9 | P3 | `start --daemon` 在 Windows 直接 panic | 否 |
| C11 | P3 | `otel` 导出失败只记录日志（设计取舍，记录备查） | 否 |
| F9 | P3 | demo 注释称 worker 复用 HTTP 数据访问（与 F2 同源） | 否 |
