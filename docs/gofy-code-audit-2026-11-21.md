# gofy 全量代码审计报告（2026-11-21）

> 审计对象：`github.com/bingcool/gofy`（本地 HEAD `3a6a851`）。
> 审计方式：从零通读根包与 `support/` 全部非测试源码，逐条回到源码核对触发条件与影响；
> 已修复的历史结论不重复计入，本轮发现均为独立取证。
> 审计范围：根包（生命周期 / 装配 / CLI / worker host）、`support/` 全部组件、
> `internal/`、`logger/`、`config/`、`command/`、`cmd/gofy` 脚手架。
>
> **取证限制（必须说明）**：Rev.1 的执行环境禁止运行 Go 工具链
> （`/usr/local/go/bin/go` 被沙箱拒绝，`go build` / `go vet` / `go test -race` 均无法执行），
> 因此**初版 13 条发现的取证方式是静态阅读与源码交叉引用**，不含当时的构建、vet、race
> 与端到端运行结果。Rev.2 的复核环境报告已跑过基线验证，但未附日志；本次执行环境仍无法复核，见 §0 与 §6。
> 每条发现都给出了可复核的 `文件:行号`；修复上线前请按 §6 的建议命令重跑一遍。

## 0. 修订记录（Rev.2–Rev.6，2026-11-21）

Rev.2 由主审在 `3a6a851` 上逐条回源码复核。复核环境报告该提交在入库前已通过
`go build ./...`、`go vet ./...`、`go test ./... -count=1` 与改动包的 `-race`；该结论未附日志，
本次执行环境仍禁止运行 Go 工具链，无法复核，按「待核实」记录（§6 同）。结论：**13 条全部成立，无撤销**；
但有一条分级偏低、7 处描述或建议需要修正，已写入对应条目；P2-03 的建议代码已替换：

| 位置 | 修订 | 原因 |
|---|---|---|
| P3-03 配置展开 | **级别 P3 → P2**；Rev.4 编号改为 `P2-04` 并移入 §2 | `config.Load` 默认 `expandEnv: true`（`config/load.go:137`），多行环境变量（PEM、JSON、含 `#` 的口令）会静默破坏 YAML 结构；默认路径 + 静默失败 + 可能连错库，符合本报告 P2 的定义 |
| P3-05 日志权限 | 补充落地前提 | `os.OpenFile` 的 perm 只在**创建**时生效，已有文件改配置不会改权限；lumberjack v2.2.1 默认 `0600`（不是 0644），原文对它的描述需要修正。另外原文「SQL 默认脱敏」不准确：默认 `mode=placeholder`，日志里本就没有字面量，`mask.enabled` 默认关闭 |
| P3-01 pid 刷新 | 补充触发前提（Rev.3 再修正理由） | 「旧 goroutine 覆盖新实例 pid」要求旧文件已删除且新实例已完成写入；`duplicatePID`（`start.go:727-739`）在此场景拦不住——文件已被 cleanup 删除，且 `Record.Verify` 对已退 pid 返回 `IdentityUnknown`。窗口更窄是因为时序要求苛刻，不是判重挡住了 |
| O-01 Header 读取 | 修正建议 | `ContextWithHeaders` 写入时已按白名单过滤（`headerpropagation.go:161-167`），ctx 内的 map 只含允许的 header，直接读不会绕过白名单；`HeadersFromContext` 的复制是给调用方的**可变副本**，两者职责不同，建议改为新增只读单值访问器 |
| O-02 台账建表 | 精确对象（Rev.3 去掉错误否定） | 对象是 `Processor`：各传输层在每个实例的 `Run` 里调用包级 `start`（`consume.go:64`），它再 `NewProcessor`（`:77`），deduper 与 `ensureSchema` 都在 `NewProcessor` 内（`:443-461`）。每个实例各建一个 Processor，所以并发度 N 仍对应 N 次建表检查 |
| O-04 logConfig 被忽略 | 修正措辞 | `WithLogConfig` 本身会 `cfg.Validate()`，非法配置在 `New` 阶段就会失败；`WithLogger` 优先也是文档写明的语义（`option.go:228-230`）。真正缺的是**运行时提示**，不是校验或文档 |
| O-05 停机错误 | 补充边界 | `Shutdown` 的 `ctx.Done()` 分支可能早于 `cleanup` 完成，此时 `cleanupErr` 尚未写入；`errors.Join(ctx.Err(), a.cleanupErr)` 只能在 `<-a.done` 之后读，或配合新的访问器 |

另把 P2-03 的建议代码改为不丢失 `http.DefaultTransport` 默认值的写法——原文的
`&http.Transport{}` 会连 `DialContext` 的 30s 超时与 `ForceAttemptHTTP2` 一起丢掉。

### 0.1 Rev.3 修正（2026-11-21 18:10）

Rev.3 只改两处**修订理由本身**，发现、分级与修复状态不变（13 条仍全部「未修复」）：

| 位置 | 修正 | 依据 |
|---|---|---|
| P3-01 | 删掉「正常启动会被 `duplicatePID` 拦住」 | `duplicatePID`（`start.go:727-739`）只在文件存在且 `record.Verify()==IdentityMatch` 时判重；该竞态里文件已被 cleanup 删除，`Record.Verify`（`internal/pidfile/pidfile.go:78-90`）对已退 pid 返回 `IdentityUnknown`。窗口更窄是时序造成，不是判重挡住；影响随之改为「`stop` 报未运行而新实例仍在跑」，不再说误杀进程 |
| O-02 | 删掉「不是每个消费者实例而是每个 `Processor`」 | 每个实例的 `Run` 都调用 `start → NewProcessor`（`consume.go:64,77`），实例数 N 等于 Processor 数 N，`ensureSchema` 仍执行 N 次；保留「对象是 Processor」的命名精确化 |

### 0.2 Rev.4 修订（2026-11-21 18:12）

| 位置 | 修订 | 依据 |
|---|---|---|
| 编号 | 原 P3-03 改为 `P2-04` 并移入 §2 | 级别在 Rev.2 已升为 P2，编号应随级别；§0、§1、§1.1 与 §6 的引用同步更新 |
| 行号 | P2-02 的 `start.go:961-963/1052-1057/1077-1088` 改为 `969-971/1053-1057/1085-1091`；P3-01 的 `1076-1084` 改为 `1077-1084`；`duplicatePID` 统一为 `727-739` | 按当前工作树逐行核对 |
| 证据 | §0 的 build/vet/test 断言改为「复核环境报告、未附日志、本次无法复核」 | 本环境仍禁止执行 Go 工具链（§6） |

### 0.3 Rev.5 复核（2026-11-21 18:20）

逐条回源核对 Rev.3 的两处理由修正：**方向都成立**，另据 `stop` 的实际判定顺序把 P3-01
的影响再收精确一层。

| 位置 | 结论 | 依据 |
|---|---|---|
| P3-01 守卫 | 成立 | `duplicatePID`（`start.go:727-739`）先 `ReadRecord`，读不到即返回 false；再要求 `Verify()==IdentityMatch`，而已退 pid 的 `process.Token` 为空，`Verify` 返回 `IdentityUnknown`。文件被 cleanup 删掉后判重根本不执行，确实不是它挡住 |
| P3-01 影响 | 成立，但停机路径需再精确 | `stopPIDFile` 的顺序是 `ReadRecord` → `process.Alive`（`stop.go:89-92`）→ `Verify`（`:100-122`）。陈旧记录指向已退进程时先命中 `Alive=false`，报 `ErrNotRunning` 并删文件；`IdentityUnknown`（不带 `--force` 即拒绝）要求 pid 仍存活但无法验证，是另一条路径。另补：同配置下新实例的刷新 tick 会覆盖陈旧记录，最长存活一个 `pidRefresh` 周期 |
| O-02 | 成立 | `Runner.Run` 按实例各起一个 goroutine（`runner.go:236-242`），每个实例的 `Run` 调用 `start → NewProcessor`（`consume.go:64,77`），因此实例数 N = Processor 数 N = `ensureSchema` 次数 N；原否定句确实会让读者以为重复次数变少 |

### 0.4 Rev.6 修复（2026-11-21 18:38）

八条 P2/P3 中七条已修，一条证伪；每条新用例都先在旧实现（或去掉该修复的版本）上确认失败。
优化项 O-01…O-05 未动。

| 编号 | 改动 | 验证 |
|---|---|---|
| P2-01 | `Client.ChannelContext(ctx)`，`Channel()` 退化为 `context.Background()` 包装；拨号移出 `c.mu`，用 `dialing` 通道合并并发重连；每 host 一次 `WithTimeout`，并用 `context.AfterFunc` 让取消能打断握手；`consumeOnce` 与 `Publish` 传入各自 ctx | `client_context_test.go`：取消后 100ms 内返回；Close 不再等 5s 拨号 |
| P2-02 | `Runner.Abandoned()` 记录 drain 超时；宿主把被放弃的分组交给 `App.abandonWorkers`；`cleanup` 命中时跳过模块 Stop、DB 关闭与遥测 Shutdown，只 flush 日志 | `runner_abandon_test.go`（2 例）、`start_internal_test.go`（2 例） |
| P2-03 | `defaultTransport()` 用 comma-ok 断言，失败时回落到带 30s Dialer、`ForceAttemptHTTP2` 的基线 Transport | `client_transport_test.go`：替换全局 Transport 后不再 panic，且 `DialContext` 非空 |
| P2-04 | 展开改在 `yaml.Node` 的字符串标量上；普通标量替换后清 tag 让类型重新推断，引号标量保持字符串；无占位符的文件原样返回 | `config/load_env_test.go`：多行 PEM、`" #"`、`: `、`- `、`{}` 均保持单值；`port: ${PORT}` 仍解码为 int |
| P3-01 | `pidRefreshExited` 在 goroutine 返回时关闭，`cleanup` 先 join 再 `RemoveIfOwner`；新增 `pidfile.WriteRecordIfOwner`，文件属于他人时刷新停止 | `start_internal_test.go`：去掉 join 即失败；`pidfile_test.go`：外来 pid 拒绝写入 |
| P3-02 | **证伪**：启动与 Reload 都把注入模块排在工厂模块之后，逆序停止时两者一致，从未出现"Reload 后先停" | `reload_order_test.go` 固定最终停止顺序为 `injected-mod → factory-mod` |
| P3-04 | `bytes` 在 `Size() < 0` 时归零 | `middleware_test.go`：204 请求记录 0 而不是 -1 |
| P3-05 | `log.file_perm`（默认 `0600`）；直写路径与轮转 sink 都对已存在文件 `Chmod`；lumberjack 前先按目标权限建好文件 | `logger_test.go`（4 例）、`rotation_internal_test.go`（1 例）：新建、收紧旧文件、自定义 0640 均生效 |



---

## 1. 结论摘要

| 级别 | 数量 | 说明 |
|---|---|---|
| P1（严重） | 0 | 未发现可直接导致越权、数据损坏或必现崩溃的缺陷 |
| P2（重要） | 4 | 依赖外部条件或停机路径才会触发，但触发后影响明确；含原 P3-03（现 P2-04）上调的一条 |
| P3（一般） | 4 | 边界场景的正确性、健壮性或可运维性问题 |
| 优化项 | 5 | 热路径分配、启动期重复工作、配置可诊断性 |

整体判断：核心生命周期（启动 / 停机 / 重载 / 代际排空）、租户隔离、JWT 校验、
文件名与路径约束等高风险面写得比一般框架更严谨，多处设计（`admissionGate` 的读写锁准入、
`generationLease` 的 panic 安全释放、`tenant` 插件按绑定值而非形状判断去重）经核对成立。
本轮问题集中在**停机与重连路径的上下文传播、超时后的资源释放顺序、配置展开的文本级
替换，以及少数热路径分配**，不属于架构性缺陷。原 P3-03（现 P2-04）因「默认开启 + 静默破坏 + 可能
连错目标」在 Rev.2 上调为 P2，Rev.4 起按新编号归入 §2。

### 1.1 问题清单与修复状态

初版为只读审计。Rev.6 已修复 8 条中的 7 条，并证伪 1 条（P3-02）；5 条优化项
（O-01…O-05）仍是建议，本轮未实施。修复范围与验证见 §0.4。

| 编号 | 级别 | 问题 | 定位 | 修复状态 |
|---|---|---|---|---|
| P2-01 | P2 | AMQP 重连忽略调用方 ctx，且在持锁状态下拨号 | `support/amqp/client.go:84-104,146-176` | ✅ 已修复 |
| P2-02 | P2 | drain 超时后仍关闭 DB/logger，实例 goroutine 可能仍在运行 | `support/worker/runner.go:262-284`；`worker_host.go:320`；`start.go:1053` | ✅ 已修复 |
| P2-03 | P2 | Nacos 对 `http.DefaultTransport` 做无保护类型断言 | `support/nacos/client.go:69` | ✅ 已修复 |
| P3-01 | P3 | pid 刷新 goroutine 未 join 就删除 pid 文件 | `start.go:757-783,1077-1084` | ✅ 已修复 |
| P3-02 | P3 | Reload 后注入模块的停止顺序发生变化 | `reload.go:276-278,312`；`start.go:1041` | ❌ 不成立（误报） |
| P2-04（原 P3-03） | P2 | 配置在 YAML 解析前展开 `${VAR}`，多行值可破坏结构 | `config/load.go:159-164,168-203` | ✅ 已修复 |
| P3-04 | P3 | 未写响应体时访问日志记录 `bytes=-1` | `support/middleware/accesslog.go:200` | ✅ 已修复 |
| P3-05 | P3 | 日志文件以 0644 创建 | `logger/logger.go:348`；`logger/rotation.go:200` | ✅ 已修复 |
| O-01 | 优化 | `HeaderFromContext` 为取一个 header 复制整张表 | `support/headerpropagation/headerpropagation.go:230-251` | 未修复 |
| O-02 | 优化 | 每个 `Processor` 重复执行台账建表检查 | `support/consumer/consume.go:984-993` | 未修复 |
| O-03 | 优化 | AMQP 重连循环每次迭代分配 timer | `support/amqp/queue.go:340` | 未修复 |
| O-04 | 优化 | 注入 logger 时 `logConfig` 被静默忽略 | `gofy.go:360-363` | 未修复 |
| O-05 | 优化 | 停机失败信息只保留在 `cleanupErr` | `start.go:903-937,960-1097` | 未修复 |

---

## 2. P2 重要问题

### P2-01 AMQP 重连忽略调用方 context，并在持锁状态下拨号

**定位**
- `support/amqp/client.go:92`：`c.conn == nil || c.conn.IsClosed()` 时调用
  `c.connectLocked(context.Background())`。
- `support/amqp/client.go:146-176`：`connectLocked` 只检查函数入参 ctx，逐个 host
  `amqp091.DialConfig(...)`（每次拨号上限为 `cfg.ConnectionTimeout`）。
- `support/amqp/client.go:84-104`：`Channel()` 全程持有 `c.mu`，`Close()` 也要拿同一把锁。

**触发条件**
broker 断开后，消费者在 `consumeOnce` 里重新 `Channel()`，而所有 host 均不可达；
或进程正在停机、`ctx` 已被取消，但工作 goroutine 仍在重连。

**影响**
1. 停机时 `Channel()` 无视已取消的 ctx，按 `hosts × ConnectionTimeout` 继续重试，
   拖长 worker 的 drain 与进程退出；
2. `Channel()` 持锁拨号期间 `Close()` 被阻塞，关闭顺序退化为“等网络超时”；
3. 消费适配器的 drain 预算（`support/worker/runner.go`）因此可能被耗尽，
   进而触发 P2-02 的资源提前关闭。

**建议**
- 新增 `ChannelContext(ctx)`，由 `queue.consumeOnce` / `Publish` 传入消费或请求 ctx；
  `Channel()` 保留为 `ChannelContext(context.Background())` 的兼容形式。
- 拨号不要持锁：用 `singleflight` 或在锁内做一次“已有可用连接”的快速检查，
  真正拨号放到锁外，完成后用 CAS 发布连接。
- 至少为 `connectLocked` 的每一次拨号套 `context.WithTimeout`，让停机路径有确定上限。

**Rev.2 复核补充**
- 不是「缺少 ctx 检查」而是「检查了错误的 ctx」：`connectLocked` 循环开头有
  `if err := ctx.Err(); err != nil { return err }`，但 `Channel()` 传的是
  `context.Background()`，该检查永远不会命中。修复时保留检查、换掉入参即可。
- 最坏阻塞时间可以精确写成 `len(cfg.Hosts) × cfg.ConnectionTimeout`；默认
  `ConnectionTimeout` 是 5s（`support/amqp/config.go:58`），结合 runner 的
  `DrainTimeout` 即可算出是否会把停机拖过预算。
- `Publish` 路径同样走 `Channel()`，因此请求 ctx 的取消也不会中断重连拨号。

---

### P2-02 worker drain 超时后仍会关闭数据库与 logger，实例 goroutine 可能仍在运行

**定位**
- `support/worker/runner.go:262-284`：`stopRuns()` 后 `wait(done, DrainTimeout+1s)`；
  若 `ctx.Err() != nil` 且 `!drained`，只告警并 `return nil`，**不等待** `wg.Wait()` 完成。
- `support/worker/runner.go:236-248`：实例 goroutine 与 `wg.Wait()` 分离，`Run` 返回不代表 goroutine 退出。
- `worker_host.go:270-320`：`errCh` 收齐各 runner 的返回值后调用 `a.cleanup()`。
- `start.go:1053-1057`：`cleanup` 中 `gofydb.Close(a.db)`；`start.go:1085-1091` 同步并
  释放 logger（`ownLogger` 分支）。

**触发条件**
某个 worker handler 忽略 `ctx`（例如把 ctx 换成 `context.Background()`、阻塞在网络/DB 调用上），
且耗时超过 `WithWorkerDrainTimeout`；进程此时收到 SIGTERM。

**影响**
`Runner.Run` 返回后，宿主关闭连接池与日志，仍在运行的 handler 继续使用已关闭的
`*sql.DB` / zap core，表现为 `sql: database is closed`、日志丢失，甚至第三方 SDK 在
关闭后的连接上 panic。若 handler 正在写库，还可能留下部分写入。

**建议**
- `Run` 在返回前对“未排空的实例”做二次等待或明确标记，并把该状态暴露给宿主；
- 宿主在所有 runner 返回后、`cleanup` 关闭 DB 之前，再等一个短硬上限（如 5s），
  仍未退出则**跳过 DB/logger 关闭**（进程即将退出，交由 OS 回收），而不是在活连接上关闭；
- 或在 `worker_host` 记录“abandoned instances”，并在 `cleanup` 中据此调整释放顺序。

**Rev.2 复核补充**
- `wait`（`runner.go:345-354`）超时只是返回 false，确实不 join；`stopDrain()` 之后
  `ctx.Err() != nil` 分支直接 `return nil`，这条路径没有任何“仍有实例在跑”的信息传给宿主。
- 走到这里的前提比原文更明确：`cleanup()` 开头就已经 `a.cancel(nil)` 取消了 `runCtx`
  （`start.go:969-971`），handler 若仍不退出，说明它完全忽略了传入的 ctx——属于违反
  消费契约的代码。但框架的职责是不让这类代码访问已关闭的 `*sql.DB`，建议仍然成立。
- 「跳过 DB/logger 关闭」要配一条明确的 Error 日志（含 group、abandoned 数量），否则
  运维只会看到进程退出、日志没有落盘，却不知道原因。

---

### P2-03 Nacos 客户端对 `http.DefaultTransport` 做无保护类型断言

**定位**
- `support/nacos/client.go:69`：
  `transport := http.DefaultTransport.(*http.Transport).Clone()`

**触发条件**
宿主或依赖把 `http.DefaultTransport` 替换成非 `*http.Transport` 的实现。常见场景：
测试注入自定义 RoundTripper，或少数在 `init` 里全局替换的 APM SDK / 代理库。
（`otelhttp.NewTransport` 通常显式传给自己的 `http.Client`，不会改写全局变量。）

**影响**
`newHTTPClient()` 在任何错误处理之前 panic，且发生在 `nacos.NewClient` / `NewRegistrar` /
`NewWatcher` 的构造路径上：表现为启动直接崩溃并打印栈，而不是“Nacos 不可用”的可诊断错误。
同一仓库的 `support/ai/provider.go:25` 已经用了 comma-ok 写法，两处不一致。

**建议**
```go
// 断言失败时不要退化成零值 Transport：DialContext 为 nil 意味着拨号没有超时，
// 比 panic 更难排查。保留一个有超时的 Dialer 作为基线。
transport := &http.Transport{
    DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
    ForceAttemptHTTP2: true,
}
if base, ok := http.DefaultTransport.(*http.Transport); ok {
    transport = base.Clone()
}
transport.Proxy = nil
```
并补一条单测，先把 `http.DefaultTransport` 换成自定义 RoundTripper 再构造客户端。

**Rev.2 复核补充**
- 原文建议的 `transport := &http.Transport{}` 会在断言失败时丢掉默认 Transport 的
  `DialContext` 超时（30s）、`ForceAttemptHTTP2`、`IdleConnTimeout` 等设置；零值
  `Transport` 的拨号没有超时，故障表现会从「启动 panic」变成「请求永久挂起」。
  上面已改为带 Dialer 的基线。
- 影响面的措辞也可以更准：`otelhttp` 通常显式传给自己的 `http.Client`，不会改
  `http.DefaultTransport`；真正会命中的是「测试注入自定义 RoundTripper」与少数在
  `init` 里全局替换的 APM SDK。

### P2-04 配置加载在 YAML 解析之前做 `${VAR}` 展开，环境变量值可破坏结构（原 P3-03）

> **Rev.2 级别调整：P3 → P2；Rev.4 编号改为 P2-04 并移入 §2。** 理由：`config.Load` 默认
> `expandEnv: true`（`config/load.go:137`），不是需要显式开启的旁路；破坏是静默的，
> 且可能让进程连到错误的目标。按本报告 §1 的分级定义属于 P2。

**定位**
- `config/load.go:159-164`：`expandEnvVars(data, ...)` 在 `yaml.Unmarshal` / `Decode` 之前执行。
- `config/load.go:168-203`：纯文本替换；`$$` 用哨兵串 `\x00gofy-dollar\x00` 保护。
- `config/load.go:137`：默认 `options{expandEnv: true, ...}`，即默认路径就开启了展开。

**触发条件**
被引用的环境变量值本身含换行、`:`、`#` 或缩进（如多行 PEM、含 `#` 的口令、
带换行的连接串），或值里恰好出现哨兵字符串。

**影响**
1. 多行值会截断当前标量并“长出”新的 YAML 键，导致配置被静默改写或解析失败；
2. 值中出现 ` #`（空格后跟井号）时，其后内容被当作 YAML 注释丢弃，口令被截短后仍可能
   “加载成功”，故障延后到连接阶段；
3. 值中若含 `\x00gofy-dollar\x00`，会在还原阶段被替换成 `$`。
4. `allowMissing=true` 时缺失变量被替换成空串（`expandEnvVars` 的 `allowMissing` 分支），
   同样走文本级替换：`password: ${DB_PASSWORD}` 会变成 `password:`（null），与「变量缺失」
   这一事实脱节，下游拿到的零值可能被当成合法配置。

**建议**
优先解析后再对**标量节点**做展开（遍历 `yaml.Node`，只替换 `!!str` 的值）；
若保留文本级展开，至少对替换值做 YAML 引号/转义，并在文档中写明“环境变量值必须是单行纯文本”。
哨兵串建议改为随机前缀，避免与真实数据碰撞。

**Rev.2 复核补充**
- 文本级替换的边界不止「多行」：单个 `#`（前面有空格）、`:` 加空格、以 `-`/`?` 开头的
  值都会改变 YAML 结构；`yaml.Node` 方案能一次覆盖全部。
- 「缺失变量替换为空串」在严格模式下是报错（`allowMissing=false` 返回
  `missing environment variables`），只有显式允许缺失时才静默变空——修复时两条路径
  要一起处理，否则严格模式修好了、宽松模式仍然静默。

---

## 3. P3 一般问题

> P3-03 已在 Rev.4 改编号为 `P2-04` 并移入 §2；本节编号保留空缺、不重排，避免已有引用失效。

### P3-01 pid 刷新 goroutine 未 join 就删除 pid 文件

**定位**
- `start.go:757-783`：`startPIDRefresh` 起 goroutine，`select` 监听 `done` 与 `ticker.C`，
  在 `ticker.C` 分支调用 `pidfile.WriteRecord`。
- `start.go:1077-1084`：`cleanup` 先 `a.pidTicker.Stop()`、`close(a.pidTickerDone)`，
  随后 `pidfile.RemoveIfOwner(a.pidPath, os.Getpid())`，**没有等待 goroutine 退出**。

**触发条件**
`WithPIDRefreshInterval` 启用了刷新，且 ticker 恰好在 `Stop()` 前已选中 `ticker.C`，
或 goroutine 正处在 `WriteRecord` 的“临时文件 → rename”之间。

**影响**
`RemoveIfOwner` 删除文件后，goroutine 的 `rename` 仍可能把它重新创建：
1. 进程已退出却留下 pid 文件（下次启动看到陈旧记录）；
2. 更糟的是若新实例已写入自己的记录，旧 goroutine 的 `rename` 会**覆盖新实例的 pid 文件**。
   文件里是已退出进程的 pid，`stop` 先看 `process.Alive`（`stop.go:89-92`）就判否，
   返回 `ErrNotRunning` 并删掉文件——新实例仍在运行，运维却被告知「未运行」。
   `IdentityUnknown` 分支（`stop.go:100-122`，不带 `--force` 即拒绝）要求 pid 仍存活但
   无法验证身份，与「已退出进程的记录」不是同一条路径；两条路径都不会向无关进程发信号。

**Rev.2 复核补充**
- 第 1 点是主路径：只要 `WriteRecord` 的 rename 晚于 `RemoveIfOwner`，文件就会复活，
  窗口是「一次 `WriteRecord` 的写临时文件 + rename」。
- 第 2 点需要三个条件同时成立：旧 cleanup 已删除文件、新实例已启动并写入记录、旧 goroutine
  的 rename 还没执行。`duplicatePID`（`start.go:727-739`）在这个序列里拦不住：新实例启动时
  文件已被删除，判重读不到记录；对已退出的旧 pid，`Record.Verify` 也返回 `IdentityUnknown`。
  窗口极窄的原因是时序苛刻（rename 必须晚于新实例写入），不是判重挡住。影响是 `stop`
  报「未运行」而新实例仍在跑，而不是误杀无关进程；仍不宜与第 1 点并列。
- 文件还会自愈：本问题的前提是旧实例开着刷新（`start.go:187`、`worker_host.go:173`），
  同一配置下新实例也开着，它下一次 tick 就会把自己的记录写回去。陈旧内容最多存在一个
  `pidRefresh` 周期，运维是否需要 `--force` 的取舍也只在这个窗口内。
- 修复时除了 join，还要注意 `WriteRecord` 本身是「写临时文件 + rename」，只加锁不校验
  归属仍可能覆盖；建议在 rename 前用 `RemoveIfOwner` 的逆检查确认文件仍属于本进程。

**建议**
给刷新 goroutine 一个 `sync.WaitGroup`（或单独的 `exited chan struct{}`），
`cleanup` 中 `close(done)` 后 `<-exited` 再删除文件；`WriteRecord` 前再校验一次
“文件仍属于本进程”（`RemoveIfOwner` 的逆检查），或在删除时先停 ticker 再等一个 interval。

---

### P3-02 Reload 后注入模块的停止顺序发生变化

**定位**
- `start.go:582-594`：启动时 `a.started` 按“工厂模块 → 注入模块”的注册顺序排列。
- `start.go:1041-1051`：`cleanup` 逆序停止。~~因此启动时注入模块最后停止。~~
  （Rev.6 更正：逆序停止意味着列表**末位先停**，注入模块在启动时就已经是先停的那个。）
- `reload.go:276-278`：`allStarted = append(started, injectedStarted...)`，
  注入模块被移到列表末尾；`reload.go:312` 写回 `a.started`。

**触发条件**
至少执行过一次 `Reload`，且存在经 `WithModules` 注入、与工厂模块有停止顺序依赖的模块
（例如注入的“指标上报模块”依赖工厂构建的“DB 模块”仍可用）。

**影响**
Reload 之后注入模块变成**最先**停止，可能先于它依赖的模块停止：
停机期间出现上报失败、连接已关等噪声错误，且同一份代码在“从未 Reload”与“Reload 过”
两种运行轨迹下行为不同，难以归因。

**建议**
不要用“两段拼接”表达顺序：为每个模块记录稳定的序号（启动与 Reload 共用），
或把注入模块固定放在列表头部（逆序时最后停止）；补一条“Reload 后停止顺序与启动一致”的断言。

**Rev.6 复核：不成立（误报）**
- `cleanup` 从 `len(a.started)-1` 往前停止，列表末位最先停。启动时
  `a.started = [工厂模块..., 注入模块...]`（`buildModulesWith` 先工厂后注入），
  注入模块本来就是先停的那个。
- `reload.go:276-278` 拼出的 `allStarted` 同样是 `[本次启动的工厂模块..., 注入模块...]`，
  逆序后仍是注入先停：两条轨迹的顺序一致，不存在“Reload 后变了”。
- `reload_order_test.go` 在**未改任何源码**的情况下通过，断言最终停止顺序为
  `injected-mod → factory-mod`，可作为该结论的回归守卫。
- 结论：本条不需要改代码；原“启动时注入最后停止”的推理是把逆序停止读成了顺序停止。

---

### P3-04 访问日志在未写响应体时记录 `bytes=-1`

**定位**
- `support/middleware/accesslog.go:200`：`zap.Int("bytes", c.Writer.Size())`

**触发条件**
handler 未写任何 body：`204`、`304`、`HEAD` 请求、以及只写头就返回的分支。
gin（v1.12.0，`response_writer.go:16,63,102`）的 `ResponseWriter.Size()` 在首次写入前返回
`noWritten = -1`。

**影响**
日志与基于日志的字节数统计出现负值；按 `bytes` 聚合的看板/告警需要额外过滤，
否则均值与分位数被污染。

**建议**
```go
size := c.Writer.Size()
if size < 0 {
    size = 0
}
```
或对 `204/304` 直接省略该字段。

**Rev.2 复核补充**
- 机制比原文更具体：`c.Status(204)` 只调用 `WriteHeader`，而 `WriteHeaderNow`
  （`gin/response_writer.go:77-82`）是在中间件链结束之后由引擎调用的，它才会把 `size`
  置 0。访问日志在 `c.Next()` 返回后立刻读，因此读到的必然是 `-1`。
- 修复后 `0` 的含义是「没有 body」，与 `-1`（未知）区分开；若采用「省略字段」方案，
  基于 `bytes` 的聚合需要把缺失当 0 处理，两种方案的看板口径不同，选一种写进文档。
- `HEAD` 请求尤其要注意：handler 可能正常返回 200 并写入 body，但 net/http 会丢弃 body，
  `Size()` 记录的仍是写入长度而非实际发送字节——这是 gin 的既有语义，不属于本缺陷。

---

### P3-05 日志文件以 0644 创建，访问日志与 SQL 日志可能含敏感值

**定位**
- `logger/logger.go:348`：`os.OpenFile(target, ..., 0o644)`
- `logger/rotation.go:200`：日期模式的轮转 sink 同样 `0o644`。

**触发条件**
多用户主机或容器内共享卷；开启了 `access_outputs` / `sql_outputs`，
且访问日志记录了 query（`LogQuery`）或 SQL 日志处于 `full` 档、`mask.enabled` 关闭。

**影响**
同机其他用户可读日志文件。虽然 `LogQuery` 默认关闭、SQL 日志默认 `mode=placeholder`
（日志里没有字面量），但“默认安全”依赖多个开关同时正确，权限是更底层的兜底。

**建议**
日志文件默认 `0640`（同组可读）或 `0600`；在 `logger.Config` 增加 `file_perm`（只对新建
生效），并对已存在的文件显式 `Chmod`。lumberjack 路径默认 `0600`，不需要额外处理；
但同一路径若可能先被直接写路径以 `0644` 创建，仍需在打开后统一 `Chmod`。

**Rev.2 复核补充**
- **lumberjack 路径不是 0644**：v2.2.1 的 `openNew` 默认 `mode = 0600`
  （`lumberjack.go:215`），只有已有文件时才沿用其权限（`:218-219`）。因此原文「交给
  lumberjack 时未显式设权限」不等于不安全，需要修正：真正以 `0644` 创建文件的只有
  `logger.go:348` 与 `rotation.go:200` 两条直接写路径。
- 但两者会互相影响：同一路径若先被直接写路径以 `0644` 创建，之后切到 lumberjack 会
  沿用 `0644`。修复应做权限归一（打开后 `Chmod`），而不是只改新建分支。
- `os.OpenFile` 的 `perm` 只在**创建**时生效，已有文件不会因为改了配置而收紧——这是
  落地时最容易漏的一点，需要显式 `Chmod` 存量文件。
- 原文「SQL 默认脱敏」应改为「默认 `mode=placeholder`，日志不含字面量」：`MaskConfig`
  默认 `enabled=false`，脱敏只对 `full` 档生效。

---

## 4. 优化建议

### O-01 `HeaderFromContext` 为取一个 header 复制整张表

**定位**
- `support/headerpropagation/headerpropagation.go:230-251`：`HeadersFromContext` 每次
  `make(http.Header, len(stored))` 并逐个复制 value 切片。
- `support/headerpropagation/headerpropagation.go:249-251`：`HeaderFromContext` 调它后只取一个值。
- `support/frameworkcontext/frameworkcontext.go:226-325`：`UserID` / `TenantID` /
  `UserCode` / `UserName` / `TraceID` / `ClientIP` / `UserAgent` 全部经由该路径。

**影响**
一次请求里身份可能被解析多次（租户插件、业务 handler、日志、下游客户端），
每次都产生 1 张 map + N 个 value 切片分配；在高 QPS 下是可观测的 GC 压力。

**建议**
新增一个只读的单值访问器，而不是让 `HeaderFromContext` 复用 `HeadersFromContext`：
直接从 `ctx.Value(ContextKey)` 取 map，按 `http.CanonicalHeaderKey(name)` 查一次即可。

**Rev.2 复核补充**
- 白名单不是问题：`ContextWithHeaders`（`:156-169`）写入时已经 `if !allowed[canonical] { continue }`，
  ctx 内的 map 只含允许的 header，直接查不会绕过过滤。原文的担心可以去掉。
- `HeadersFromContext` 的复制仍有必要：它返回给调用方的是一份**可变副本**，调用方
  `Add`/`Set` 不能污染 ctx。两者职责不同，优化只应针对单值读取路径。
- 若要进一步省分配，可在 `ContextWithHeaders` 里改为存一份不可变的
  `map[string]string`（只保留首值），但那是语义变化（多值 header 会丢），不建议为了
  分配而改。单值访问器已经能消除热路径上的 map + 切片分配。

### O-02 每个 `Processor` 都执行一次台账建表检查

**定位**
- `support/consumer/consume.go:984-993`：`ensureSchema` 先 `HasTable`，不存在再 `AutoMigrate`。
- `support/consumer/consume.go:64-89`：包级 `start` 辅助函数在每个实例的 `Run` 里被调用，
  它再调 `NewProcessor`（`:77`）。
- `support/consumer/consume.go:443-461`：deduper 在 `NewProcessor` 内创建，`ensureSchema`
  也在同一函数里调用（`:457`）。

**影响**
并发度为 N 的分组在启动时产生 N 次 `HasTable`（首次还会 N 路 `AutoMigrate` 竞争），
拉长启动时间并给数据库增加无谓 DDL 争用；`isTableExists` 只按错误文本兜底，跨库方言不一定命中。

**建议**
以 `consumer` 名（或固定表名）为键做进程级 `sync.Once`/`singleflight`，
只让第一个实例建表，其余等待结果；或把建表移到 `NewProcessor` 之前的装配阶段统一执行一次。

**Rev.2 复核补充**
- 对象应写成 `Processor`：各传输层在每个实例的 `Run` 里调用包级 `start` 辅助函数
  （`consume.go:64`），它再调 `NewProcessor`（`:77`）；deduper 与 `ensureSchema` 都在
  `NewProcessor` 内完成（`:443-461`）。注意每个实例各建一个 Processor，因此「按实例计数」
  与「按 Processor 计数」是同一个 N，这条问题的重复次数没有变。
- 进程级 `sync.Once` 要以 `consumer + 表名` 为键：同一进程可能有多个不同 consumer
  共用一张台账表，键太粗会让第二个 consumer 跳过建表。
- 建表本身有 `schemaTimeout` 预算（`ProcessorConfig`），singleflight 等待方应共享
  同一份结果与错误，不要各自重试。

### O-03 AMQP 重连循环每次迭代分配一个 timer

**定位**
- `support/amqp/queue.go:340`：`case <-time.After(opts.ReconnectDelay):`

**影响**
每次重连分配一个 `Timer`；虽然会被回收，但在长时间 broker 故障下是无谓的分配与 GC 噪声。

**建议**
循环外建一个 `time.NewTimer`，每次 `Reset` 后复用（注意 `Stop` + drain 的标准写法）；
或改用 `time.Ticker` 配合显式恢复。

### O-04 注入 logger 时 `logConfig` 不生效且无运行时提示

**定位**
- `gofy.go:358-363`：`if a.opts.logger != nil { return loggers{app: ..., system: ...,
  access: ..., sql: ...}, nil }` 直接返回，`own` 保持 false。
- `option.go:228-241`：`WithLogConfig` 的注释已写明「only applies when WithLogger was
  not injected」，且构造函数内部会 `cfg.Validate()`。

**影响**
同时使用 `WithLogger` 与 `WithLogConfig` 时，后者（级别、输出、轮转、`sql_outputs`）完全不生效，
且没有任何日志或错误；运维会以为配置已应用。这是**可诊断性**问题，不是校验缺失：
非法配置仍然会在 `WithLogConfig` 阶段报错，文档也写明了优先关系，缺的是运行时的
一致性提示。

**建议**
在 `New` 或 `buildLoggers` 中对“注入了 logger 且显式设置了 logConfig”给出一次 Warn
（或在 `options.validate` 中直接拒绝该组合），让配置冲突可见。

**Rev.2 复核补充**
- 原文「不校验也不提示」需要拆开看：校验在 `WithLogConfig` 里已经做了，不成立；
  不提示成立，但要标成可诊断性而非正确性缺陷。
- 判断「显式设置了 logConfig」不能用零值比较：`logger.Config{}` 是合法配置（全默认），
  需要在 `options` 里加一个 `logConfigSet bool`，否则零值配置会被误报冲突。

### O-05 停机失败信息只保留在 `cleanupErr`

**定位**
- `start.go:960-1097`：`cleanup` 把各阶段错误 `errors.Join` 到 `cleanupErr`；
  `Shutdown`（`start.go:903-937`）在 ctx 超时时直接返回 `ctx.Err()`。

**影响**
调用方拿不到“哪些模块停止失败、DB 是否关闭成功”，只能看到超时；
排障需要翻日志重新拼装。

**建议**
`Shutdown` 在 `ctx.Done()` 分支同时返回 `errors.Join(ctx.Err(), a.cleanupErr)`（在 cleanup 已完成的场景），
或提供 `App.CleanupError()` 只读访问器；`cleanup` 内部对每个失败项保留模块名（现在 DB/logger 错误缺少上下文）。

**Rev.2 复核补充**
- 时序上有个坑：`Shutdown` 的 `select` 在 `<-a.done` 与 `<-ctx.Done()` 之间竞争，ctx 先到时
  `cleanup` 可能仍在执行，`cleanupErr` 还没赋值。此时把 `a.cleanupErr` join 进去只会拿到 nil，
  看起来像「没有失败」。访问器必须配合 `<-a.done`（或已有的状态判断）使用，或者在
  `cleanup` 内部改为每完成一项就记录，而不是最后一次性赋值。
- 原文说「DB/logger 错误缺少上下文」成立：`gofydb.Close` 的错误直接 `errs = append(errs, err)`，
  没有 `fmt.Errorf("gofy: close database: %w", err)`；logger 的 Sync 失败甚至没有进入
  `errs`（`syncLoggers` 忽略返回值）。后者是否要升级为错误需要单独决定——日志通道关闭
  失败通常只值得告警。

---

## 5. 已核对且未发现问题的关键点

以下实现经逐行核对，结论成立，列出以说明覆盖范围并避免后续误改：

| 位置 | 核对结论 |
|---|---|
| `admission.go:56-105` | 准入与 lease 获取在同一读锁内完成，`close` 取写锁，关闭后不会再有新请求进入 handler；`isOpen` 仅用于日志与启动检查 |
| `generation.go:92-144,251-256` | lease 在 panic 路径仍由 `defer release()` 释放；退役代在 `active==0` 时关闭 `drained`，`sync.Once` 防重复关闭 |
| `generation.go:221-241` | 最多重试 8 次加载当前代，避免 Reload 瞬间的“退役代”死循环 |
| `reload.go:351-358` | `discardReloadResources` 只释放本次 Reload 新建的 DB / logger，不会误关线上资源 |
| `support/tenant/plugin.go:215-300,406-420` | 租户条件按“绑定值等于生效租户”判断是否去重（`pinsTenant`），不进入 `OrConditions` / `NotConditions`；`filterUpdate` 把租户列加入 `Omits`，阻止 `Save`/`Select("*")` 写零值 |
| `support/tenant/tenant.go:109-136` | `WithTenant` 显式覆盖优先于 Handler，空/空白回退；无租户时 `ok=false` 触发失败关闭 |
| `support/cors.go:60-70` | `AllowCredentials` + `"*"` 在 `Validate` 阶段被拒绝，`middleware/cors.go` 的回显分支不会落到该组合 |
| `support/jwtauth/jwt.go:183-265` | 只接受白名单内的 HMAC 算法；`hmac.Equal` 常数时间比较；`exp` 存在但非数字日期一律按 malformed 拒绝；`iss`/`aud` 可选校验 |
| `support/phpoffice/support.go:130-152` | 导出文件名只接受 basename，拒绝路径分隔符、控制字符、`.`/`..`；sheet 名按 Excel 规则校验 |
| `support/phpoffice/export.go:174-186` | 目标文件以 `O_CREATE` + `O_EXCL` 排他预留，失败路径删除半成品 |
| `support/image/support.go:380-433` | 水印/字体路径拒绝 `..` 段、要求常规文件，并用 `EvalSymlinks` + `filepath.Rel` 限定在允许根目录内 |
| `support/image/support.go:61-66,88-141` | 解码前用 `DecodeConfig` 预检像素数，`int64` 计算避免溢出；超大图不进入内存 |
| `support/claim/claim.go:116-119` | 表名走 `identifierPattern` 白名单，值全部参数绑定 |
| `support/middleware/bodylimit.go:25-31` | `http.MaxBytesReader` 在读 body 的 handler 之前安装，`nil` body 不处理 |
| `support/middleware/accesslog.go:255-265` | query 先脱敏再截断；解析失败直接返回 `***`，不落原始内容 |
| `start.go:727-739` | `duplicatePID` 只把 `IdentityMatch` + 存活判为重复，避免 pid 复用误杀 |
| `restart.go` 的 `isRestartIgnorableError` | join 错误仅当所有叶子可忽略时才视为“无实例”，不会掩盖部分失败（Rev.3 审计后已修，按函数名定位） |

---

## 6. 建议的验证与回归

由于本次无法执行 Go 工具链，以下命令应在具备 Go 1.26 的环境（仓库根目录）执行，
并与本报告的条目逐项对照（Rev.2 复核时 `3a6a851` 已在入库前跑过这三条，改动后仍应重跑）：

```bash
make fmt-check vet test-race        # 基础三件套（test-race 需要 cgo）
make guards                         # import 方向、无 init、模板快照
make cgo-free cross                 # CGO_ENABLED=0 构建 + Windows vet
```

针对性回归建议：

1. **P2-01**：用一个“拨号必超时”的 AMQP 地址构造 `Client`，关闭连接后调
   `Channel()` 并取消 ctx，断言在 `ConnectionTimeout` 内返回 ctx 错误（当前实现不会）。
2. **P2-02**：写一个忽略 ctx 的 worker handler（`select{}` 或 `time.Sleep(2*drain)`），
   在 drain 期间触发 `Shutdown`，用 `-race` 观察是否出现“关闭后写 DB”；修复后应看到
   “abandoned instance”告警且 DB 未被关闭。
3. **P2-03**：`http.DefaultTransport = roundTripFunc(...)` 后调用 `nacos.NewClient`，
   当前实现应 panic；修复后应正常返回客户端。
4. **P3-01**：把 `WithPIDRefreshInterval` 设为 1ms，循环启动/停止 100 次，
   断言退出后 pid 文件不存在，且不存在“旧进程覆盖新进程 pid”的情况。
5. **P3-02**：构造两个有停止依赖的模块（工厂 + 注入），执行一次 `Reload` 后 `Shutdown`，
   记录 `Stop` 调用顺序，应与启动时逆序一致。
6. **P2-04（原 P3-03）**：用含换行的环境变量值加载 YAML，当前实现应产生
   结构破坏；修复后应保持为单个标量（或明确报错）。同时覆盖 `allowMissing=true` 的
   空串替换路径。
7. **O-01**：对 `HeaderFromContext` 做 benchmark，比较当前实现与“直接读 map”的
   `allocs/op` 与 `ns/op`。

---

## 7. 附：审计方法与覆盖说明

- **覆盖方式**：先按包列出全部非测试文件，逐个读取；对并发/资源/边界相关的函数
  完整阅读上下文，而不是只看函数体；对跨文件调用链（如 `Reload → retireGeneration →
  stopModuleList → cleanup`）做了反向追踪。
- **风险面优先级**：生命周期与并发（start/stop/reload/worker）> 安全（tenant、JWT、
  CORS、路径、SQL）> 资源与错误处理（DB/连接/文件/goroutine）> 性能（热路径分配）。
- **未覆盖/受限**：
  - 无构建、vet、race、端到端运行结果；
  - `examples/` 与 `cmd/gofy/contract-template/` 生成物只做了抽样阅读，未逐文件审计；
  - 第三方 SDK 的语义（franz-go、amqp091、rocketmq-clients、excelize、goai）
    按公开契约理解，未核对其内部实现；
  - 未做模糊测试、压力测试与安全扫描（gosec/staticcheck）。
- **复核路径**：报告中每条 `文件:行号` 均对应当前工作树（`3a6a851`）；
  若后续提交改动了这些文件，请以提交 diff 为准重新确认行号。