# Gofy 全代码架构审计报告（第三轮）

-   **审计日期**：2026-11-17
-   **审计原则**：不新增功能；只识别现有实现中的
    Bug、边界条件、临界条件、生命周期问题、并发问题及架构契约不一致
-   **审计范围**：
    -   `bingcool/gofy`
    -   `bingcool/gofy-interface-api`
    -   `bingcool/gofy-adapter`
-   **审计方式**：基于 GitHub 当前 `main`
    分支源码、跨仓库提交差异、核心调用链及现有测试代码进行静态审计
-   **重要限制**：本轮没有在真实
    MySQL、Nacos、Kafka、RabbitMQ、RocketMQ、Redis 或 Kubernetes
    环境执行端到端集成测试；本文中的结论区分源码可确认问题与需要运行环境进一步验证的风险。

## 1. 审计基线

  -------------------------------------------------------------------------------------------------------------------------
  仓库                            本轮审计提交                                 相对上一轮基线
  ------------------------------- -------------------------------------------- --------------------------------------------
  `bingcool/gofy`                 `a2742808abf2b78b02284ca3161a0f443f3e6a8e`   从
                                                                               `58a7abcce54c1b041fee98d1969fe86c9561bda4`
                                                                               前进 14 个提交

  `bingcool/gofy-interface-api`   `748b657094d55c823020c8d9ed21ecaba71b6dd2`   从
                                                                               `5e5e1c3c8fd69353585b0b3073b868d12ad07f66`
                                                                               前进 1 个提交

  `bingcool/gofy-adapter`         `3e7929e5bd154e0c3e65318d26fc414c3b6ed7ba`   从
                                                                               `c5ef2a90acda4c92dcd91c05bb53482af331f7a0`
                                                                               前进 2 个提交
  -------------------------------------------------------------------------------------------------------------------------

本轮新增提交主要集中在：

-   Claim 批量认领后的逐行租约重新武装、批次上限及相关集成测试；
-   HTTP/Worker `status` 命令；
-   Demo Service 的服务发现客户端装配、降级访问及服务调用巡检 Worker；
-   Nacos Resolver 的并发刷新合并、失败节流、取消语义；
-   Kubernetes Lease 冲突后的本地 reservation 释放；
-   Interface API 客户端的 route-group prefix
    支持，以及避免服务发现地址走环境代理。

## 2. 总体结论

```{=html}
<details>
```
```{=html}
</details>
```
本轮没有发现需要推翻现有整体架构的理由。Gofy 的主要边界仍然清晰：

1.  框架核心负责应用生命周期、命令行、HTTP、Worker、进程管理；
2.  `support/*` 提供数据库、Redis、消息队列、Nacos、Claim、Lease
    等能力；
3.  `gofy-interface-api` 保持框架无关，负责契约与客户端；
4.  `gofy-adapter` 承担外部基础设施适配，避免把 Kubernetes
    等重依赖带入框架核心；
5.  Demo Service 通过装配根组装业务模块、基础设施和下游客户端。

上一轮指出的 Claim
批次尾部租约过期风险，已经通过"执行每条任务前重新续租"明显收敛；Nacos
Resolver 并发刷新放大问题、K8s Lease Renew Conflict
后本地状态残留问题也已有对应修复。

但本轮仍发现一个跨仓库的实际契约断裂：**Nacos 注册器已经发布
`inner_external_base_uri` 元数据，Resolver 却仍然只使用实例 IP/Port
构造地址。**
这会使"外部网络通过实例元数据访问集群内服务"的设计路径无法成立。

### 2.1 问题统计

  -----------------------------------------------------------------------------------
  优先级                                        数量 说明
  --------------------- ---------------------------- --------------------------------
  P1                                               1 已发布的 Nacos
                                                     外部访问元数据没有被 Resolver
                                                     消费

  P2                                               3 客户端零值参数丢失、权重为 0
                                                     的实例仍被选中、Claim
                                                     空队列回调不受超时约束

  P3                                               3 降级错误分类过宽、Multipart
                                                     上传全量缓冲、手工构造 Nacos
                                                     配置时健康实例默认值语义不一致
  -----------------------------------------------------------------------------------

> P1/P2 表示建议进入近期修复；P3
> 表示边界约束或低频风险。没有把"可能需要增加的功能"列为问题。

------------------------------------------------------------------------

## 3. P1：需要优先处理

### P1-01：Nacos 注册了 `inner_external_base_uri`，但服务发现 Resolver 没有读取它

**涉及仓库**

-   `bingcool/gofy`
    -   `support/nacos/register.go`
    -   `support/nacos/naming.go`
-   `bingcool/gofy-adapter`
    -   `client/nacos/resolver.go`

**代码事实**

`support/nacos/register.go` 定义：

-   `MetadataInnerExternalBaseURI = "inner_external_base_uri"`
-   `RegisterConfig.SetInnerExternalBaseURI()` 会把外部可访问地址写入
    `Instance.Metadata`
-   `RegisterInstance()` 会将 Metadata 序列化后提交给 Nacos

注册侧的设计意图很明确：Nacos 中的实例 IP 可能是集群内部 Pod
IP，办公网或本地开发机无法访问；调用方解析实例时应优先使用
`inner_external_base_uri`。

但 `gofy-adapter/client/nacos/resolver.go` 当前的 `baseURL()` 只读取：

-   `inst.Metadata["scheme"]`
-   `inst.IP`
-   `inst.Port`

最终始终构造 `scheme://IP:port`，没有读取 `inner_external_base_uri`。

**影响场景**

例如：

1.  服务 A 部署在 Kubernetes，向 Nacos 注册 `10.244.x.x:8080`；
2.  注册时同时发布
    `inner_external_base_uri=http://service-a-dev.example.com:8080`；
3.  本地开发机通过 Nacos 解析服务 A；
4.  Resolver 仍返回 `http://10.244.x.x:8080`；
5.  本地网络无法路由到 Pod IP，请求失败。

这正是注册侧增加外部访问元数据要解决的问题，因此当前实现形成了"生产者发布了元数据，但消费者忽略元数据"的断链。

Demo Service 的 `serviceclients.degradable`
不能完全替代该机制：它使用的是**当前调用方配置中的单个 fallback
URL**，并不是从被调用服务的 Nacos
实例记录中获取地址。对于一个服务调用多个下游服务的情况，调用方自己的地址并不能代表每个下游服务的外部地址。

**建议修复**

不增加新配置项，只让 Resolver 消费已经存在的元数据：

1.  解析到实例后，优先读取并校验 `inner_external_base_uri`；
2.  该值非空且是合法 HTTP/HTTPS URL 时，直接作为实例 BaseURL；
3.  元数据不存在或无效时，保持现有 `scheme + IP + Port` 兼容路径；
4.  不要把元数据 URL 与实例 IP/Port 再次拼接；
5.  增加测试覆盖：有效外部地址优先、元数据缺失回退 IP、非法 URL 回退
    IP、HTTPS 外部地址。

**验收标准**

-   注册器发布的元数据能够被 Resolver 实际使用；
-   没有配置该元数据的历史实例行为不变；
-   不允许无效 metadata 让整个服务发现失败；
-   本地开发机解析集群内服务时，能够得到配置中发布的外部可达地址。

------------------------------------------------------------------------

## 4. P2：建议近期修复

### P2-01：Interface API 客户端会丢弃显式传入的零值参数

**涉及文件**

`gofy-interface-api/client/encode.go`

重点函数：

-   `isZero()`
-   `collectForm()`
-   `collectHeaders()`
-   `collectCookies()`
-   `substitutePath()`
-   `writeMultipart()`

**问题原因**

当前 `isZero()`
会先递归解引用指针，再判断底层值是否为零值。调用方即使传入了一个非 nil
指针，只要它指向 `0`、`false`、空字符串或零时间，仍会被当作"没有提供"。

此外，`collectForm()` 等函数不区分字段是否明确存在，也没有按 tag 的
`omitempty` 语义决定是否省略，而是统一通过 `isZero()` 丢弃零值。

**可能表现**

例如：

``` go
type Query struct {
    Page    *int  `form:"page"`
    Enabled *bool `form:"enabled"`
}

zero := 0
disabled := false

req := Query{
    Page:    &zero,
    Enabled: &disabled,
}
```

调用方明确表达了 `page=0` 和
`enabled=false`，但编码时两个字段都可能被跳过。

对于 GET 查询、URI 路径参数、Header、Cookie 和 Multipart
字段，这会导致"调用方显式传入的值"与"服务端实际收到的值"不一致。尤其是指针字段通常就是为了表达字段存在性与零值之间的区别。

**建议修复**

1.  将"字段是否存在"与"字段底层值是否为零"分开判断；
2.  对非 nil 指针，应保留其显式值，包括 `0` 和 `false`；
3.  对非指针字段是否省略，应遵循明确的 tag
    约定，不应无条件把所有零值都当成缺省；
4.  为 `form`、`uri`、`header`、`cookie`、`multipart`
    分别增加零值与指针零值测试；
5.  检查匿名嵌入结构体、指针嵌入结构体是否保持相同语义。

**回归用例**

-   `*int(0)` 能发送为 `page=0`；
-   `*bool(false)` 能发送为 `enabled=false`；
-   非 nil 的 `*string("")` 是否发送，按最终明确的 tag 语义测试；
-   nil 指针仍然按未提供处理；
-   普通非指针零值的省略行为有明确测试，不要无意改变既有契约。

### P2-02：Nacos 权重负载均衡会给 `weight=0` 的实例分配流量

**涉及文件**

`gofy-adapter/client/nacos/resolver.go`

重点函数：

-   `weightedPick()`
-   `weight()`

当前实现：

``` go
func weight(value float64) float64 {
    if value <= 0 {
        return 1
    }
    return value
}
```

**问题原因**

当负载均衡策略为 `weight` 时，权重为 0 的实例会被转换为权重
1。只要该实例同时满足 Enabled/Healthy 和 IP/Port
条件，它就仍然有概率被选中。

Nacos 中的 0 权重通常用于让实例不承接流量。将其改成
1，会破坏运维通过权重将实例流量降为零的意图。

例如：

  实例     Nacos 权重   当前 Resolver 实际权重
  ------ ------------ ------------------------
  A               100                      100
  B                 0                        1

B
仍然可能收到请求。虽然比例很低，但在滚动发布、实例摘流或故障隔离时，这种流量可能直接落到不应接收请求的实例上。

**建议修复**

-   区分"权重为 0"和"权重字段缺省"；
-   对权重为 0 的实例不分配流量；
-   负权重属于非法数据，不应被静默当作 1；
-   当全部实例权重为 0
    时，应明确返回无可选实例或采用有文档依据的退化行为，不要随机给零权重实例分配流量。

**测试缺口**

当前 Resolver
测试覆盖了缓存、轮询、健康过滤、失败节流和并发刷新，但没有覆盖权重为 0
的情况。

建议新增：

1.  一个权重 100、一个权重 0，重复选择时绝不返回权重 0 的实例；
2.  所有实例权重为 0；
3.  权重为负数；
4.  权重字段缺省时的兼容语义。

### P2-03：Claim Runner 在空队列时调用 Handler，但没有应用 HandlerTimeout，且忽略回调错误

**涉及文件**

`gofy/support/claim/runner.go`

重点函数：

-   `Runner.Run()`
-   `Runner.process()`

当前 `Run()` 在没有任务时直接执行：

``` go
_, _ = r.handler(ctx, nil)
```

而有任务时则通过 `process()` 创建 `context.WithTimeout()`。

**问题原因**

同一个 Handler 在两种路径下有不同的超时语义：

  场景                         是否受 HandlerTimeout 约束
  ---------------------------- ----------------------------
  有任务，执行真实 payload     是
  没有任务，执行 nil payload   否

空队列回调被设计用于心跳或空闲指标，但它仍然是用户传入的业务回调。若回调在
`data == nil`
分支里执行了慢查询、远程调用或意外阻塞，它可以无限期占住当前 Runner
的调用 goroutine，导致后续 ticker 无法继续轮询。

此外，空队列回调返回的错误被完全丢弃。调用方无法从 `Run()`
的返回值判断这次空闲回调是否失败。

**建议修复**

在不增加新接口的前提下，统一两条执行路径的超时边界：

1.  空队列回调也使用 `HandlerTimeout`；
2.  明确空闲回调错误的处理契约：如果设计上确实不影响任务轮询，至少应有可观测的错误出口；不要在没有说明的情况下静默吞掉；
3.  不要为了实现超时而简单地把 Handler 放进
    goroutine、超时后直接返回。那样无法终止仍在运行的业务副作用，反而会产生后台任务继续执行的问题。

**测试用例**

-   空队列 Handler 阻塞时，Runner 能按约定结束本轮；
-   空队列 Handler 返回错误时，行为符合明确的错误契约；
-   有任务与无任务两条路径的超时行为一致；
-   父 context 取消时，空闲回调能及时退出。

------------------------------------------------------------------------

## 5. P3：边界与运行约束

### P3-01：Demo Service 的降级判断把所有非 API/HTTP/Protocol 错误都当作可重试的传输错误

**涉及文件**

`examples/demo-service/serviceclients/serviceclients.go`

重点函数：

-   `degradable.GetConfig()`
-   `shouldDegrade()`

当前逻辑排除：

-   `APIError`
-   `HTTPError`
-   `ProtocolError`
-   调用方 context 已取消

除此之外的错误都会触发 fallback。

**风险**

除真正的连接拒绝、DNS
失败、连接超时、服务发现失败外，客户端本地也可能产生普通 error，例如：

-   Request 编码失败；
-   TokenSource 获取凭据失败；
-   本地参数校验失败；
-   其它在 HTTP 请求真正发出之前产生的构造错误。

这些错误并不意味着"解析到的实例不可达"，换一个 BaseURL
重试通常不会解决问题，只会增加一次无意义调用，并让最终错误变成两个错误的
Join。

目前该降级只包裹
GET，因此没有发现直接重复写入的风险；但错误分类过宽，会让排障信息变复杂。

**建议**

不需要增加新的重试系统，只需把降级边界收窄到"服务发现失败或请求没有得到有效
HTTP 响应的传输层失败"。本地编码、凭据、参数错误不应触发地址降级。

测试至少应覆盖：

-   `connection refused`：降级；
-   DNS/连接超时：降级；
-   `APIError`：不降级；
-   `ProtocolError`：不降级；
-   TokenSource 错误：不降级；
-   请求编码错误：不降级；
-   调用方取消：不降级。

### P3-02：Multipart 上传当前会把整个文件读入内存

**涉及文件**

`gofy-interface-api/client/encode.go`

当前调用链：

`encodeBody() → encodeMultipart() → bytes.Buffer → writeMultipart() → io.Copy()`

文件内容通过 `io.Copy()` 写入内存中的 `bytes.Buffer`，最后
`encodeBody()` 返回完整 `[]byte`，再交给 HTTP Request。

**影响**

上传文件越大，单次请求的内存占用越接近文件大小；并发上传时内存占用按并发数叠加。文件可能同时存在于调用方持有的对象、编码缓冲区以及其它业务缓存中。

这不是小文件上传的功能错误，但对于大文件或高并发上传，是明确的资源边界。

**建议**

-   当前如果只定位为小文件/普通表单上传，应明确最大允许文件大小；
-   不要把当前实现描述为大文件流式上传；
-   如果现有业务已经允许大文件，优先考虑流式 Request
    Body，而不是继续增加缓冲区；
-   测试覆盖超大文件、并发上传和上传中断时资源释放。

本项属于容量与资源边界，不建议为了它扩大本轮改动范围。

### P3-03：手工构造 `nacos.Config` 时，HealthyOnly 的零值可能导致 Resolver 默认包含不健康实例

**涉及文件**

-   `gofy-adapter/client/nacos/resolver.go`
-   `gofy/support/nacos/config.go`

`ConfigFromNacos()` 当前执行：

``` go
IncludeUnhealthy: !cfg.Discovery.HealthyOnly
```

通过正常配置加载流程，Discovery 配置会被归一化，HealthyOnly 默认值为
true，因此正常 YAML 配置路径符合预期。

但 `ConfigFromNacos()` 是导出函数，`NewFromConfig()` 也接收一份
`nacos.Config`。如果调用方手工构造一个零值 `nacos.Config`，没有显式设置
`Discovery.HealthyOnly=true`，那么零值 false 会被反转成
`IncludeUnhealthy=true`。

**影响**

-   正常文件加载路径：默认只选健康实例；
-   手工构造配置路径：可能把不健康实例纳入候选集。

这会造成相同配置结构在不同构造入口下语义不一致。

**建议**

明确 `NewFromConfig()` 的输入契约：

-   若只接受已经 Normalize 的 Config，应在 API 文档和校验中明确；
-   若允许手工构造，则应在统一归一化入口补齐默认值，不能在映射时把 Go
    bool 零值直接解释成用户明确选择了"包含不健康实例"。

增加一个零值 Config 测试，验证默认只选择健康实例。

------------------------------------------------------------------------

## 6. 上一轮问题复核：本轮代码状态

以下是上一轮主要问题在本轮提交中的复核结果。

  ----------------------------------------------------------------------------------------------------------------------------
  上轮问题                                       本轮状态                复核说明
  ---------------------------------------------- ----------------------- -----------------------------------------------------
  Claim                                          **已明显修复**          默认 Batch 已降为 10，MaxBatch=20；Runner
  默认批量领取较多，串行执行导致尾部租约先过期                           每条任务开始前调用 Extend，使用 owner/status/attempts
                                                                         做 CAS，失去租约则跳过 Handler

  Claim HandlerTimeout 被误认为强制终止          **文档已明确边界**      代码使用
                                                                         context.WithTimeout，仍然是协作式取消；业务必须传递
                                                                         ctx，不能把它理解成进程级强杀

  K8s Lease Renew Conflict 后本地 held           **已修复**              Conflict 分支调用 unreserve；新增同一个 Locker 再次
  reservation 未释放                                                     Acquire 的回归测试

  Nacos Resolver 缓存过期时并发请求同时回源      **已修复**              增加按 service 的 inflight singleflight；32
                                                                         并发测试验证一次刷新

  Nacos 失败后每次请求都等待完整超时             **已修复**              增加 lastAttempt 与 lastSuccess；失败按 CacheTTL
                                                                         节流，StaleTTL 单独约束旧实例可用时间

  Nacos 成功返回空列表后每个请求持续查询         **已修复**              空列表作为成功结果负缓存

  Resolver 刷新被 leader 的调用方取消后污染缓存  **已修复**              leader 自身取消时不写缓存、不更新节流时间

  Worker daemon 启动握手、PID 身份与锁文件问题   **上一轮修复仍保留**    本轮未发现回退证据；仍需在目标 OS 上做进程级测试

  Consumer dead ledger 落库失败却 ACK            **上一轮修复仍保留**    本轮主仓库相关差异未显示回退

  Consumer Ready 早于真实 Subscribe              **上一轮修复仍保留**    本轮未发现回退

  Order 状态迁移发生旧事件回退                   **上一轮修复仍保留**    本轮未发现回退
  ----------------------------------------------------------------------------------------------------------------------------

### 6.1 Claim 本轮修复的正确性边界

本轮 Claim 改动值得肯定：

-   `DefaultBatch=10`；
-   `MaxBatch=20`；
-   `Runner.process()` 在 Handler 启动前重新续租；
-   Extend 使用 `owner + status + attempts` 围栏；
-   如果已被其它 worker 接管，则不执行旧任务 Handler；
-   HandlerTimeout 必须小于 Lease。

这解决了"批次尾部任务尚未开始就被另一实例回收"的主要并发窗口。

仍需保留以下边界认知：

1.  Handler 已经开始执行后，如果它忽略 ctx、执行超过 Lease，仍可能被
    Reap 接管；
2.  Handler 已产生外部副作用但 Succeed
    尚未提交时进程崩溃，任务仍可能被再次执行；
3.  围栏字段只能阻止旧 owner 更新任务行，不能撤销已经发出的外部副作用；
4.  所以 Handler 幂等仍然是必需条件，不能因为增加 Extend 就把 Claim
    描述为 exactly-once。

这些是当前 at-least-once 任务模型的边界，不是本轮新功能缺失。

------------------------------------------------------------------------

## 7. 跨仓库架构复核

### 7.1 `gofy` 核心与应用生命周期

当前主要调用链仍然是：

`main → planCommand → load config → build clients/groups → gofy.New → Execute`

命令分类前置是合理的：

-   `stop`、`status` 不应因为数据库、Nacos 或 Broker 不可用而无法执行；
-   `version/help` 不应依赖业务配置；
-   Worker 子命令应只构建当前命令真正需要的资源。

本轮新增 `status` 后，状态查询按 PID 文件、进程存活、PID
身份、健康探针逐层判断，避免了只看 PID 是否存在就认定服务运行的错误。

需要继续保持的约束：

-   status 是只读命令，不应清理 PID 文件；
-   `running` 且 `identity_verified=false`
    代表进程存活但身份无法证明，不能把它等价为完全可信的 running；
-   Worker 分组状态是按 PID 文件分别检查，不应只看主 HTTP PID 文件；
-   `/healthz` 的健康结果和 PID 进程身份是两个维度，不应混为一个状态。

### 7.2 `gofy-interface-api` 客户端

本轮新增的 `WithPathPrefix()` 修复了分组路由的路径契约：

-   `endpoint.Group` 持有部署前缀；
-   `endpoint.Spec.Path` 持有组内路径；
-   生成客户端在统一 `call()` 中传入 `WithPathPrefix(prefix)`；
-   `buildRequest()` 统一拼接 prefix 与 path。

该修复放在统一请求构造入口是正确的，普通 Call、SSE、Download、Chunked
可以复用同一条路径逻辑。

此外，默认 Transport 不再使用环境代理，符合 Nacos 返回 Pod
IP/内网地址的使用场景；确实需要代理的调用方可以注入自定义 HTTPClient。

仍需注意：

-   `WithPathPrefix` 是按次覆盖项，调用方可以覆盖生成客户端传入的
    prefix；
-   生成代码与契约中的 Group 前缀必须保持一致；
-   不能只测试手工
    `WithPathPrefix`，还要测试真实生成客户端最终请求路径；
-   零值参数丢失问题见 P2-01；
-   Multipart 全量内存缓冲见 P3-02。

### 7.3 `gofy-adapter` 的边界

适配层仍然承担了正确的职责：

-   K8s Lease 使用 typed coordination/v1 client；
-   K8s 依赖不进入 Gofy 主模块；
-   Lease 通过 resourceVersion 更新；
-   `leaseTransitions` 作为 fencing token；
-   Resolver 负责 Nacos 服务实例缓存、刷新、负载均衡；
-   Adapter 不应反向依赖 Gin 或 Gofy 根包。

K8s Lease 本轮 Conflict reservation 问题已修复。仍要坚持：

-   Identity 必须是每个应用进程唯一；Pod name 只有在一个 Pod
    一个应用进程时才足够；
-   Lease 对象不能随意删除，否则 fencing token 可能归零；
-   fencing token 必须由被保护资源真正校验，单纯持有 Lease
    不会自动阻止旧持有者写数据库；
-   Redis Lock token 与 K8s Lease fencing token
    的保证级别不同，不应混用概念。

------------------------------------------------------------------------

## 8. 重点调用链与正确性检查

### 8.1 Claim 任务执行链

`Queue.Reap → Queue.Claim → Runner.process → Queue.Extend → Handler → Succeed/Fail`

检查结论：

-   Claim 与任务状态更新处于事务中；
-   `FOR UPDATE SKIP LOCKED` 适用于多个 worker 并发领取；
-   attempts 作为每次认领的 fencing 值；
-   Extend 在 Handler 前执行，保护排队等待窗口；
-   Succeed/Fail 通过 owner/status/attempts 做条件更新；
-   Reap 将过期任务恢复为 Pending 或 Dead；
-   `rerun_allowed=0` 的行不会被再次安排执行。

需要业务侧遵守：

-   `rerun_allowed` 必须在插入任务时确定；
-   Handler 必须幂等；
-   Handler 必须透传 ctx；
-   不要把非幂等外部写操作放进无法去重的 Claim Handler。

### 8.2 服务发现调用链

`serviceclients.New → nacos.NewFromConfig → Resolver.Resolve → ListInstances → filter → chooseLocked → baseURL → HTTP Client`

本轮检查发现：

-   per-service singleflight 已存在；
-   失败查询节流已存在；
-   stale TTL 与 cache TTL 已分开；
-   成功空列表有负缓存；
-   leader 取消不污染缓存；
-   轮询选择在锁内推进游标；
-   **但 baseURL 未读取注册器已经发布的 `inner_external_base_uri`
    元数据（P1-01）**；
-   权重为 0 的实例仍可能被选中（P2-02）。

### 8.3 Demo Service 降级调用链

`DemoService.GetConfig → primary(Nacos Resolver) → shouldDegrade → fallback(BaseURL)`

当前只对 GET
做降级，避免将写操作发往另一个地址造成重复写入，这是正确的保守策略。

但降级判断不应把所有普通 error 都理解为网络失败，详见 P3-01。

此外，fallback URL
代表"当前配置环境中的本服务地址"。生产配置若错误地指向其它环境，可能让故障流量跨环境流动。配置文件注释已经提示这一点，部署时仍需把它作为环境隔离检查项。

------------------------------------------------------------------------

## 9. 建议修复顺序

### 第一批：优先修复

1.  **P1-01**：Resolver 读取注册器发布的 `inner_external_base_uri`。
2.  **P2-02**：权重为 0 的 Nacos 实例不能接收流量。
3.  **P2-01**：客户端编码保留显式传入的指针零值、false/0 值。
4.  **P2-03**：Claim 空队列回调也遵循
    HandlerTimeout，并明确错误处理语义。

### 第二批：小范围收敛

5.  **P3-01**：缩小 Demo Service 降级错误分类范围。
6.  **P3-03**：统一手工构造 Nacos Config 的默认值语义。
7.  **P3-02**：明确 Multipart 上传的内存容量边界。

建议不要同时重构
Claim、Resolver、客户端编码和应用生命周期。每项都可以独立小改并增加针对性回归测试。

------------------------------------------------------------------------

## 10. 回归测试清单

### Nacos 注册与发现

-   [ ] Registrar 将 `inner_external_base_uri` 写入实例 Metadata；
-   [ ] Resolver 优先使用合法的外部 BaseURL；
-   [ ] 无 metadata 时回退 IP/Port；
-   [ ] metadata 非法时回退 IP/Port；
-   [ ] `weight=0` 实例不被选中；
-   [ ] 所有实例权重为 0 时行为明确；
-   [ ] 32/100 并发 Resolve 只触发一次回源；
-   [ ] Nacos 失败后 CacheTTL 窗口内不重复打满超时；
-   [ ] leader context 取消不污染缓存；
-   [ ] StaleTTL 到期后 fail closed。

### Claim

-   [ ] 批次尾部任务等待期间，另一个 Runner 不会提前执行它；
-   [ ] Extend 发现 owner/attempts 已变化时跳过 Handler；
-   [ ] 同一秒续租导致 RowsAffected=0 时，仍能正确区分无变化与租约丢失；
-   [ ] Handler 超时且正确传播 ctx 时能退出；
-   [ ] Handler 忽略 ctx 时，不把协作式超时误认为强制终止；
-   [ ] 空队列 Handler 也受超时约束；
-   [ ] 空队列 Handler 错误按明确约定处理；
-   [ ] Handler 副作用完成但 Succeed 失败时，重复执行由业务幂等保证；
-   [ ] rerun_allowed=0 的任务不会被再次调度。

### Interface API Client

-   [ ] 指针 `0`、`false` 能正确编码；
-   [ ] nil 指针仍按未提供处理；
-   [ ] URI 路径参数零值语义明确；
-   [ ] Form、Header、Cookie、Multipart 的零值语义一致；
-   [ ] 真实生成客户端最终请求包含 Group Prefix；
-   [ ] 调用方覆盖 PathPrefix 的行为有测试；
-   [ ] SSE 重连重新 Resolve；
-   [ ] Download/Chunked 与普通 Call 使用一致的路径前缀；
-   [ ] Multipart 大文件与并发上传的内存边界可控。

### 进程与状态查询

-   [ ] HTTP status 能区分 missing/stopped/stale/running；
-   [ ] Worker status 能按组查询；
-   [ ] 一个 Worker 组停止不影响其它组的状态展示；
-   [ ] PID IdentityUnknown 不被误认为 IdentityMatch；
-   [ ] 健康探针失败不篡改 PID 身份判断；
-   [ ] `stop/status/version/help` 在业务依赖不可用时仍能运行。

------------------------------------------------------------------------

## 11. 最终审计意见

本轮优化整体方向正确，且不是简单增加抽象层：Claim
通过逐行续租处理批次排队窗口，Resolver 通过 singleflight 与失败节流控制
Nacos 故障放大，K8s Lease 修复了本地 reservation
残留，客户端通过统一请求构造入口补齐了 Group Prefix。

当前最值得优先修复的不是大规模架构问题，而是几个具体契约边界：

1.  注册侧发布的外部访问地址没有被发现侧消费；
2.  Nacos 权重为 0 的实例仍可能获得请求；
3.  API Client 对显式零值的编码不可靠；
4.  Claim 空队列回调绕过了统一超时边界。

**建议本轮不要新增功能，也不要继续扩大架构范围。先修复以上问题，再用对应回归测试固定行为。**

本报告是源码级审计，不代表已通过真实基础设施集成验证。特别是 Nacos
外部地址元数据、权重分配、Claim 多实例竞争和真实 HTTP
大文件上传，仍应在对应运行环境进行验证。
