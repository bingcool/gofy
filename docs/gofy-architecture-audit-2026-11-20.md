# Gofy 三仓库新一轮代码审计报告

**审计基线：2026-11-20**\
**审计范围：**
`bingcool/gofy`、`bingcool/gofy-interface-api`、`bingcool/gofy-adapter`\
**审计原则：** 不新增功能，重点发现现有代码中的
Bug、边界问题、生命周期、并发、安全及架构约束问题。

## 1. 审计基线

| 仓库 | HEAD |
| --- | --- |
| `bingcool/gofy` | `576b2256a908c23af44a4cdb1e20f252a69256b2` |
| `bingcool/gofy-interface-api` | `5394a8121e5f06b66920b4bfe1baded270cbd503` |
| `bingcool/gofy-adapter` | `a33d782f3f32b99477de56381d2fe734d80243b2` |

本轮 `gofy` 有新提交；另外两个仓库的 HEAD
与上一轮审计基线相同。报告基于当前 GitHub
源码静态审查，未在实际工作树中运行
`go test -race`、集成测试或完整构建。问题定性应结合修复后的针对性测试确认。

## 2. 上一轮问题修复情况

### 2.1 IO Module 资源关闭顺序：已修复

涉及 `examples/demo-service/app/appio/io_runtime.go` 和对应测试。

现在通过 `namedCloser` 记录资源打开顺序，并由 `closeAll()`
反向释放。顺序为：

``` text
Start: DB → Redis → AMQP → Kafka → RocketMQ → UUID Pool → RedisLock
Stop:  RedisLock → UUID Pool → RocketMQ → Kafka → AMQP → Redis → DB
```

该顺序符合资源依赖关系。UUID Pool
在启动前登记清理动作，也能覆盖部分启动失败场景。

### 2.2 UUID Pool 增加 Context 关闭能力：已修复，超时边界仍需检查

涉及
`support/uuidint/pool.go`、`examples/demo-service/app/appio/io_runtime.go`、`reload.go`。增加
`StopPoolContext(ctx)`，使关闭等待受 Context
约束。超时后后台任务是否仍可能访问 Redis，见第 3.8 节。

### 2.3 Consumer 默认数据库依赖启动检查：已修复

涉及
`support/worker/dbrequirement.go`、`worker_host.go`、`support/consumer/consume.go`。通过
`DefaultDBRequirer` / `RequiresDefaultDB()` 声明 Consumer
对默认数据库的依赖，并在 Worker 启动前检查。该方式没有强制所有 Worker
都配置默认数据库，保留了仅使用命名数据库实例的部署方式。

### 2.4 SQL Logger 包依赖方向：已修复

SQL 日志实现迁移到 `support/sqllog`，减少数据库包对上层 Logger
的反向依赖，包职责和依赖方向更合理。

## 3. 本轮发现的问题

### 3.1 P1：SQL 脱敏默认规则可被外部修改

**文件：** `support/sqllog/sqlmask.go`

当前默认规则通过导出包级变量提供，例如
`DefaultMaskColumns`、`DefaultMaskPatterns`。内部 `defaultMaskSet()`
会读取这些变量。

由于变量及其底层切片可由外部包修改，业务代码可以清空或替换默认规则；并发修改还可能产生数据竞争。这会使框架内置的安全基线受到外部可变状态影响。

**建议：** 1. 将内部默认规则改为包内私有变量。 2.
内部策略构造只读取私有规则。 3. 如需对外展示默认值，提供返回副本的函数。
4. 增加测试，验证外部修改规则副本不会影响内部策略。

**优先级：P1。**

### 3.2 P1：AuthRequired 声明不保证实际认证中间件已安装

**涉及：**
`gofy-adapter/adapter/gofy/bind.go`、`gofy-interface-api/contracts/common/endpoint/endpoint.go`

契约层保留 `AuthNone`、`AuthRequired`、`AuthOptional`。代码注释明确
`AuthMode` 用于契约、OpenAPI 和可选启动校验，Adapter 不会因为
`AuthRequired` 自动安装认证中间件。

当前 `validateAuth()` 在 `opts.AuthValidator == nil`
时不会执行实际认证声明校验。直接调用 `Bind()`
只安装显式传入的中间件，也不会执行 `BindAll()` 的认证声明校验。

可能出现：

``` text
路由声明 AuthRequired
        ↓
忘记 group.Use("auth") 或路由 Middleware
        ↓
没有配置 AuthValidator
        ↓
路由仍然正常绑定
        ↓
实际请求没有认证保护
```

这里的问题不是 Adapter
没有自动认证，而是生产环境可能没有发现契约声明与运行时 Middleware
之间的不一致。

**建议：** 1. 生产应用使用 `AuthRequired` / `AuthOptional`
时，应要求配置 `AuthValidator`，或执行等价启动校验。 2. 对直接使用
`Bind()` 的场景，明确其不会验证契约认证声明。 3.
为需要认证的路由增加装配测试，验证 Middleware 确实安装并生效。 4.
统一三个仓库的认证使用规范，避免 Auth 元数据与 Middleware
声明形成两套容易混淆的表达。

**优先级：P1（条件性安全风险）。**
如果应用层已经通过其他方式强制保证认证中间件安装，风险可降级；但不能仅凭
`AuthRequired` 就认定运行时已受保护。

### 3.3 P2：Consumer Schema 初始化使用 `context.Background()`

**文件：** `support/consumer/consume.go`

启用幂等功能时，`NewProcessor()` 调用：

``` go
p.deduper.ensureSchema(context.Background())
```

该初始化发生在 Consumer 启动生命周期内，却使用脱离 Worker 生命周期的
Context。Worker 开始 Shutdown 时，Schema
初始化不会收到取消信号。数据库连接或 DDL 长时间阻塞时，可能拖延
Consumer/Worker 退出。

**建议：** 1. 将 Consumer 启动 Context 传递给 Schema 初始化。 2. 为
Schema 初始化增加合理超时。 3. 确认底层数据库操作能够响应 Context。 4.
明确 `NewProcessor()` 的 Context
语义，避免初始化路径重新使用无生命周期约束的 Background Context。

**优先级：P2。**

### 3.4 P2：AMQP / Kafka Producer 合并 Headers 时修改调用方 Map

**涉及：**
`support/amqp/queue.go`、`support/kafka/producer.go`、`support/headerpropagation/headerpropagation.go`

`PublishOptions` 虽然按值传递，但 `Headers` 是 Map。Producer 合并 Trace
Header 时直接写入 `opts.Headers`，实际上会修改调用方传入的 Map。

**风险：** - Producer 对调用方数据产生隐式副作用。 - 调用方在其他
goroutine 复用该 Map 时可能产生数据竞争。 - 同一个 Map
多次复用时可能出现难以预期的状态变化。

**建议：** Producer 内部先复制 Headers，再合并 Trace
Header；继续保留调用方显式设置的 Trace Header 优先级更高这一语义。

**优先级：P2。**

### 3.5 P2：SQLLog Config 通过 Go 结构体构造时，Enabled 语义可能与 YAML 不一致

**文件：** `support/sqllog/sqlpolicy.go`

`ResolveEnabled()` 通过 YAML 解析时记录的 `keys` 判断是否存在非
`enabled` 配置项。如果业务直接构造：

``` go
cfg := sqllog.Config{Mode: "full"}
```

此时 `keys` 和 `Enabled` 都可能是零值，`ResolveEnabled()` 返回
`false`。这会造成 YAML 配置和程序化构造配置的行为不一致。

**建议：** - 如果程序化构造必须显式设置
`Enabled`，在类型注释和示例中明确说明。 - 如果 `Mode`
等非零配置项应自动启用，则调整配置模型，使 YAML 与 Go
结构体构造行为一致。 - 增加两种配置方式的等价测试。

**优先级：P2。**

### 3.6 P2：SQL 脱敏扫描器对 INSERT 列名与 VALUES 的关联能力不足

**文件：** `support/sqllog/sqlmask.go`

当前扫描器主要通过 `lastIdent` 判断当前值对应的字段。对于：

``` sql
SELECT * FROM users WHERE phone = '13800138000'
```

字段和值之间关系较直接。但对于：

``` sql
INSERT INTO users (employee_no, remark)
VALUES ('ORD-20261119001', 'customer-private-information')
```

扫描器经过 `VALUES`
时会清理之前的标识符状态，无法继续根据列名判断后续值属于哪个字段。

默认手机号、邮箱、身份证、银行卡、长 Token
等模式仍可能被识别，但自定义敏感字段、无法由默认正则识别的业务隐私数据，不能依赖列名关联完成脱敏。

**建议：** 1. 明确 `WHERE`、`IN`、普通 INSERT、批量 INSERT
的字段和值关联支持范围。 2. 如果不准备引入完整 SQL Parser，不要宣称所有
SQL 形态都能按字段名脱敏。 3. 对无法可靠识别的 SQL
形态明确脱敏边界，必要时采取更保守的日志策略。 4. 增加普通 INSERT、批量
INSERT、自定义敏感列测试。

**优先级：P2，数据安全边界问题。**

### 3.7 P2：IO Module 启动失败时，回滚清理错误被丢弃

**文件：** `examples/demo-service/app/appio/io_runtime.go`

启动失败时存在类似：

``` go
_ = closeAll(ctx, opened)
return err
```

如果启动某个资源失败，回滚关闭之前已打开的资源也失败，清理错误会被丢弃。正常
`Stop()` 已聚合错误，但启动失败回滚路径没有统一处理。

**建议：**

``` go
cleanupErr := closeAll(ctx, opened)
return errors.Join(err, cleanupErr)
```

保留原始启动错误，同时允许调用方通过 `errors.Is()` / `errors.As()`
检查清理错误。

**优先级：P2。**

### 3.8 P2：UUID Pool 关闭超时后，后台任务可能与 Redis 关闭发生竞态

**涉及：**
`support/uuidint/pool.go`、`examples/demo-service/app/appio/io_runtime.go`

增加 `StopPoolContext(ctx)` 是正确方向，但需要检查如下超时路径：

``` text
StopPoolContext(ctx)
       ↓
等待 ticker / refill 退出
       ↓
Context 超时
       ↓
返回错误，后台任务可能尚未完全退出
       ↓
closeAll 继续执行
       ↓
关闭 Redis
```

此时 UUID Pool 后台任务可能仍在执行 Redis
操作，而资源释放流程已经开始关闭 Redis Client。不能仅因为
`StopPoolContext()` 返回超时，就认为后台任务已经停止。

**建议重点检查：** 1. `done`、停止信号与 `refill()` 的并发交互。 2. Pool
超时后，后台任务是否可能继续写入 Buffer。 3. Redis Client
关闭后，后台任务是否还可能发起新的 Redis 操作。 4.
超时退出路径是否可能触发重复错误回调或重复释放。 5. Buffer
关闭与生产者写入之间是否有明确同步关系。

如果关闭预算耗尽后仍允许继续释放依赖资源，需要确保后台任务能安全处理依赖资源已经关闭的情况。

**优先级：P2。**
这是需要通过竞态测试和压力测试确认的边界问题，当前不宜仅凭静态阅读认定必然发生。

### 3.9 P2：AccessLog 默认记录 Query String，黑名单脱敏存在遗漏风险

**文件：** `support/middleware/accesslog.go`

当前默认配置仍为
`LogQuery: true`，并通过敏感参数黑名单脱敏。黑名单只能覆盖已知参数名。业务若使用未列入黑名单的参数名，例如自定义手机号、邮箱、证件号或银行账户字段，就可能被记录到访问日志。

**建议：** 1. 生产默认关闭 Query String 记录。 2.
确实需要记录时优先采用白名单。 3. 对显式开启 Query
记录增加配置说明和测试。 4. 不要仅依赖持续扩充黑名单来保证隐私安全。

**优先级：P2。** 上一轮已提出，本轮仍未解决。

## 4. 另外两个仓库的复查结果

### 4.1 gofy-interface-api

当前 HEAD：`5394a8121e5f06b66920b4bfe1baded270cbd503`

本轮没有新提交，重点复核契约层与客户端边界：

| 检查点 | 当前评价 |
| --- | --- |
| Route Spec 切片可变性 | 文档已说明 `Spec` 返回值中的切片与调用方共享，声明后应只读 |
| SSE 行和事件大小限制 | 上一轮增加的内存保护方向正确 |
| POST 幂等重试 | 上一轮对结构体 `Idempotency-Key` 的识别修复仍然有效 |
| 流式请求超时 | 仍应明确整体流生命周期由谁控制 |
| PageRequest Offset | 仍需关注客户端与服务端对负数 `Offset` 的处理一致性 |

本轮没有发现足够证据支持新增一个确定性的 P1
问题。流式生命周期与分页参数属于后续应继续验证的边界项，不应在没有复现前直接定性为
Bug。

### 4.2 gofy-adapter

当前 HEAD：`a33d782f3f32b99477de56381d2fe734d80243b2`

上一轮 Resolver 零值问题的修复仍然有效。当前对 nil Resolver、nil
client、nil cache、nil inflight 的检查能够避免未初始化状态被误用。

本轮仍需关注认证声明与实际 Middleware 安装之间的关系，详见第 3.2 节。

## 5. 建议修复顺序与当前状态

状态口径：在 `gofy@3e39da2`、`gofy-interface-api@dd1cd4a`、`gofy-adapter@76f6e25` 上逐条核对代码；
「已修复」指当前代码不再具备该问题成立的前提，而不是"已经安排了修复"。审计基线 `576b225` 之后
`gofy` 的提交（SQL 日志目录、配置按段拆文件、本轮两项与第二批两项 P2 修复）中，表格里标
「已修复」的条目都已包含在内。

| 优先级 | 问题 | 建议 | 状态 |
| --- | --- | --- | --- |
| P1 | SQL 脱敏默认规则可被外部修改 | 优先修复，保护内置安全基线 | **未修复** |
| P1 | AuthRequired 可能没有实际认证保护 | 明确生产装配约束并增加验证 | **已修复**（删除声明） |
| P2 | Consumer Schema 初始化使用 `context.Background()` | 传递 Worker Context | **已修复** |
| P2 | AMQP / Kafka 修改调用方 Headers Map | 复制后合并 | **已修复** |
| P2 | SQLLog 程序化配置启用语义不一致 | 统一配置语义 | **已修复** |
| P2 | INSERT 字段与值无法关联脱敏 | 明确能力边界并补足必要保护 | **未修复**（边界已文档化） |
| P2 | IO 启动失败清理错误被丢弃 | 使用 `errors.Join` | **已修复** |
| P2 | UUID Pool 超时后后台任务与 Redis 关闭竞态 | 检查停止与 refill 的同步 | **已修复**（在途命令仍受客户端读超时约束） |
| P2 | AccessLog 默认记录 Query String | 调整生产默认值 | **未修复** |

### 5.1 已修复项的依据

**P1 AuthRequired —— 已修复（按"删除声明"的方式，不是"补校验"）。**
三个仓库同时去掉了契约层的认证声明：`gofy` `3ee594a` 删除了 `Spec.Auth`（认证只由
`Group.Use("auth")` 表达），`gofy-interface-api` `dd1cd4a` 删除 `AuthMode` / `AuthRequired` /
`AuthOptional` / `AuthNone`，`gofy-adapter` `76f6e25` 删除 `AuthValidator` / `validateAuth`。
"路由声明 AuthRequired、运行时却没装中间件"这条不一致至此不可能再发生——契约里已经没有一个
可以声明却无法强制的字段。

残留（不构成本条问题，但值得记）：第 3.2 节建议中的第 3 条尚未落实——没有"需要认证的路由确实
安装了中间件"的装配测试。忘记写 `Use("auth")` 仍只能靠评审发现，而不是测试失败。

### 5.2 本轮新增修复的三项（P2）

对应提交 `5d177e9 fix: close three P2 findings in consumer, producers and sqllog`。

**Consumer Schema 初始化 —— 改用调用方 context 并加时限。**
`NewProcessor` 的签名改为 `NewProcessor(ctx, rt, cfg)`，`ctx` 是消费者生命周期 context，成为
台账建表的父 context；`ProcessorConfig.SchemaTimeout`（默认 `DefaultSchemaTimeout`，30s）再套
一层上限。建表改用 `context.WithTimeout(ctx, ...)`，取消与超时都能打断 DDL。回归测试三条：
已取消的父 context 必须让构造失败（`errors.Is(err, context.Canceled)`）、`SchemaTimeout` 到期
必须失败（`DeadlineExceeded`）、nil context 被拒绝。

**AMQP / Kafka Producer —— 复制后合并。**
两个生产者各自新增 `publishHeaders`：先放链路头，再覆盖调用方的值（保持"调用方优先"），
返回的是新 map/table；没有可新增的项时原样返回，避免多一次分配。`opts.Headers` 不再被写入，
调用方复用同一个 map 也不会与发布并发冲突。两侧各三条测试：不改调用方、调用方优先、无链路
上下文时是 no-op。

**SQLLog 启用语义 —— YAML 与代码构造统一。**
`ResolveEnabled` 增加第三条规则：没有 YAML 键可数时（即代码里构造的 `Config`），任一非零字段
即视为启用，`Config{Mode: "full"}` 与 `sql_log: {mode: full}` 从此同解。为了让这条规则成立，
`Mode` 移除了 `default:"placeholder"` 标签——`config.Load` 在解码前跑 `defaults.Set`，标签会让
整段缺席的配置也被填成 `placeholder`，按值规则就会把每个部署的 SQL 日志打开；默认值改由
`normalize()` 在构造策略时补，那里才是真正需要它的地方。测试含一组"同一配置两种写法必须同解"
的等价用例。

### 5.3 第二批修复的两项（P2）

对应提交 `188f34d fix: stop discarding the IO rollback error and tighten pool shutdown`。

**IO 启动失败回滚 —— 清理错误不再丢弃。**
`Start` 的 7 个失败分支原先都是 `_ = closeAll(...); return err`，清理失败会被启动错误
盖掉——而那正是最需要知道"有连接没关掉"的时刻。现在统一走
`rollback(ctx, opened, cause) = errors.Join(cause, closeAll(...))`：`errors.Is` 仍能命中
启动原因，失败的 Close 按资源名并列报出。测试钉住两点——两个错误都保留且各占一行、
清理干净时错误文本与原始错误逐字相同（`errors.Join` 聚合 nil 不产生噪音）。

**UUID Pool 停止与 refill 的同步 —— 停止后不再产生新工作，并把"留在原地"变成可证明的安全。**
原先 `refill` 在申请批次之前既不读停止标志也不看 ctx，于是两种竞态成立：预热恰好与
`StopPoolContext` 撞上时，调用方已经被告知池已停止，它仍会访问 Redis；已经在重试循环里
的那一轮同样会继续跑完整个重试与备用序列。现在：

- `refill` 在申请批次之前检查 `stopped`，一轮在途期间到达的停止优先——那批号直接丢弃，
  不写进没人会消费的缓冲；
- `generate` 在每次尝试之前检查 ctx，取消之后不再发起新的尝试，关闭的代价因此是"一个
  命令"而不是整个重试序列；
- 新增 `WithRefillTimeout`（默认 3s）限制单轮：它限制等待连接池的时间，并让本轮到期后
  不再重试。

**边界（已写进 README 与注释，不做过度承诺）**：`WithRefillTimeout` **不能**取消已经发出的
那条命令——go-redis 只有在客户端启用 `ContextTimeoutEnabled` 时才按 ctx deadline 缩短
socket I/O，因此单条在途命令仍受客户端 `ReadTimeout` 约束，两个值应一起调大。这也正是
3.8 里"超时后 closeAll 继续关 Redis"的场景：现在的答案是它在构造上安全——缓冲只由那个
负责发送的 goroutine 关闭，停止之后它只可能读到停止标志或撞上已关闭的客户端。

**测试**（4 条，其中 2 条跑在 `-race` 下）：停止后 `refill` 连 Redis key 都不写（用计数器
证明，而不是看缓冲是否恰好为空）；已取消的 ctx 不会发起尝试；客户端在池运行中被关闭后
`StopPoolContext` 仍能收敛、`Get` 报错而不 panic；四个消费者并发取号时 start/refill/drain/
stop 无竞争。

**UUID Pool 的历史**：上一轮 `1051025` 已经解决的是"无限等待"——`StopPoolContext(ctx)` 让
超时不再挂住调用方（见 2.2）。3.8 剩下的部分这一轮才关掉：停止标志与 ctx 的检查、单轮上限、
以及"被放弃的 goroutine 只会撞上停止标志或已关闭的客户端"这条用测试钉住的性质。超时后
缓冲仍然**不关闭**——此时关闭会让遗留 goroutine 的下一次发送 panic——这一点没有改变，
只是现在它不会再往缓冲里写东西了。

### 5.4 其余项的当前状态

均与第 3 节描述一致，未被本轮任何提交触及：脱敏默认规则仍是可写的导出变量
（`support/sqllog/sqlmask.go` 的 `DefaultMaskColumns` / `DefaultMaskPatterns`）；
AccessLog 的 `LogQuery` 默认值仍是 `true`（`support/middleware/accesslog.go`）。

INSERT 脱敏需要补充一点实测结论：不只是批量 INSERT，**单行** `INSERT INTO users (employee_no,
remark) VALUES (...)` 也不会按列名脱敏——扫描器在 `VALUES` 处清空标识符状态，值原样保留；
`SELECT` / `UPDATE` 的列名关联正常。设计文档已把"按列位置映射"列为二期，代码注释也写明不宣称
支持，因此第 3.6 节建议的第 1、2 条（明确边界）已满足，第 3、4 条（更保守策略、补充测试）仍未做。

## 6. 总体评价

本轮三个仓库的整体架构没有发现需要推倒重来的问题。`gofy`
最近的调整主要改善了资源生命周期、Worker 依赖约束和包依赖方向。

值得肯定的改进：

1.  **资源生命周期更统一。** IO Module
    通过记录打开顺序并反向关闭，减少资源依赖顺序错误。
2.  **Worker 依赖约束更清晰。** `DefaultDBRequirer`
    让框架能在启动阶段发现明确声明的默认数据库依赖。
3.  **包依赖方向更合理。** SQL Logger 独立到
    `support/sqllog`，减少底层组件对上层 Logger 的反向依赖。

接下来不建议继续增加框架能力。优先处理 SQL
脱敏安全基线、认证装配约束、Consumer Context、Producer Header
副作用和资源清理错误。

尤其是 SQL 日志层，应重点验证不同 SQL
形态下的脱敏覆盖边界，避免日志系统在生产环境中成为敏感数据泄漏入口。

## 7. 修复后的验证建议

### 7.1 并发与竞态

``` bash
go test -race ./...
```

重点覆盖： - SQL 脱敏默认规则的并发读取。 - Producer Headers Map
的并发复用。 - UUID Pool Stop / refill 并发。 - IO Module 启动失败回滚与
Stop 并发边界。

### 7.2 生命周期与取消

验证： - Consumer Schema 初始化过程中取消 Worker Context。 - DB DDL
阻塞时 Consumer 能否在超时预算内退出。 - UUID Pool
超时后后台任务是否会继续访问 Redis。 - IO Module
部分资源启动成功、后续资源启动失败时，所有已打开资源是否都得到关闭。

### 7.3 安全与脱敏

验证： - 外部修改默认规则副本不会影响内部策略。 - 普通 INSERT、批量
INSERT、自定义敏感列的脱敏结果。 - Query String
中未列入黑名单的敏感参数。 - AuthRequired 路由缺少认证 Middleware
时，生产启动校验是否失败。 - 直接使用 Bind
的路由是否有明确的认证装配测试。

### 7.4 配置语义

验证 YAML 配置与 Go 结构体直接构造的 SQLLog Config
是否得到一致的启用结果。

------------------------------------------------------------------------

**审计结论：**
本轮没有建议大规模重构，也没有建议新增框架功能。优先修复报告中的 P1/P2
问题，再通过 race、生命周期和安全测试确认修复效果。
