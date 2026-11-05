# gofy-library 详细设计（v1）

> 状态：**草案，待评审**（2026-11-04）
>
> 关联文档：[gofy 框架设计](./library-design.md)、[gofy-interface-service 设计](./gofy-interface-service-design.md)
>
> 参考实现：`/Users/huangzengbing/Documents/wwwroot/library`（PHP，命名空间 `Swoolefy\Library`）
>
> 本文的结论只有一个：**新建 `gofy-library` 仓库，作为与 `gofy` 框架解耦、按需引入的 Go 组件库。** 它只移植 swoolefy library 的“能力与语义”，不移植 Swoole 的协程、容器、进程池和阻塞调用模型。业务服务可以只依赖 `gofy`，也可以在此基础上按需依赖 `gofy-library` 的若干包。

---

## 目录

1. [背景、目标与非目标](#1-背景目标与非目标)
2. [模块清单与移植矩阵](#2-模块清单与移植矩阵)
3. [仓库结构与依赖规则](#3-仓库结构与依赖规则)
4. [通用契约](#4-通用契约)
5. [数据组件](#5-数据组件)
6. [消息组件](#6-消息组件)
7. [安全与身份组件](#7-安全与身份组件)
8. [文件、媒体与办公文档](#8-文件媒体与办公文档)
9. [集成、可观测性与工具](#9-集成可观测性与工具)
10. [业务服务接入](#10-业务服务接入)
11. [测试策略](#11-测试策略)
12. [CI、版本与发布](#12-ci版本与发布)
13. [实施计划](#13-实施计划)
14. [验收标准](#14-验收标准)
15. [风险与开放问题](#15-风险与开放问题)
16. [附录](#16-附录)

---

## 1. 背景、目标与非目标

### 1.1 为什么需要独立的 gofy-library

gofy 目前的定位是**框架库**：HTTP 生命周期、路由、绑定校验、响应信封、日志、MySQL 连接、脚本与命令、脚手架。它刻意保持小而稳定，避免把 Redis、对象存储、短信、Excel 这类“业务常用但非框架必需”的能力塞进核心依赖。

swoolefy 的 `library` 恰好承担了这层职责：它是与框架并列的组件库，业务按需取用。Go 侧同样需要这一层，但形态必须改变：

- PHP 侧靠 `Application::getApp()->get('redis')` 拿协程内单例；Go 侧必须**显式构造、显式传递、显式关闭**。
- PHP 侧大量组件绑定 Swoole 协程、`Coroutine\Channel`、`ConnectionPool`、`PCNTL`；Go 侧对应的是 goroutine、`context.Context`、`database/sql` 连接池、`sync` 原语。
- PHP 侧的“重连后按只读白名单重放一次”是经过事故验证的语义，必须保留；Go 客户端默认重试策略更激进，不能直接沿用。
- PHP 侧的数组 DTO、魔术方法、`serialize()` 负载不适合跨语言；Go 侧统一用结构体、JSON、显式编解码。

因此 `gofy-library` 不是一个“PHP library 的逐类翻译”，而是**按 Go 运行时重新实现的组件集合**，同时保留 swoolefy 在可靠性上的关键取舍。

### 1.2 三仓关系

```text
                    ┌──────────────────────────────┐
                    │  gofy（框架库）               │
                    │  lifecycle / transport /     │
                    │  config / logging / mysql    │
                    └──────────────┬───────────────┘
                                   │ 业务服务同时依赖
┌──────────────────────────────┐   │   ┌──────────────────────────────────┐
│ gofy-library（可选组件库）     │◄──┼──►│ gofy-interface-service（契约库）  │
│ redis / lock / storage / ... │   │   │ DTO / endpoint / client / openapi │
└──────────────────────────────┘   │   └──────────────────────────────────┘
                                   ▼
                         ┌───────────────────┐
                         │ 业务服务           │
                         │ order-service ... │
                         └───────────────────┘
```

三条硬规则：

1. `gofy` **不依赖** `gofy-library`，也不依赖 `gofy-interface-service`。
2. `gofy-library` **不依赖** `gofy`，也不依赖 `gofy-interface-service`。它可以被任何 Go 服务使用，不要求使用 gofy 框架。
3. `gofy-interface-service` **不依赖** `gofy-library`。契约保持纯净，不能因为某个 DTO 需要 `Uint64String` 就把 Redis 依赖带进调用方。

依赖方向只允许业务服务指向三者，绝不允许三者互相反向依赖。CI 必须用 `go list -deps` 和源码扫描双重校验。

### 1.3 已锁定的决策

| # | 决策 | 结果 |
| --- | --- | --- |
| L1 | 仓库形态 | 独立仓库 `github.com/bingcool/gofy-library`，独立 Go module、独立版本号 |
| L2 | 与 gofy 的关系 | 零依赖。`db` 包接收外部传入的 `*gorm.DB`，不负责创建连接 |
| L3 | 与契约仓库的关系 | 零依赖。契约是线上协议，组件库是运行时能力，二者不互相 import |
| L4 | 状态管理 | 禁止 `init()` 副作用、禁止包级可变单例；所有组件由构造函数创建 |
| L5 | 资源所有权 | 谁创建谁关闭；`Close(ctx)` 幂等；关闭后调用返回 `ErrClosed` |
| L6 | 配置 | 每个组件自带 `Config` 结构体和 `Validate()`；库不读配置文件、不读环境变量 |
| L7 | 日志 | 统一使用标准库 `log/slog`；未传 logger 时使用丢弃实现，不落全局 logger |
| L8 | 上下文 | 所有 I/O 方法第一个参数是 `context.Context`；超时由调用方或组件默认值控制 |
| L9 | 重试 | “重连”和“重放”分离；只读命令最多重放一次；写操作、事务内、未知命令绝不重放 |
| L10 | 依赖分层 | 核心包只依赖标准库和少量基础库；云厂商 SDK 隔离在 driver 子包 |
| L11 | 模块策略 | v1 单 module；重量级 driver 用子包隔离，暂不做嵌套 module |
| L12 | 兼容性 | 不承诺与 PHP 的序列化、加密负载、验证 DSL 二进制兼容；只承诺语义等价 |
| L13 | 排除项 | cron、worker/process 管理、进程池、Swoole 协程运行时、daemon 管理不在本仓库 |
| L14 | 稳定性分级 | 每个包标注 `stable` / `beta` / `experimental`，README 与 CHANGELOG 同步 |

### 1.4 目标

1. 把 swoolefy library 中**与运行时无关、可复用**的能力，按 Go 习惯重新实现为独立包。
2. 为业务服务提供一致的构造、配置、错误、日志、关闭、健康检查约定，避免每个服务各写一套 Redis/锁/限流封装。
3. 保留 swoolefy 经过生产验证的关键语义：只读重放、失败关闭的多租户过滤、原子 Lua、一次性验证码、幂等边界。
4. 让组件可测试：每个外部依赖都有 `fake` 或接口替身，默认单测不连外部服务。
5. 控制依赖体积：核心包不因引入对象存储而被迫拉入 AWS SDK。

### 1.5 非目标

- 不做第二个 Web 框架。HTTP 生命周期、路由、响应信封仍归 `gofy`。
- 不做 ORM。`db` 只在 GORM 之上提供事务、分页、租户、只读重试等薄封装。
- 不做服务网格、注册中心抽象层。Nacos 只是可选适配器，接口留在使用方。
- 不做统一配置中心。配置加载仍由业务或 `gofy/config` 负责。
- 不做任务调度、队列消费进程管理、worker 池编排；只提供队列/消息客户端。
- 不复制 PHP 的 `ArrayDto`、`SplBean`、`ValidateRule` DSL、`Purl`、`SplArray` 等语言特有抽象。
- 不保证 PHP 与 Go 之间的加密、缓存、队列负载可直接互通；跨语言互通必须走显式协议（JSON/Protobuf + 版本字段）。

### 1.6 与 swoolefy library 的对应关系

| 维度 | swoolefy library（PHP） | gofy-library（Go） |
| --- | --- | --- |
| 获取实例 | `Application::getApp()->get('redis')`，协程内单例 | `redis.New(cfg)`，显式持有并传递 |
| 生命周期 | 随 worker/协程创建销毁 | 随 `main` 或 module 创建，`Close(ctx)` 关闭 |
| 并发模型 | 协程 + Channel + 连接池 | goroutine + context + 原生连接池 |
| 配置 | 数组 + 组件名 | 强类型 `Config` + `Validate()` |
| 日志 | 框架 LogManager | `*slog.Logger` |
| 重试 | 重连 + 只读白名单重放一次 | 同语义，显式 API 区分 `Do` / `DoRead` |
| 多租户 | 协程 Context + SQL 拦截器 | `context.Context` + GORM 实例级回调，失败关闭 |
| 队列 | Redis List/ZSET，pop 即删除，无 ack | Redis Streams 消费组，ack + pending 认领 + DLQ |
| 验证 | ThinkPHP 风格规则 DSL | `go-playground/validator` tag + 自定义规则 |
| 序列化 | PHP `serialize()` / 数组 | JSON / 显式编解码，负载带版本 |

### 1.7 设计原则

1. **显式优于隐式**：依赖从构造函数进入，不从包级变量或全局容器取。
2. **失败关闭优于静默降级**：租户缺失、锁未持有、限流后端不可用（安全场景）都必须报错，而不是放行。
3. **重连不等于重放**：网络断开可以重建连接；是否重放由命令语义和事务状态决定。
4. **接口在使用方定义**：库只为自己需要多态的地方定义接口（`storage.Store`、`sms.Sender`、`mail.Sender`），不预造大而全的抽象。
5. **driver 隔离**：云厂商 SDK、图片编解码、Excel 引擎都放在子包，核心包不 import。
6. **可观测但不绑定**：日志用 `slog`，追踪用可选 `otelx`，指标由调用方决定；库不强制任何厂商。
7. **安全默认值**：TLS 校验默认开启、密钥长度校验、路径穿越防护、敏感字段脱敏、随机数使用 `crypto/rand`。
8. **测试不依赖外网**：单测使用 fake；集成测试用 testcontainers 并打 build tag。

---

## 2. 模块清单与移植矩阵

### 2.1 移植分级

| 级别 | 含义 |
| --- | --- |
| **A 语义直译** | 能力与行为基本一致，只做语言适配 |
| **B 改造移植** | 保留核心语义，按 Go 运行时重写并发、生命周期或数据结构 |
| **C 仅借鉴思路** | 不复制实现，用 Go 生态更合适的方案重做 |
| **D 不移植** | 语言/运行时特有，或已被标准库/框架覆盖 |

阶段：P0 与框架同期；P1 首批业务需要；P2 第二批；P3 可选增强。

### 2.2 总表

| # | swoolefy 模块 | 能力 | gofy-library 包 | 级别 | 关键依赖 | 阶段 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `Db` | 事务、分页、软删除、多租户、只读重放、批量写 | `db` | B | GORM | P0 |
| 2 | `Redis` | 连接、集群、重连、只读命令重放 | `redis` | B | go-redis v9 | P0 |
| 3 | `Cache` | JSON 缓存、批量、TTL | `cache` | B | go-redis | P0 |
| 4 | `Lock` | Redis 互斥锁、租约、续期 | `lock` | B | go-redis + Lua | P0 |
| 5 | `RateLimit` | 滑动窗口、令牌桶 | `ratelimit` | A | go-redis + Lua | P0 |
| 6 | `Uuid` | 分布式单调 ID | `idgen` | B | go-redis / snowflake | P0 |
| 7 | `Jwt` | HMAC/RSA/ECDSA 签发与校验 | `jwt` | B | golang-jwt v5 | P0 |
| 8 | `Encryption` | 对称加密、密钥轮换 | `encrypt` | B | 标准库 AES-GCM | P0 |
| 9 | `Hashing` | bcrypt / argon2id | `password` | A | x/crypto | P0 |
| 10 | `Validate` | 参数规则与消息 | `validation` | C | validator v10 | P0 |
| 11 | `Queues` | 即时/延迟队列、重试 | `queue` | B | Redis Streams | P1 |
| 12 | `PubSub` | Redis 发布订阅 | `pubsub` | B | go-redis | P1 |
| 13 | `Amqp` | RabbitMQ 发布/消费、DLX 延迟 | `amqp` | B | amqp091-go | P1 |
| 14 | `Kafka` | 生产/消费、消费组 | `kafka` | B | segmentio/kafka-go | P1 |
| 15 | `Mongodb` | 集合操作、ObjectID 转换 | `mongo` | B | mongo-driver v2 | P2 |
| 16 | `Aliyun/Datahub` | DataHub 读写与位点提交 | `datahub` | C | 阿里云官方 Go SDK | P2 |
| 17 | `FileStorageSystem` | 本地/S3/OSS/COS、预签名、分片 | `storage` | B | 各云 SDK | P1 |
| 18 | `Sms` | 多网关短信、验证码 | `sms` | B | 阿里云/腾讯云 SDK | P1 |
| 19 | `Mail` | SMTP、模板、附件 | `mail` | B | go-mail | P1 |
| 20 | `Captcha` | 图形验证码 | `captcha` | B | base64Captcha | P1 |
| 21 | `Image` | 缩放、裁剪、水印、编码 | `imagex` | B | imaging + x/image | P2 |
| 22 | `Qrcode` | PNG/SVG/WiFi/Logo | `qrcode` | A | QR 编码库 | P2 |
| 23 | `Barcode` | CODE128/39、EAN、UPC、ITF | `barcode` | A | barcode 库 | P2 |
| 24 | `PhpOffice/Excel` | 流式导入导出 | `excel` | B | excelize v2 | P2 |
| 25 | `CurlProxy` / `HttpClient` | 超时、重试、日志、追踪 | `httpclient` | B | 标准库 | P0 |
| 26 | `Nacos` | 配置中心、服务发现 | `nacos` | B | nacos-sdk-go v2 | P2 |
| 27 | `OpenTelemetry` | Trace 初始化与导出 | `otelx` | B | otel-go | P1 |
| 28 | `Events` | 进程内事件分发 | `events` | B | 标准库 | P2 |
| 29 | `Clock` | 可替换时钟 | `clock` | A | 标准库 | P0 |
| 30 | `Health`（swoolefy HTTP 模块） | 依赖健康检查 | `health` | B | 标准库 | P1 |
| 31 | `Oauth` | 第三方登录门面 | `oauth` | C | 各厂商 SDK/OIDC | P3 |
| 32 | `LinuxDash` | 主机指标 | `sysinfo` | C | gopsutil v4 | P3 |
| 33 | `Helper` / `ArrayHelper` / `Spl` / `Purl` | 字符串、数组、集合、URL | — | D | 标准库 `slices`/`maps`/`net/url` | — |
| 34 | `Protobuf` | 动态数组/JSON 桥接 | — | D | 生成的 proto struct | — |
| 35 | `Pool` | Swoole 连接池 | — | D | `database/sql` / go-redis 内置池 | — |
| 36 | `Component/ListObject` | PHP 分页对象 | — | D | `gofy-interface-service/paging` | — |
| 37 | 协程/进程/定时器相关 | worker、process、Channel、tick | — | D | context/goroutine/部署层 | — |

### 2.3 不移植清单与理由

| 项目 | 不移植理由 |
| --- | --- |
| `Application::getApp()` 容器 | Go 没有协程局部容器；显式构造和依赖注入更清晰，也便于测试 |
| `Swoole\ConnectionPool` / `Pool` | `database/sql`、go-redis、http.Transport 已内置连接池，且更成熟 |
| `SplContextArray` | 以协程 CID 为键的上下文；Go 用 `context.Context` + 显式传递 |
| `CoroutineSynchronizedTrait` | 子协程执行临界区 + Channel 握手是 Swoole 特有；Go 只需租约、令牌、Lua 释放 |
| `PCNTLHeartbeatSender` | 依赖 pcntl 信号；amqp091-go 自带心跳与 `NotifyClose` |
| `LinuxDash` 的 shell 脚本 | 不执行 shell、不解析 `/proc` 文本；用 gopsutil 或直接读结构化文件 |
| `Protobuf` 动态桥接 | Go 用生成代码 + `protojson`；反射填值是反模式 |
| `ValidateRule` DSL | 与 PHP 字符串规则耦合；Go 用 validator tag，复杂规则写函数 |
| `Helper` / `ArrayHelper` / `SplArray` | 标准库 `slices`、`maps`、`strings`、`iter` 已覆盖绝大多数场景 |
| `Purl` | `net/url` 已覆盖解析、拼接、查询参数 |
| `Component/ListObject` | 分页协议属于契约层，不应由组件库定义第二套 |
| PHP `serialize()` 加密负载 | 跨语言不可移植且不安全；改用带版本号的自定义信封 |
| Redis List 队列的“pop 即删除” | 崩溃即丢消息；改用 Streams 消费组并显式 ack |
| 手写 DataHub 签名 | 官方 Go SDK 已覆盖签名、重试、位点；手写只增加维护成本 |

### 2.4 与 gofy 现有能力的边界

| 能力 | 归属 | 说明 |
| --- | --- | --- |
| MySQL 连接创建、连接池参数 | `gofy/support/mysql` | `gofy-library/db` 只接收 `*gorm.DB` |
| 日志构建、轮转 | `gofy/logging` | 组件库只接收 `*slog.Logger` |
| 配置加载、环境变量展开 | `gofy/config` | 组件库只定义 `Config`，不读文件 |
| HTTP 路由、绑定、校验、信封 | `gofy/transport` | 组件库不提供 gin 中间件，只在文档给 10 行适配示例 |
| 健康检查端点 | `gofy/start.go` | 组件库提供 `health.Checker`，由业务注册 |
| 业务错误码 | `gofy/apperr` + 契约 `errcode` | 组件库返回自己的哨兵错误，由业务映射 |
| 分页协议 `page/page_size` | 契约 `paging` | 组件库 `db.PageResult[T]` 是内部结果，由业务映射到契约 |
| DTO / OpenAPI / Client | `gofy-interface-service` | 组件库不感知契约 |

---

## 3. 仓库结构与依赖规则

### 3.1 最终目录

```text
gofy-library/
├── go.mod                          # module github.com/bingcool/gofy-library
├── go.sum
├── README.md                       # 模块清单、稳定性分级、快速开始
├── CHANGELOG.md
├── Makefile
├── .golangci.yml
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── integration.yml
├── logx/
│   └── logx.go                     # Discard、脱敏字段辅助
├── clock/
│   └── clock.go                    # Clock、System、Fake
├── health/
│   └── health.go                   # Checker、Registry、Report
├── redis/
│   ├── redis.go                    # Config、New、Client、Do/DoRead
│   ├── retry.go                    # 只读命令白名单与分类
│   └── script.go                   # EVALSHA 封装与回退
├── cache/
│   └── cache.go                    # JSON 编解码、批量、TTL、GetOrLoad
├── lock/
│   └── lock.go                     # Acquire/TryAcquire/WithLock/Renew/Release
├── ratelimit/
│   ├── ratelimit.go                # Result、FailureMode
│   ├── sliding.go                  # 滑动窗口
│   ├── bucket.go                   # 令牌桶
│   └── lua.go                      # go:embed 的 Lua 脚本
├── idgen/
│   ├── idgen.go                    # Generator 接口
│   ├── redis.go                    # 时间戳 + 序号
│   └── snowflake.go                # 64 位雪花
├── db/
│   ├── db.go                       # DB、New、GORM、Tx、Read
│   ├── paginate.go                 # PageRequest、PageResult、Paginate、Cursor
│   ├── tenant.go                   # 租户 context 与 GORM 回调
│   ├── softdelete.go               # WithTrashed、OnlyTrashed、Restore
│   └── logger.go                   # GORM logger -> slog
├── queue/
│   ├── queue.go                    # Message、Producer、Consumer、Handler
│   ├── redis.go                    # Redis Streams 实现
│   ├── delay.go                    # 延迟 ZSET 调度
│   └── lua.go
├── pubsub/
│   └── pubsub.go                   # Subscribe、并发上限、统计
├── amqp/
│   ├── amqp.go                     # Config、Publisher、Consumer
│   └── delay.go                    # TTL + DLX 延迟队列声明
├── kafka/
│   ├── producer.go
│   └── consumer.go
├── mongo/
│   └── mongo.go                    # Client、Collection、ObjectID 转换
├── datahub/
│   └── datahub.go                  # 官方 SDK 薄封装（P2）
├── jwt/
│   ├── jwt.go                      # Manager、Sign、Parse
│   ├── keyset.go                   # kid 轮换
│   └── claims.go                   # RegisteredClaims 辅助
├── encrypt/
│   └── encrypt.go                  # AES-256-GCM、版本化信封、密钥轮换
├── password/
│   └── password.go                 # bcrypt、argon2id、NeedsRehash
├── validation/
│   ├── validation.go               # New、Details
│   └── rules.go                    # 手机号、身份证、枚举、强密码、金额
├── captcha/
│   └── captcha.go                  # Generate、Verify、一次性消费
├── httpclient/
│   ├── client.go                   # Config、New、Do、JSON
│   ├── retry.go                    # 幂等方法重试
│   ├── error.go                    # Error、状态码与请求 ID
│   └── redact.go                   # Header/Body 脱敏
├── support/nacos/
│   ├── config.go                   # Config、Load、FromEnv、Register/Discovery 配置
│   ├── client.go                   # Open API 客户端、鉴权 token 缓存
│   ├── sync.go / watch.go          # 配置同步、长轮询、热更新回调
│   ├── naming.go                   # Instance、Register/Deregister/Beat/List
│   └── register.go                 # Registrar、心跳、IP 探测
├── storage/
│   ├── storage.go                  # Store、ObjectInfo、Capabilities
│   ├── fake/
│   │   └── fake.go                 # 内存实现，单测用
│   └── driver/
│       ├── local/local.go
│       ├── s3/s3.go
│       ├── oss/oss.go
│       └── cos/cos.go
├── sms/
│   ├── sms.go                      # Message、Sender、Manager
│   ├── verification/
│   │   └── verification.go         # 验证码发送/校验（Redis + 限流）
│   └── gateway/
│       ├── aliyun/aliyun.go
│       ├── tencent/tencent.go
│       └── log/log.go              # 测试与本地开发
├── mail/
│   └── mail.go                     # Message、Sender、SMTP、模板
├── imagex/
│   └── imagex.go                   # 解码限制、缩放裁剪、水印、编码
├── qrcode/
│   └── qrcode.go                   # PNG/SVG/WiFi
├── barcode/
│   └── barcode.go                  # CODE128/39、EAN、UPC、ITF
├── excel/
│   ├── export.go                   # StreamWriter 导出
│   └── import.go                   # 流式读取
├── events/
│   └── events.go                   # 同步/异步分发、panic 恢复
├── otelx/
│   └── otelx.go                    # Setup、Shutdown、HTTP transport
├── sysinfo/
│   └── sysinfo.go                  # gopsutil 指标（P3）
├── internal/
│   ├── errs/                       # 共享哨兵错误
│   ├── lua/                        # 脚本加载与 SHA 缓存
│   ├── retry/                      # 退避、抖动、Permanent
│   ├── rand/                       # crypto/rand token
│   ├── redact/                     # 日志与错误脱敏
│   └── depcheck/                   # 依赖边界测试
├── examples/
│   └── quickstart/                 # 独立 module，演示构造/关闭/健康检查
└── docs/
    └── gofy-library-design.md      # 本文（可同步到主仓 docs）
```

### 3.2 包命名

Go 的包名会被调用方直接使用，必须避免与标准库或主流依赖混淆：

| 目录 | 包名 | 说明 |
| --- | --- | --- |
| `redis` | `redis` | 包装 go-redis；业务通常只 import 本包；需要原生客户端时 `client.Raw()` |
| `db` | `db` | 仅操作传入的 `*gorm.DB`，不创建连接 |
| `idgen` | `idgen` | 生成的是分布式自增 ID / snowflake，不是 UUID v4，避免叫 `uuid` |
| `encrypt` | `encrypt` | 避免与标准库 `crypto` 同名 |
| `password` | `password` | 表达“密码哈希”，避免与标准库 `hash` 同名 |
| `validation` | `validation` | 避免与 `validator` 依赖混淆 |
| `imagex` | `imagex` | 避免与标准库 `image` 同名 |
| `otelx` | `otelx` | 明确是封装层，不是 otel API 本身 |
| `sms` / `mail` | `sms` / `mail` | 与 `net/mail` 同时使用时调用方按需起别名 |

所有导出标识符使用完整英文单词，不使用 `Xxx` 缩写；错误变量统一 `Err` 前缀；接口名不使用 `I` 前缀。

### 3.3 依赖分层

| 层级 | 包 | 允许依赖 |
| --- | --- | --- |
| T0 基础 | `logx`、`clock`、`health`、`events`、`encrypt`、`password`、`jwt`、`validation`、`httpclient` | 标准库 + 少量通用库（jwt、validator） |
| T1 基础设施 | `redis`、`db`、`cache`、`lock`、`ratelimit`、`idgen`、`queue`、`pubsub` | T0 + go-redis / GORM |
| T2 驱动与集成 | `storage`、`sms`、`mail`、`captcha`、`imagex`、`qrcode`、`barcode`、`excel`、`amqp`、`kafka`、`mongo`、`nacos`、`otelx`、`datahub`、`sysinfo` | T1 + 厂商 SDK / 编解码库 |
| T3 适配器 | v1 不提供 | 若未来提供 `adapter/gofy`，才允许依赖 `gofy` |

规则：

1. T0 不得 import T1/T2。
2. T1 不得 import T2。
3. T2 之间允许有限复用：`storage/driver/local` 可 import `storage`；`sms/verification` 可 import `redis`、`ratelimit`、`cache`；`captcha` 可 import `redis`。
4. 任何包不得 import `gofy`、`gofy-interface-service`。
5. `internal/` 可被仓库内任意包 import，但不得被外部 import。

### 3.4 依赖边界如何强制

仅靠约定不够，CI 必须自动检查。实现两种检查：

1. **编译期**：`go list -deps ./...` 输出每个包的全部依赖，脚本按层级断言。
2. **源码期**：`internal/depcheck` 用 `go/parser` 扫描 import，发现 `github.com/bingcool/gofy`、`gofy-interface-service` 直接失败。

示例检查逻辑：

```go
// internal/depcheck/depcheck_test.go
func TestNoForbiddenImports(t *testing.T) {
	forbidden := []string{
		"github.com/bingcool/gofy",
		"gofy/gofy-interface-service",
	}
	for _, pkg := range packages(t, "./...") {
		for _, imp := range pkg.Imports {
			for _, f := range forbidden {
				if strings.HasPrefix(imp, f) {
					t.Errorf("%s imports forbidden package %s", pkg.PkgPath, imp)
				}
			}
		}
	}
}
```

T0/T1 的层级检查用正则匹配 import 路径前缀，例如 `redis` 包出现 `storage/driver/s3` 即失败。SDK 的间接依赖无法完全禁止，但通过“包级 import”可以保证调用方不 import 的包不会被链接进二进制。

### 3.5 为什么 v1 用单 module

备选方案是每个重量级驱动一个嵌套 module（`storage/s3/v1.0.0`），优点是消费者不下载无关 SDK，缺点是：

- 版本标签复杂，一次跨模块变更要打多个 tag；
- `go.work`、本地联调、CI 缓存都更复杂；
- 组件之间共享 `Config`、错误、Lua 脚本时会出现循环或重复。

v1 选择单 module，原因：

1. Go 1.17+ 的模块图裁剪会避免构建未 import 的包；下载体积增加，但编译产物不受影响。
2. 业务服务只 import 需要的包，云 SDK 不会被链接。
3. 单版本号便于文档和发布。

若后续下载体积成为问题，再把 `storage/driver/*`、`sms/gateway/*` 拆成嵌套 module，import 路径保持不变，属于兼容演进。

### 3.6 driver 的创建方式

不使用全局注册表。每个有驱动的包在 `New` 中按 `Config.Driver` 分支：

```go
func New(cfg Config, opts ...Option) (Store, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Driver {
	case "local":
		return local.New(cfg.Local, opts...)
	case "s3":
		return s3.New(cfg.S3, opts...)
	case "oss":
		return oss.New(cfg.OSS, opts...)
	case "cos":
		return cos.New(cfg.COS, opts...)
	default:
		return nil, fmt.Errorf("storage: unknown driver %q", cfg.Driver)
	}
}
```

这样做的代价是 `storage` 包必须 import 四个 driver，无法只编译其中一个。若希望“只编译选中的驱动”，调用方直接 import 具体 driver 包并调用 `s3.New`，根包 `storage.New` 作为便利入口。这是有意的取舍：便利入口依赖全量 driver，精细控制走子包。

### 3.7 文档与示例

- 每个包必须有 `doc.go` 或包注释，说明职责、默认值、并发安全、关闭语义。
- 每个包至少一个 `example_test.go`，示例必须可编译、可运行（不依赖外部服务时用 fake）。
- `examples/quickstart` 是独立 module，通过 `replace` 指向仓库根，CI 编译验证。
- README 的模块表必须标注稳定性：`stable`、`beta`、`experimental`。

---

## 4. 通用契约

这一节定义所有包共同遵守的规则。后续章节只写差异，不重复。

### 4.1 Config

每个组件暴露一个强类型 `Config`：

```go
type Config struct {
	Addr        string        `yaml:"addr" default:"127.0.0.1:6379"`
	Password    string        `yaml:"password"`
	DB          int           `yaml:"db"`
	DialTimeout time.Duration `yaml:"dial_timeout" default:"2s"`
	PoolSize    int           `yaml:"pool_size" default:"20"`
}

// Validate 只做静态检查，不连接外部服务。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("redis: addr is required")
	}
	if c.PoolSize < 0 {
		return errors.New("redis: pool_size must not be negative")
	}
	return nil
}

func (c Config) withDefaults() Config { /* 显式补默认值，不依赖 init */ }
```

约定：

1. `Config` 字段使用 `yaml` tag，便于业务直接嵌入自己的配置结构。
2. 默认值由 `withDefaults()` 补，**不使用 `init()`，也不依赖第三方 default 库**，避免隐式行为。
3. `Validate()` 不发起网络请求、不读文件、不读环境变量。
4. 构造函数顺序固定：补默认值 → `Validate` → 建立资源 → 返回。
5. 连接类组件在 `New` 中做一次带超时的 `Ping`（可配置 `SkipPing`），避免“启动成功、首次请求才失败”。

### 4.2 构造、关闭与并发安全

统一形态：

```go
type Client struct {
	cfg    Config
	log    *slog.Logger
	raw    *redis.Client

	mu     sync.RWMutex
	closed bool
}

func New(cfg Config, opts ...Option) (*Client, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := options{log: logx.Discard()}
	for _, opt := range opts {
		if opt != nil {
			if err := opt(&o); err != nil {
				return nil, err
			}
		}
	}
	// 创建底层资源...
	return &Client{cfg: cfg, log: o.log}, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.raw.Close()
}

func (c *Client) check(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return ErrClosed
	}
	return ctx.Err()
}
```

规则：

- 所有导出类型**并发安全**，除非注释明确写“非并发安全”。
- `Close` 幂等，第二次返回 `nil`；关闭后调用返回 `ErrClosed`。
- 不在 `Close` 中执行无限等待；需要排空的组件（`queue.Consumer`、`events.Bus`）接受 `Close(ctx)`，由 context 控制排空上限。
- 不提供 `Reopen`；需要重建时由调用方重新 `New`。
- 不把 `*sql.DB`、`*gorm.DB` 的关闭责任藏在组件内部；`db.DB` 不拥有 GORM 连接。

### 4.3 日志

统一使用 `log/slog`：

```go
package logx

// 未传 logger 时使用丢弃实现，绝不使用 slog.Default()。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

- 组件的 `Option` 提供 `WithLogger(*slog.Logger)`。
- 日志字段固定：`component`、`op`、`key`（必要时 Hash）、`duration`、`error`。
- 敏感字段统一脱敏：`password`、`token`、`secret`、`authorization`、`cookie`、`set-cookie`、`access_key`、`private_key`。
- 手机号只保留前 3 后 4；邮箱只保留首字符与域名；对象存储签名 URL 去掉 query。
- 请求/响应 body 只在 `httpclient` 的调试日志中出现，且截断到 2 KiB，先脱敏再截断。
- 日志不打印完整 DSN；DSN 中的密码在 `String()` 中替换为 `***`。

gofy 使用 zap。两个日志栈并存不是理想状态，过渡方案：

1. 业务在 `main` 中创建 zap logger 后，另外创建一个 `slog` logger 传给组件库。
2. 或使用 `slog.New(slogHandlerFromZap(zapLogger))` 适配（适配器放业务侧或 gofy 后续版本）。
3. 组件库不 import zap，避免把日志实现强加给非 gofy 用户。

### 4.4 错误模型

组件库**不依赖** `gofy/apperr`，也不依赖契约 `errcode`。每个包定义自己的哨兵错误，调用方用 `errors.Is` 判断，再映射为业务错误码。

```go
var (
	ErrNilContext  = errors.New("gofy-library: nil context")
	ErrClosed      = errors.New("gofy-library: component is closed")
	ErrNotFound    = errors.New("gofy-library: not found")
	ErrConflict    = errors.New("gofy-library: conflict")
	ErrTimeout         = errors.New("gofy-library: timeout")
	ErrUnavailable     = errors.New("gofy-library: unavailable")
	ErrInvalid         = errors.New("gofy-library: invalid argument")
	ErrUnauthorized    = errors.New("gofy-library: unauthorized")
	ErrForbidden       = errors.New("gofy-library: forbidden")
	ErrTooManyRequests = errors.New("gofy-library: too many requests")
)
```

共享哨兵放在 `internal/errs`，各包 re-export：

```go
var (
	ErrClosed   = errs.ErrClosed
	ErrNotFound = errs.ErrNotFound
)
```

错误包装规则：

1. 使用 `%w` 保留链，禁止只返回字符串。
2. 对外错误包含“组件 + 操作 + 关键标识”，不包含凭据和完整请求体。
3. 底层驱动错误可以保留在链中，但日志只打一次，避免重复。
4. 可重试性通过 `errors.Is` 或内部分类函数判断，不靠字符串匹配业务错误。
5. 驱动错误码（MySQL 2006/2013、Redis `READONLY`、HTTP 429/503）在组件内部转换为哨兵或 `retry.Permanent`。

### 4.5 context 与超时

- 所有 I/O 方法第一个参数是 `ctx context.Context`；`nil` 直接返回 `ErrNilContext`。
- 组件不创建后台 context（不用 `context.Background()` 做请求），只在需要独立生命周期时从调用方接收。
- 默认超时只作为兜底：连接 2s、单次读 3s、单次写 3s；调用方可通过 `Config` 或 `context.WithTimeout` 覆盖。
- 长驻循环（队列消费、Nacos 监听、异步事件）以 `ctx.Done()` 作为退出信号，退出时先停止取新任务，再在 `Close(ctx)` 的期限内排空。
- 不把 `ctx` 存进结构体字段。

### 4.6 重试语义

统一的“重连 ≠ 重放”规则：

| 场景 | 重建连接 | 重放 | 说明 |
| --- | --- | --- | --- |
| Redis 只读命令（GET/MGET/HGET/LRANGE/ZRANGE…） | 是 | 最多 1 次 | 命令必须在白名单内，且不在 MULTI/PIPELINE/WATCH 中 |
| Redis 写命令（SET/INCR/LPUSH/EVAL…） | 是 | 否 | 网络错误时不重放，返回错误由业务决定 |
| Redis SCAN/XREAD/SUBSCRIBE | 是 | 否 | 游标/位点/订阅状态不可安全重放 |
| MySQL 普通 SELECT（自动提交） | 是 | 最多 1 次 | 不含 `FOR UPDATE`、`INTO OUTFILE`、`WITH`、`CALL` |
| MySQL 事务内任何语句 | 否 | 否 | 事务已不可信，返回错误并回滚 |
| MySQL 写语句/DDL | 是 | 否 | 只重建连接，不重放 |
| HTTP GET/HEAD/OPTIONS | 是 | 可配置 | 幂等方法默认重试 2 次 |
| HTTP POST/PATCH | 是 | 仅显式开启 | 需要 `Idempotency-Key` 或业务确认幂等 |
| AMQP publish（confirm 模式） | 是 | 可重试 | 可能重复投递，消费方必须幂等 |
| Kafka produce | 是 | 由客户端决定 | 至少一次；不做应用层盲目重试 |

内部实现：

```go
package retry

type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Jitter      float64
}

var ErrPermanent = errors.New("retry: permanent")

// Permanent 标记不可重试错误。
func Permanent(err error) error { return fmt.Errorf("%w: %v", ErrPermanent, err) }

func Do(ctx context.Context, p Policy, fn func(context.Context) error) error
```

`Do` 只负责退避与上下文取消，是否重试由调用方传入的 `fn` 错误决定；`ErrPermanent` 立即终止。

### 4.7 健康检查

```go
package health

type Checker interface {
	Check(ctx context.Context) error
}

type CheckFunc func(ctx context.Context) error

func (f CheckFunc) Check(ctx context.Context) error { return f(ctx) }

type Registry struct {
	timeout time.Duration
	mu      sync.RWMutex
	checks  map[string]Check
}

type Check struct {
	Checker  Checker
	Timeout  time.Duration
	Critical bool
}

func NewRegistry(timeout time.Duration) *Registry
func (r *Registry) Register(name string, c Check, opts ...Option) error
func (r *Registry) Check(ctx context.Context) Report

type Report struct {
	Status string                 // ok | degraded | down
	Checks map[string]CheckResult
}

type CheckResult struct {
	Status  string
	Latency time.Duration
	Error   string
}
```

- 检查并行执行，单个检查有超时。
- `Critical` 检查失败 → `down`；非关键失败 → `degraded`。
- 组件统一暴露 `Ping(ctx)`；用 `health.CheckFunc(rdb.Ping)` 适配为 `Checker`。对象存储用 `Check(ctx)` 探测 bucket 可访问，Nacos 用查询实例探活。
- 业务把 `Registry` 适配到 gofy 的健康端点，不要求 gofy 依赖组件库。

### 4.8 时钟与可测试性

```go
package clock

type Clock interface {
	Now() time.Time
}

type System struct{}
func (System) Now() time.Time { return time.Now() }

type Fake struct {
	mu sync.Mutex
	t  time.Time
}
func NewFake(t time.Time) *Fake
func (f *Fake) Now() time.Time
func (f *Fake) Advance(d time.Duration)
func (f *Fake) Set(t time.Time)
```

需要时间的组件（`idgen`、`lock` 续期、`ratelimit` 本地测试、`jwt` 校验）接受 `WithClock`。生产默认 `clock.System{}`，测试用 `Fake`，避免 `time.Sleep`。

### 4.9 负载版本与兼容性

组件产生的持久化负载（缓存值、加密信封、队列消息、延迟任务）必须自带版本：

| 负载 | 版本方式 |
| --- | --- |
| 缓存值 | JSON 对象包含 `v` 字段；或 key 前缀带版本 `cache:v1:` |
| 加密信封 | `v1:<kid>:<nonce>:<ciphertext>` |
| 队列消息 | 消息头 `x-schema-version`；body 为 JSON |
| 延迟任务 | 与队列消息同一结构，额外 `due_at` |
| 分布式 ID | 纯十进制字符串，不解析内部位布局；布局变更通过新前缀区分 |

不得依赖 PHP 的 `serialize()`、PHP 数组顺序、`DateTime` 字符串格式。跨语言场景一律 JSON + 明确字段名 + 秒/毫秒单位后缀。

---

## 5. 数据组件

### 5.1 `db`：GORM 薄封装

#### 5.1.1 定位

`db` 不创建连接、不管理连接池、不替代 GORM。它只补齐 swoolefy `Db` 模块中被业务反复使用的语义：

- 事务边界与 panic 回滚；
- 分页与游标；
- 失败关闭的多租户过滤；
- 只读查询的连接失败重放；
- 软删除辅助；
- GORM 日志适配 `slog`；
- 批量写入。

连接创建仍由 `gofy/support/mysql` 或业务自己的 `gorm.Open` 负责：

```go
gdb, err := mysql.New(ctx, mysqlCfg, logger)
if err != nil {
	return err
}
database, err := db.New(gdb, db.Config{
	DefaultPageSize: 20,
	MaxPageSize:     200,
	Tenant: &db.TenantConfig{
		Column:   "tenant_id",
		Tables:   []string{"orders", "order_items"},
		Required: true,
	},
})
if err != nil {
	return err
}
```

#### 5.1.2 API

```go
package db

type Config struct {
	DefaultPageSize int
	MaxPageSize     int
	SlowThreshold   time.Duration
	Tenant          *TenantConfig
}

type DB struct {
	gdb   *gorm.DB
	cfg   Config
	inTx  bool
}

func New(gdb *gorm.DB, cfg Config) (*DB, error)
func (d *DB) GORM() *gorm.DB
func (d *DB) WithContext(ctx context.Context) *DB

func (d *DB) Tx(ctx context.Context, fn func(tx *DB) error, opts ...*sql.TxOptions) error
func (d *DB) Read(ctx context.Context, fn func(tx *DB) error) error

func (d *DB) Ping(ctx context.Context) error
```

`db.DB` 不拥有底层 GORM 连接，因此没有 `Close`；连接由 `mysql.Close(gdb)` 或创建方关闭。

`GORM()` 用于需要 GORM 原生能力的场景。一旦使用原生链式调用，分页、租户、只读重试等封装不再自动生效，调用方需自行保证。

#### 5.1.3 事务

```go
func (d *DB) Tx(ctx context.Context, fn func(tx *DB) error, opts ...*sql.TxOptions) (err error) {
	if ctx == nil {
		return errs.ErrNilContext
	}
	if d.inTx {
		// 嵌套事务：GORM 在已有事务上创建 SAVEPOINT。
		tx := d.gdb.WithContext(ctx).Begin(opts...)
		if tx.Error != nil {
			return tx.Error
		}
		defer func() {
			if r := recover(); r != nil {
				_ = tx.Rollback().Error
				panic(r)
			}
			if err != nil {
				_ = tx.Rollback().Error
			}
		}()
		if err = fn(&DB{gdb: tx, cfg: d.cfg, inTx: true}); err != nil {
			return err
		}
		return tx.Commit().Error
	}

	tx := d.gdb.WithContext(ctx).Begin(opts...)
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback().Error
			panic(r)
		}
		if err != nil {
			_ = tx.Rollback().Error
		}
	}()
	if err = fn(&DB{gdb: tx, cfg: d.cfg, inTx: true}); err != nil {
		return err
	}
	return tx.Commit().Error
}
```

语义：

1. `fn` 返回错误 → 回滚；返回 `nil` → 提交。
2. `fn` panic → 回滚后重新 panic，由上层 recovery 处理。
3. 嵌套调用使用 SAVEPOINT；内层回滚不影响外层已执行语句，但外层仍可继续。
4. 事务内不做只读重放。连接断开后事务不可信，直接返回错误。
5. 事务内禁止启动独立 goroutine 使用同一个 `*DB`；GORM 事务绑定单连接，不并发安全。
6. `ctx` 取消时 GORM/SQL 驱动应中断；若驱动不支持，`Tx` 在进入 `fn` 前检查一次，提交前再检查一次。

#### 5.1.4 只读重放

```go
func (d *DB) Read(ctx context.Context, fn func(tx *DB) error) error
```

契约：

- `fn` **必须是只读操作**，不得包含 `Create/Update/Delete/Exec`；这是 API 契约，代码评审与测试共同保证。
- `d.inTx == true` 时直接执行，不重试。
- 仅当错误被 `classifyConnErr` 判定为连接中断时重放一次。
- 重放前通过 `gdb.DB().PingContext` 或直接重试确认连接可用；仍失败则返回原始错误。
- 不重放 `SELECT ... FOR UPDATE`、`WITH`、`CALL`、`EXPLAIN`、`INTO OUTFILE`；这些语句通过 `fn` 内的 GORM 子句无法可靠识别，因此 `Read` 只用于普通查询，复杂语句直接 `GORM().Raw`。

连接错误识别：

```go
func classifyConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 2006 || me.Number == 2013
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
```

死锁（1213、40001）**不重试**。死锁应由业务重新执行整个事务，而不是在语句级盲目重放。

#### 5.1.5 分页

```go
type PageRequest struct {
	Page     int
	PageSize int
}

func (r PageRequest) Normalize(def, max int) PageRequest

type PageResult[T any] struct {
	Total    int64
	List     []T
	Page     int
	PageSize int
	HasMore  bool
}

func Paginate[T any](ctx context.Context, q *gorm.DB, req PageRequest, cfg Config) (PageResult[T], error)
```

实现要点：

1. `PageSize <= 0` 用默认值；`PageSize > max` 截断；`Page <= 0` 归一为 1。
2. 先 `Count` 再查询；`Count` 使用 `Session(&gorm.Session{})` 克隆，避免污染原语句。
3. 查询失败返回空结果与错误，不返回半成品。
4. `HasMore` 由 `page*pageSize < total` 计算。
5. 返回的 `PageResult` 是组件内部结果；业务映射到契约 `paging.PageData[T]`，不要直接把 `db.PageResult` 当 API 响应。

游标分页用于大表或深分页：

```go
func Cursor[T any](ctx context.Context, q *gorm.DB, after uint64, limit int) (items []T, next uint64, err error)
```

默认按主键 `id` 升序，`after == 0` 表示第一页；`next` 为本次最后一条的 ID。复合游标（时间 + ID）不在 v1，业务可传自定义 `*gorm.DB` 自行实现。

#### 5.1.6 多租户

PHP 侧通过元数据判断表是否有 `tenant_id`，并失败关闭。Go 侧无法在每次查询时廉价读取表结构，因此改为**显式表清单 + 失败关闭**：

```go
type TenantConfig struct {
	Column   string
	Tables   []string
	Required bool
}

type tenantKey struct{}
type bypassKey struct{}

func WithTenant(ctx context.Context, tenantID string) context.Context
func TenantID(ctx context.Context) (string, bool)
func WithoutTenant(ctx context.Context) context.Context
```

GORM 回调在 `db.New` 时注册到该 `*gorm.DB` 实例：

```text
Query.Before("gorm:query")   -> inject tenant_id = ?
Create.Before("gorm:create") -> inject tenant_id
Update.Before("gorm:update") -> inject tenant_id
Delete.Before("gorm:delete") -> inject tenant_id
```

规则：

1. 只有 `TenantConfig.Tables` 中列出的表会被注入。
2. `Required == true` 且 context 中没有 tenant ID → 返回 `ErrTenantMissing`，查询不执行。
3. `WithoutTenant(ctx)` 是唯一显式绕过方式，必须用于平台级查询；审计日志记录调用点。
4. `Raw` / `Exec` 不经过回调，SQL 必须手写租户条件；提供 `db.HasTenantPredicate(ctx, sql)` 仅作为辅助检查，不保证正确。
5. 表名匹配使用大小写不敏感的完整表名；视图、临时表不自动注入。
6. 多租户列不参与索引设计讨论；组件只负责过滤，不负责建索引。

#### 5.1.7 软删除与批量写

```go
func WithTrashed(q *gorm.DB) *gorm.DB
func OnlyTrashed(q *gorm.DB) *gorm.DB
func Restore[T any](ctx context.Context, q *gorm.DB, id any) error

func BatchInsert[T any](ctx context.Context, q *gorm.DB, rows []T, batchSize int) error
```

- 软删除依赖模型字段 `gorm.DeletedAt`；组件不自定义字段名。
- `Restore` 使用 `Unscoped().Model(&T{}).Where("id = ?", id).Update("deleted_at", nil)`，必须确认模型确实有该列。
- `BatchInsert` 调用 `CreateInBatches`；`batchSize <= 0` 默认 500；空切片直接返回 `nil`。

#### 5.1.8 日志与慢查询

```go
func NewGormLogger(log *slog.Logger, cfg Config) gormlogger.Interface
```

- 慢查询阈值来自 `Config.SlowThreshold`，默认 200ms。
- 日志级别：慢查询 `Warn`，错误 `Error`，普通 SQL `Debug`。
- SQL 参数脱敏：`password`、`token`、`secret`、`phone`、`id_card` 字段值替换为 `***`。
- 不打印完整结果集。

---

### 5.2 `redis`：连接与只读重放

#### 5.2.1 Config

```go
type Config struct {
	Mode       string        `yaml:"mode"`        // single|sentinel|cluster
	Addrs      []string      `yaml:"addrs"`
	MasterName string        `yaml:"master_name"` // sentinel
	Username   string        `yaml:"username"`
	Password   string        `yaml:"password"`
	DB         int           `yaml:"db"`
	TLS        *tls.Config   `yaml:"-"`

	DialTimeout  time.Duration `yaml:"dial_timeout" default:"2s"`
	ReadTimeout  time.Duration `yaml:"read_timeout" default:"3s"`
	WriteTimeout time.Duration `yaml:"write_timeout" default:"3s"`

	PoolSize     int `yaml:"pool_size" default:"20"`
	MinIdleConns int `yaml:"min_idle_conns" default:"2"`

	KeyPrefix string `yaml:"key_prefix"`
	SkipPing  bool   `yaml:"skip_ping"`
}
```

- `Mode` 为空时根据 `Addrs` 长度推断：1 个 → single，多个 + `MasterName` → sentinel，多个无 `MasterName` → cluster。
- 生产环境必须显式设置 `Mode`，避免部署变化导致客户端类型漂移。
- `PoolSize <= 0` 使用默认值；`MinIdleConns` 不得大于 `PoolSize`。
- `KeyPrefix` 只影响本库提供的 `Key()` 与内建组件，不影响 `Raw()`。

#### 5.2.2 API

```go
// 包内以 goredis 别名导入 go-redis，对外 re-export 类型别名，
// 避免业务为了使用 Raw()/Nil 再 import 一次 go-redis。
type Cmder = goredis.Cmder
type UniversalClient = goredis.UniversalClient
type Script = goredis.Script

type Client struct {
	cfg Config
	log *slog.Logger
	raw UniversalClient
}

func New(cfg Config, opts ...Option) (*Client, error)
func (c *Client) Raw() UniversalClient
func (c *Client) Ping(ctx context.Context) error
func (c *Client) Close() error

func (c *Client) Key(parts ...string) string
func (c *Client) Do(ctx context.Context, cmd Cmder) Cmder
func (c *Client) DoRead(ctx context.Context, cmd Cmder) Cmder

func NewScript(src string) *Script
var Nil = goredis.Nil
```

- `New` 内部把 go-redis 的 `MaxRetries` 设为 `-1`（关闭客户端内建重试；在 go-redis 中 `0` 表示使用默认 3 次）。原因：默认策略会在网络错误时重试任意命令，可能重放 `INCR`、`LPUSH` 等非幂等写操作。
- 重连由 go-redis 连接池在下次取连接时自动完成，不需要手动 `Reconnect`。
- `DoRead` 只在命令名命中只读白名单时重放，最多 1 次；`Do` 永不重放。
- `Raw()` 返回原生客户端，调用方自担重试风险；README 必须显著提示。
- `Nil` 直接 re-export，业务不需要同时 import go-redis。
- `NewScript` 返回 go-redis 的脚本对象，内部自动 `EVALSHA`，遇到 `NOSCRIPT` 回退 `EVAL`。

只读命令白名单：

| 类别 | 命令 |
| --- | --- |
| String | `GET`、`MGET`、`STRLEN`、`GETRANGE` |
| Hash | `HGET`、`HMGET`、`HGETALL`、`HKEYS`、`HVALS`、`HLEN`、`HEXISTS` |
| List | `LLEN`、`LRANGE`、`LINDEX` |
| Set | `SMEMBERS`、`SISMEMBER`、`SCARD`、`SRANDMEMBER` |
| ZSet | `ZRANGE`、`ZREVRANGE`、`ZSCORE`、`ZCARD`、`ZCOUNT`、`ZRANK` |
| 通用 | `EXISTS`、`TYPE`、`TTL`、`PTTL`、`PING`、`ECHO` |

明确排除：

- `SCAN` 系列：游标不可安全重放；
- `XREAD` / `XREADGROUP`：位点与 pending 状态不可重放；
- `SUBSCRIBE` / `PSUBSCRIBE`：订阅状态不可重放；
- `EVAL` / `EVALSHA` / `FCALL`：脚本可能写数据；
- `MULTI` / `EXEC` / `WATCH`：事务状态不可重放；
- 所有写命令与未知命令。

#### 5.2.3 集群与故障转移

- Cluster：`redis.NewClusterClient`，`Addrs` 为种子节点；`DB` 必须为 0。
- Sentinel：`redis.NewFailoverClient`，`MasterName` 必填；`DB` 允许非 0。
- Single：`redis.NewClient`。
- `TLS != nil` 时所有模式都启用 TLS；禁止 `InsecureSkipVerify` 出现在组件代码中，只能由调用方显式传入。
- 集群模式下 Lua 脚本涉及的所有 key 必须落在同一 slot；组件内部 key 统一使用 `{prefix}:...`，需要多 key 原子操作时由组件生成 hash tag，例如 `{ratelimit}:key`。

---

### 5.3 `cache`：JSON 缓存

```go
type Config struct {
	Prefix        string        `yaml:"prefix"`
	DefaultTTL    time.Duration `yaml:"default_ttl" default:"5m"`
	MaxValueBytes int           `yaml:"max_value_bytes" default:"1048576"`
	NotFoundTTL   time.Duration `yaml:"not_found_ttl"` // 0 表示不缓存未命中
}

type Cache struct{ /* redis client, json codec, singleflight */ }

func New(client *redis.Client, cfg Config, opts ...Option) *Cache

func (c *Cache) Get(ctx context.Context, key string, dst any) (bool, error)
func (c *Cache) Set(ctx context.Context, key string, value any, ttl time.Duration) error
func (c *Cache) Delete(ctx context.Context, keys ...string) error
func (c *Cache) Exists(ctx context.Context, key string) (bool, error)
func (c *Cache) TTL(ctx context.Context, key string) (time.Duration, error)

func (c *Cache) GetMulti(ctx context.Context, keys []string) (map[string]json.RawMessage, error)
func (c *Cache) SetMulti(ctx context.Context, values map[string]any, ttl time.Duration) error

func (c *Cache) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (any, error), dst any) (bool, error)
```

约定：

1. 编码统一 JSON；写入前检查序列化后大小，超过 `MaxValueBytes` 返回 `ErrValueTooLarge`。
2. `Get` 未命中返回 `(false, nil)`，不把 `redis.Nil` 暴露给业务。
3. `Get` 解码失败视为缓存损坏：删除该 key，返回 `ErrCorrupt`；是否回源由 `GetOrLoad` 决定。
4. `GetOrLoad` 使用进程内 `singleflight` 合并同 key 并发回源；跨进程不合并。
5. `NotFoundTTL > 0` 时，`load` 返回 `errs.ErrNotFound` 会写入一个短 TTL 的占位值，防止缓存穿透；其他错误不缓存。
6. `SetMulti` 使用 pipeline 逐条 `SET EX`，不使用 `MSET`，以保证每条都有 TTL。
7. key 由调用方提供业务前缀，`Config.Prefix` 再统一加一层；最终 key 形如 `{prefix}:{key}`。
8. 不实现 PHP `serialize()` 兼容；缓存内容不跨语言共享。

---

### 5.4 `lock`：Redis 互斥锁

```go
type Config struct {
	Prefix        string        `yaml:"prefix"`
	DefaultTTL    time.Duration `yaml:"default_ttl" default:"10s"`
	RetryInterval time.Duration `yaml:"retry_interval" default:"50ms"`
}

type Locker struct{ /* redis client */ }
type Lock struct{ /* key, token, ttl, client */ }

func New(client *redis.Client, cfg Config, opts ...Option) *Locker

func (l *Locker) TryAcquire(ctx context.Context, name string, ttl time.Duration) (*Lock, bool, error)
func (l *Locker) Acquire(ctx context.Context, name string, ttl time.Duration) (*Lock, error)
func (l *Locker) WithLock(ctx context.Context, name string, ttl time.Duration, fn func(context.Context) error) error

func (l *Lock) Renew(ctx context.Context, ttl time.Duration) error
func (l *Lock) Release(ctx context.Context) error
func (l *Lock) KeepAlive(ctx context.Context, interval time.Duration) (stop func())
```

语义：

1. `SET key token NX PX ttl`，token 为 16 字节 `crypto/rand` 的 hex。
2. 释放使用 Lua：`if GET(key) == token then DEL(key) end`，绝不无条件 `DEL`。
3. `Renew` 同样使用 Lua 比较 token 后 `PEXPIRE`；token 不匹配返回 `ErrNotHeld`。
4. `Acquire` 在 `ctx` 截止前按 `RetryInterval` 重试；`ctx` 取消返回 `ctx.Err()`。
5. `ttl <= 0` 直接报错；不建议使用无限租约。
6. `KeepAlive` 启动一个 goroutine 定期续期，`stop()` 或 `ctx` 取消时停止；续期失败时停止并在日志中记录，不 panic。
7. `WithLock` 使用 `defer` 释放，`fn` panic 时也会尝试释放；释放失败只记录日志，不覆盖原 panic。
8. 不实现 Redlock。单 Redis 主从切换期间可能短暂出现双持有者；对强一致场景，业务应使用数据库行锁或 fencing token。
9. 锁名由调用方提供语义化名称，组件加 `{prefix}:lock:` 前缀。

---

### 5.5 `ratelimit`：滑动窗口与令牌桶

```go
type FailureMode string

const (
	FailClosed FailureMode = "closed"
	FailOpen   FailureMode = "open"
)

type Config struct {
	Prefix      string        `yaml:"prefix"`
	FailureMode FailureMode   `yaml:"failure_mode" default:"closed"`
	KeyTTL      time.Duration `yaml:"key_ttl" default:"10m"`
}

type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	ResetAt    time.Time
}

type SlidingWindow struct{ /* redis, limit, window */ }
type TokenBucket struct{ /* redis, capacity, rate */ }

func NewSlidingWindow(client *redis.Client, cfg Config, limit int, window time.Duration) (*SlidingWindow, error)
func NewTokenBucket(client *redis.Client, cfg Config, capacity int, rate float64) (*TokenBucket, error)

func (l *SlidingWindow) Allow(ctx context.Context, key string, n int) (Result, error)
func (b *TokenBucket) Allow(ctx context.Context, key string, n int) (Result, error)
```

实现要点：

1. 时间基准使用 Redis `TIME`，避免多实例时钟漂移。
2. 滑动窗口使用 ZSET：`ZREMRANGEBYSCORE` 清理窗口外成员，`ZCARD` 计数，`ZADD` 记录本次；member 使用 `{score}:{随机/序号}` 防止同毫秒冲突；`EXPIRE` 设为窗口的 2 倍。
3. 令牌桶使用 HASH 保存 `tokens`、`timestamp`；按微秒精度补充令牌，桶初始为满；失败请求也刷新补充状态。
4. `RetryAfter` 为下一次可放行所需时间，可直接写入 HTTP `Retry-After`。
5. `FailureMode`：
   - `closed`：Redis 不可用时拒绝请求，并返回 `ErrUnavailable`。登录、短信、支付等安全场景必须使用。
   - `open`：Redis 不可用时放行，同时记录 `Warn` 日志与指标。仅用于非关键限流。
6. `n <= 0` 视为非法参数；`n > limit` 直接拒绝，不消耗计数。
7. 集群模式下所有 key 使用 hash tag，保证 Lua 单 slot 执行。
8. 不在组件内做本地缓存；本地令牌桶会破坏多实例一致性。

---

### 5.6 `idgen`：分布式 ID

```go
type Generator interface {
	Next(ctx context.Context) (string, error)
	NextN(ctx context.Context, n int) ([]string, error)
}

type RedisConfig struct {
	Key    string        `yaml:"key"`
	TTL    time.Duration `yaml:"ttl" default:"24h"`
	Prefix string        `yaml:"prefix"`
}

type SnowflakeConfig struct {
	NodeID int64 `yaml:"node_id"`
	Epoch  time.Time
}

func NewRedis(client *redis.Client, cfg RedisConfig, opts ...Option) (*RedisGenerator, error)
func NewSnowflake(cfg SnowflakeConfig, opts ...Option) (*Snowflake, error)
```

Redis 方案：

- Lua 脚本原子执行：读取毫秒时间戳，`INCR` 当前毫秒 key，超过每毫秒上限时自旋到下一毫秒；返回 `{timestamp, seq}`。
- 输出 `timestamp*1000 + seq` 或带固定前缀的十进制字符串；保证同一进程/同一毫秒单调递增。
- key 过期时间远大于业务保留期；时间戳回拨时以 Redis 时间与上次值较大者为准。
- 失败重试 3 次，仍失败返回错误；**不返回可能重复的降级 ID**。

Snowflake 方案：

- 41 位毫秒时间戳 + 10 位节点 + 12 位序列；epoch 由配置固定，不能随意变更。
- 时钟回拨策略：
  - `Reject`：直接返回 `ErrClockRollback`（默认）；
  - `Wait`：等待回拨窗口过去，最多等待 `MaxBackoff`。
- 节点号必须唯一；组件无法自动保证，部署时通过环境或配置注入。
- 输出为十进制字符串，避免 JavaScript 精度丢失；不要解析内部位布局。

不提供 UUID v4 包装。需要 UUID 时直接使用 `github.com/google/uuid`，那是另一个语义。

---

## 6. 消息组件

### 6.1 `queue`：基于 Redis Streams 的可靠队列

#### 6.1.1 为什么不沿用 List + BRPOP

swoolefy 的 `Queues` 使用 List（即时）和 ZSET（延迟），`pop` 即删除。优点是简单，缺点是：

- 消费者崩溃时消息直接丢失；
- 没有 ack、pending、认领机制；
- 重试次数耗尽后静默丢弃，没有死信；
- 无法水平扩展消费者组。

Go 侧改用 Redis Streams：

- `XADD` 写入，`XREADGROUP` 消费；
- 处理成功 `XACK`；
- 失败进入延迟 ZSET，到期重新入流；
- 超过最大次数进入死信流；
- `XAUTOCLAIM` 回收崩溃消费者的 pending 消息；
- 多消费者共享 group，天然分流。

语义为**至少一次**，处理器必须幂等。

#### 6.1.2 API

```go
type Message struct {
	ID         string
	Stream     string
	Body       []byte
	Headers    map[string]string
	Attempts   int64
	EnqueuedAt time.Time
}

type Handler func(ctx context.Context, msg *Message) error

type ProducerConfig struct {
	Stream      string `yaml:"stream"`
	MaxLen      int64  `yaml:"max_len"`       // 0 表示不裁剪
	Approximate bool   `yaml:"approximate"`   // XADD MAXLEN ~
	Prefix      string `yaml:"prefix"`
}

type ConsumerConfig struct {
	Stream         string        `yaml:"stream"`
	Group          string        `yaml:"group"`
	Consumer       string        `yaml:"consumer"`
	Block          time.Duration `yaml:"block" default:"5s"`
	Count          int           `yaml:"count" default:"10"`
	MaxAttempts    int           `yaml:"max_attempts" default:"5"`
	RetryBaseDelay time.Duration `yaml:"retry_base_delay" default:"5s"`
	RetryMaxDelay  time.Duration `yaml:"retry_max_delay" default:"5m"`
	PendingIdle    time.Duration `yaml:"pending_idle" default:"60s"`
	DLQStream      string        `yaml:"dlq_stream"`
	Prefix         string        `yaml:"prefix"`
}

type Producer struct{ /* redis client, config */ }
type Consumer struct{ /* redis client, config */ }

func NewProducer(client *redis.Client, cfg ProducerConfig, opts ...Option) (*Producer, error)
func NewConsumer(client *redis.Client, cfg ConsumerConfig, opts ...Option) (*Consumer, error)

func (p *Producer) Add(ctx context.Context, body []byte, opts ...AddOption) (string, error)
func (p *Producer) AddDelay(ctx context.Context, body []byte, at time.Time, opts ...AddOption) (string, error)
func (p *Producer) AddJSON(ctx context.Context, v any, opts ...AddOption) (string, error)

func (c *Consumer) Run(ctx context.Context, handler Handler) error
func (c *Consumer) Close() error

// SkipRetry 标记“直接进死信、不消耗剩余重试次数”的错误。
func SkipRetry(err error) error
func IsSkipRetry(err error) bool
```

`AddOption`：`WithHeader(k, v)`、`WithMaxLen(n)`。延迟由 `AddDelay` 单独表达，避免同一个入口有两种时间语义。`AddJSON` 使用 `json.Marshal`，不写 PHP 序列化格式。

#### 6.1.3 消费循环

```text
1. XGROUP CREATE stream group $ MKSTREAM（已存在则忽略 BUSYGROUP）
2. 启动延迟调度 goroutine：ZRANGEBYSCORE due <= now → XADD stream → ZREM（Lua 原子）
3. 循环：
   a. XAUTOCLAIM group consumer min-idle-time=PendingIdle COUNT=Count
      - 回收到的消息读取 attempts 字段，以 attempts+1 交给 handler
   b. XREADGROUP GROUP group consumer COUNT=Count BLOCK=Block STREAMS stream >
   c. 对每条消息：
      - handler(ctx, msg)
      - 成功：XACK
      - 失败：attempts+1
          - attempts >= MaxAttempts：XADD DLQStream（保留原消息头与失败原因）→ XACK
          - 否则：ZADD delay due=now+backoff(attempts) → XACK
4. ctx.Done()：停止读取；等待当前 handler 返回；返回 ctx.Err()
```

关键点：

1. **先 ack 后处理会丢消息，先处理后 ack 会重复**；本设计选择至少一次。
2. 处理器返回 `queue.SkipRetry(err)` 时直接进死信，不消耗剩余次数。
3. 处理器 panic 时由 `Run` 捕获，按失败处理并记录堆栈摘要；不把 panic 传给进程。
4. `Consumer` 单 goroutine 顺序处理，保证同一消费者内有序；需要吞吐时启动多个 `Consumer`（同一 group、不同 consumer name），不提供包内并发参数，避免破坏顺序语义。
5. `PendingIdle` 必须大于正常处理时长，否则消息会被其他消费者重复认领。
6. 延迟调度使用独立 goroutine，多个消费者同时调度需要 Lua 原子“取出并删除”，避免重复入流。
7. DLQ 消息保留 `x-error`、`x-attempts`、`x-original-stream` 头，便于人工重放。
8. Stream key 与 delay key 使用同一 hash tag：`{queue}:stream`、`{queue}:delay`，保证集群 Lua 单 slot。

#### 6.1.4 配置建议

| 场景 | MaxAttempts | RetryBaseDelay | PendingIdle |
| --- | --- | --- | --- |
| 支付/订单回调 | 8 | 10s | 5m |
| 普通通知 | 3 | 5s | 60s |
| 可丢弃日志 | 1 | — | 30s |

消费者必须实现幂等：用业务唯一键做去重，或让操作天然幂等（例如 `UPDATE status WHERE status = 'pending'`）。

---

### 6.2 `pubsub`：Redis 发布订阅

```go
type Config struct {
	Prefix      string `yaml:"prefix"`
	Concurrency int    `yaml:"concurrency" default:"16"`
}

type Handler func(ctx context.Context, channel string, payload []byte) error

type Stats struct {
	Received   uint64
	Dispatched uint64
	Failed     uint64
}

type Subscriber struct{ /* redis client, config */ }

func NewSubscriber(client *redis.Client, cfg Config, opts ...Option) (*Subscriber, error)
func (s *Subscriber) Subscribe(ctx context.Context, channels []string, h Handler) error
func (s *Subscriber) Stats() Stats
```

语义：

1. **至多一次**。订阅者离线期间的消息不会补发；需要可靠投递请用 `queue` 或 `kafka`。
2. 单个 `Subscribe` 调用独占一个 Redis 连接；不要在同一个 `Subscriber` 上并发调用多个 `Subscribe`。
3. handler 通过有界信号量控制并发；并发满时阻塞读取，形成背压。
4. handler 返回错误时记录日志并继续；连续错误超过阈值可由调用方通过 `ctx` 取消。
5. 不把 Redis 连接或 `PubSub` 对象传给 handler，避免业务误用。
6. `Subscribe` 阻塞直到 `ctx` 取消；返回 `ctx.Err()` 或连接错误。
7. 集群模式下 pub/sub 为广播语义，消息会到达所有节点；业务需自行去重。

---

### 6.3 `amqp`：RabbitMQ

```go
type Config struct {
	URLs          []string      `yaml:"urls"`
	Exchange      string        `yaml:"exchange"`
	ExchangeType  string        `yaml:"exchange_type" default:"direct"`
	Queue         string        `yaml:"queue"`
	RoutingKey    string        `yaml:"routing_key"`
	Durable       bool          `yaml:"durable" default:"true"`
	AutoDelete    bool          `yaml:"auto_delete"`
	Prefetch      int           `yaml:"prefetch" default:"10"`
	Confirm       bool          `yaml:"confirm" default:"true"`
	Heartbeat     time.Duration `yaml:"heartbeat" default:"10s"`
	DialTimeout   time.Duration `yaml:"dial_timeout" default:"5s"`
	ReconnectMin  time.Duration `yaml:"reconnect_min" default:"1s"`
	ReconnectMax  time.Duration `yaml:"reconnect_max" default:"30s"`
	DLQExchange   string        `yaml:"dlq_exchange"`
	DLQQueue      string        `yaml:"dlq_queue"`
	RequeueOnError bool         `yaml:"requeue_on_error"`
	DelayQueue    string        `yaml:"delay_queue"`
	DelayTTL      time.Duration `yaml:"delay_ttl"`
}

type Publisher struct{ /* connection, channel, mutex */ }
type Consumer struct{ /* connection, channel */ }

func NewPublisher(cfg Config, opts ...Option) (*Publisher, error)
func NewConsumer(cfg Config, opts ...Option) (*Consumer, error)

func (p *Publisher) Publish(ctx context.Context, body []byte, opts ...PublishOption) error
func (p *Publisher) PublishJSON(ctx context.Context, v any, opts ...PublishOption) error
func (p *Publisher) Close() error

type Delivery struct {
	Body      []byte
	Headers   map[string]any
	MessageID string
	Timestamp time.Time
	Redelivered bool
}

func (c *Consumer) Run(ctx context.Context, h func(context.Context, Delivery) error) error
func (c *Consumer) Close() error
```

`PublishOption`：`WithRoutingKey(k)`、`WithExpiration(d)`、`WithMessageID(id)`、`WithHeader(k, v)`。

实现要点：

1. 多 URL 按顺序尝试，失败切换下一个；全部失败返回聚合错误。
2. `Publisher` 使用单个 channel + 互斥锁，开启 confirm 模式；`Publish` 等待 `Confirmation`，Nack 或超时返回 `ErrNotConfirmed`。
3. `Consumer` 设置 `Qos(prefetch, 0, false)`；每条消息处理成功后 `Ack`，失败按配置 `Nack(requeue)` 或进 DLQ。
4. `NotifyClose` + 指数退避重连；重连后重新声明 exchange/queue/binding（声明必须幂等）。
5. AMQP channel 不是并发安全的；一个 `Consumer.Run` 使用一个 channel，不要跨 goroutine 复用。
6. 延迟队列通过“TTL 队列 + DLX 转发”实现；`DelayTTL` 是队列级默认值，单条消息可用 `Expiration` 覆盖。
7. 投递语义为至少一次；消费者必须幂等。
8. `context` 取消时停止消费并关闭 channel；正在处理的消息允许完成，最长等待由调用方 `ctx` 控制。

---

### 6.4 `kafka`：生产者与消费组

```go
type ProducerConfig struct {
	Brokers      []string      `yaml:"brokers"`
	Topic        string        `yaml:"topic"`
	BatchTimeout time.Duration `yaml:"batch_timeout" default:"10ms"`
	BatchSize    int           `yaml:"batch_size" default:"100"`
	Compression  string        `yaml:"compression" default:"snappy"` // none|gzip|snappy|lz4|zstd
	RequiredAcks int           `yaml:"required_acks" default:"-1"`   // -1 all
}

type ConsumerConfig struct {
	Brokers        []string      `yaml:"brokers"`
	GroupID        string        `yaml:"group_id"`
	Topics         []string      `yaml:"topics"`
	MinBytes       int           `yaml:"min_bytes" default:"1"`
	MaxBytes       int           `yaml:"max_bytes" default:"10485760"`
	MaxWait        time.Duration `yaml:"max_wait" default:"1s"`
	CommitInterval time.Duration `yaml:"commit_interval"` // 0 表示手动提交
	StartOffset    int64         `yaml:"start_offset" default:"-2"` // -2 earliest, -1 latest
}

type Message struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   map[string]string
	Time      time.Time
}

type Producer struct{ /* kafka.Writer */ }
type Consumer struct{ /* kafka.Reader */ }

func NewProducer(cfg ProducerConfig, opts ...Option) (*Producer, error)
func NewConsumer(cfg ConsumerConfig, opts ...Option) (*Consumer, error)

func (p *Producer) Produce(ctx context.Context, key, value []byte, headers map[string]string) error
func (p *Producer) Close() error

func (c *Consumer) Run(ctx context.Context, h func(context.Context, Message) error) error
func (c *Consumer) Close() error
```

选择 `segmentio/kafka-go` 的原因：纯 Go、无 cgo、`context` 支持完整、部署简单。代价是不支持 Kafka 事务/EOS；需要精确一次语义时，业务必须自己做幂等。

语义：

1. 生产者 `RequiredAcks = -1`（all），同步写入；失败返回错误，不自动重试。
2. 消费者默认手动提交：handler 成功后才 `CommitMessages`；失败不提交，消息会被重新投递。
3. 重平衡期间可能重复投递；handler 必须幂等。
4. 不内置重试 topic / DLQ；文档给出模式：失败消息写入 `{topic}.retry.{attempt}`，超过次数写 `{topic}.dlq`，由独立消费者处理。
5. `Run` 在 `ctx` 取消后停止拉取，等待当前 handler 返回，再关闭 reader。

---

### 6.5 `mongo`：官方驱动薄封装

```go
type Config struct {
	URI            string        `yaml:"uri"`
	Database       string        `yaml:"database"`
	ConnectTimeout time.Duration `yaml:"connect_timeout" default:"3s"`
	SocketTimeout  time.Duration `yaml:"socket_timeout" default:"30s"`
	MaxPoolSize    uint64        `yaml:"max_pool_size" default:"100"`
}

// 包内以 mongodriver 别名导入官方驱动，对外 re-export。
type Collection = mongodriver.Collection
type Database = mongodriver.Database

type Client struct{ /* mongo.Client, database */ }

func New(ctx context.Context, cfg Config, opts ...Option) (*Client, error)
func (c *Client) Collection(name string) *Collection
func (c *Client) Database() *Database
func (c *Client) Ping(ctx context.Context) error
func (c *Client) Close(ctx context.Context) error
```

不提供 ODM、不定义 schema、不做关系映射。只补两个便利点：

1. `ID` 类型：以 24 位 hex 字符串做 JSON 编解码，避免业务在 DTO 与 `primitive.ObjectID` 之间反复转换。

```go
type ID primitive.ObjectID

func ParseID(s string) (ID, error)
func (id ID) MarshalJSON() ([]byte, error)
func (id *ID) UnmarshalJSON(b []byte) error
func (id ID) String() string
func (id ID) ObjectID() primitive.ObjectID
```

2. `IDFilter(id string) bson.M`：把字符串转成 `{"_id": ObjectID}`；非法字符串返回错误，不静默当普通字符串查询。

其余操作直接使用官方驱动的 `*mongo.Collection`，不做二次包装。上下文、超时、重试均由官方驱动与调用方负责。

---

### 6.6 `datahub`：阿里云 DataHub（P2）

不手写签名与 HTTP 客户端，直接封装阿里云官方 Go SDK：

```go
type Config struct {
	Endpoint  string `yaml:"endpoint"`
	Project   string `yaml:"project"`
	Topic     string `yaml:"topic"`
	AccessID  string `yaml:"access_id"`
	AccessKey string `yaml:"access_key"`
}

type Producer struct{ /* datahub client */ }
type Consumer struct{ /* datahub client, shard cursors */ }

func NewProducer(cfg Config, opts ...Option) (*Producer, error)
func NewConsumer(cfg Config, opts ...Option) (*Consumer, error)
```

- 生产者：`PutRecords` 批量写入，按返回的失败记录做有限重试。
- 消费者：按 shard 拉取、处理、提交 offset；提交失败不推进内存位点。
- 位点必须持久化到业务可恢复的位置；组件只提供 `Checkpoint` 回调。
- 该模块优先级低；在正式使用前先确认官方 SDK 的维护状态与认证方式。

---

## 7. 安全与身份组件

### 7.1 `jwt`：签发与校验

```go
type Config struct {
	Algorithm string        `yaml:"algorithm"` // HS256|HS384|HS512|RS256|RS384|RS512|ES256|ES384|ES512
	Secret    string        `yaml:"secret"`    // HMAC
	PrivateKey string       `yaml:"private_key"` // PEM，RSA/ECDSA
	PublicKey  string       `yaml:"public_key"`  // PEM
	Issuer     string       `yaml:"issuer"`
	Audience   string       `yaml:"audience"`
	Leeway     time.Duration `yaml:"leeway" default:"30s"`
	MaxTokenSize int        `yaml:"max_token_size" default:"8192"`
}

// 包内以 jwtlib 别名导入 golang-jwt，对外 re-export。
type RegisteredClaims = jwtlib.RegisteredClaims

type Claims struct {
	RegisteredClaims
	// 业务字段由调用方嵌入
}

type Manager struct{ /* parser, signer, keyset */ }

func New(cfg Config, opts ...Option) (*Manager, error)
func (m *Manager) Sign(ctx context.Context, claims Claims) (string, error)
func (m *Manager) Parse(ctx context.Context, token string, dst any) error
func (m *Manager) ParseClaims(ctx context.Context, token string) (Claims, error)
```

安全规则：

1. **算法白名单**：解析时使用 `jwt.WithValidMethods([]string{cfg.Algorithm})`，杜绝 `alg=none` 与算法混淆攻击。
2. **必须校验 `exp`**：未设置过期时间的 token 拒绝签发；解析时 `exp` 缺失视为无效。
3. **校验 `iss` / `aud`**：配置了就必须匹配；空配置表示不校验，但生产环境应在评审中明确。
4. **密钥长度**：HS256 至少 32 字节；RSA 至少 2048 位；ECDSA 使用 P-256 及以上。
5. **token 大小上限**：超过 `MaxTokenSize` 直接拒绝，防止解析放大。
6. **时钟偏移**：`Leeway` 默认 30s，可通过 `WithClock` 注入测试时钟。
7. 不实现自定义 JWT 解析；签名、base64、JSON 全部交给 `golang-jwt/jwt/v5`。

密钥轮换：

```go
type KeySet struct {
	ActiveKID string
	Keys      map[string]Key // kid -> 公钥/共享密钥
}

func (m *Manager) WithKeySet(ks KeySet) *Manager
```

签发时写入 `kid` 头；校验时按 `kid` 选 key，未命中则尝试 `ActiveKID`（仅兼容期），仍失败返回 `ErrUnknownKey`。删除旧 key 前必须确认所有未过期 token 都已失效。

Claims 建议：

- `sub` 放用户 ID，`iss` 放服务名，`aud` 放调用方；
- 业务字段用短名并固定类型，避免把整个用户对象塞进 token；
- 不在 token 中放手机号、身份证、密码哈希等敏感信息；
- 刷新 token 与访问 token 使用不同的 `aud` 或 `typ` 字段。

---

### 7.2 `encrypt`：对称加密

```go
type Key struct {
	KID   string
	Bytes []byte // 32 字节
}

type Config struct {
	ActiveKID string `yaml:"active_kid"`
	Keys      []Key  `yaml:"-"`
}

type Cipher struct{ /* keyring */ }

func New(cfg Config, opts ...Option) (*Cipher, error)
func (c *Cipher) Encrypt(ctx context.Context, plaintext, aad []byte) (string, error)
func (c *Cipher) Decrypt(ctx context.Context, token string, aad []byte) ([]byte, error)
func (c *Cipher) EncryptString(ctx context.Context, s string) (string, error)
func (c *Cipher) DecryptString(ctx context.Context, token string) (string, error)
```

算法与格式：

- 只使用 **AES-256-GCM**。不实现 CBC + HMAC，不实现 PHP 兼容负载。
- 输出：`v1:<kid>:<base64url(nonce)>:<base64url(ciphertext+tag)>`。
- nonce 每次 12 字节，来自 `crypto/rand`；禁止复用。
- `aad` 用于绑定上下文（如用户 ID、用途），解密时必须一致。
- 密钥必须是 32 字节；从配置读取时支持 hex/base64，解码后校验长度。
- 解密失败统一返回 `ErrDecrypt`，不区分“key 不存在 / tag 不匹配 / 格式错误”，避免填充预言。
- 密钥轮换：新数据用 `ActiveKID` 加密；旧 `kid` 保留用于解密，确认无旧数据后移除。
- 不提供“自动检测并降级到明文”的兼容逻辑。

---

### 7.3 `password`：密码哈希

```go
type Argon2Params struct {
	Memory      uint32 `yaml:"memory"`      // KiB
	Iterations  uint32 `yaml:"iterations"`
	Parallelism uint8  `yaml:"parallelism"`
	SaltLength  uint32 `yaml:"salt_length"`
	KeyLength   uint32 `yaml:"key_length"`
}

type Config struct {
	Algorithm  string        `yaml:"algorithm" default:"argon2id"` // bcrypt|argon2id
	BcryptCost int           `yaml:"bcrypt_cost" default:"12"`
	Argon2     Argon2Params  `yaml:"argon2"`
}

type Hasher struct{ /* config */ }

func New(cfg Config, opts ...Option) (*Hasher, error)
func (h *Hasher) Hash(password string) (string, error)
func (h *Hasher) Verify(password, encoded string) (bool, error)
func (h *Hasher) NeedsRehash(encoded string) bool
```

规则：

1. 默认 `argon2id`：`m=19456 KiB`、`t=2`、`p=1`（OWASP 推荐起点），按机器性能调整。
2. `bcrypt` 默认 cost 12；密码超过 72 字节直接返回 `ErrPasswordTooLong`，**不静默截断**。
3. `Verify` 对格式错误返回 `(false, ErrInvalidHash)`；密码不匹配返回 `(false, nil)`。
4. `NeedsRehash` 只比较参数，不重新哈希；登录成功后调用，需要时更新数据库。
5. 不实现“pepper”或可逆加密；密码只存哈希。
6. 哈希计算是 CPU 密集操作，调用方应避免在单请求内对大量用户循环校验；批量场景由业务控制并发。

---

### 7.4 `captcha`：图形验证码

```go
type Config struct {
	Width       int           `yaml:"width" default:"160"`
	Height      int           `yaml:"height" default:"60"`
	Length      int           `yaml:"length" default:"5"`
	TTL         time.Duration `yaml:"ttl" default:"5m"`
	MaxAttempts int           `yaml:"max_attempts" default:"3"`
	Prefix      string        `yaml:"prefix"`
}

type Captcha struct{ /* redis, generator */ }

func New(client *redis.Client, cfg Config, opts ...Option) (*Captcha, error)
func (c *Captcha) Generate(ctx context.Context) (id string, png []byte, err error)
func (c *Captcha) Verify(ctx context.Context, id, answer string, consume bool) (bool, error)
```

语义：

1. `Generate` 生成随机字符、渲染 PNG、写入 Redis（key `{prefix}:captcha:{id}`，值为小写答案），返回 `id` 与图片字节。
2. `Verify` 大小写不敏感，使用常数时间比较；成功后按 `consume` 删除，默认一次性。
3. 失败次数超过 `MaxAttempts` 立即删除，要求重新获取。
4. `id` 使用 `crypto/rand` 生成，不使用可预测的自增 ID。
5. 字体与背景内置，不依赖工作目录文件；不提供 `shell_exec` 之类的外部 OCR 检测。
6. 组件的职责到“验证码正确性”为止；IP/手机号维度的限流由 `ratelimit` 或业务中间件负责。
7. 不宣称“防 OCR”；高风险场景叠加行为风控、短信验证或第三方验证码。

---

### 7.5 `validation`：validator 规则集

不移植 PHP 的 `ValidateRule` DSL。Go 侧统一使用 `go-playground/validator`，组件只提供常用自定义规则和错误详情转换。

```go
func New() *validator.Validate
func Register(v *validator.Validate) error
func Details(req any, err error) map[string]string
```

默认规则：

| tag | 含义 | 示例 |
| --- | --- | --- |
| `cn_mobile` | 中国大陆手机号 | `validate:"required,cn_mobile"` |
| `cn_idcard` | 18 位身份证（含校验位） | `validate:"omitempty,cn_idcard"` |
| `oneof` | 值必须在允许集合内（validator 内建） | `validate:"oneof=paid unpaid"` |
| `uint64str` | 可解析为 uint64 的字符串 | `validate:"uint64str"` |
| `password` | 长度与字符种类 | `validate:"password"` |
| `decimal` | 定点小数字符串 | `validate:"decimal"` |
| `datetime` | 指定布局的时间字符串 | `validate:"datetime=2006-01-02"` |
| `notblank` | 去除空白后非空 | `validate:"notblank"` |

约定：

1. `New()` 返回独立实例；配置完成后 `validator.Validate` 可并发使用。
2. 不注册全局 validator；gofy 的 `transport.NewValidator` 可调用 `validation.Register(v)` 复用规则。
3. `Details` 的 key 使用 `json/form/uri` tag 名，与 API 字段一致；消息优先取结构体 `message` tag，其次用默认英文。
4. 复杂跨字段规则写自定义函数并注册，不拼接字符串表达式。
5. 校验错误是参数错误，不是业务错误；由 HTTP 层映射为 400。

---

### 7.6 `sms`：短信与验证码

#### 7.6.1 发送

```go
type Message struct {
	Phones   []string          `json:"phones"`
	Sign     string            `json:"sign"`
	Template string            `json:"template"`
	Params   map[string]string `json:"params"`
}

type Result struct {
	Gateway       string
	RequestID     string
	SuccessPhones []string
	FailedPhones  map[string]string // phone -> reason
}

type Sender interface {
	Name() string
	Send(ctx context.Context, msg Message) (Result, error)
}

type Config struct {
	Default   string                  `yaml:"default"`
	Gateways  map[string]GatewayConfig `yaml:"gateways"`
	MaxPhones int                     `yaml:"max_phones" default:"100"`
}

type Manager struct{ /* senders, config */ }

func New(cfg Config, opts ...Option) (*Manager, error)
func (m *Manager) Send(ctx context.Context, msg Message) (Result, error)
func (m *Manager) SendVia(ctx context.Context, gateway string, msg Message) (Result, error)
```

网关：

- `aliyun`：阿里云短信 SDK；模板变量按 JSON 对象传递。
- `tencent`：腾讯云短信 SDK；注意参数顺序与模板参数映射。
- `log`：不发网络请求，记录脱敏日志，用于本地开发和测试。
- 厂商临时错误（限流、网络、5xx）可重试；参数错误、黑名单、模板不存在不重试。
- 手机号统一做格式校验与去重；单次超过 `MaxPhones` 拒绝。
- 日志中手机号脱敏，绝不打印模板参数中的验证码、密码、身份证。

#### 7.6.2 验证码

```go
type RateLimitConfig struct {
	Enabled bool          `yaml:"enabled" default:"true"`
	Max     int           `yaml:"max" default:"5"`
	Period  time.Duration `yaml:"period" default:"1h"`
}

type VerifyConfig struct {
	Length          int           `yaml:"length" default:"6"`
	TTL             time.Duration `yaml:"ttl" default:"5m"`
	MaxAttempts     int           `yaml:"max_attempts" default:"5"`
	ResendInterval  time.Duration `yaml:"resend_interval" default:"60s"`
	RateLimit       RateLimitConfig `yaml:"rate_limit"`
	KeyPrefix       string        `yaml:"key_prefix"`
	HashKey         string        `yaml:"hash_key"` // 至少 16 字节
}

type VerifyStatus string

const (
	VerifyOK       VerifyStatus = "ok"
	VerifyMismatch VerifyStatus = "mismatch"
	VerifyExpired  VerifyStatus = "expired"
	VerifyAbsent   VerifyStatus = "absent"
	VerifyLocked   VerifyStatus = "locked"
)

type VerifyResult struct {
	OK        bool
	Status    VerifyStatus
	Remaining int // 剩余尝试次数
}

type Verifier struct{ /* manager, redis, limiter */ }

func NewVerifier(manager *Manager, client *redis.Client, cfg VerifyConfig, opts ...Option) (*Verifier, error)
func (v *Verifier) Send(ctx context.Context, phone, scene string) error
func (v *Verifier) Verify(ctx context.Context, phone, scene, code string) (VerifyResult, error)
```

流程：

1. `Send`：
   - 校验手机号、scene 是否在允许列表；
   - 检查重发间隔（Redis key）；
   - 检查发送频率（`ratelimit`，key 含 phone + scene）；
   - 生成 `crypto/rand` 数字码；
   - 存储 `HMAC-SHA256(hash_key, phone + ":" + scene + ":" + code)`，不存明文；
   - 调用短信网关；
   - 网关失败时删除已写入的验证码与冷却 key。
2. `Verify`：Lua 原子执行
   - 读取哈希与尝试次数；
   - 不存在 → `absent`；
   - 已锁定 → `locked`；
   - 不匹配 → `INCR` 尝试次数，达到上限删除验证码并标记 `locked`，返回 `mismatch`；
   - 匹配 → 删除验证码与尝试次数，返回 `ok`。
3. 验证成功后不自动删除重发间隔 key；重发间隔独立控制。
4. `HashKey` 必须由配置提供，不能硬编码；不同环境使用不同值。
5. 返回结果不包含正确验证码；错误信息不区分“手机号不存在”等业务信息。

---

### 7.7 `mail`：邮件发送

```go
type Address struct {
	Name  string
	Email string
}

type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type Message struct {
	To          []Address
	CC          []Address
	BCC         []Address
	From        Address
	ReplyTo     []Address
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
	Headers     map[string]string
}

type Config struct {
	Default            string                 `yaml:"default"`
	Mailers            map[string]MailerConfig `yaml:"mailers"`
	MaxRecipients      int                    `yaml:"max_recipients" default:"50"`
	MaxAttachmentBytes int64                  `yaml:"max_attachment_bytes" default:"10485760"`
	TemplateDir        string                 `yaml:"template_dir"`
}

type Mailer struct{ /* senders */ }

func New(cfg Config, opts ...Option) (*Mailer, error)
func (m *Mailer) Send(ctx context.Context, msg Message) (Result, error)
func (m *Mailer) SendTemplate(ctx context.Context, name string, data any, msg Message) (Result, error)
```

规则：

1. 使用 `wneessen/go-mail`，支持 `context` 取消与连接超时。
2. TLS 默认开启；`insecure_skip_verify` 只能显式配置，并在启动日志中告警。
3. 地址、主题、header 做 CRLF 注入过滤；非法地址直接拒绝。
4. 收件人总数、附件总大小超限拒绝。
5. 模板使用 `html/template` 自动转义；禁止把用户输入直接拼进 HTML。
6. `log` transport 只记录元数据，不写正文，用于测试。
7. 发送是阻塞 I/O；不要在一个请求中串行发送大量邮件，批量场景交给队列。

---

## 8. 文件、媒体与办公文档

### 8.1 `storage`：对象存储统一接口

#### 8.1.1 接口

```go
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}

type PutOptions struct {
	ContentType string
	Metadata    map[string]string
	ACL         string // private|public-read，默认 private
	CacheControl string
}

type ListOptions struct {
	Prefix    string
	Recursive bool
	MaxKeys   int
	Cursor    string
}

type ListResult struct {
	Objects   []ObjectInfo
	NextCursor string
}

type SignOptions struct {
	Method string // GET|PUT
	ContentType string
}

type Capabilities struct {
	Presign   bool
	Multipart bool
	List      bool
	Copy      bool
}

type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	List(ctx context.Context, opts ListOptions) (ListResult, error)
	SignURL(ctx context.Context, key string, ttl time.Duration, opts SignOptions) (string, error)
	Capabilities() Capabilities
	Check(ctx context.Context) error
}
```

可选能力用独立接口，不把不支持的方法塞进 `Store`：

```go
type Copier interface {
	Copy(ctx context.Context, src, dst string) error
}

type Multipart interface {
	CreateUpload(ctx context.Context, key string, opts PutOptions) (uploadID string, err error)
	UploadPart(ctx context.Context, key, uploadID string, part int32, r io.Reader, size int64) (etag string, err error)
	CompleteUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) (ObjectInfo, error)
	AbortUpload(ctx context.Context, key, uploadID string) error
}
```

调用方用类型断言判断能力，例如 `if m, ok := store.(storage.Multipart); ok { ... }`。

#### 8.1.2 配置

```go
type Config struct {
	Driver           string      `yaml:"driver"` // local|s3|oss|cos|fake
	KeyPrefix        string      `yaml:"key_prefix"`
	MaxObjectBytes   int64       `yaml:"max_object_bytes" default:"104857600"`
	AllowedTypes     []string    `yaml:"allowed_types"` // 为空表示不限制
	Local            LocalConfig `yaml:"local"`
	S3               S3Config    `yaml:"s3"`
	OSS              OSSConfig   `yaml:"oss"`
	COS              COSConfig   `yaml:"cos"`
}

type LocalConfig struct {
	Root       string `yaml:"root"`
	PublicBase string `yaml:"public_base"`
}

type S3Config struct {
	Region       string `yaml:"region"`
	Bucket       string `yaml:"bucket"`
	Endpoint     string `yaml:"endpoint"`
	UsePathStyle bool   `yaml:"use_path_style"`
	AccessKey    string `yaml:"access_key"`
	SecretKey    string `yaml:"secret_key"`
}

type OSSConfig struct {
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type COSConfig struct {
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AppID     string `yaml:"app_id"`
	SecretID  string `yaml:"secret_id"`
	SecretKey string `yaml:"secret_key"`
}
```

#### 8.1.3 安全与行为规则

1. **key 校验**：禁止空 key、绝对路径、`..` 段、反斜杠、控制字符；长度上限 1024；统一使用 `/` 分隔。
2. **本地驱动**：`filepath.Join(root, key)` 后必须确认结果仍在 `root` 内（`filepath.Rel` + 前缀判断）；禁止跟随符号链接逃逸；默认不提供目录列表之外的能力。
3. **大小限制**：`size > MaxObjectBytes` 或读取超过上限返回 `ErrObjectTooLarge`；`size < 0` 表示未知，读取时用 `io.LimitReader` 限制。
4. **Content-Type**：优先使用调用方传入值；未传时按扩展名推断，但只作为元数据，不参与安全判断。
5. **ACL**：默认 `private`；`public-read` 必须显式设置并在审计日志中记录。
6. **预签名**：`ttl` 必须大于 0 且不超过驱动上限（S3 最长 7 天）；本地驱动返回 `ErrNotSupported`。
7. **错误映射**：不存在 → `ErrNotFound`；权限 → `ErrForbidden`；限流 → `ErrUnavailable`；其他驱动错误保留在链中。
8. **关闭**：`Store` 不实现 `Close`；云 SDK 客户端由 driver 内部持有，随进程退出。若 driver 需要释放资源，实现 `io.Closer`，由调用方按需关闭。
9. **测试**：`storage/fake` 提供内存实现，支持 Put/Get/Delete/List/Stat/SignURL（伪签名），并记录调用次数，便于断言。

#### 8.1.4 分片上传

- 大文件（默认 > 8 MiB）走 `Multipart`。
- `CreateUpload` 返回 `uploadID`；每个 part 独立上传并记录 ETag；`CompleteUpload` 按 part 序号组合。
- 失败或取消时调用 `AbortUpload`，避免产生无主分片费用。
- 本地驱动不实现 `Multipart`；调用方按能力降级为普通 `Put`。
- 所有 part 的序号从 1 开始，最大 10000；单 part 最小 5 MiB（S3 约束），最后一块除外。

---

### 8.2 `imagex`：图片处理

```go
type Config struct {
	MaxPixels     int64    `yaml:"max_pixels" default:"40000000"`
	MaxDimension  int      `yaml:"max_dimension" default:"12000"`
	AllowedFormats []string `yaml:"allowed_formats"` // jpeg|png|gif|webp
	JPEGQuality   int      `yaml:"jpeg_quality" default:"85"`
	MaxOutputBytes int64   `yaml:"max_output_bytes" default:"20971520"`
}

type Mode string

const (
	Fit   Mode = "fit"
	Fill  Mode = "fill"
	Exact Mode = "exact"
)

type Position string

const (
	TopLeft     Position = "top-left"
	Top         Position = "top"
	TopRight    Position = "top-right"
	Left        Position = "left"
	Center      Position = "center"
	Right       Position = "right"
	BottomLeft  Position = "bottom-left"
	Bottom      Position = "bottom"
	BottomRight Position = "bottom-right"
)

type TextOptions struct {
	Size  float64
	Color color.Color
	Font  font.Face // 为空时使用内置字体
}

type Processor struct{ /* config */ }

type Op func(*state) error

func New(cfg Config) *Processor
func (p *Processor) Process(ctx context.Context, r io.Reader, ops ...Op) (Result, error)

func Resize(width, height int, mode Mode) Op
func Crop(x, y, width, height int) Op
func Rotate(degrees int) Op
func FlipHorizontal() Op
func FlipVertical() Op
func Grayscale() Op
func WatermarkImage(r io.Reader, opacity float64, position Position) Op
func WatermarkText(text string, opts TextOptions) Op
func Encode(format string, quality int) Op
```

`Mode`：`Fit`（等比缩放并留白）、`Fill`（等比缩放后裁剪）、`Exact`（强制拉伸）。`Position`：九宫格。

规则：

1. 解码前先 `image.DecodeConfig` 读取尺寸，超过 `MaxPixels` 或 `MaxDimension` 直接拒绝，防止解压炸弹。
2. 格式白名单默认 `jpeg/png/gif/webp`；**不支持 SVG**（XSS 与外部实体风险），不支持 TIFF/HEIC（需额外依赖）。
3. GIF 只处理第一帧；动画场景不在 v1。
4. 输出统一重新编码，天然去除 EXIF、ICC、注释等元数据；不保留原始元数据。
5. `WatermarkImage` 接受 `io.Reader`，不接受文件路径，避免路径穿越与工作目录依赖。
6. 文本水印使用内置字体或调用方提供的 `font.Face`；不依赖系统字体路径。
7. `Result` 包含编码后的字节、格式、宽高；超过 `MaxOutputBytes` 返回错误。
8. 图片处理是 CPU 密集操作；组件不启动 worker 池，调用方通过信号量或队列控制并发，避免打满 CPU。

---

### 8.3 `qrcode` 与 `barcode`

#### 8.3.1 QR

```go
type Config struct {
	Size            int    `yaml:"size" default:"300"`
	Margin          int    `yaml:"margin" default:"1"`
	ErrorCorrection string `yaml:"error_correction" default:"M"` // L|M|Q|H
	Foreground      string `yaml:"foreground" default:"#000000"`
	Background      string `yaml:"background" default:"#ffffff"`
	MaxContentBytes int    `yaml:"max_content_bytes" default:"2953"`
}

type Generator struct{ /* config */ }

func New(cfg Config, opts ...Option) *Generator
func (g *Generator) PNG(content string, opts ...Option) ([]byte, error)
func (g *Generator) SVG(content string, opts ...Option) ([]byte, error)
func (g *Generator) WiFi(ssid, password, auth string, opts ...Option) ([]byte, error)
```

- 内容长度超过上限返回 `ErrContentTooLong`（上限按纠错级别不同，取保守值）。
- SVG 输出对内容做 XML 转义，禁止注入。
- Logo 叠加复用 `imagex`：Logo 面积不超过二维码的 20%，纠错级别至少 `Q`。
- WiFi 串格式：`WIFI:T:WPA;S:<ssid>;P:<password>;;`，对 `;`、`,`、`:`、`\` 转义。

#### 8.3.2 Barcode

```go
type Type string

const (
	Code128 Type = "code128"
	Code39  Type = "code39"
	EAN13   Type = "ean13"
	EAN8    Type = "ean8"
	UPCA    Type = "upca"
	ITF     Type = "itf"
)

type Generator struct{ /* config */ }

func New(cfg Config) *Generator
func (g *Generator) PNG(content string, typ Type, opts ...Option) ([]byte, error)
func (g *Generator) SVG(content string, typ Type, opts ...Option) ([]byte, error)
```

- 调用库前校验长度与字符集；EAN/UPC 校验位由库计算或校验，失败返回 `ErrInvalidContent`。
- 高度、宽度、边距可配置；宽度为 0 时按内容自动计算。
- 不渲染人眼可读文本（PNG 场景库支持有限）；需要文本时由调用方在图片上叠加。
- 输出格式与尺寸上限与 `imagex` 一致，防止生成超大图片。

---

### 8.4 `excel`：流式导入导出

```go
type Sheet struct {
	Name    string
	Headers []string
	Rows    iter.Seq2[int, []any] // 行号（从 1 开始）与单元格
}

type ExportOptions struct {
	SheetName     string
	FreezeHeader  bool
	AutoFilter    bool
	ColumnWidths  []float64
	MaxRows       int
	MaxCellBytes  int
}

func Export(ctx context.Context, w io.Writer, sheet Sheet, opts ExportOptions) error

type ImportOptions struct {
	SheetName     string
	HeaderRow     int
	DataStartRow  int
	MaxRows       int
	MaxCellBytes  int
	RequiredHeaders []string
}

type Row struct {
	Number  int
	Values  map[string]string
	Raw     []string
}

func Import(ctx context.Context, r io.Reader, opts ImportOptions, h func(Row) error) error
```

规则：

1. 使用 `excelize/v2` 的 `StreamWriter` 与流式读取，避免整表进内存。
2. 只接受 `io.Reader` / `io.Writer`，不接受文件路径，避免路径穿越。
3. 表头映射：首行作为字段名，重复表头报错；`RequiredHeaders` 缺失报错。
4. 行数、列数、单元格字节数超限立即中止；已写入的行不回滚，由调用方决定是否删除临时文件。
5. 导出时所有值通过 `SetCellStr`/`SetCellValue` 写入，**不使用公式写入 API**；以 `=`、`+`、`-`、`@` 开头的用户输入按字符串写入，避免公式注入。
6. 日期使用明确的 `time.Time` + 格式，不猜测 Excel 序列号。
7. 导入时空白行跳过；类型转换由调用方在 `h` 中完成，组件不猜 int/float/date。
8. 不实现 PHP 模板方法式的 `AbstractExcelExport`；Go 用结构体 + 迭代器组合。

---

## 9. 集成、可观测性与工具

### 9.1 `httpclient`：出站 HTTP

#### 9.1.1 Config

```go
type Config struct {
	BaseURL          string        `yaml:"base_url"`
	Timeout          time.Duration `yaml:"timeout" default:"10s"`
	ConnectTimeout   time.Duration `yaml:"connect_timeout" default:"3s"`
	IdleConnTimeout  time.Duration `yaml:"idle_conn_timeout" default:"90s"`
	MaxIdleConns     int           `yaml:"max_idle_conns" default:"100"`
	MaxConnsPerHost  int           `yaml:"max_conns_per_host" default:"20"`
	MaxResponseBytes int64         `yaml:"max_response_bytes" default:"10485760"`
	UserAgent        string        `yaml:"user_agent"`
	ProxyURL         string        `yaml:"proxy_url"`
	FollowRedirects  bool          `yaml:"follow_redirects" default:"true"`
	InsecureSkipVerify bool        `yaml:"insecure_skip_verify"` // 默认 false
	Retry            RetryConfig   `yaml:"retry"`
	Headers          map[string]string `yaml:"headers"`
}

type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts" default:"2"`
	BaseDelay   time.Duration `yaml:"base_delay" default:"100ms"`
	MaxDelay    time.Duration `yaml:"max_delay" default:"2s"`
	RetryOn5xx  bool          `yaml:"retry_on_5xx" default:"true"`
}
```

#### 9.1.2 API

```go
type Client struct{ /* http.Client, config */ }

func New(cfg Config, opts ...Option) (*Client, error)
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error)
func (c *Client) GetJSON(ctx context.Context, path string, out any, opts ...RequestOption) error
func (c *Client) PostJSON(ctx context.Context, path string, in, out any, opts ...RequestOption) error
func (c *Client) PutJSON(ctx context.Context, path string, in, out any, opts ...RequestOption) error
func (c *Client) Delete(ctx context.Context, path string, opts ...RequestOption) error
func (c *Client) Download(ctx context.Context, path string, w io.Writer, opts ...RequestOption) (int64, error)
func (c *Client) Close() error
```

`RequestOption`：`WithHeader`、`WithQuery`、`WithIdempotencyKey`、`WithRequestID`、`WithTimeout`。

#### 9.1.3 行为

1. **TLS 校验默认开启**。`InsecureSkipVerify` 为 true 时在 `New` 返回错误，除非同时传入 `WithAllowInsecureTLS()`；启动日志必须有 `Warn`。
2. **超时**：`http.Client.Timeout` 为总超时；`Transport.DialContext` 使用 `ConnectTimeout`；请求可通过 `WithTimeout` 进一步收紧。
3. **重试**：
   - GET/HEAD/OPTIONS 默认重试；
   - POST/PATCH 只有携带 `Idempotency-Key` 时才重试；
   - 429/502/503/504 与网络错误可重试，尊重 `Retry-After`；
   - 请求体必须可重放（`GetBody != nil`），否则不重试。
4. **响应大小**：`io.LimitReader` 限制为 `MaxResponseBytes + 1`，超限返回 `ErrResponseTooLarge`。
5. **错误类型**：

```go
type Error struct {
	Method    string
	URL       string
	Status    int
	Code      string
	RequestID string
	Body      []byte // 最多 2 KiB 的脱敏预览
	Err       error
}
```

6. **脱敏**：日志与错误中的 `Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key` 替换为 `***`；JSON body 中的 `password`、`token`、`secret`、`refresh_token` 同样替换。
7. **追踪**：通过 `WithTransport(otelhttp.NewTransport(...))` 注入 OTel；组件本身不 import otel，保持 T0 无重依赖。
8. **连接复用**：每个 `Client` 持有一个 `*http.Transport`，不要每次请求新建；`Close` 调用 `CloseIdleConnections`。
9. **不跟随重定向到不同主机时携带 Authorization**：默认 `CheckRedirect` 在跨主机时移除敏感头。

---

### 9.2 `nacos`：配置中心与服务发现

```go
type Config struct {
	Enabled   bool
	Addr      string // http://127.0.0.1:8848，优先级高于 Host/Port
	Host      string
	Port      int
	Username  string
	Password  string
	Namespace string
	DataID    string
	Group     string
	File      string
	Watch     bool
	Timeout   time.Duration
	PollTimeout time.Duration
	RetryDelay  time.Duration

	Register  RegisterConfig  // application.yaml service_register
	Discovery DiscoveryConfig // application.yaml discovery_service_client
}

func Load(nacosPath, appPath string) (Config, error)
func Sync(ctx context.Context, cfg Config) (Result, error)
func Watch(ctx context.Context, cfg Config, opts ...WatchOption) error

// Naming（服务注册与发现）
func NewNamingClient(cfg Config) (*Client, error)
func (c *Client) RegisterInstance(ctx context.Context, inst Instance) error
func (c *Client) DeregisterInstance(ctx context.Context, inst Instance) error
func (c *Client) BeatInstance(ctx context.Context, inst Instance, light bool) (BeatResult, error)
func (c *Client) ListInstances(ctx context.Context, q InstanceQuery) ([]Instance, error)

func NewRegistrar(cfg Config, logger *zap.Logger) (*Registrar, error)
func (r *Registrar) Start(ctx context.Context) error // 注册 + 心跳
func (r *Registrar) Stop(ctx context.Context) error  // 停止心跳 + 注销
```

规则：

1. 直接调用 Nacos Open API（configs / listener / instance / beat），不引入 SDK；
   长轮询、鉴权 token 缓存都在包内完成。
2. `WatchConfig` 由 `Watch` 承担，阻塞直到 `ctx` 取消；回调中不要执行长时间阻塞操作，
   必要时投递到队列。
3. 服务实例固定为临时实例（ephemeral）：`Registrar` 维持重/轻心跳，Nacos 可在心跳
   响应中调整间隔（限制 1s–30s）；连续失败后先补完整心跳，仍失败则重新注册；
   `Stop` 注销。
4. 配置获取失败时**不返回空字符串**；返回错误，由业务决定是否使用本地已同步的文件。
5. `Sync` 先校验远端内容是合法 YAML，再以临时文件 + rename 原子替换本地文件（0600）。
6. 启动时校验连接字段与端口；认证信息不写日志。
7. `gofy.WithNacosRegistration(cfg)` 在监听端口可用后注册、`Shutdown` 前注销；
   `App.Reload` 不重复注册或注销，热更新期间服务发现保持连续。
8. `nacos` 是 P2 可选模块；不使用 Nacos 的服务不会引入该依赖。

---

### 9.3 `otelx`：OpenTelemetry 初始化

```go
type Config struct {
	ServiceName    string            `yaml:"service_name"`
	ServiceVersion string            `yaml:"service_version"`
	Endpoint       string            `yaml:"endpoint"` // OTLP HTTP/gRPC
	Insecure       bool              `yaml:"insecure"`
	SampleRatio    float64           `yaml:"sample_ratio" default:"0.1"`
	Headers        map[string]string `yaml:"headers"`
	ExportTimeout  time.Duration     `yaml:"export_timeout" default:"10s"`
	BatchTimeout   time.Duration     `yaml:"batch_timeout" default:"5s"`
	MaxQueueSize   int               `yaml:"max_queue_size" default:"2048"`
}

func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error)
func HTTPTransport(base http.RoundTripper) http.RoundTripper
func Propagator() propagation.TextMapPropagator
```

- 使用 OTLP exporter；`Endpoint` 为空时返回 no-op shutdown，不报错（本地开发友好）。
- 采样：`ParentBased(TraceIDRatioBased(SampleRatio))`；`SampleRatio=1` 全采样，仅用于排障。
- 资源属性包含 `service.name`、`service.version`、`deployment.environment`（由调用方传入）。
- 传播器使用 W3C `traceparent` + `tracestate` + `baggage`。
- **全局状态例外**：OTel SDK 的 `otel.SetTracerProvider` 是进程级全局，这是 SDK 设计决定的。`Setup` 只允许在 `main` 中调用一次；测试中使用 no-op provider。组件库其余部分不依赖全局。
- `shutdown` 必须在进程退出前调用，并设置超时，确保 batch 导出完成。
- 不内置 Prometheus/metrics SDK；指标由业务选择。

---

### 9.4 `events`：进程内事件

```go
type Handler func(ctx context.Context, payload any) error

type Bus struct{ /* subscribers, queue, workers */ }

type Config struct {
	AsyncWorkers int `yaml:"async_workers" default:"4"`
	QueueSize    int `yaml:"queue_size" default:"1024"`
}

func New(cfg Config, opts ...Option) *Bus
func (b *Bus) Subscribe(name string, h Handler) (cancel func())
func (b *Bus) Publish(ctx context.Context, name string, payload any) error
func (b *Bus) PublishAsync(ctx context.Context, name string, payload any) error
func (b *Bus) Close(ctx context.Context) error
```

语义：

1. 同步发布按订阅顺序执行；任一 handler 返回错误时停止后续 handler，并返回聚合错误。
2. 异步发布写入有界队列；队列满时阻塞或返回 `ErrQueueFull`（可配置）。
3. handler panic 被捕获并转为错误，不影响其他订阅者与进程。
4. 进程内事件**不持久化、不保证投递**；需要可靠投递请用 `queue` / `kafka`。
5. `Close(ctx)` 停止接收新事件，等待队列排空或 `ctx` 超时。
6. 不使用包级单例；`Bus` 由业务创建并传递。

---

### 9.5 `sysinfo`：主机指标（P3）

使用 `gopsutil/v4` 读取 CPU、内存、磁盘、负载、网络、进程数，不执行 shell、不解析 `/proc` 文本。

```go
type Snapshot struct {
	CPUPercent float64
	Load1      float64
	Memory     MemoryInfo
	Disks      []DiskInfo
	Network    []NetworkInfo
	Uptime     time.Duration
}

func Collect(ctx context.Context) (Snapshot, error)
```

- 指标采集有超时；磁盘列表可能很慢，按需采集。
- 不内置 HTTP handler；由业务挂到管理端口，并做访问控制。
- 容器环境下部分指标来自宿主机，README 必须说明。

---

### 9.6 不移植的工具类

| PHP 工具 | Go 替代 |
| --- | --- |
| `Helper::data_get/data_set` | 显式结构体、`map` 访问；需要路径时写业务函数 |
| `StringUtil` | `strings`、`strconv`、`unicode/utf8` |
| `ArrayHelper` | `slices`、`maps`、`cmp`、`iter` |
| `SplArray` / `SplStrictArray` | 切片、泛型函数 |
| `SplStream` / `SplFileStream` | `io.Reader`、`io.Writer`、`os.File` |
| `SplEnum` | 自定义类型 + 常量 + `Valid()` 方法 |
| `Purl` | `net/url` |
| `Clock` | 保留为轻量 `clock` 包，便于测试 |
| `Protobuf` 动态桥接 | 生成代码 + `protojson` |
| `LinuxDash` | `sysinfo` + gopsutil |
| `Component/ListObject` | 契约 `paging` + 业务查询结构 |

原则：能用标准库就不封装；封装的唯一理由是统一语义、减少重复错误处理，而不是“像 PHP 一样有个类”。

---

### 9.7 `oauth`：第三方登录（P3）

v1 不提供统一 OAuth 门面。原因：

- QQ、微信、支付宝、飞书、钉钉、企业微信的协议差异大，统一接口会泄漏大量厂商特有字段；
- Go 侧 OIDC 生态成熟，标准场景直接用 `coreos/go-oidc/v3`；
- 厂商登录通常需要业务侧维护 state、session、绑定关系，组件无法替业务决定。

P3 若要做，按厂商拆子包：`oauth/wechat`、`oauth/dingtalk`、`oauth/feishu`，每个子包只封装“换 token / 取用户信息 / 解密回调”，不提供全局 Manager，不缓存 token。

---

## 10. 业务服务接入

### 10.1 组装顺序

业务服务的 `main` 负责创建和关闭所有资源，顺序固定：

```text
1. 解析 CLI / 加载配置
2. 创建 slog logger
3. 创建 MySQL 连接（gofy/support/mysql）
4. 创建 db.DB（gofy-library/db）
5. 创建 Redis 客户端（gofy-library/redis）
6. 创建 cache / lock / ratelimit / idgen / queue / storage / sms ...
7. 创建 gofy App，注册 module，把组件注入 module
8. app.Run(ctx)
9. 退出时按 7 → 6 → 5 → 4 → 3 的逆序关闭
```

关闭顺序的原则：先停入口（HTTP server 不再接新请求），再停消费者，再关依赖；`db` 不拥有 GORM 连接，连接由第 3 步关闭。

```go
func main() {
	ctx := signal.SignalContext(context.Background())

	cfg, err := config.Load[AppConfig](ctx, "config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	logger, err := logging.New(cfg.Log)
	if err != nil {
		log.Fatal(err)
	}
	defer logger.Sync()

	// 组件库统一使用 slog；这里另建一个，避免组件反向依赖 zap。
	slogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	gdb, err := mysql.New(ctx, cfg.DB, logger)
	if err != nil {
		log.Fatal(err)
	}
	defer mysql.Close(gdb)

	database, err := db.New(gdb, db.Config{
		DefaultPageSize: 20,
		MaxPageSize:     200,
		Tenant: &db.TenantConfig{
			Column:   "tenant_id",
			Tables:   []string{"orders", "order_items"},
			Required: true,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	rdb, err := redis.New(cfg.Redis, redis.WithLogger(slogger))
	if err != nil {
		log.Fatal(err)
	}
	defer rdb.Close()

	locker := lock.New(rdb, cfg.Lock, lock.WithLogger(slogger))
	limiter, err := ratelimit.NewSlidingWindow(rdb, cfg.RateLimit, 100, time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	store, err := storage.New(cfg.Storage, storage.WithLogger(slogger))
	if err != nil {
		log.Fatal(err)
	}

	app, err := gofy.New(gofy.WithModules(module.New(database, rdb, locker, limiter, store)))
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
```

### 10.2 依赖注入方式

不引入 DI 框架。module 结构体显式持有依赖：

```go
type Module struct {
	db      *db.DB
	redis   *redis.Client
	locker  *lock.Locker
	limiter *ratelimit.SlidingWindow
	store   storage.Store
}

func New(database *db.DB, rdb *redis.Client, locker *lock.Locker, limiter *ratelimit.SlidingWindow, store storage.Store) *Module {
	return &Module{db: database, redis: rdb, locker: locker, limiter: limiter, store: store}
}

func (m *Module) Register(r *transport.Router) error {
	h := handler.New(m)
	transport.GET(r, "/orders/:id", h.GetOrder)
	return nil
}
```

service 层通过构造函数接收需要的组件，不接收整个 `Module`，避免依赖膨胀：

```go
type OrderService struct {
	repo   *repository.OrderRepository
	locker *lock.Locker
	store  storage.Store
}

func NewOrderService(repo *repository.OrderRepository, locker *lock.Locker, store storage.Store) *OrderService {
	return &OrderService{repo: repo, locker: locker, store: store}
}
```

### 10.3 配置示例

```yaml
redis:
  mode: single
  addrs:
    - 127.0.0.1:6379
  password: ""
  db: 0
  dial_timeout: 2s
  read_timeout: 3s
  write_timeout: 3s
  pool_size: 50
  key_prefix: order-service

lock:
  prefix: order-service
  default_ttl: 10s
  retry_interval: 50ms

ratelimit:
  prefix: order-service
  failure_mode: closed
  key_ttl: 10m

idgen:
  redis:
    key: order-service:id
    ttl: 24h

storage:
  driver: local
  key_prefix: order
  max_object_bytes: 104857600
  local:
    root: ./var/uploads
    public_base: https://cdn.example.com

sms:
  default: aliyun
  gateways:
    aliyun:
      access_key: ${SMS_ACCESS_KEY}
      access_secret: ${SMS_ACCESS_SECRET}
      sign: 示例签名
      endpoint: dysmsapi.aliyuncs.com
  max_phones: 100

sms_verify:
  length: 6
  ttl: 5m
  max_attempts: 5
  resend_interval: 60s
  key_prefix: order-service
  hash_key: ${SMS_VERIFY_HASH_KEY}
  rate_limit:
    max: 5
    period: 1h
```

注意：`${VAR}` 展开由 `gofy/config` 完成；组件库不读环境变量。`hash_key`、云厂商密钥必须来自环境或密钥管理系统，不能提交到仓库。

### 10.4 与 gofy 健康端点对接

`gofy` 的健康端点不需要知道组件库类型。业务构造一个适配器：

```go
type healthAdapter struct {
	registry *health.Registry
}

func (a healthAdapter) Check(ctx context.Context) error {
	report := a.registry.Check(ctx)
	if report.Status == "down" {
		return fmt.Errorf("dependency check failed: %s", report.Status)
	}
	return nil
}
```

然后把 `healthAdapter` 交给 gofy 的健康检查选项。若 gofy 当前版本没有自定义健康检查注入点，则先把 `report` 渲染到业务自己的 `/internal/health` 路由，不修改框架。

### 10.5 与 gofy 限流中间件对接

组件库不提供 gin 中间件，业务侧 10 行适配：

```go
func RateLimit(limiter *ratelimit.SlidingWindow, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP() + ":" + c.FullPath()
		res, err := limiter.Allow(c.Request.Context(), key, 1)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": 50300, "msg": "rate limiter unavailable"})
			return
		}
		c.Header("X-RateLimit-Limit", strconv.Itoa(res.Limit))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		if !res.Allowed {
			c.Header("Retry-After", strconv.Itoa(int(res.RetryAfter.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": 42900, "msg": "too many requests"})
			return
		}
		c.Next()
	}
}
```

错误码与响应信封由业务决定；组件库不 import gin，也不假设 gofy 的 responder。

### 10.6 与 gofy-interface-service 的关系

- 契约仓库定义 API 的 DTO、分页、错误码；组件库提供实现这些 API 所需的基础设施。
- 典型映射：
  - `db.PageResult[T]` → 业务映射 → `paging.PageData[OrderDTO]`
  - `sms.VerifyResult` → 业务映射 → 契约中的 `LoginResponse`
  - `idgen.Next()` → 契约 `types.Uint64String`
  - `storage.SignURL` → 契约 `FileDTO.URL`
- 契约仓库不得 import 组件库；组件库不得 import 契约。映射代码写在业务 service 层。
- OpenAPI 生成器只关心契约类型，不需要知道 Redis、MySQL 或对象存储。

### 10.7 反模式

| 反模式 | 后果 | 正确做法 |
| --- | --- | --- |
| 包级 `var Redis *redis.Client` | 测试污染、关闭顺序混乱 | 构造后传递 |
| 每个请求 `redis.New()` | 连接风暴、池失效 | 进程级创建一次，请求内复用 |
| 把 `context.Context` 存进结构体 | 请求取消语义错乱 | 每个方法显式传 ctx |
| 在 handler 里启动 goroutine 使用请求 ctx 后不等待 | 请求结束后写入被取消 | 用队列或 `errgroup` 并等待 |
| 在事务内调用 `db.Read` 期望重试 | 事务不可信 | 事务内不做重试 |
| 用 `Raw()` 绕过只读策略后抱怨重放 | 写操作被重复执行 | 明确使用 `Do` / `DoRead` |
| 在日志中打印 DSN、token、手机号 | 泄密 | 使用脱敏字段与 `redact` |
| 用 Redis List 队列承载支付消息 | 崩溃丢消息 | 使用 `queue`（Streams）并幂等 |
| 在 OAuth 回调中把 token 写进全局缓存 | 串号 | 请求内传递，按用户维度存储 |

---

## 11. 测试策略

### 11.1 单元测试

- 每个包至少覆盖：配置校验、默认值、错误分支、关闭后调用、并发安全。
- 外部依赖用接口替身：
  - `storage/fake` 内存对象存储；
  - `sms/gateway/log` 记录发送；
  - `mail` 的 log transport；
  - `clock.Fake` 控制时间；
  - Redis/MySQL 的 Lua 与 SQL 逻辑用集成测试，不用 mock 假装。
- 不 mock 标准库；不为了覆盖率写无断言测试。

### 11.2 集成测试

使用 `testcontainers-go`，通过 build tag 隔离：

```bash
go test -tags=integration ./...
```

覆盖矩阵：

| 组件 | 依赖 | 必测场景 |
| --- | --- | --- |
| `redis` | Redis 7 | 连接、集群、只读重放、写不重放、NOSCRIPT 回退 |
| `db` | MySQL 8 | 事务提交/回滚/嵌套、分页、租户注入、只读重放、连接断开 |
| `lock` | Redis | 互斥、超时、续期、错误 token 释放、并发竞争 |
| `ratelimit` | Redis | 窗口边界、令牌补充、并发原子性、失败模式 |
| `queue` | Redis | ack、重试、DLQ、pending 认领、延迟调度、重复投递 |
| `pubsub` | Redis | 订阅/取消、并发上限、断线重连 |
| `amqp` | RabbitMQ | 发布确认、消费 ack/nack、DLQ、重连 |
| `kafka` | Kafka | 生产、消费组、手动提交、重平衡重复 |
| `storage` | MinIO / 本地 | Put/Get/List/Sign/分片、路径穿越 |
| `sms` | 厂商 sandbox 或 mock server | 成功、部分失败、限流、超时 |
| `mail` | MailHog | 发送、附件、模板、注入过滤 |
| `nacos` | Nacos | 配置读取、监听、注册、实例查询 |
| `captcha` | Redis | 一次性、过期、尝试上限 |
| `sms/verification` | Redis | 原子校验、重发间隔、锁定 |

集成测试必须可在 CI 中一键启动；不允许依赖开发者本机的固定 IP 或已存在的数据库。

### 11.3 失败注入

- Redis：用 `CLIENT KILL`、`DEBUG SLEEP`（仅测试环境）或 toxiproxy 模拟断连与超时。
- MySQL：`KILL CONNECTION` 模拟 2006/2013；验证只读重放与事务不重放。
- HTTP：`httptest.Server` 返回 429/503、慢响应、超大 body、连接重置。
- AMQP/Kafka：重启 broker 容器，验证重连与至少一次语义。
- 时间：`clock.Fake` 验证 TTL、租约、限流窗口、JWT 过期。

### 11.4 安全测试

- JWT：`alg=none`、算法混淆、缺失 `exp`、错误 `aud`、超长 token。
- 加密：篡改 nonce/ciphertext/tag、错误 AAD、未知 kid、短密钥。
- 存储：`../`、绝对路径、符号链接、超长 key、超大对象。
- HTTP：重定向跨主机携带 token、日志脱敏、响应截断。
- 短信/邮件：CRLF 注入、超长主题、非法地址、超量收件人。
- 验证码：暴力尝试、一次性消费、并发校验。

### 11.5 并发与泄漏

- 所有包在 `-race` 下运行。
- 长驻 goroutine（`queue.Consumer`、`pubsub`、`nacos.Watch`、`events`）用 `go.uber.org/goleak` 验证关闭后无泄漏。
- 关闭测试：`Close` 后调用返回 `ErrClosed`；重复 `Close` 返回 `nil`。
- 基准测试覆盖热路径：`redis.DoRead`、`ratelimit.Allow`、`encrypt.Encrypt`、`jwt.Parse`、`cache.Get`。

### 11.6 覆盖率目标

| 层级 | 语句覆盖率 |
| --- | --- |
| T0 基础包 | ≥ 85% |
| T1 基础设施 | ≥ 75% |
| T2 驱动包 | ≥ 60%（主要覆盖参数映射与错误分支，SDK 内部不测） |
| 整体 | ≥ 70% |

覆盖率是下限，不是目标；关键错误分支必须有独立用例。

---

## 12. CI、版本与发布

### 12.1 CI 流水线

```text
ci.yml（每次 push / PR）
├── gofmt / goimports 检查
├── go vet ./...
├── golangci-lint run
├── go test -race ./...
├── go test -run Example ./...        # 示例可运行
├── depcheck                          # 禁止反向依赖与跨层 import
├── govulncheck ./...
└── 构建 examples/quickstart

integration.yml（ nightly / 手动）
├── testcontainers 启动 Redis/MySQL/RabbitMQ/Kafka/MinIO/Mongo/Nacos
├── go test -tags=integration ./...
└── 失败注入用例
```

规则：

- PR 必须通过 `ci.yml` 才能合并。
- 依赖升级单独 PR，附 `go mod tidy` 与集成测试结果。
- 不允许在测试中使用真实云厂商凭据；需要时用 mock server 或 sandbox 并加密注入。

### 12.2 版本与稳定性

- 语义化版本：`vMAJOR.MINOR.PATCH`。
- 稳定性分级写入 README：

| 级别 | 含义 | 兼容承诺 |
| --- | --- | --- |
| `stable` | 生产可用 | 同一 major 内不破坏 API；弃用至少保留一个 minor |
| `beta` | 可用但可能调整 | 允许 minor 内小范围破坏，需 CHANGELOG 说明 |
| `experimental` | 试验 | 不承诺兼容 |

v1 建议分级：

- `stable`：`redis`、`db`、`cache`、`lock`、`ratelimit`、`idgen`、`jwt`、`encrypt`、`password`、`validation`、`clock`、`health`、`httpclient`；
- `beta`：`queue`、`storage`、`sms`、`mail`、`captcha`、`events`、`otelx`；
- `experimental`：`amqp`、`kafka`、`mongo`、`nacos`、`imagex`、`qrcode`、`barcode`、`excel`、`datahub`、`sysinfo`、`oauth`。

### 12.3 弃用流程

1. 在 CHANGELOG 标注 `Deprecated`，给出替代 API。
2. 代码注释 `// Deprecated: use X instead.`，IDE 会提示。
3. 至少保留一个 minor 版本；下一个 major 删除。
4. 删除前用 `grep` 确认仓库内示例与文档已更新。

### 12.4 Go 版本支持

- 跟随 gofy：当前 `go 1.26.0`。
- 最低支持 Go 1.26；CI 使用 1.26 验证。
- 不使用实验性语言特性；泛型仅用于集合、分页、结果类型。

### 12.5 发布清单

- [ ] CHANGELOG 更新，包含破坏性变更与迁移说明
- [ ] README 模块表与稳定性分级更新
- [ ] 示例编译通过
- [ ] 集成测试通过
- [ ] `govulncheck` 无高危
- [ ] 打 tag `vX.Y.Z`
- [ ] 通知业务方升级注意事项

---

## 13. 实施计划

### 13.1 阶段划分

按“先底座、再安全、再消息、再媒体、最后可选”的顺序推进。每个阶段必须可独立发布，不把半成品合入主干。

| 阶段 | 周期（1 人） | 范围 | 退出标准 |
| --- | --- | --- | --- |
| P0 骨架与底座 | 2 周 | 仓库骨架、`internal/errs`、`internal/retry`、`internal/redact`、`clock`、`health`、`db`、`redis`、`httpclient` | 可在 order-service 中替换现有直连；单测、集成测试、CI 通过 |
| P1 安全与并发 | 2 周 | `jwt`、`encrypt`、`password`、`validation`、`cache`、`lock`、`ratelimit`、`idgen` | 登录、鉴权、限流、分布式锁可用；安全测试通过 |
| P2 消息与存储 | 2.5 周 | `queue`、`pubsub`、`amqp`、`kafka`、`storage`、`sms` + `verification`、`mail`、`captcha`、`otelx` | 订单异步链路、文件上传、短信验证码可用；故障注入通过 |
| P3 媒体与可选 | 2.5 周 | `mongo`、`datahub`、`imagex`、`qrcode`、`barcode`、`excel`、`nacos`、`events`、`sysinfo` | 按业务实际需求启用；没有需求则保持 experimental |
| 硬化与发布 | 1 周 | 文档、示例、基准、依赖审计、v1.0.0 发布 | 验收清单全部通过 |

两人并行时约 6 周；一人约 10 周。P3 可以延后，不阻塞 v1 发布。

### 13.2 依赖关系

```text
P0 ──> P1 ──> P2 ──> P3
 │       │
 │       └── sms/verification 依赖 ratelimit + cache
 └── storage/fake 供后续模块测试使用
```

- P0 的 `redis` 是 P1/P2 多个模块的基础，必须先稳定。
- P1 的 `ratelimit` 与 `cache` 是 `sms/verification`、`captcha` 的前置。
- P2 的 `queue` 是 `mail` 批量发送、`sms` 异步发送的推荐承载。
- `otelx` 可在 P2 接入 HTTP/Redis/GORM 追踪，不阻塞业务功能。

### 13.3 首个接入服务

用 `examples/demo-service` 作为首个验证对象，顺序：

1. 用 `redis` + `lock` 替换订单号生成/防重逻辑中的临时实现。
2. 用 `db` 的分页与租户封装替换手写分页。
3. 用 `storage` 承载订单附件上传。
4. 用 `queue` 承载订单超时关闭（替代进程内定时器）。
5. 用 `sms/verification` 承载登录验证码。

每替换一处，保留原有测试作为回归；不一次性重写业务。

### 13.4 里程碑

| 里程碑 | 标志 |
| --- | --- |
| M0 | 仓库可编译，CI 绿灯，`redis`/`db` 可用 |
| M1 | 安全组件齐备，order-service 登录链路切换完成 |
| M2 | 队列与存储可用，异步订单链路切换完成 |
| M3 | v1.0.0 发布，文档与示例完整 |

---

## 14. 验收标准

### 14.1 跨切面验收

- [ ] 全仓库 `go build ./...`、`go vet ./...`、`golangci-lint run` 通过。
- [ ] `go test -race ./...` 通过。
- [ ] 无 `init()` 副作用；`grep -r "func init()"` 只允许出现在测试或第三方依赖说明中。
- [ ] 无包级可变单例；`grep` 检查 `var .*\\*Client` 等模式。
- [ ] 无 `context.Background()` 用于请求路径；只允许在 `main` 或测试。
- [ ] 所有 I/O 方法接收 `context.Context`，`nil` 返回 `ErrNilContext`。
- [ ] 所有 `Close` 幂等，关闭后调用返回 `ErrClosed`。
- [ ] 依赖边界检查通过：组件库不 import `gofy`、`gofy-interface-service`。
- [ ] T0/T1 不 import T2 驱动包。
- [ ] 敏感字段脱敏测试通过。
- [ ] `govulncheck` 无高危漏洞。

### 14.2 模块验收

| 模块 | 验收条件 |
| --- | --- |
| `db` | 事务提交/回滚/嵌套/panic 回滚；分页边界；租户缺失失败关闭；只读重放且写不重放；事务内不重放 |
| `redis` | 三种模式连接；只读白名单重放一次；写命令不重放；`Raw` 行为有文档；`Nil` 正确暴露 |
| `cache` | 未命中、损坏、超大值、批量 TTL、`GetOrLoad` 并发合并、未命中缓存 |
| `lock` | 互斥、超时、续期、错误 token 不释放、并发竞争、`ctx` 取消 |
| `ratelimit` | 窗口边界、令牌补充、并发原子性、失败关闭/开放、`RetryAfter` 正确 |
| `idgen` | 并发唯一、单调、时间回拨策略、批量、Redis 故障不返回重复 ID |
| `jwt` | 算法白名单、`exp/iss/aud`、leeway、kid 轮换、超长 token、`alg=none` 拒绝 |
| `encrypt` | GCM 往返、AAD 不匹配失败、篡改失败、密钥轮换、短密钥拒绝 |
| `password` | bcrypt/argon2id 往返、`NeedsRehash`、超长密码拒绝、错误哈希 |
| `validation` | 自定义规则、字段名与 JSON tag 一致、消息 tag 生效、并发安全 |
| `queue` | ack、失败重试、DLQ、pending 认领、延迟调度、重复投递、panic 处理 |
| `pubsub` | 订阅/取消、并发上限、断线重连、至多一次语义文档 |
| `amqp` | 发布确认、ack/nack、DLQ、延迟队列、重连 |
| `kafka` | 生产、消费组、手动提交、重平衡重复、关闭 |
| `storage` | 四驱动基础操作、预签名、分片、路径穿越、超大对象、fake 驱动 |
| `sms` | 成功/部分失败/限流/超时、手机号校验、脱敏、验证码原子校验 |
| `mail` | 发送、附件、模板转义、CRLF 注入、大小限制 |
| `captcha` | 一次性、过期、尝试上限、并发校验 |
| `imagex` | 解码限制、缩放/裁剪/水印、元数据剥离、格式白名单 |
| `qrcode`/`barcode` | 内容长度、字符集、校验位、SVG 转义、尺寸限制 |
| `excel` | 流式导出/导入、表头映射、行数/大小限制、公式注入防护 |
| `httpclient` | 超时、重试策略、幂等键、响应截断、脱敏、跨主机重定向 |
| `nacos` | 配置读取/监听、注册/注销、上下文取消、错误不返回空值 |
| `otelx` | no-op、采样、shutdown 刷新、传播器 |
| `events` | 同步顺序、异步队列、panic 恢复、关闭排空 |
| `health` | 并行检查、超时、关键/非关键状态 |

### 14.3 文档验收

- [ ] 每个包有包注释，说明职责、默认值、并发安全、关闭语义。
- [ ] README 模块表与稳定性分级完整。
- [ ] 至少一个可运行示例（`examples/quickstart`）。
- [ ] 所有配置项在文档中有默认值与取值范围。
- [ ] 破坏性变更在 CHANGELOG 有迁移说明。

---

## 15. 风险与开放问题

### 15.1 风险

| 风险 | 可能性 | 影响 | 缓解 |
| --- | --- | --- | --- |
| 单 module 依赖体积过大 | 中 | 中 | 包级隔离 + 模块图裁剪；必要时拆分嵌套 module |
| 云厂商 SDK 体积/维护状态 | 中 | 中 | driver 子包隔离；优先官方 SDK；锁定版本 |
| 关闭 go-redis 自动重试导致行为变化 | 中 | 中 | 文档强调 `Do`/`DoRead`；集成测试覆盖 |
| GORM 租户回调对 `Raw` 无效 | 高 | 高 | 明确规则；Code Review；辅助检查；关键查询不用 Raw |
| 多实例时钟漂移影响限流/ID | 中 | 高 | 限流用 Redis TIME；ID 用 Redis 时间或雪花；监控回拨 |
| 单 Redis 锁在主从切换时双持有 | 中 | 高 | 文档声明；强一致场景用数据库行锁 + fencing token |
| Redis Streams 与现有 List 队列不兼容 | 高 | 中 | 新老队列并行迁移；消费端幂等；提供重放工具 |
| 队列至少一次导致重复处理 | 高 | 中 | 强制幂等；DLQ；去重键 |
| 图片/Excel 内存放大 | 中 | 中 | 解码前限制尺寸；流式读写；并发控制 |
| OTel 全局 provider 与“无全局”原则冲突 | 高 | 低 | 明确例外；只在 main 设置一次 |
| zap 与 slog 双日志栈 | 高 | 低 | 提供适配器；逐步统一 |
| 短信/邮件厂商 API 变更 | 中 | 中 | 网关接口隔离；集成测试；版本锁定 |
| 组件库版本与 gofy 版本漂移 | 中 | 中 | 零依赖设计；CI 中不互相引用 |
| 集成测试依赖容器，CI 变慢 | 中 | 低 | nightly + 手动触发；单测不依赖容器 |

### 15.2 开放问题

| # | 问题 | 备选方案 | 建议 |
| --- | --- | --- | --- |
| Q1 | module 路径用 `github.com/bingcool/gofy-library` 还是 `github.com/bingcool/gofy/library` | 独立仓库 / 子目录 | 独立仓库，避免 gofy 依赖膨胀 |
| Q2 | 是否把 MySQL 连接创建也迁到组件库 | 保留在 gofy / 迁入 | 保留在 gofy；组件库只操作 `*gorm.DB` |
| Q3 | `db.Read` 的只读契约是否需要用类型强制 | 文档契约 / 只读副本连接 | v1 文档契约；后续可提供 `Reader()` 绑定只读 DSN |
| Q4 | 队列是否同时保留 List 实现兼容旧数据 | 只做 Streams / 双实现 | 只做 Streams；迁移期用独立消费脚本处理旧 List |
| Q5 | Kafka 客户端选 `segmentio/kafka-go` 还是 `confluent-kafka-go` | 纯 Go / cgo | `segmentio/kafka-go`，部署简单优先 |
| Q6 | 图片库选 `imaging` 还是 `govips` | 纯 Go / libvips | 默认 `imaging`；高性能场景后续加 `govips` driver |
| Q7 | 是否提供 gofy 中间件适配包 | 业务自写 / 组件库提供 | v1 业务自写 10 行；后续视重复度再抽 |
| Q8 | 组件库日志统一 slog 还是自定义接口 | slog / 自定义 | slog；zap 适配由业务或 gofy 侧完成 |
| Q9 | `sms/verification` 是否属于组件库 | 组件库 / 业务 | 组件库，多个服务会重复实现 |
| Q10 | 是否支持 Redis Cluster 的跨 slot 脚本 | 限制同 slot / 拆分 | 所有多 key 脚本使用 hash tag，限制同 slot |

---

## 16. 附录

### 16.1 包速查

| 包 | 构造 | 关闭 | 健康检查 | 主要依赖 |
| --- | --- | --- | --- | --- |
| `db` | `db.New(*gorm.DB, Config)` | 不拥有连接 | `Ping(ctx)` | GORM |
| `redis` | `redis.New(Config)` | `Close()` | `Ping(ctx)` | go-redis |
| `cache` | `cache.New(*redis.Client, Config)` | 随 Redis | — | go-redis |
| `lock` | `lock.New(*redis.Client, Config)` | 随 Redis | — | go-redis |
| `ratelimit` | `NewSlidingWindow` / `NewTokenBucket` | 随 Redis | — | go-redis |
| `idgen` | `NewRedis` / `NewSnowflake` | 随 Redis | — | go-redis |
| `queue` | `NewProducer` / `NewConsumer` | `Close()` | — | go-redis |
| `pubsub` | `NewSubscriber` | 随 Redis | — | go-redis |
| `amqp` | `NewPublisher` / `NewConsumer` | `Close()` | — | amqp091-go |
| `kafka` | `NewProducer` / `NewConsumer` | `Close()` | — | kafka-go |
| `mongo` | `New(ctx, Config)` | `Close(ctx)` | `Ping(ctx)` | mongo-driver |
| `jwt` | `jwt.New(Config)` | — | — | golang-jwt |
| `encrypt` | `encrypt.New(Config)` | — | — | 标准库 |
| `password` | `password.New(Config)` | — | — | x/crypto |
| `captcha` | `captcha.New(*redis.Client, Config)` | 随 Redis | — | 图像库 + Redis |
| `validation` | `validation.New()` | — | — | validator |
| `httpclient` | `httpclient.New(Config)` | `Close()` | — | 标准库 |
| `nacos` | `nacos.New(Config)` | `Close()` | 可扩展 | nacos-sdk-go |
| `storage` | `storage.New(Config)` | 可选 `io.Closer` | `Check(ctx)` | 云 SDK |
| `sms` | `sms.New(Config)` | — | — | 厂商 SDK |
| `mail` | `mail.New(Config)` | — | — | go-mail |
| `imagex` | `imagex.New(Config)` | — | — | imaging |
| `qrcode` | `qrcode.New(Config)` | — | — | QR 库 |
| `barcode` | `barcode.New(Config)` | — | — | barcode 库 |
| `excel` | 函数式 | — | — | excelize |
| `events` | `events.New(Config)` | `Close(ctx)` | — | 标准库 |
| `otelx` | `Setup(ctx, Config)` | `shutdown(ctx)` | — | otel-go |
| `clock` | `clock.System{}` / `NewFake` | — | — | 标准库 |
| `health` | `health.NewRegistry(timeout)` | — | 自身 | 标准库 |

### 16.2 补充配置示例

```yaml
httpclient:
  base_url: https://api.example.com
  timeout: 10s
  connect_timeout: 3s
  max_idle_conns: 100
  max_response_bytes: 10485760
  follow_redirects: true
  retry:
    max_attempts: 2
    base_delay: 100ms
    max_delay: 2s
    retry_on_5xx: true

storage:
  driver: s3
  key_prefix: order
  max_object_bytes: 104857600
  allowed_types:
    - image/jpeg
    - image/png
    - application/pdf
  s3:
    region: ap-east-1
    bucket: example-bucket
    endpoint: https://s3.example.com
    use_path_style: true
    access_key: ${S3_ACCESS_KEY}
    secret_key: ${S3_SECRET_KEY}

mail:
  default: smtp
  max_recipients: 50
  max_attachment_bytes: 10485760
  template_dir: ./templates/mail
  mailers:
    smtp:
      host: smtp.example.com
      port: 587
      username: ${MAIL_USER}
      password: ${MAIL_PASSWORD}
      from: no-reply@example.com
      start_tls: true

nacos:
  hosts:
    - 127.0.0.1:8848
  namespace: order
  group: DEFAULT_GROUP
  timeout: 5s

otel:
  service_name: order-service
  service_version: 1.0.0
  endpoint: http://otel-collector:4318
  insecure: true
  sample_ratio: 0.1
```

### 16.3 关键 Lua 脚本

#### 滑动窗口限流

```lua
-- KEYS[1] = rate key
-- KEYS[2] = sequence key（与 KEYS[1] 同 hash tag）
-- ARGV[1] = limit
-- ARGV[2] = window(ms)
-- ARGV[3] = n
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local n = tonumber(ARGV[3])

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)
if count + n > limit then
  local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
  local retry = 0
  if oldest[2] then
    retry = tonumber(oldest[2]) + window - now
    if retry < 0 then retry = 0 end
  end
  return {0, count, retry}
end

local seq = redis.call('INCR', KEYS[2])
for i = 1, n do
  redis.call('ZADD', key, now, now .. ':' .. seq .. ':' .. i)
end
redis.call('PEXPIRE', key, window * 2)
redis.call('PEXPIRE', KEYS[2], window * 2)
return {1, count + n, 0}
```

#### 令牌桶

```lua
-- KEYS[1] = bucket key
-- ARGV[1] = capacity
-- ARGV[2] = rate (tokens/second)
-- ARGV[3] = n
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local n = tonumber(ARGV[3])

local t = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

local delta = now - ts
if delta < 0 then delta = 0 end
tokens = math.min(capacity, tokens + delta * rate)

local allowed = 0
if tokens >= n then
  tokens = tokens - n
  allowed = 1
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
local ttl = math.ceil(capacity / rate * 2)
if ttl < 60 then ttl = 60 end
redis.call('EXPIRE', key, ttl)
return {allowed, math.floor(tokens), ttl}
```

#### 锁释放与续期

```lua
-- 释放
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0

-- 续期
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
```

#### 分布式 ID

```lua
-- KEYS[1] = 基础 key
-- ARGV[1] = 每毫秒最大序号
-- ARGV[2] = key TTL(ms)
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local key = KEYS[1] .. ':' .. now
local seq = redis.call('INCR', key)
redis.call('PEXPIRE', key, ARGV[2])
if seq > tonumber(ARGV[1]) then
  return {0, seq}
end
return {now, seq}
```

客户端在 `{0, seq}` 时等待下一毫秒重试，最多重试 3 次；仍失败返回错误，不返回可能重复的 ID。

#### 验证码校验

```lua
-- KEYS[1] = code key
-- KEYS[2] = attempts key
-- ARGV[1] = 输入值的 HMAC
-- ARGV[2] = 最大尝试次数
-- ARGV[3] = attempts TTL(秒)
local stored = redis.call('GET', KEYS[1])
if not stored then
  return 'absent'
end

local attempts = tonumber(redis.call('GET', KEYS[2]) or '0')
if attempts >= tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1], KEYS[2])
  return 'locked'
end

if stored ~= ARGV[1] then
  attempts = redis.call('INCR', KEYS[2])
  redis.call('EXPIRE', KEYS[2], ARGV[3])
  if attempts >= tonumber(ARGV[2]) then
    redis.call('DEL', KEYS[1], KEYS[2])
    return 'locked'
  end
  return 'mismatch'
end

redis.call('DEL', KEYS[1], KEYS[2])
return 'ok'
```

#### 延迟队列调度

```lua
-- KEYS[1] = delay zset
-- KEYS[2] = stream
-- ARGV[1] = now(ms)
-- ARGV[2] = batch
local due = redis.call('ZRANGEBYSCORE', KEYS[1], 0, ARGV[1], 'LIMIT', 0, ARGV[2])
for _, member in ipairs(due) do
  redis.call('XADD', KEYS[2], '*', 'payload', member)
  redis.call('ZREM', KEYS[1], member)
end
return #due
```

脚本内 `XADD` 与 `ZREM` 原子执行，避免“入流成功但删除失败”导致重复；即使客户端崩溃，也不会两者只做一半。

### 16.4 错误到 HTTP/业务码的映射建议

组件库不直接决定 HTTP 状态码；业务在 handler 或统一错误中间件中映射：

| 组件错误 | HTTP | 业务码建议 | 说明 |
| --- | --- | --- | --- |
| `ErrInvalid` | 400 | 40001 | 参数非法 |
| `ErrNotFound` | 404 | 40400 | 资源不存在 |
| `ErrConflict` | 409 | 40900 | 并发冲突/重复 |
| `ErrForbidden` | 403 | 40300 | 无权限 |
| `ErrUnauthorized` | 401 | 40100 | 未认证 |
| `ErrTooManyRequests` | 429 | 42900 | 限流 |
| `ErrTimeout` | 504 | 50300 | 上游超时 |
| `ErrUnavailable` | 503 | 50300 | 依赖不可用 |
| 其他未分类错误 | 500 | 50000 | 内部错误，不暴露细节 |

业务码段以 `gofy-interface-service/contracts/common/errcode` 为准；组件库只提供语义分类。

### 16.5 参考

- swoolefy library 源码：`/Users/huangzengbing/Documents/wwwroot/library`
- swoolefy InterfaceApi 文档：`/Users/huangzengbing/Documents/wwwroot/swoolefy/docs/InterfaceApi.md`
- gofy 框架设计：[library-design.md](./library-design.md)
- gofy 契约设计：[gofy-interface-service-design.md](./gofy-interface-service-design.md)
- go-redis：https://github.com/redis/go-redis
- GORM：https://gorm.io/docs/
- golang-jwt：https://github.com/golang-jwt/jwt
- OpenTelemetry Go：https://opentelemetry.io/docs/languages/go/

---

> 本文是 `gofy-library` 的实施基线。新增包、修改公共 API、调整依赖分层时，必须同步更新本文与 CHANGELOG；实现与文档冲突时，以本文的决策表为准，先改文档再改代码。
