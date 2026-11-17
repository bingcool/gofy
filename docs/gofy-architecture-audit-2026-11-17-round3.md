# Gofy 全代码架构审计报告（第三轮）

> **修复状态：已修复**（修复时间：2026-11-17）

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

> **复核（2026-11-17，对照 `gofy@b1da15d` / `gofy-adapter@3e7929e` /
> `gofy-interface-api@748b657` 逐条核对源码）**
>
> 七条结论里 **6 条成立**（其中 3 条方案需改）、**1 条部分成立**
> （P2-03 的"忽略错误"与代码注释不符）。原文一律保留，每条下方加「复核结论」说明依据与调整，
> 末尾给修订后的顺序与清单。
>
> **一次更正**：初版复核判定 P1-01"从未有消费方"，是搜索范围只覆盖 swoolefy 与三个 Go 仓库
> 所致。消费方在独立仓库 `interface-api-service`（PHP SDK）里，且这套门控语义正好是 Go 侧
> 该补的——见 §3 的复核结论（已重写）。

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

本轮 P1 是一条**移植缺口**：Nacos 注册器会发布 `inner_external_base_uri` 元数据，
PHP 栈（`interface-api-service`）的发现层已经按"本地分组无实例 → 切部署分组 → 用该
metadata"消费它，而 Go 栈的 Resolver 只使用实例 IP/Port，没有对应机制。
修复必须照参照实现的门控语义，不能无条件优先，见 §3。

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

> 上表是**审计时的原始定级**，保留以便对照；复核后的定级见 §2.2。
> 其中 P1 一条被下调：它要的修复本身就是一个新功能，与审计"不新增功能"的前提冲突。

### 2.2 复核后的定级

  ----------------------------------------------------------------------------------------------
  复核定级                            数量 条目
  ------------------------------ ---------- ----------------------------------------------------
  P1（成立；方案按参照实现修正）         1 P1-01：Go 侧缺"本地分组→部署分组→metadata"这套门控，
                                              报告的问题对、无条件优先的方案错

  P2（成立，方案已修正）                 3 P2-01 编码器丢弃必填字段零值（论证应从"指针"改成
                                              "required"）；P2-02 权重 0 仍被选中（缺省与 0 不可
                                              区分，需先定语义）；P2-03 空队列回调绕开超时

  P3（成立，影响面收窄）                 3 P3-01 降级分类过宽；P3-02 Multipart 全量缓冲（"不要描述
                                              为流式"无的放矢）；P3-03 HealthyOnly 三态在导出入口
                                              丢失

  补充（报告未列）                       1 降级让单次逻辑调用的最坏耗时变成 2× `client.timeout`
  ----------------------------------------------------------------------------------------------

------------------------------------------------------------------------

## 3. P1：metadata 的消费机制在 Go 侧缺失（PHP 侧已有参照实现）

### P1-01：Nacos 注册了 `inner_external_base_uri`，但服务发现 Resolver 没有读取它

**复核结论（2026-11-17 二次核对：P1 成立，方案需按参照实现修正）**

-   **问题成立**：`gofy-adapter/client/nacos/resolver.go:444-450` 的 `baseURL()` 只读
    `Metadata["scheme"]`、`IP`、`Port`；Go 栈没有任何读取 `inner_external_base_uri` 的路径。
-   **参照实现（PHP，已核对）**：`interface-api-service/Support/NacosServiceDiscovery.php`
    的 `resolveInstance()`（47-118）按三步走：
    1. 先用 `discovery_service_client.group_name`（**本地分组**，可由
       `NACOS_SERVICE_GROUP_NAME` 覆盖）选实例；该值留空时回落
       `service_register.group_name`（`DiscoveryConfig.php:47-50`）；
    2. 本地分组选不到、**且** `LOCAL_NACOS_SERVICE_AUTO_SWITCH=true`
       （`NacosConst.php:58`）时，切到 `application.yaml` 的
       `nacos.service_register.group_name`（**部署分组**）再选一次，成功才置
       `$useInnerExternalBaseUri = true`；
    3. 只有该标志为真才用 `metadata["inner_external_base_uri"]`，否则仍用 `IP:port`
       （`resolveBaseUriFromInstance`，110-118）。调用点在
       `Support/BaseClientApi.php:59-61`（`makeService`）与 `:191-193`
       （`reconnectViaNacosDiscovery`）。
-   **所以报告的"问题"对、"无条件优先"错**：PHP 的门控是"本地分组无实例 + 显式开关"，
    不是"解析到就用 metadata"。照报告改会把集群内调用也推到外部地址（多一跳 ingress、
    TLS/白名单/DNS），并让 per-Pod 的 `random`/`round_robin`/`weight` 退化成同一个 URL。
-   **Go 侧落点不是 `client/client.go`**：`client.Resolver` 只返回字符串
    （`Resolve(ctx, service) (string, error)`），拿不到 metadata；判定需要"本地组 + 部署组 +
    开关"三样 Nacos 语义。参照实现在 PHP 里同样落在**发现层**
    （`NacosServiceDiscovery`），`BaseClientApi` 只持有 `baseUri`/`metadata`。因此 Go 的
    对应物是 `gofy-adapter/client/nacos/resolver.go`；`client/client.go` 继续只做
    `validateResolvedBase` 与路径拼接。
-   **报告漏了一处会改变行为的细节**：现有缓存、轮询游标与单飞表都以**裸 `service`** 为键
    （`resolver.go:269`、`280`、`303`），切换分组后会把部署分组的实例写进本地组的键——
    下一次直接命中，再也不试本地分组。PHP 的 `getDiscoveryClient()` 用的是
    `service@group` 缓存键，Go 必须对齐，"是否切换过"也要跟着缓存条目走。

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

> 复核注（二次核对修正）：这个推断是对的。消费方不在 swoolefy 核心，而在
> `interface-api-service`（PHP SDK）的 `Support/NacosServiceDiscovery.php:110-118`；
> 初版复核只搜了 swoolefy 与三个 Go 仓库，据此得出的"从未有消费方"已撤销。

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

这正是注册侧增加外部访问元数据**想要**解决的问题，而且 PHP 栈已经实现了。Go 栈缺的是
同一套机制，所以这是**移植缺口**："生产者发布了、Go 的消费者没有"确实成立，只是消费方
在另一个仓库。

Demo Service 的 `serviceclients.degradable`
不能完全替代该机制：它使用的是**当前调用方配置中的单个 fallback
URL**，并不是从被调用服务的 Nacos
实例记录中获取地址。对于一个服务调用多个下游服务的情况，调用方自己的地址并不能代表每个下游服务的外部地址。

**建议修复（照参照实现的门控语义，落在发现层）**

1.  **输入**：`nacos.Config` 已有两个分组——`Discovery.Group`（本地分组）与
    `Register.Group`（部署分组）。新增 `Discovery.LocalAutoSwitch bool`，由环境变量
    `LOCAL_NACOS_SERVICE_AUTO_SWITCH` 解析（与 PHP 同名，便于两套栈共用部署脚本）；
    不要新增"允许 resolver 优先 metadata"这种无门控开关。
2.  **选址**（`Resolver.Resolve`）：本地分组 `list()` 为空且 `LocalAutoSwitch` 为真 →
    用 `Register.Group` 再 `list()` 一次；命中则本条记录 `switched=true`。
    开关为假、或两个分组本来就相同（`Discovery.Group` 未配时会默认沿用
    `Register.Group`，`config.go:184`）时，保持今天的行为：直接返回 `ErrNoInstances`，
    不重复查询、也不偷偷换组。
3.  **构造 base URL**（`baseURL()`）：仅当 `switched` 且 metadata 值通过
    `http(s)` + host 校验时用它（可带 path，`client` 的 `base + path` 拼接天然支持
    网关前缀）；缺失或非法则回退 `scheme+IP:port`。**非法值只影响该实例**——
    否则会走到 `client.validateResolvedBase`，变成整个服务解析失败。
4.  **缓存键**：`cache`/`rr`/`inflight` 从裸 `service` 改成 `service@group`
    （PHP `getDiscoveryClient` 的键就是 `service@group`），并把 `switched` 存进条目；
    否则部署分组的实例会被写进本地组的键，下一次直接命中、再也不试本地组。
5.  **可观测**：切换时打一行 `service / local group → fallback group / source=metadata|ip`，
    这是线上唯一能区分"本地命中"与"切到开发环境"的地方（PHP 对应的是
    `fmtPrintNote`/`NacosLogger::info`）。
6.  **不做**：不在 `client.Resolver` 接口里塞 metadata（会波及所有实现与 SSE 重连路径），
    也不把 metadata 当默认寻址。

**验收标准**

-   PHP 的三种结果在 Go 侧一一对应：本地命中（`IP:port`）、本地空 + 开关关
    （`ErrNoInstances`）、本地空 + 开关开 + 部署分组有实例（metadata，缺失则 `IP:port`）；
-   开关默认 false 时，集群内调用行为与今天完全一致；
-   非法 metadata 只让该实例回退 `IP:port`，服务级解析仍可用；
-   切换结果不写进本地分组的缓存键，下一次 `Resolve` 仍先试本地组；
-   日志能区分"本地命中"与"切到部署分组"。

------------------------------------------------------------------------

## 4. P2：建议近期修复

### P2-01：Interface API 客户端会丢弃显式传入的零值参数

**复核结论（成立，但论证对象与方案都要换）**

-   **代码事实成立**：`client/encode.go:487-499` 的 `isZero()` 会解引用指针再判断，
    调用点 225/328/363/392/418 一律 `isZero(value) → 跳过`。
-   **"指针零值"这个论证打不到人**：三份契约的**请求结构体**（`form`/`uri`/`header`/
    `cookie`/`multipart` 的载体）里**没有一个指针字段**；全仓唯一的指针字段是
    `orderdto.OrderDTO.PaidAt *time.Time`，它在响应 DTO 里、走 `json.Marshal`，
    根本不经过 `isZero()`。所以这条今天不会伤害任何调用方。
-   **真正的伤害在必填字段上**：`docgen/schema.go:243-259` 的 `fieldRequired` 把
    "非指针 + 无 `omitempty`/`default` + 无 `validate:"omitempty"`"判为 **required**，
    而编码器对同一字段的零值直接丢弃 → OpenAPI 说必填、请求里没有这个参数。
    `orderrequest.ListOrders.UserID`（`form:"user_id" validate:"required"`）就是这种：
    传空值时会发出一个**缺参数**的请求，被服务端 400。
-   **方案要改一处**：不能"对非指针字段一律发送零值"。`orderrequest.ListOrders.Status`
    只有 `validate:"omitempty"`，一律发送会发出 `status=`，引入新的错误。
-   **正确做法**：把 `fieldRequired` 抽到 `contracts/common/endpoint`（`docgen` 与 `client`
    都已依赖它），编码器与 OpenAPI schema **共用同一个判定**，两者不可能再漂移：
    required → 必发（含零值）；optional → 省略零值；指针非 nil → 必发（含 `0`/`false`/`""`），
    nil → 省略。这一条同时覆盖了报告建议的第 1-3 项，且不需要新增 tag 约定。
-   **`uri` 是例外**：`*string("")` 应继续按"路径参数缺失"报错（空 segment 不合法），
    不要照搬"非 nil 就发送"。报告建议第 4 项要求 `uri` 与其它位置语义一致，这一点应推翻。

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

> 复核注：这段推理对通用编解码器成立，但**对本仓库当前契约不成立**——请求结构体里没有
> 指针字段，唯一那个指针在响应 DTO 里、不经过 `isZero()`。真正会出事的是必填字段
> （见上方复核结论）。

**建议修复（修订）**

1.  抽一个 required 判定（`endpoint.FieldRequired`，从 `docgen.fieldRequired` 平移），
    `docgen` 与 `client` 共用；先补一组"schema required == 编码器发送"的表驱动测试，
    再改编码器；
2.  编码器按判定分流：required 字段必须出现在请求里（零值也发）；optional 保持省略；
    指针非 nil 一律发送，nil 省略；
3.  `uri` 单独处理：非 nil 但值为空 → 继续报 `path parameter ... is missing`；
4.  为 `form`、`header`、`cookie`、`multipart` 各补：required 零值、optional 零值、
    nil 指针、非 nil 指针指向零值；
5.  匿名嵌入与指针嵌入结构体必须与扁平结构体语义一致（`walkStruct` 现有行为要先用
    测试固定，再动判定）。

**回归用例**

-   `*int(0)` 能发送为 `page=0`；
-   `*bool(false)` 能发送为 `enabled=false`；
-   非 nil 的 `*string("")`：`form`/`header` 发送空串，`uri` 报参数缺失；
-   nil 指针仍然按未提供处理；
-   `validate:"required"` 的非指针零值**必须**出现在请求里（今天被丢掉）；
-   `validate:"omitempty"` 的非指针零值**仍然不发**（例如 `status`），防止修 A 破坏 B；
-   匿名嵌入/指针嵌入结构体的行为与扁平结构体一致。

### P2-02：Nacos 权重负载均衡会给 `weight=0` 的实例分配流量

**复核结论（成立；"区分缺省与 0"这一步做不到，方案需降一档）**

-   **代码事实成立**：`resolver.go:437-442` 的 `weight(v)` 把 `v <= 0` 一律变成 1；
    `support/nacos/naming.go:180-189` 的 `AvailableInstances` 不看权重，所以 0 权重实例
    会带着"权重 1"进入候选集。
-   **报告的第一步做不到**：`Instance.Weight` 是 `float64`（`naming.go:26`），JSON 里
    "字段缺省"与"显式 0"解码后完全相同，**在 Resolver 层无法区分**。要区分只能把
    模型改成 `*float64`，那是 `support/nacos` + `gofy-adapter` 的联动改动。
-   **建议先按"0 就是 0"落地**：Nacos 的 hosts 响应在笔者的经验里总带 `weight`
    （未设置时下发 1.0），`weight` 缺省只可能来自坏掉的代理，不值得为它牺牲
    "权重 0 不接流量"这个运维语义。**这一条属于待验证前提**：本轮复核时本地
    dev-nacos 已停止，未能用一次真实响应确认；实现前应先 `curl` 一次
    `/nacos/v1/ns/instance/list` 看清 `weight` 是否总在。若发现真会缺省，就改 `*float64`。
-   **"全部为 0 返回无可选实例"要保留可诊断性**：错误信息必须写明
    `all instances have weight 0`，否则运维看到"no available instances"会去查注册，
    而真正的原因是有人把所有实例摘流了。

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

**建议修复（修订）**

-   `weight <= 0` 的实例**不参与**加权抽取（`<= 0` 直接排除，不再映射成 1）；负权重同样排除，
    它和 0 在运维语义上都是"不给流量"，不必单独报错；
-   加权抽取前先算出有效集合：有效集合非空 → 按权重抽；有效集合为空
    （全部实例 weight<=0）→ 返回 `ErrNoInstances` 并带
    `all instances have weight 0` 的错误信息，**不要**退回等概率随机——那会把
    "被全部摘流"这个明确的运维状态伪装成正常；
-   若担心历史上真出现过"weight 字段缺省"，把 `Instance.Weight` 改 `*float64` 作为
    后续独立改动，现在就做会牵连 `support/nacos` 的公开模型，收益不抵成本；
-   `random`/`round_robin` 不受影响：它们本来就不看权重，0 权重实例在这两种策略下
    仍会收到流量，这一点要在文档里写清（"摘流要么用 weight 策略，要么把实例下线"）。

**测试缺口**

当前 Resolver
测试覆盖了缓存、轮询、健康过滤、失败节流和并发刷新，但没有覆盖权重为 0
的情况。

建议新增：

1.  一个权重 100、一个权重 0，重复选择时绝不返回权重 0 的实例；
2.  所有实例权重为 0 → `ErrNoInstances`，错误信息含 `weight 0`；
3.  权重为负数 → 与 0 同样处理（排除）；
4.  权重字段缺省 → 当前实现按 0 处理（排除），这条要写成显式期望，
    未来改 `*float64` 时它就是行为变更的锚点；
5.  `random`/`round_robin` 下 0 权重实例仍会被选中（记录现状，避免误以为摘流生效）。

### P2-03：Claim Runner 在空队列时调用 Handler，但没有应用 HandlerTimeout，且忽略回调错误

**复核结论（前半成立，后半不成立）**

-   **超时不一致成立**：`support/claim/runner.go:273` 是 `_, _ = r.handler(ctx, nil)`，
    真实任务在 `runner.go:330` 才套 `context.WithTimeout`。同一份 `Handler` 两种超时语义，
    而 `Handler` 的文档（`runner.go:79-81`）说 ctx 携带 `HandlerTimeout`，确实自相矛盾。
-   **"忽略错误"不成立为缺陷**：`Handler` 的文档写得很明确（`runner.go:62-66` 与 94-96）：
    "The return values on that nil call are ignored — there is no row to succeed or fail —
    and no column is written"。报告说"不要在没有说明的情况下静默吞掉"，与代码注释不符，
    这一条应撤销。
-   **不要改 `Run` 的返回值**：多实例部署时 worker 的 tick 若因为一次空闲回调报错而被当作
    失败轮次，会改变 worker 的重试/日志语义，属于行为变更而非修 bug。要可观测就加可选钩子
    （如 `OnIdle func(err error)`），由使用者决定是否接。
-   **补超时后别夸大效果**：`WithTimeout` 仍是协作式取消，阻塞在 `time.Sleep` 或忽略 ctx
    的回调照样占住这一轮 goroutine，只是 ctx 被取消。

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

**建议修复（修订）**

1.  空队列回调也套 `context.WithTimeout(ctx, r.handlerTimeout)`，与真实任务一致；
2.  **保留**"返回值被忽略"的现有契约（它已有文档），但要把它写进 `Runner.Run` 的注释而
    不只是 `Handler` 的注释——读者是先看 Run 的；
3.  需要可观测时加可选 `OnIdle func(err error)`（nil 即不接），不要改 `Run` 的返回值；
4.  不要为了实现超时而把 Handler 放进 goroutine、超时后直接返回：无法终止副作用，
    还会产生后台任务继续执行的问题（这一条报告说得对，保留）。

**测试用例**

-   空队列 Handler 阻塞时，Runner 能按约定结束本轮；
-   空队列 Handler 返回错误时，按现有契约被忽略（不写列、不影响本轮结果）；
-   有任务与无任务两条路径的超时行为一致；
-   父 context 取消时，空闲回调能及时退出。

------------------------------------------------------------------------

## 5. P3：边界与运行约束

### P3-01：Demo Service 的降级判断把所有非 API/HTTP/Protocol 错误都当作可重试的传输错误

**复核结论（成立，影响比描述小，方案可以更具体）**

-   **代码事实成立**：`serviceclients.go:302-316` 是排除法——不是 `APIError`/`HTTPError`/
    `ProtocolError`、且调用方没取消，就降级。
-   **影响比描述小**：编码失败、参数缺失发生在**发出请求之前**，兜底那一次必然以同样方式失败，
    不会产生任何额外副作用；代价只是多一次本地失败，以及最终错误变成 `errors.Join` 的两层。
    所以 P3 这个定级是合适的，不必上调。
-   **方案不需要新增错误类型**：`client.Call` 已经用 `%w` 包装传输错误
    （`client/call.go:115`），`errors.As(err, &netErr)` 能直接判。判定收敛为：
    `net.Error`（连接拒绝、DNS、连接/读写超时）+ `context.DeadlineExceeded` +
    Resolver 的 `ErrNoInstances`/`ErrStaleExpired`。
-   **别漏了 Resolver 失败**：只按"传输层失败"收窄会漏掉"解析不到实例"——而这正是本地开发
    最常撞到的路径（namespace 配错、实例已下线）。这条必须留在降级集合里，否则新的判定会把
    最该兜底的情况排除掉。

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

**建议（修订）**

不需要新的重试系统，只把判定从"排除法"改成"白名单"：

1.  `var netErr net.Error; errors.As(err, &netErr)` → 降级（连接拒绝、DNS、超时都在这里）；
2.  `errors.Is(err, context.DeadlineExceeded)` → 降级（本客户端 deadline 到期）；
3.  `errors.Is(err, nacosresolver.ErrNoInstances) || errors.Is(err, nacosresolver.ErrStaleExpired)`
    → 降级（解析不到实例，本地开发最常见的触发条件）；
4.  其它一律不降级：`APIError`、`HTTPError`、`ProtocolError`、编码/参数错误、调用方取消。

测试至少应覆盖：

-   `connection refused`：降级；
-   DNS/连接超时：降级；
-   `ErrNoInstances`：降级（新增，报告漏了这条）；
-   `APIError`：不降级；
-   `ProtocolError`：不降级；
-   请求编码错误（例如路径参数缺失）：不降级；
-   调用方取消：不降级。

### P3-02：Multipart 上传当前会把整个文件读入内存

**复核结论（事实成立；"不要描述为流式"这句无的放矢）**

-   **事实成立**：`client/encode.go:195-210` 用 `bytes.Buffer` 收完整个 multipart，
    `encodeBody` 返回 `[]byte` 再交给 `http.Request`；峰值 ≈ 文件大小 × 并发数。
-   **"不要把当前实现描述为大文件流式上传"没有对象**：三个仓库的文档与注释里
    **没有任何一处**这样描述（`grep -rn "multipart|Upload|上传" --include=*.md` 在
    `gofy-interface-api` 下 0 命中）。这句可以删，改成"补一条边界"更有用。
-   **真正缺的是上限**：现在没有任何大小限制，也没有文档说明"这是小文件路径"，
    出问题只能靠线上 OOM 发现。

**涉及文件**

`gofy-interface-api/client/encode.go`

当前调用链：

`encodeBody() → encodeMultipart() → bytes.Buffer → writeMultipart() → io.Copy()`

文件内容通过 `io.Copy()` 写入内存中的 `bytes.Buffer`，最后
`encodeBody()` 返回完整 `[]byte`，再交给 HTTP Request。

**影响**

上传文件越大，单次请求的内存占用越接近文件大小；并发上传时内存占用按并发数叠加。文件可能同时存在于调用方持有的对象、编码缓冲区以及其它业务缓存中。

这不是小文件上传的功能错误，但对于大文件或高并发上传，是明确的资源边界。

**建议（修订）**

-   在 `client.Config` 加 `MaxMultipartBytes`（零值给一个默认上限，例如 16 MiB），
    编码前先累加各 part 的长度，超限返回可识别的错误（`client: multipart body exceeds N bytes`），
    而不是等到 OOM；
-   在 `client` 包文档写一行"multipart 在内存中缓冲，峰值 ≈ 请求体大小 × 并发"，
    让调用方自己决定要不要把大文件放外面；
-   真要支持大文件，是"给 `Call` 加 `io.Reader` body + 放弃重试"的独立改动，
    不要在本轮用加大缓冲区的方式打补丁；
-   测试覆盖：刚好等于上限、超过上限、并发两个中等文件（确认上限按单请求算）。

本项属于容量与资源边界，不建议为了它扩大本轮改动范围（上限这一条除外，它很小）。

### P3-03：手工构造 `nacos.Config` 时，HealthyOnly 的零值可能导致 Resolver 默认包含不健康实例

**复核结论（成立，且现成机制就能修，方案要换）**

-   **事实成立**：`gofy-adapter/client/nacos/resolver.go:126` 是
    `IncludeUnhealthy: !cfg.Discovery.HealthyOnly`。
-   **但报告没看到已有的一半**：`support/nacos/config.go:180` 有一个私有的
    `healthyOnlySet bool`，`normalize()`（`config.go:209-217`）正是用它区分
    "YAML 里写了 `healthy_only: false`"与"没写"（没写 → `HealthyOnly = true`）。
    所以问题不是"没有三态"，而是**这个三态没有导出**，adapter 读不到。
-   **报告的方案会制造新问题**："在统一归一化入口补齐默认值"等于把所有 false 都改成 true，
    显式想"包含不健康实例"的调用方从此无法表达。
-   **正确修法**：导出 `HealthyOnlyEnabled() bool`（`!healthyOnlySet` 时返回 true）与
    `SetHealthyOnly(bool)`，`ConfigFromNacos` 改用前者；或者把字段改成 `*bool`。
    前者改动更小，且保留"显式 false"这条路径。

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

**建议（修订）**

-   `support/nacos` 导出 `DiscoveryConfig.HealthyOnlyEnabled() bool`
    （`!healthyOnlySet → true`）与 `SetHealthyOnly(bool)`（同时置位），
    `normalize()` 改为调用 setter，语义不变；
-   `ConfigFromNacos` 用 `HealthyOnlyEnabled()` 计算 `IncludeUnhealthy`，
    不再直接取字段；
-   文档写清：手工构造 `nacos.Config` 时默认"只选健康实例"，要包含不健康实例必须显式
    `SetHealthyOnly(false)`；
-   测试：零值 Config → `IncludeUnhealthy == false`；`SetHealthyOnly(false)` →
    `IncludeUnhealthy == true`；YAML 写了 `healthy_only: false` 仍为 true。

### 5.1 补充：降级让单次逻辑调用的最坏耗时翻倍（报告未列）

**涉及文件**：`examples/demo-service/serviceclients/serviceclients.go:266-290`、
`gofy-interface-api/client/call.go:74-81`

`degradable.GetConfig` 用**同一个父 ctx** 连续调用两次 `Call`，而每次 `Call` 都会从父 ctx
派生一个 `client.timeout`（默认 5s）的 deadline。于是：

-   正常路径：最坏 5s；
-   降级路径：最坏 10s（5s 用完再 5s）。

`config.yaml` 里写的是"timeout 包含寻址耗时，默认 5s"，读者会理解成"这次调用最多 5s"。
两种改法，任选其一：把注释改成"每次尝试 5s，降级时最坏 10s"；或者给兜底那次传
`client.WithTimeout(剩余预算)`，让整次逻辑调用守住一个上限。前者零风险，建议先做前者。

这不是 bug，是文档与实现的措辞差，因此不单列编号。

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
-   **baseURL 未读取注册器已发布的 `inner_external_base_uri`**，也没有"本地分组 →
    部署分组"的切换（P1-01，成立；参照实现在 PHP 侧）；
-   权重为 0 的实例仍可能被选中（P2-02，成立）。

### 8.3 Demo Service 降级调用链

`DemoService.GetConfig → primary(Nacos Resolver) → shouldDegrade → fallback(BaseURL)`

当前只对 GET
做降级，避免将写操作发往另一个地址造成重复写入，这是正确的保守策略。

但降级判断不应把所有普通 error 都理解为网络失败，详见 P3-01。

此外，fallback URL
代表"当前配置环境中的本服务地址"。生产配置若错误地指向其它环境，可能让故障流量跨环境流动。配置文件注释已经提示这一点，部署时仍需把它作为环境隔离检查项。

------------------------------------------------------------------------

## 9. 建议修复顺序

### 第一批：优先修复（都是小改 + 可测）

1.  **P1-01**：照 PHP 的门控补齐"本地分组 → 部署分组 → metadata"，
    同时把缓存/单飞键改成 `service@group`。先补测试再改实现：三个分支各一条，
    加一条"切换后再次 Resolve 仍先试本地组"。
2.  **P2-02**：权重 <= 0 的实例不参与加权抽取；全为 0 时返回带原因的
    `ErrNoInstances`。改动只在 `resolver.go` 一个函数加两个测试。
3.  **P2-01**：先抽 `endpoint.FieldRequired` 让 schema 与编码器共用判定，再改编码器；
    顺序不能反——先改编码器会让 `omitempty` 字段开始发空值。
4.  **P2-03**：空队列回调套 `HandlerTimeout`（3 行）+ 把"返回值被忽略"写进 `Run` 的注释。

### 第二批：小范围收敛

4.  **P3-03**：导出 `HealthyOnlyEnabled()`/`SetHealthyOnly()`，`ConfigFromNacos` 改用前者。
5.  **P3-01**：降级判定改白名单（`net.Error` + deadline + resolver 两类错误）。
6.  **P3-02**：`MaxMultipartBytes` 上限 + 包文档一句"内存缓冲，峰值≈体积×并发"。
7.  **补充项**：`config.yaml` 注释写清"每次尝试 5s，降级最坏 10s"。

### 第三批：可延后

8.  **P1-01 的观测面**：切换日志接入指标（按 service + 目标分组计数），
    让"本地开发在借开发环境"这件事在监控上可见。

建议不要同时重构
Claim、Resolver、客户端编码和应用生命周期。每项都可以独立小改并增加针对性回归测试。

------------------------------------------------------------------------

## 10. 回归测试清单

### Nacos 注册与发现

-   [ ] Registrar 将 `inner_external_base_uri` 写入实例 Metadata（已有，回归用）；
-   [ ] 本地分组命中 → 用 `IP:port`，即使 metadata 存在也不读；
-   [ ] 本地分组空 + 开关关 → `ErrNoInstances`，不偷偷切分组；
-   [ ] 本地分组与部署分组相同 → 不做第二次查询（避免同组查两遍）；
-   [ ] 本地分组空 + 开关开 + 部署分组有实例 → 用 metadata；metadata 缺失 → 用 `IP:port`；
-   [ ] metadata 非法（非 http(s)/无 host）→ 该实例回退 `IP:port`，服务级仍可用；
-   [ ] 切换结果不写进本地组的缓存键，下一次 `Resolve` 仍先试本地组；
-   [ ] `weight=0` 实例不被选中；
-   [ ] 所有实例权重为 0 → `ErrNoInstances` 且错误信息含 `weight 0`；
-   [x] 零值 `nacos.Config` → `IncludeUnhealthy == false`；`SetHealthyOnly(false)` → `true`；
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
-   [x] 空队列 Handler 也受超时约束；
-   [x] 空队列 Handler 的返回值仍被忽略（这是文档契约，不要顺手改成"上报错误"）；
-   [ ] Handler 副作用完成但 Succeed 失败时，重复执行由业务幂等保证；
-   [ ] rerun_allowed=0 的任务不会被再次调度。

### Interface API Client

-   [ ] 指针 `0`、`false` 能正确编码；
-   [ ] nil 指针仍按未提供处理；
-   [ ] URI 路径参数为非 nil 但值为空时，报"参数缺失"而不是发送空 segment；
-   [ ] Form、Header、Cookie、Multipart 的零值语义一致；
-   [ ] 真实生成客户端最终请求包含 Group Prefix；
-   [ ] 调用方覆盖 PathPrefix 的行为有测试；
-   [ ] SSE 重连重新 Resolve；
-   [ ] Download/Chunked 与普通 Call 使用一致的路径前缀；
-   [ ] `validate:"required"` 的零值字段确实出现在请求里；
-   [ ] `validate:"omitempty"` 的零值字段仍然不发送；
-   [x] Multipart 超过 `MaxMultipartBytes` 时报错而不是继续缓冲。

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

复核后，最值得优先修的是四条具体契约边界：

1.  注册侧发布的外部访问地址在 Go 侧没有消费机制（P1-01）——**PHP 已有参照实现**，
    照它的门控补齐即可，前提是同时改缓存键，否则切换会污染本地分组；
2.  Nacos 权重为 0 的实例仍会获得请求（P2-02，运维语义被破坏，改动最小）；
3.  API Client 对**必填**字段的零值编码不可靠（P2-01，OpenAPI 说必填、请求里没有）；
4.  Claim 空队列回调绕过了统一超时边界（P2-03，只修超时，不动错误契约）。

**建议本轮不要新增功能，也不要继续扩大架构范围。先修复以上问题，再用对应回归测试固定行为。**

本报告是源码级审计，不代表已通过真实基础设施集成验证。特别是 Nacos
外部地址元数据、权重分配、Claim 多实例竞争和真实 HTTP
大文件上传，仍应在对应运行环境进行验证。
