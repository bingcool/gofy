# Gofy 三仓库代码审计报告（第二轮）

**审计时间：** 2026-11-20\
**审计基线：** `gofy@6eb7f05`、`gofy-interface-api@dd1cd4a`、`gofy-adapter@76f6e25`\
**审计范围：** 三个仓库的全部非测试代码（gofy 约 92.5k 行 / 496 文件，interface-api 约 8.4k 行，
adapter 约 4.5k 行）\
**审计原则：** 不新增功能。只报告 Bug、正确性缺陷、并发与生命周期问题、资源泄漏、安全隐患、
明显的性能与一致性缺陷。\
**修复进度：** §3 的 8 组 P0/P1 **全部已修**（`a697efd`：§3.1(a)、§3.2、§3.3、§3.4；
`cd6aa3c`：§3.1(b)(c)、§3.5–§3.8）。§4 的 P2 未动。已修项在标题上标注，正文末尾附修复依据。

## 1. 方法与边界

本轮不是纯静态阅读，分三层：

1. **工具链基线**：`go build`、`go vet`、`go test -race`、`cgo-free`、Windows cross、`staticcheck`
   （2025.1.1，`-checks all`）。三个仓库 build/vet/test 全绿，staticcheck 只报出死代码、过时 API
   与测试里的 nil context——这些在 §4 统一列出。
2. **可复现探针**：对高风险结论不靠推理下结论，直接写一次性测试跑出结果（§3.1 的三条租户问题
   都是这样定性的），跑完即删，仓库不留测试残留。
3. **区域深扫**：按「核心生命周期 / 数据层 / 消息与 worker / 其余支撑包 / 契约与适配器」分区阅读，
   候选条目逐条回到源码核对后才写进本报告。

**没有做的事，需要读者知道**：没有对真实 Kafka / RabbitMQ / RocketMQ / Redis / MySQL 跑集成
压测，没有做 fuzz，没有在 Windows 上实际运行。凡依赖运行时环境才能定性的线索，放在 §6 而
不是正文。

与上一轮（`docs/gofy-architecture-audit-2026-11-20.md`）的关系：该报告已修的 6 项不再列入；
用户明确暂缓的 3 项（脱敏默认变量可写、INSERT 按列名脱敏、AccessLog 默认记录 query）也不
重复列出。

## 2. 结论摘要

| 级别 | 数量 | 已修复 | 主题 |
| --- | --- | --- | --- |
| P0 | 1 | 1 | 延迟队列在停机瞬间永久丢消息（一次正常发布即触发） |
| P1 | 7 | 7 | 租户隔离、普通队列停机丢消息、消息 ID 长度、Nacos 配置变更丢失、reload 丢 SQL 策略、worker 模式 Shutdown 无效、`silent` 语义失效 |
| P2 | 6 组 | 0 | DDL 时间戳、Slice 越界、JSON null、测试不可重复、死代码与过时 API、OTEL 未用 ctx |

**两批修完 §3 全部 P0/P1**：`a697efd` 修了 §3.1(a)、§3.2、§3.3、§3.4；`cd6aa3c` 修了写入侧
的 §3.1(b)(c) 与 §3.5–§3.8。每条都补了能在旧代码上失败的回归测试，失败信息里带着现场
（`ZCARD = 0`、"leaked tenant b"、`tenant=""`）。

§4 的 6 组 P2 仍未动；其中最值得先做的是 4.1 的 `created_at` 与 4.4 的测试可重复性——前者影响
运维判断，后者影响后续租户相关改动的可信度。

---

## 3. P0 / P1

### 3.1 【P1，安全 / 数据完整性】租户隔离插件有三条静默失效路径（全部已修复）

三条都已用一次性探针在本仓库实测复现（`support/tenant`，sqlite 内存库），不是推理。

#### (a) WHERE 里只要「提到」tenant_id，自动过滤就被关掉 —— **已修复**

- **位置**：`support/tenant/plugin.go:184`（`filter`）、`:231`（`exprsMentionColumn`）
- **证据**：

```go
if hasTenantCondition(stmt, p.column) {
    return                       // 认为调用方已经过滤过租户
}
stmt.AddClause(clause.Where{...})
```

`hasTenantCondition` 遍历表达式，遇到 `clause.OrConditions`、`clause.NotConditions` 直接递归；
遇到 `clause.Expr`（手写 SQL）用**子串匹配**判断：

```go
case clause.Expr:
    if strings.Contains(strings.ToLower(v.SQL), strings.ToLower(column)) {
        return true
    }
```

探针结果（租户 a 的上下文，表里有 a、b 各一行）：

```
db.Where("tenant_id <> ?", "zzz").Find(&all)
→ probe3 rows=2
   leaked: tenant="a"
   leaked: tenant="b"
```

- **影响**：`Where("tenant_id <> ?")`、`Where("tenant_id = ? OR is_public = 1")`、`Where("NOT (tenant_id = ?)")`
  这类写法会让插件认为「租户条件已经有了」，于是**不追加任何过滤**，跨租户数据直接可见。
  这不是「绕过开关」那种显式行为，而是写了一个带 tenant_id 的正常条件后，防护静默消失。
- **建议**：把「已满足」的判定收紧到**只在 AND 链顶层的 `clause.Eq` 且列名等于租户列**时成立；
  `OrConditions` / `NotConditions` 内部一律不算；`clause.Expr` 不再用子串猜测（要支持手写条件，
  就要求调用方显式用 `tenant.Without` 表达意图）。
- **验证**：`go test ./support/tenant -run TestProbe...`（探针已删除，可按上面三行代码重写）。

**修复（`a697efd`）**：判定改为「只在合取路径上的等值才算」，并且顺带修掉了第二个必须一起改
的地方。

- `constrainsColumn` 只认顶层 AND 链里的 `clause.Eq`（含 `AndConditions` 分组与嵌套
  `Where`）；`OrConditions` / `NotConditions` 不再进入。
- 手写 SQL 不再子串匹配，`isTenantEquality` 只接受**恰好一条** `tenant_id = ?`（允许
  `` `tenant_id` ``、`orders.tenant_id`）：`=` 只有一个、右侧必须是占位符、左侧末段等于列名。
  `<>`、`!=`、`LIKE`、`IN`、析取、子查询、右侧字面量一律不认。
- **顺带发现的第二个缺陷**：即使判定为「没过滤」，原来的 `stmt.AddClause` 也补不对。
  GORM 把新表达式追加到 WHERE 列表末尾，而 `a OR b` 再追加 `AND tenant` 渲染成
  `a OR b AND tenant`——AND 只约束最后一个操作数，`tenant_id = ? OR code = ?` 仍然返回别的
  租户的行（这一条是写测试时才暴露的：只改判定，`or_branch` 用例照样泄漏）。现在把调用方已有
  的 WHERE 先包成一个 `AndConditions` 分组，得到
  `(tenant_id = ? OR code = ?) AND tenant_id = ?`。
- 各类形态的生成 SQL（租户 a）：

```
none  WHERE `tenant_id` = ?
eq    WHERE tenant_id = ?                                     ← 不重复追加
ne    WHERE tenant_id <> ? AND `tenant_id` = ?                ← 追加
or    WHERE (tenant_id = ? OR code = ?) AND `tenant_id` = ?    ← 分组后追加
and   WHERE (code = ? AND amount > ?) AND `tenant_id` = ?
```

- 回归测试 `TestTenantFilterSurvivesConditionsThatMentionTheColumn` 覆盖五种形态（不等式、
  析取、取反、列名只作为字面量、`Or` 落在租户列上）。旧实现下 `inequality` / `disjunction` /
  `value mentions the column` 三个用例返回两个租户的行，`or branch` 用例在只改了判定的中间
  版本上仍然泄漏——两半都被钉住。

#### (b) `Save` / `Select("*").Updates` 会把 tenant_id 写成零值 —— **已修复**

- **位置**：`support/tenant/plugin.go:184`（只在 WHERE 侧加固，SET 侧没有保护）
- **证据**：GORM 的 `Save` 对带主键的结构体走 `Updates` 并把 `Selects` 设为 `*`，零值字段也会
  进 SET 列表。探针：

```go
partial := order{ID: row.ID, Code: "renamed"}   // 只带 ID 和要改的字段
db.WithContext(ctxFor("a")).Save(&partial)
→ after Save: id=1 tenant="" code="renamed"
```

- **影响**：行还在，但 `tenant_id` 变成空串，从此**不属于任何租户**——所有带租户过滤的查询都
  看不到它，等于业务层的数据丢失。`Select("*").Updates(model)` 是同样的效果。这不是调用方
  「显式改租户」，而是 GORM 全字段更新的常规用法。
- **建议**：在 `filter` 里同时检查 SET 子句：若 UPDATE 的赋值集合包含租户列，则把它从 SET 中
  移除（或强制写回 `resolve` 得到的当前租户）。
- **验证**：同上探针。

**修复（`cd6aa3c`）**：`filterUpdate` 在 UPDATE 的 Before 回调里把租户列追加到
`stmt.Omits`。`Omits` 在 `Selects` 之后生效，因此能压过 `Save` 装上的 `*`；GORM 构造 SET 时
读到该列为 false，就不写入它。WHERE 已经把行限定在当前租户，原本也没有什么租户值需要写。
`tenant.Without` 的路径直接跳过这个守卫，管理端仍能搬行——`TestWithoutCanStillMoveARowAcrossTenants`
钉住这一点，`TestSaveDoesNotClearTenant` 覆盖 `Save` 与 `Select("*").Updates` 两种写法。

#### (c) 批量插入混合租户时，未赋值的那几行不会被填充 —— **已修复**

- **位置**：`support/tenant/plugin.go:314`（`explicitColumnSet` 的 slice 分支）
- **证据**：注释写的是「任一行显式赋值即视为已提供，GORM 的 SetColumn(fromCallbacks=true) 会
  填充其余行」，但代码是 `return true` → `fill` 直接返回，**根本没有调用 SetColumn**：

```go
case reflect.Slice, reflect.Array:
    for i := 0; i < rv.Len(); i++ {
        if _, zero := field.ValueOf(...); !zero {
            return true          // ← 只要有一行有值，整批都不填
        }
    }
```

探针：

```
rows := []order{{TenantID: "a", Code: "explicit"}, {Code: "implicit"}}
Create(&rows)
→ inserted: id=1 tenant="a"
→ inserted: id=2 tenant=""      ← 未赋值那行落库为空租户
```

- **影响**：与 (b) 相同的后果——插入成功、无错误、行却游离在所有租户之外。批量导入、批量
  补数据这类场景很容易踩到。
- **建议**：逐行判断并逐行 `SetColumn`（GORM 支持按行设置），或者干脆规定批量插入必须整批
  显式赋值并在不一致时报错。
- **验证**：同上探针。

**修复（`cd6aa3c`）**：按行填，不再整批跳过。原注释「GORM 的 `SetColumn(fromCallbacks=true)`
会填充其余行」其实是两处错：代码在 `return true` 处直接退出，根本没调 `SetColumn`；而查 GORM
1.31 的 `ConvertToCreateValues` 可见，批量插入的 VALUES 是**逐行** `field.ValueOf` 读出来的，
整批 `SetColumn` 即使调用也只能覆盖显式值，填不了空值。现在：

- 结构体切片：逐行判断，只对零值行 `field.Set(ctx, rv.Index(i), id)`；
- map 切片（含指针形态）：只对缺键或零值的 map 写 `row[column] = id`；
- `OverwriteExplicit=true` 仍走原来的整批 `SetColumn`。

`TestBatchInsertFillsOnlyZeroTenantRows` 的 struct / map 两组都用「一行显式写 b、一行留空」
验证：显式行保持 b，空行落成 a。

### 3.2 【P1，消息可靠性】普通 Redis 队列在停机时丢掉手上那条消息 —— **已修复**

- **位置**：`support/queues/consume.go:180`
- **证据**：

```go
msg, ok, err := q.Pop(ctx, 0)     // LPOP：已经从 Redis 删除
...
if err := callHandler(ctx, handler, msg); err != nil {
    if ctx.Err() != nil {
        return nil                // 直接退出，没有把 msg 写回去
    }
```

- **影响**：优雅停机时 ctx 被取消，handler 通常也会因为同一个 ctx 失败；此时这条消息既不重试
  也不回队，**永久丢失**。Kafka/AMQP 那几条链路不会丢（不 ack 即可），Redis 队列因为 POP 是
  破坏性操作，必须显式回写。
- **建议**：ctx 已取消且 handler 失败时，用独立的短超时 ctx（`context.WithoutCancel` +
  `Timeout`）把消息 `LPUSH` 回队再返回。
- **验证**：起一个 Redis，投一条消息，handler 内 `time.Sleep`，期间发 SIGTERM，观察 `LLEN` 归零。

**修复（`a697efd`）**：与 3.3 同一处逻辑，一起改。ctx 已取消而 handler 失败时，先用
`shutdownRequeue` 造一个回写 context 再把消息 `RetryInFlight` 回重试 ZSET：
`context.WithoutCancel(调用方 ctx)` 保留链路/租户值、丢掉会让写入必然失败的取消信号，外套
`requeueTimeout`（5s）避免一个不可达的 Redis 拖住停机。回写不消耗重试预算——失败由取消造成，
不能说明消息有问题，扣预算可能在停机时丢掉最后一份副本。

回归测试 `TestQueueConsumeRequeuesOnShutdown` 断言三件事：重试 ZSET 里有 1 条、用新 context
`Pop` 能取回它、`RetryNum` 仍是 0。把回写那行换成空操作后测试立刻失败（`retry ZSET size = 0`）。

### 3.3 【P0，消息可靠性】延迟队列在停机时丢掉整批（最多 10 条） —— **已修复**

- **位置**：`support/queues/delayconsume.go:78`，配套 `support/queues/delayqueue.go:79`、
  `support/queues/lua.go:30`
- **证据**：`Pop` 的 Lua 脚本「取出并删除」到期消息（注释原文：*takes and deletes due messages*），
  消息只存在于 Go 内存里；消费循环逐条处理，但每轮开头先判 ctx：

```go
for _, msg := range messages {
    if ctx.Err() != nil {
        return nil            // 剩下的 messages 直接丢弃
    }
    if err := callHandler(...); err != nil {
        if ctx.Err() != nil {
            return nil        // 这一条也没回写
        }
```

`defaultPopLimit = 10`（`delayqueue.go:15`）。

- **影响**：一次 SIGTERM 最多丢 10 条延迟消息，且**没有任何日志**。触发条件是一次正常的
  滚动发布，代价是数据永久丢失——这是本轮唯一的 P0。
- **建议**：同 3.2——退出循环前把未处理的和处理失败的都写回 ZSET（`RetryInFlight` 已是现成的
  不消耗重试预算的回写路径）。
- **验证**：向延迟队列投 10 条已到期消息，`PopLimit` 默认，消费中途 SIGTERM，检查 `ZCARD`。

**修复（`a697efd`）**：循环改成按下标遍历，两处 `return nil` 之前都先
`q.requeueAfterCancel(ctx, opts, messages[i:]...)`——**从当前这条起**全部写回，含 handler 失败
的那条（导致失败的是取消，不是消息）。写回走 `RetryInFlight`、延迟 0：消息立刻重新到期，任何
实例的下一次轮询都能取到，且不消耗重试预算。整批共用一份 5s 回写预算（`requeueTimeout`），
最坏情况下停机也只多等这么久；单条失败通过 `OnError` 上报，不静默吞掉。

两条回归测试分别钉住两条分支：

| 用例 | 场景 | 断言 |
| --- | --- | --- |
| `TestDelayConsumeRequeuesBatchOnShutdown` | handler 在取消时失败 | `ZCARD = 3`（整批回来），handler 只进入 1 次 |
| `TestDelayConsumeRequeuesOnlyUnprocessedOnShutdown` | 第一条处理成功后取消 | `ZCARD = 2`（已处理的不重复投递） |

旧实现下两条都读到 `ZCARD = 0`：整批已从 ZSET 删除且没有任何回写。

### 3.4 【P1，消息可靠性】幂等台账 `message_id` 只有 128 字节，外部可控 —— **已修复**

- **位置**：`support/consumer/consume.go:879`；取值来源 `support/consumer/amqp.go:365`
- **证据**：

```go
MessageID string `gorm:"size:128;not null;uniqueIndex:uk_worker_consumed,priority:2"`
...
func amqpMessageID(delivery amqp091.Delivery, headers map[string]string) string {
    if id := delivery.MessageId; id != "" {
        return id                    // 生产者完全可控，长度无上限
    }
```

而 `claim` 失败会被当作**可重试**错误（`consume.go:507-515`）。

- **影响**：一条 `MessageId` 超过 128 字符的消息会造成两种后果，取决于 MySQL 的
  `sql_mode`：严格模式下 insert 报 `Data too long` → 一直重试、永远不 ack，**卡死一个分区**；
  非严格模式下被截断 → 两条前缀相同的长 ID 撞进同一个唯一索引 → 后者被当成「重复消息」静默
  ack，**真消息丢失**。
- **建议**：在适配器侧把 ID 规整到列宽（超长时用 `sha256(id)[:32]` 这类稳定摘要），并在台账
  侧显式声明列宽约束；不要依赖数据库截断。
- **验证**：AMQP 投一条 `MessageId` 为 200 字符的消息，开严格模式观察重投日志。

**修复（`a697efd`）**：`ledgerMessageID` 在**写入台账之前**把 ID 压到列宽，超长时存
`sha256:<64 位十六进制>`（71 字符，重投之间稳定）。判定按**字符数**而不是字节数，因为
`VARCHAR(n)` 限制的是字符：128 个汉字的 ID 放得下，不应该被摘要。

与 `msg.ID` 分离是关键：handler 与日志仍然看到生产者的原值（否则按 ID 排查会突然对不上），
只有台账键是摘要；两者不同时日志多一个 `ledger_id` 字段，运维据此仍能定位那一行。`claim`、
`succeed`、`fail`、`park`、`renew` 五处全部改用同一个键——续期用错键只会每一跳都报
`ErrStaleClaim`，租约当场失效。

三条测试：200 字符 ID 存成摘要且 handler 看到原值；放得下的 ID 原样存（含 128 个多字节字符）；
只在第 129 个字符起才有差别的两个 ID 不会塌成同一个键。

### 3.5 【P1，配置生效】Nacos 配置变更在 apply 失败后不会再来一次 —— **已修复**

- **位置**：`support/nacos/watch.go:230`
- **证据**：

```go
current = result.Digest          // 先把游标推上去
if !result.Changed {
    continue
}
if err := w.apply(ctx, result); err != nil {
    w.logger.Error(...)          // 只记日志，游标不回退
}
```

- **影响**：`apply` 回调（应用侧的重载逻辑）只要失败一次——比如新配置里的某个连接串暂时连不上
  ——这份配置就再也不会被应用，直到**下一次配置内容变化**为止。进程看起来正常，跑的是旧配置。
- **建议**：只在 `apply` 成功后推进 `current`；失败时保留旧 digest 并退避重试。
- **验证**：让 apply 回调第一次返回错误，观察后续轮询是否重试（当前不会）。

**修复（`cd6aa3c`）**：新增独立的 `applied` 摘要，只在 `apply` 成功后与 `current` 一起推进；
失败时保留游标、`sleep(ctx, cfg.RetryDelay)` 退避后重放，即使重放的 `result.Changed` 为 false
也照样调用 handler（文件已经被写过了，`Sync` 不会再报 Changed）。测试里的 fake server 也改成
真实 Nacos 的行为——客户端摘要与服务端不一致时立即回答"变更"，一致才挂起——这才让"游标提前
推进就再也收不到变更"成为可观测的失败。`TestWatcherRetriesAFailedApply` 断言 handler 至少被调用
两次，并重放时 `File` 正确、`Changed=false`。旧用例 `TestListenEncodesRequest` 里写死的
`abc123` 与新 fake 行为冲突，改用内容的真实摘要，测试意图（第二次"无远端变更"）不变。

### 3.6 【P1，一致性】热重载重建数据库时丢掉了 SQL 日志策略 —— **已修复**

- **位置**：`reload.go:207` 对照 `gofy.go:420`
- **证据**：

```go
// 启动时
db, err := gofydb.New(ctx, *a.opts.dbConfig, logger,
    gofydb.WithSQLPolicy(a.opts.sqlPolicy),
    gofydb.WithSQLLogger(a.sqlLogger))

// reload 时
newDB, err := gofydb.New(ctx, *spec.Database, lg)     // 两个选项都没带
```

- **影响**：只要重载时 `database` 段发生了变化，重建的连接池就**没有策略**：SQL 日志层整体失效
  （`support/db` 在 policy 为 nil 时退回按 `log_level` 输出，即脱敏、采样、限流全部消失）。
  同一个进程在重载前后行为不同，排查时极难归因。
- **建议**：把 `WithSQLPolicy` / `WithSQLLogger` 的构造抽成一个函数，启动与 reload 共用。
- **验证**：开启 sql_log 与 mask，改一次 database 段触发重载，观察 SQL 条目是否还带脱敏。

**修复（`cd6aa3c`）**：新增 `App.databaseOptions(sqlLogger)`，启动的 `buildDB` 与 Reload 都
经过它，两个 Option 不再有分叉。Logger 段也一起重建：`logger.NewSQL(*spec.Log)` 生成新的 SQL
通道，没配 `sql_outputs` 时回落到应用 logger；新通道同时装进 `a.sqlLogger` 与新代
`generation.sqlLogger`。退役旧代时 `retireGeneration` 除了应用/system/access 也 flush SQL
通道。失败的 Reload 走 `discardReloadResources`，只释放本次新建的资源，不碰线上池与线上 logger。
`TestReloadRebuildsTheSQLLogger` 覆盖这条路径。

### 3.7 【P1，生命周期】worker 模式下 `App.Shutdown` 不生效，会阻塞到超时 —— **已修复**

- **位置**：`start.go:918`（只 `close(a.stopCh)`）、`start.go:255`（唯一监听者，属于 HTTP 的 `run`）、
  `worker_host.go:244`（worker 只等 `runCtx.Done`）
- **证据**：`Shutdown` 的流程是「关 stopCh → 等 `a.done`」，但 worker 路径既不监听 `stopCh`，
  也没有任何地方把 `a.cancel` 接进来：

```
$ grep -rn "stopCh" --include="*.go" . | grep -v _test
start.go:255:  case <-a.stopCh:          ← HTTP run()
start.go:918:  close(a.stopCh)           ← Shutdown
```

- **影响**：宿主如果按文档调用 `app.Shutdown(ctx)` 来停一个 worker 进程，等到的只有
  `ctx.Err()`，worker 继续跑；用 `context.Background()` 调用则**永久阻塞**。信号驱动的停机不受
  影响（走 `signal.NotifyContext`），所以这个问题在只用信号的部署里看不出来。
- **建议**：worker 模式也监听 `stopCh`（或在 `Shutdown` 里对 worker 模式调用 `a.cancel`），
  两条路径共用同一个停机入口。
- **验证**：`app.Shutdown(context.Background())` 于 `worker --group=...` 运行期间调用。

**修复（`cd6aa3c`）**：`runWorker` 的 select 增加 `case <-stopCh:`，调用与信号处理相同的
`cancelRunners(nil)`，让两条停机入口共用同一段排空和 `a.done` 收尾。触发后把局部 `stopCh`
别名置 nil，避免已关闭的通道一直就绪、抢在 runner 错误之前反复进入该分支。
`TestShutdownStopsAWorkerProcess` 在真实 worker 分组上调用 `App.Shutdown` 并断言收敛。

### 3.8 【P1，可观测性 / 数据】`log_level: silent` 在开启 sql_log 后不再是静默 —— **已修复**

- **位置**：`support/sqllog/gorm.go:48`、`:74`；`support/db/db.go:66`（`silent` 是合法值）
- **证据**：`parseLevel` 不认识 `silent`，落到 default 分支返回错误，调用方退化成 `warn`：

```go
level, err := parseLevel(cfg.LogLevel)
if err != nil {
    level = zap.WarnLevel
}
```

- **影响**：配置写 `log_level: silent` 表达「这个库不要打 SQL」，开启 sql_log 后慢语句与失败
  语句仍会输出（携带语句文本，`mode: full` 时含参数）。这是**配置语义被静默改写**——运维
  以为自己关掉了。
- **建议**：`parseLevel` 接受 `silent` 并映射到 `gormlogger.Silent`；非法值保持退化成 warn 但
  应在 `Validate` 阶段就报错。
- **验证**：`log_level: silent` + `sql_log.enabled: true` + 构造一条慢查询。

**修复（`cd6aa3c`）**：`parseLevel` 接受 `silent`，返回 `(zap.ErrorLevel, true, nil)`；单独
的 silent 布尔映射到 `gormlogger.Silent`，并被 `LogMode` 一起复制，所以 `Info`/`Warn`/`Error`/
`Trace` 的级别判断之外还有 `enabled()` 这层硬关断，慢语句与失败语句都出不来。非法值仍退化成
warn，但错误信息现在列出 `silent` 作为合法取值。
`TestGormLoggerHonoursSilent` 构造慢查询断言零输出。

---

## 4. P2

### 4.1 `claim` 的 DDL 让 `created_at` 每次更新都变

- **位置**：`support/claim/claim.go:827`
- **证据**：`created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP`
- **影响**：每一次认领、续约、成功回写都会刷新「创建时间」，任何按创建时间排序或统计的运维
  查询都是错的。`updated_at` 有 `ON UPDATE` 是设计意图，`created_at` 显然不是。
- **建议**：去掉 `created_at` 的 `ON UPDATE`。表由应用自己建，改 DDL 常量的同时要在 README
  标出迁移语句。

### 4.2 AccessLog 的 `MaxPathLength` 为负数时 panic

- **位置**：`support/middleware/accesslog.go:166`、`:182`；`withDefaults`（`:112`）只处理 0
- **证据**：`if len(path) > opts.MaxPathLength { path = path[:opts.MaxPathLength] }` —— 负数时条件
  恒真，`path[:-1]` 直接 panic。
- **影响**：配置写错一个符号，每个请求都 panic（有 Recovery 时变成全站 500）。
- **建议**：构造时校验 `MaxPathLength > 0`，非法直接返回错误。

### 4.3 契约类型不接受 JSON `null`

- **位置**：`gofy-interface-api/contracts/common/types/date.go:46`、`int64_string.go:26`/`:62`
- **证据**：`strings.Trim(string(b), `"`)` 之后直接 `time.Parse` / `ParseUint`，`null` 会解析失败。
- **影响**：前端把可选字段显式序列化成 `"date": null` 是常见行为，当前会得到 400。属于跨语言
  契约的兼容性缺口。
- **建议**：`UnmarshalJSON` 首行判 `bytes.Equal(b, []byte("null"))` 时置零并返回 nil。

### 4.4 `support/tenant` 的测试不可重复，且失败信息会冒充隔离缺陷

- **位置**：`support/tenant/plugin_test.go:57`
- **证据**：内存库名取自 `t.Name()`，重复运行时同名库被复用：

```
$ go test -count=2 ./support/tenant/
--- FAIL: TestConcurrentTenantsDoNotLeak
    tenant "b" saw [3 rows...]
--- FAIL: TestCoexistsWithAContextReplacingPlugin
    tenant plugin read a stale context: ...
```

全仓 `go test -count=2 ./...` 只有这一个包失败。
- **影响**：CI 重试、`-count` 压测、并行执行都会红；而报错文本恰好是「租户插件读到了过期的
  context」——正是这条测试要防的故障模样，真假问题分不开。
- **建议**：库名加进程内自增序号（或用 `time.Now().UnixNano()`，与 `support/consumer` 的写法
  一致），并 `t.Cleanup` 关闭连接。

### 4.5 死代码与过时 API

| 位置 | 内容 |
| --- | --- |
| `start.go:490` | `buildModules` 无人调用（实际走 `buildModulesWith`） |
| `cmd/gofy/main.go:24` | `gofyModule` 常量未使用 |
| `support/ai/redact.go:72` | `redactValue` 未使用（`redactError` 才是生效路径） |
| `support/phpoffice/export.go:212` | `dir, path := plan.dir, plan.path` 中 `dir` 之后从未使用 |
| `support/consumer/consume.go:749` | `trace.NewNoopTracerProvider` 已废弃 |

- **影响**：无功能影响，但前三条会误导阅读者以为存在第二条路径。
- **建议**：删除或接上；废弃 API 换成 `noop.NewTracerProvider()`。

### 4.6 OTEL 的 `New(ctx, ...)` 接受 context 却从不使用

- **位置**：`support/otel/provider.go:122`
- **证据**：`if ctx == nil { ctx = context.Background() }`，之后整个函数再未读 `ctx`
  （staticcheck SA4006/SA4017）。
- **影响**：调用方以为可以用 ctx 控制初始化超时，实际不能；导出器建连超时只能靠自身配置。
- **建议**：要么把 ctx 传给 `newExporter`（OTLP 建连用它），要么去掉这个参数并在文档里说清。

---

## 5. 已经排除的候选（避免后续重复排查）

| 候选 | 结论 |
| --- | --- |
| `headerpropagation` / `client` 用 string 作 context key（staticcheck SA1029） | **不是缺陷**。两个仓库的 `ContextKey` 字面量故意相同，注释写明这是跨部署边界的 API 契约 |
| CORS `Allow-Origin: *` 与 credentials 组合 | **正确**。仅 `allowAll && !credentials` 时写 `*`，否则回显 Origin |
| HTTP Server 缺超时（Slowloris） | **已覆盖**。`ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout` 全部可配，diagnostics 也有 `ReadHeaderTimeout` |
| Recovery 把内部错误返回给客户端 | **已覆盖**。固定响应 `internal server error`，panic 值与堆栈只进日志 |
| 限流存储故障时放行流量 | **已覆盖**，默认 fail-closed 返回 503 |
| 租户插件并发串扰 | **已覆盖**，100 goroutine 的 race 用例存在且通过。并发共享状态不是问题；租户绕过出在**单条语句的条件判定**上，见 §3.1(a) |
| `claim` 的表名拼接 SQL 注入 | **不成立**，`identifierPattern` 校验 + `quoteTable` |

## 6. 未定性线索（需要运行时环境，未写入正文）

以下线索有代码依据，但定性依赖真实中间件或平台，本轮没有环境复现，列出来供后续排查：

1. `support/consumer/kafka.go` 与 `rocketmq.go`：ctx 取消时可能跳过 offset 提交 / ack，导致
   重启后重复消费（at-least-once 下可接受，但如果文档宣称「不重复」就是文档问题）。
2. `support/consumer/amqp.go`：nack-requeue 与 `ErrInFlight` 的组合在无延迟的 broker 上是否形成
   紧循环。
3. `support/ai` 的流式接口：`Stream.Err()` 与 `Close()` 的先后是否存在读侧永久阻塞。
4. `support/nacos` 的登录/订阅在持锁状态下发网络请求，P99 抖动时的排队放大。
5. Windows 上 `process.Alive` 的实现是否真的能区分「同 PID 的另一进程」。

## 7. 建议修复顺序

1. ~~**§3.3 延迟队列停机丢消息**（P0，发布即触发）与 **§3.2 普通队列**同一处逻辑，一起修。~~
   **已完成**（`a697efd`）。
2. ~~**§3.1 租户三条**（安全与数据完整性，且都是静默失败）——先修 (a)，它最容易被误用。~~
   三条**全部完成**（(a) `a697efd`，(b)(c) `cd6aa3c`）。
3. ~~**§3.4 消息 ID 长度**（一条脏消息能卡死分区）。~~ **已完成**（`a697efd`）。
4. ~~**§3.5 / §3.6 配置与重载**（排查成本最高的两类：静默用旧配置、重载后行为变化）。~~
   **已完成**（`cd6aa3c`）。
5. ~~**§3.7 / §3.8 生命周期与日志语义**。~~ **已完成**（`cd6aa3c`）。
6. **§4 的 P2**（当前仅剩的部分），其中 4.1 的 `created_at` 与 4.4 的测试可重复性建议尽早做
   ——前者影响运维判断，后者影响后续所有租户相关改动的可信度。
