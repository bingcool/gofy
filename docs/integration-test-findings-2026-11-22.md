# 集成测试补齐过程中的发现（2026-11-22）

本文档是「真实中间件集成测试补齐」（见 `integration-tests-2026-11-22.md`）过程中
发现问题的结论汇总。原则：能修的当场修，修不了或需要额外环境的如实记录在这里，
不假装测试覆盖了它实际没有覆盖的东西。

## 1. Kubernetes Lease：框架没有实现（无法测试）

需求表格中的「Kubernetes Lease：获取、续租、失锁、Fencing、Close」对应的实现
在仓库里不存在：全仓检索 `kubernetes` / `coordination` 零命中。当前锁能力只有
`support/lease`（Locker 接口）+ `support/lease/redislease`（Redis 实现）。

结论：不是缺测试，是缺能力。两条路——
- 需要租约但不依赖 k8s 的场景继续用 `redislease`（语义等价：token 递增即 fencing）；
- 确要基于 `coordination.k8s.io/v1 Lease` 的实现时，实现一个 `support/lease/k8slease`
  Locker，并把上表的五个场景写成对应集成测试（gated by `GOFY_TEST_K8S_CONFIG`）。

## 2. Redis「主从切换」无法用单机集成测试覆盖

`TestIntegrationOperationsRecoverAfterClientPause` 用 `CLIENT PAUSE` 模拟了
「服务端短暂不可达 + 驱动重试恢复」，这是单机 Redis 上最接近网络抖动的手段。
真正的主从切换（哨兵/Cluster failover）需要多节点拓扑：

- sentinel / cluster 拓扑不适合放进默认 `go test ./...` 的入口约定（需要额外的
  编排），本期不引入；
- 语义风险低的部分已有覆盖：租约续期、过期接管、断线恢复对「连接换了一个
  服务端」是同一代码路径（goredis 重连重试）；
- 真正切换相关的是「写入主节点后主节点切换、key 未同步」场景，这属于部署
  演练（chaos drill），建议进 runbook 而不是单元化集成测试。

## 3. RocketMQ FIFO / 顺序消费不在本期覆盖内

重投用例（`TestIntegrationNackRedelivers`）覆盖的是普通消息。FIFO 消息的
「失败后同队列内阻塞重投」语义未测——当前业务没有 FIFO 需求，引入时再补。

## 4. AI：SSE 断连没有自动恢复，断连 = 带错误的终止（已按实测确认）

实测结论（`TestIntegrationStreamStopsOnDisconnect`，httptest + TCP hijack）：

- 流中途断开 TCP 后，`Stream.Text()` 通道会正常关闭、已收到的部分内容保留、
  `Stream.Err()` 返回非 nil 错误——**框架不会假装生成已完成，也不会自动重连续流**；
- 这是刻意行为：LLM 生成不可续传（重连等于重新计费重新生成），自动重试会让
  用户收到拼接两段的伪文本。上层（ai-check worker 等）决定是否重试。

需要「断线续传」语义时，应该在调用方实现「保存已生成前缀 + 重新发起并让模型
续写」，而不是期待传输层恢复。

## 5. AI：瞬时错误重试由 goai 在请求层执行（实测确认）

`TestIntegrationRetriesTransientFailures` 实测：对 502 响应，`MaxRetries=3`
时库层自动重试，三次请求的 body 逐字节一致。注意两个边界：

- 重试发生在 **goai 内部**，gofy 层看到的只是最终结果；`ai.IsRetryable` 用于
  日志/指标分类，不驱动重试；
- **流式请求（Stream）的重试语义不同**：连接建立后的断连不会重放（见第 4 条），
  连接前的瞬时失败是否重试由 goai 决定，未单独断言。

## 6. MySQL「连接中断」用例的适用边界

`KILL <id>` / `pg_terminate_backend` 验证的是「池内连接被服务端杀死后的报错
与重拨」，即**服务端主动断开**。两类相关场景不在此列：

- 网络分区（连接既收不到响应也收不到 RST）：行为取决于驱动的
  `readTimeout`/`writeTimeout` 配置，属于 DSN 调优项，建议在部署层设置；
- 服务器重启后首次连接：由 `PingTimeout` 与 lazy connect 覆盖，路径相同。

## 7. 过程中顺手修正的两处测试基建问题

- `support/lease/redislease` 原有套件基于 miniredis，证明的是 Lua 脚本语义；
  新增的真实 Redis 用例（`integration_test.go`）才覆盖网络行为。两套并存，
  gate 不同，互不替代。
- `support/ai` 原测试文件是内部测试包（`package ai`），集成用例沿用内部包
  以复用 `compatConfig`/`newTestClient`，文件不能自导入框架包——用例内所有
  引用不带 `ai.` 限定符，新增文件时注意。

## 8. 运行清单（本机未跑真实 broker 的用例）

本机只有 MySQL 之外的中间件不齐全，以下用例编写完成、编译通过、无服务时正确
跳过，**尚未在真实中间件上执行过**，接 CI 环境后首跑：

- `support/amqp`：`TestIntegrationClosedConnectionFailsPublish`、
  `TestIntegrationQueueRecreatedAfterDelete`
- `support/kafka`：`TestIntegrationRebalanceHandsOffWithoutLoss`、
  `TestIntegrationRedeliveryWithoutCommit`
- `support/rocketmq`：`TestIntegrationNackRedelivers`、
  `TestIntegrationInvisibleDurationHidesDelivery`
- `support/nacos`：`TestIntegrationWatchReceivesConfigChange`、
  `TestIntegrationRegisterListDeregister`
- `support/db`、`support/pg`、`support/lease/redislease`：全部新增用例
  （本机 MySQL/PG/Redis 可用时即可跑）

已在真实进程上跑通且通过：`support/ai` 全部三个场景（httptest，无需外部服务）。
