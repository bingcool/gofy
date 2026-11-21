# Phase 2 / Phase 3 指标实现复核

## 任务要求

### 用户原始 QUERY
> gofy-health-and-worker-metrics-design-2026-11-21.md。这个的Phase 2和Phase 3也已经实现落地了，请你重新验证一下是否存在bug或者不严谨，或者需要优化的，请在文档后面补充上去

### 明确调整
无

### 需求清单
- r1（must, content_quality）：独立复核 Phase 2（指标管道解耦、`WithMetrics`、`MeterProvider`、worker 指标、worker `/metrics` 监听）与 Phase 3（consumer received/acked/nacked/dead、claim、lease 指标）的实际实现，不以落地记录自述为依据。
- r2（must, content_quality）：给出 bug / 不严谨 / 可优化三类结论，每条必须回源码取证（文件:行号）。
- r3（must, delivery）：把结论补充到 `docs/gofy-health-and-worker-metrics-design-2026-11-21.md` 文档**后面**（新增章节，不改既有章节编号语义）。
- r4（must, content_quality）：区分已验证、待确认、环境不可验证三类；`go` 工具链在本沙箱不可执行，编译/测试类结论不得声称已跑。

## 环境理解
- 文档：`docs/gofy-health-and-worker-metrics-design-2026-11-21.md`，855 行左右，§4 指标设计、§5 分阶段（含 Phase 2/3 落地记录）、§6 验收。
- 提交：`a9e2546`(Phase 1) → `7def447`(Phase 2 指标解耦 + worker 指标) → `3ceb9dc`(Phase 3 consumer/claim/lease) → `529814d`(文档) → 工作树干净。
- 环境限制：`go`/`gofmt`/`make` 被 DuMate sandbox-manager 按 exec 白名单拦住（`/usr/local/go/bin` 不在名单），只能静态阅读 + tree_sitter 语法解析。

## 规划及进度

### 当前计划
- [x] 提取文档对 Phase 2/3 的设计承诺与验收项，作为核对清单
- [x] 复核 Phase 2 实现（otel/metrics、option、gofy、worker_host、core/runtime、worker/metrics）
- [x] 复核 Phase 3 实现（consumer、claim、lease 的 instrument 与标签）
- [x] 汇总问题分级并写入文档新增章节

### 未解决项与下一步
- 发现的 10 项已写入文档 §9，全部未修复（用户本轮只要求复核与补充文档）。
- 编译/测试仍未验证：本机 `go` 不可执行，修复后需用户在终端跑 gofmt/build/vet/test/guards。

## 关键证据及证据分析

- M-01：`support/consumer/consume.go:504-510` 的顺序调用 + 同处注释声称 defer；`support/consumer/*.go` 无 `recover()`。
- M-02：`examples/demo-service/worker/group1/scan_claim.go:355-380` 对 `MeterProvider`/`Metrics` 零命中；字段定义在 `support/claim/runner.go:188-194`。
- M-03：`rg "lease\.Run\("` 排除 `_test.go` 后为空；`support/lease/lease.go:132-143` 的 `Name`/`MeterProvider`/`Metrics` 无人设置。
- M-04：`rg "gofy\.health" --glob '*.go'` 零命中，§4.2/§4.6 却已承诺三个指标与 NotReady 告警。
- M-05：`worker/ready.go:91-100` 持锁回调，`worker/runner.go:358` 回调为 `setInstances`。
- M-06：四处“provider + 布尔”双字段；`newXxxMetrics` 的 `if !enabled || provider == nil` 让漏传静默失效。
- M-07：`support/consumer/metrics.go:116-121` 空 map 透传；六适配器均已传配置值，Kafka 有 `topic is required`。
- M-08：`start.go:1162-1163` 有 Shutdown，`worker_host.go:397` 只有 `a.cleanup()`。
- M-09：`support/consumer/metrics.go:168-171` 把 ledger 并成 retry，与 §4.3 枚举冲突。
- M-10：`worker_host.go:242` 未看 `DisableMetrics`，`support/diagnostics/diagnostics.go:234` 才决定是否挂 `/metrics`。
- 已核对无问题：view 按 (Histogram, Unit=s) 匹配（`support/otel/metrics.go:167-172`）、`MeterProvider()` nil 安全（`:217-222`）、四个新直方图均带 `WithUnit("s")`、`buildMetrics` 优先级、worker `/metrics` 由 `startDiagnostics` 提供、决策单点、claim 五处互斥、broker 专用钩子、禁用时零分配。

## 工具使用与问题反馈

## 中间产物与文档
- 交付物：`docs/gofy-health-and-worker-metrics-design-2026-11-21.md`，新增 §9（9.1 方法边界 / 9.2 汇总表 / 9.3 十条逐项 / 9.4 已核对无问题 / 9.5 修复顺序）。
- `/Users/huangzengbing/Documents/wwwroot/gofy/docs/gofy-phase23-metrics-review-worklog.md`：本工作文档，交付后删除。

## 二次复核修正（2026-11-21 23:47）

按用户要求结合源码逐条复核 §9 的 10 项（本轮 `go` 工具链可用，均回读源码取证）。结论：

**成立并保留（7 项）**：M-01～M-07。其中四处就地修正：
- **M-01**：补充 broker 层 recover 证据（`kafka/consumer.go:541-548`、`amqp/queue.go:458-465`、
  `rocketmq/consumer.go:638-645`、`queues/consume.go:250-257`），panic 不会崩进程，影响限定为
  「panic 那次投递漏计、重投计入」；修法改为 defer + 预置 `decision{reason:"retry"}`——裸 defer
  会产出 `reason=""` 的枚举外标签。
- **M-02**：确认修法可直接编译（`rt` 是 `*core.Runtime`，`MeterProvider`/`Metrics` 为导出字段，
  `:324` 取得）；补充对比——六个消息消费者都经 `start()` 接线，只有 claim 掉线。
- **M-03**：P2→P3 降级为文档缺口（库能力仓库内无调用点属正常）；补证据 `worker/README.md:433`
  与 `docs/worker-guide.md:281` 示例未带 `Name`/`MeterProvider`/`Metrics`。
- **M-05**：补充频率边界——onChange 仅在注册与每实例首次上报时触发（约 `instances+1` 次/Runner
  生命周期），非稳态热路径。
- **M-06**：修法选项「布尔由 provider != nil 推导」被否——`Runtime.MeterProvider` 永不为 nil
  （noop 默认，`runtime.go:116-118`），推导会破坏零分配目标；保留帮助函数与 godoc 两选项。

**撤销（3 项）**：
- **M-08**：`start.go:1162-1163` 在 `cleanup()`（`start.go:1031-1198`）内部的 `cleanupOnce.Do`
  闭包里，`worker_host.go:397` 调用的正是同一个 `cleanup()`（HTTP 路径 `start.go:296/988/1007`
  也走它）。worker 正常停机会关闭 metrics/telemetry；唯一跳过场景是 abandoned（刻意，与跳过
  关库同一分支）。原判把共享函数当成了 HTTP 专属路径。
- **M-09**：§4.3 的 outcome 枚举（`:505`）本就不含 `ledger`——`ledger` 只在 reason 枚举（`:504`）；
  代码 ledger→retry 恰是按设计执行，`metrics.go:138-142` 注释已写明。原判误读了枚举归属。
  已在 §4.3 outcome 行补注防再误读。
- **M-10**：HTTP 路径（`start.go:208`）在同组合下行为完全相同（监听启动、`/metrics` 404、
  `gofy.go:512-514` 告警），非 worker 特有缺陷；原修法会制造 worker/HTTP 分叉并顺带关掉 pprof。
  worker 真正的不对称（无指标则完全不开监听）是 `worker_host.go:237-241` 注释声明的刻意设计。

**未解决项与下一步**：7 项有效发现未修复，修复顺序见设计文档 §9.5（已同步更新：M-01 → M-02 →
M-03/M-04 → M-06 → M-05/M-07）。