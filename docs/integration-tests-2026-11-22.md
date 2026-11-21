# 集成测试补齐方案与入口（2026-11-22）

本文档记录「真实中间件集成测试补齐」的入口约定、场景矩阵与运行方式。原则延续
README 已有的 MySQL / PostgreSQL 约定：

1. **默认跳过**：所有真实中间件集成测试都由 `GOFY_TEST_*` 环境变量显式开启，
   没有服务的环境跑 `go test ./...` 不会被卡住（用例开头 `t.Skip`）。
2. **单测与集成各司其职**：miniredis / fake server 单测负责语义与边界
   （分配、幂等、错误分类），真实集成负责单测无法覆盖的网络行为——断线、
   重连、池恢复、重平衡、重投。
3. **用例只做黑盒验证**：不依赖中间件内部状态表，观察点全部落在 API 返回、
   收到的消息与耗时上。

## 入口一览

| 环境变量 | 指向 | 覆盖的包 | 未设置时 |
|---|---|---|---|
| `GOFY_TEST_MYSQL_DSN` | MySQL 8 DSN（utf8mb4、parseTime） | `support/db`、`support/claim`、`support/consumer` | 跳过 |
| `GOFY_TEST_PG_DSN` | PostgreSQL DSN（sslmode=disable） | `support/pg` | 跳过 |
| `GOFY_TEST_REDIS_ADDR` | Redis 6+ 地址（如 `127.0.0.1:6379`） | `support/lease/redislease` | 跳过 |
| `GOFY_TEST_AMQP_ADDR` | RabbitMQ 地址（如 `127.0.0.1:5672`；用户名/密码/vhost 分别由 `GOFY_TEST_AMQP_USERNAME` / `GOFY_TEST_AMQP_PASSWORD` / `GOFY_TEST_AMQP_VHOST` 提供） | `support/amqp` | 跳过 |
| `GOFY_TEST_KAFKA_BROKERS` | Kafka broker 列表（逗号分隔） | `support/kafka` | 跳过 |
| `GOFY_TEST_ROCKETMQ_ENDPOINT` | RocketMQ 5 gRPC 端点（如 `127.0.0.1:8081`） | `support/rocketmq` | 跳过 |
| `GOFY_TEST_NACOS_ADDR` | Nacos 地址（如 `127.0.0.1:8848`） | `support/nacos` | 跳过 |

AI（`support/ai`）的集成用例不需要真实供应商：超时、重试、幂等、SSE 断连
全部用 `httptest` 模拟端点验证，随 `go test ./support/ai` 常跑。

## 场景矩阵

| 基础设施 | 场景 | 文件 | 用例 |
|---|---|---|---|
| MySQL | 事务提交与回滚 | `support/db/integration_test.go` | `TestIntegrationTransactionsCommitAndRollback` |
| MySQL | 连接中断 | 同上 | `TestIntegrationKilledConnectionsFailFast` |
| MySQL | 连接池恢复 | 同上 | `TestIntegrationPoolRecoversAfterKill` |
| PostgreSQL | 事务提交与回滚 | `support/pg/integration_test.go` | `TestIntegrationTransactionsCommitAndRollback` |
| PostgreSQL | 连接中断与池恢复 | 同上 | `TestIntegrationPoolRecoversAfterTermination` |
| Redis | 锁续期与过期接管 | `support/lease/redislease/integration_test.go` | `TestIntegrationRenewKeepsLeaseAlive`、`TestIntegrationExpiredLeaseIsTakenOver` |
| Redis | 断线恢复 | 同上 | `TestIntegrationOperationsRecoverAfterClientPause` |
| RabbitMQ | 连接断开 | `support/amqp/integration_test.go` | `TestIntegrationClosedConnectionFailsPublish` |
| RabbitMQ | 队列重建 | 同上 | `TestIntegrationQueueRecreatedAfterDelete` |
| Kafka | Rebalance | `support/kafka/integration_test.go` | `TestIntegrationRebalanceHandsOffWithoutLoss` |
| Kafka | 重复投递 | 同上 | `TestIntegrationRedeliveryWithoutCommit` |
| RocketMQ | 不可见时长 | `support/rocketmq/integration_test.go` | `TestIntegrationInvisibleDurationHidesDelivery` |
| RocketMQ | ACK 与重投 | 同上 | `TestIntegrationNackRedelivers` |
| Nacos | 配置监听 | `support/nacos/integration_test.go` | `TestIntegrationWatchReceivesConfigChange` |
| Nacos | 注册/注销/发现 | 同上 | `TestIntegrationRegisterListDeregister` |
| HTTP Client | 超时 | `support/ai/integration_test.go` | `TestIntegrationTimeoutCancelsSlowGeneration` |
| HTTP Client | 重试与幂等 | 同上 | `TestIntegrationRetriesTransientFailures` |
| HTTP Client | SSE 断连 | 同上 | `TestIntegrationStreamStopsOnDisconnect` |
| Kubernetes Lease | —— | —— | 框架无实现，见 findings 文档 |

## 运行方式

```bash
# 单项
GOFY_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/db -run TestIntegration -v

# 全部（本机 docker 起齐中间件后）
GOFY_TEST_MYSQL_DSN='...' GOFY_TEST_PG_DSN='...' GOFY_TEST_REDIS_ADDR=127.0.0.1:6379 \
GOFY_TEST_AMQP_URL='amqp://guest:guest@127.0.0.1:5672/' GOFY_TEST_KAFKA_BROKERS=127.0.0.1:9092 \
GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 GOFY_TEST_NACOS_ADDR=127.0.0.1:8848 \
  go test ./support/... -run 'Integration' -v
```

验证过程中的发现与结论（含无法自动化的场景）见
`docs/integration-test-findings-2026-11-22.md`。
