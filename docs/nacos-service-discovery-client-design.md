# Nacos 服务发现接入方案（参考 swoolefy）

> 2026-11-17 复核：对照 swoolefy `swoolefy-6.2-x` 与 gofy / gofy-adapter /
> gofy-interface-api 当前 HEAD 重写。原稿有三处判断已过时（把已落地的事列成缺口），
> 并漏掉一个会在生产放大的可用性缺陷（§4.1）。改动集中在 §1、§3、§4、§5、§6。

## 0. 实施状态（2026-11-17）

阶段零与阶段一已落地，并在本地 docker `dev-nacos`（namespace `dev` / group
`GO_GROUP`）上跑通：HTTP 进程注册、`group3` worker 作为**独立进程**解析服务名并调通
`GET /api/v1/order/config`。

落地过程中撞到两个方案外的问题，一并修了：

1. **分组路由的客户端少拼前缀**。契约把部署前缀放在 `endpoint.Group`、`Spec.Path` 只
   留组内路径，而 `client` 只用 `Spec.Path` 建 URL，于是类型化客户端打的是 `/config`
   而不是 `/api/v1/order/config`，服务端一律 404（`GetOrder` 等同样受影响，集成测试的
   假后端也照抄了错误路径，所以一直没暴露）。修法是新增
   `client.WithPathPrefix`，在唯一构造 URL 的地方拼接，`Call` / SSE / download /
   chunked 全覆盖；demo 的三个客户端分别传各自分组的 `Prefix`。
2. **内网调用走了环境代理**。`client` 与 `support/nacos` 都用
   `http.ProxyFromEnvironment`，而 `NO_PROXY` 通常只含 localhost：从 Nacos 解析出的
   Pod IP / 内网地址被送给代理，目标死掉时拿到的是代理的 502（甚至可能变成一次超时，
   再被发现的节流窗口缓存）。两者都改为默认不走代理，确实需要时由调用方注入
   `*http.Client`。

寻址配置随后按实际用法收敛了一次：原来的 `clients.demoservice.base_url`（每个下游一条）
改成 `client.inner_external_base_uri` + `client.timeout`。语义从"钉死下游地址"变成"本服务
自己在配置来源那套环境里的地址"：注册时写进实例 metadata，调用失败时作为降级目标（§4.7）。


## 1. 背景与结论

> §1–§9 保留复核当时的口径（"缺口"指当时的状态）；已落地的结果见 §0。

`examples/demo-service/application.yaml` 的 `nacos.discovery_service_client` 六个键目前
**会被解析，但在 demo 里没有任何代码消费**（调查见 §3）。在 swoolefy 里，这段配置是
API 客户端通过服务名寻址的核心输入。

结论：

1. **解析层已经完整**，比原稿描述的更完整：配置 → resolver → 类型化 client 三段都在，
   而且"注册 → 发现 → 类型化调用 → 注销后重新不可发现"的端到端用例已经存在
   （`examples/demo-service/internal/integration/nacos/registration_test.go`）。
2. **真正的缺口只有两个**：
   - **P0 可用性缺陷**：resolver 只在成功时更新缓存时间戳，Nacos 不可达后**每个请求**
     都要先吃一次回源超时（默认 10s）才落到 stale。swoolefy 用 `lastFetchAt`（失败也
     更新）节流，没有这个问题。**接线之前必须先修**，否则下游注册中心一挂，本服务每个
     请求被拖 10s，比直接报错更糟。
   - **P1 装配缺口**：demo 没有业务代码调用外部服务，`nacos.New` 也没有非测试调用点。
3. 原稿阶段三列的"端到端测试 / 故障注入"大部分已经写好，**不要再写一遍**（§3.1 列了
   现有用例），剩下的是 `yaml → Resolve` 的串联。
4. 三处与 swoolefy 的语义差异必须写进文档并定死，否则运维会按 PHP 的经验调错参数：
   `cache_ttl` / `stale_ttl` 的 0 值含义、stale 下限的静默修正、`load_balancer` 不支持
   自定义类名。差异清单见 §9。

## 2. swoolefy 的参考链路

配置在 `swoolefy/Test/application.yaml` 的 `nacos.discovery_service_client`。实际文件里
`namespace_id` 为空、`group_name: pwa_group`（与示例的 `dev/GO_GROUP` 值不同，不影响
链路；namespace 为空时回退 `service_register.namespace_id`）。

### 2.1 加载与注入

```
application.yaml
  → ApplicationConfig::load()                  src/Support/ApplicationConfig.php:36
  → nacosSection('discovery_service_client')   src/Support/ApplicationConfig.php:184
  → DiscoveryConfig（final readonly VO）       src/Support/Nacos/Discovery/DiscoveryConfig.php:40
  → DiscoveryClient::create()                  src/Support/Nacos/Discovery/DiscoveryClient.php:67
  → NacosDiscoveryDriver + LoadBalancerFactory 同上 :67-88
```

框架没有发现客户端的常驻进程，业务按需调用 `DiscoveryClient::create()`（参考
`Test/Scripts/TestScript/NacosTest.php:97`）。注入方式是**工厂 + 构造注入**，没有服务
容器绑定；`DiscoveryClient` 的构造函数额外接受 `clock` 闭包，纯为测试注入。

### 2.2 七个键

| 键 | 行为 | 默认值 / 回退 |
|---|---|---|
| `load_balancer` | `random` / `round_robin` / `weight`，其余值当**自定义类名**反射实例化 | `random` |
| `cache_ttl` | 实例列表保鲜时长；同时是**回源节流窗口**（失败也计时） | 60s；`<=0` 表示每次调用都回源、不缓存 |
| `stale_ttl` | Nacos 不可达时旧列表还能用多久，超时清空 fail-closed | `max(3 × cache_ttl, 180s)`；`<=0` 表示**不兜底** |
| `healthy_only` | 既作为 Nacos API 参数，也在本地二次过滤 | `true` |
| `namespace_id` | 发现请求的 namespace | 空则回退 `service_register.namespace_id` |
| `group_name` | 发现请求的 group | 空则回退 `service_register.group_name` |
| `clusters` | 逗号分隔的集群过滤，作为 Nacos API 参数 | 空表示不过滤 |

原稿只列六个键，漏了 `clusters`——它在 `DiscoveryConfig` 与驱动里都被消费，Go 侧也已
支持。`public` 规范化为空串（`DiscoveryConfig.php:87-90`），env 优先于 yaml（`:48-56`）。

### 2.3 请求时的行为（校正版）

- **两个时间戳，口径不同**：`lastFetchAt` 每次拉取结束都更新（成功、成功空列表、异常）；
  `lastSuccessAt` 只在 driver 正常返回时更新。前者用于节流，后者用于 stale 判定。
- `DiscoveryClient::getInstances()`（`DiscoveryClient.php:108-120`）：`isFetching` 为
  false 且 `now - lastFetchAt >= cache_ttl` 才回源；否则直接返回内存列表。**所以 Nacos
  挂掉时，每个 cache_ttl 窗口只探一次**。
- `fetchInstances()`（`:166-208`）：异常时更新 `lastFetchAt` 但不动 `lastSuccessAt`；
  `canServeStale()`（`:219-227`）在 `stale_ttl` 内返回旧列表，超时清空。成功空列表会
  覆盖 `instances`，同时刷新两个时间戳。
- 缓存直返路径**不复查** `lastSuccessAt`：因此守卫可以比 stale_ttl 多用一个 cache_ttl
  （有界越界，`<= cache_ttl`）。Go 侧不跟随（§4.1）。
- 选择：`choose()`（`:146-156`）选不到时强制刷新再选一次，返回 `?ServiceInstance`
  （调用方必须判空）；`chooseUri()`（`:158-161`）拼 `scheme://ip:port`。
- 负载均衡：`RandomLoadBalancer.php:23-33`、`RoundRobinLoadBalancer.php:23-52`（随机起点
  + 游标回绕）、`WeightLoadBalancer.php:23-49`（权重和 ×1000 定点轮盘赌）。
- HTTP 结合：`docs/InterfaceApi.md:680-752` 的 `BaseClientApi` 在未给 `base_uri` 时用
  `NacosServiceDiscovery::resolveInstance()` 委托发现；`requestWithConnectRetry()` :728-752
  连接失败重新解析换实例重试（GET 1 次、POST/PATCH 0 次，上限 3）。**换实例不排除刚失败
  的地址**，也没有熔断。

## 3. Go 侧现状

### 3.1 能力对照

| 能力 | swoolefy | Go 现状 | 结论 |
|---|---|---|---|
| yaml 六键 + clusters 解析 | `DiscoveryConfig` VO | `support/nacos/config.go` 的 `DiscoveryConfig` + `normalize()` | 已具备 |
| namespace/group 回退注册段 | 有 | `Config.normalize()` 先归一 `Register` 再给 `Discovery` 兜底 | 已具备 |
| 三种负载均衡 | 有（+自定义类名） | `resolver.go` 的 `chooseLocked` / `weightedPick` | 已具备，不支持自定义类名 |
| cache_ttl 节流 | **失败也更新 `lastFetchAt`** | **只在成功时写 `fetchedAt`** | **缺口，见 §4.1** |
| stale_ttl / fail-closed | 按 `lastSuccessAt` | `resolveRefreshed()` 按 `lastSuccess` | 已具备（Go 更严） |
| 成功空列表立即生效 | 覆盖 `instances` | 删除缓存后返回 `ErrNoInstances` | 已具备，但会退化成每次回源（§4.1.2） |
| healthy_only | API 参数 + 本地过滤 | `InstanceQuery.HealthyOnly` + `AvailableInstances` | 已具备 |
| 并发刷新去重 | `isFetching`（可能返回旧值） | `inflight`，等待者共享结果并各自尊重 ctx | 已具备，比 swoolefy 强 |
| `choose()` / `chooseUri()` | 有 | `Resolve(ctx, service) (baseURL, err)` | 等价，保持 |
| 每次 attempt 重新解析 | `requestWithConnectRetry()` | `client/call.go` 的 `send()` 每 attempt 调 `base()` | 已具备（都不排除失败地址） |
| 非幂等不重试 | GET 1 / POST 0 | `retryFor()`：GET/HEAD/OPTIONS/DELETE，写方法需 `Idempotency-Key` | 已具备，比 swoolefy 宽 |
| 时钟注入 | 构造注入 `clock` | 无 | 可选（§4.5） |
| 配置期校验 `load_balancer` | 首次选择时才抛 | 无，要到 `nacos.New` 才报错 | **缺口，见 §4.3** |
| 回源超时 | Guzzle 的 connect_timeout | 复用 `nacos.Timeout`（默认 10s，配置中心口径） | **缺口，见 §4.2** |
| yaml → Resolve 串联 | 有 NacosTest | `TestNewFromFiles` 只校验 cfg | 缺口（小） |
| 端到端（注册→发现→调用→注销） | 有 | `internal/integration/nacos/registration_test.go` | **已具备** |
| 配置 → resolver → client → 业务接线 | 工厂 + 业务调用 | 缺装配根接线 | **主要缺口** |

### 3.2 数据流（解析层无断裂）

```
application.yaml nacos.discovery_service_client
  → support/nacos/config.go   rawApplication.Nacos.DiscoveryServiceClient
  → config.go                 cfg.Discovery = ...（秒 → Duration、healthyOnly 指针）
  → Config.normalize()        Register.normalize(ns) → Discovery.normalize(reg)
  → adapter/client/nacos.ConfigFromNacos(cfg)
  → adapter/client/nacos.New(cfg) → *Resolver
  → Resolver.Resolve(ctx, service) → baseURL
```

adapter 直接 import `github.com/bingcool/gofy/support/nacos`，两边是同一份配置类型，不需要
转接层。

### 3.3 已经存在的测试（不要重写）

| 用例 | 位置 | 覆盖 |
|---|---|---|
| 并发只回源一次 | `gofy-adapter/client/nacos/resolver_test.go` | `inflight` 单飞 |
| 命中缓存不回源、轮询、过滤不健康、scheme metadata | 同上 | 选址 |
| Nacos 挂掉在 stale 内继续用、超时后报错 | 同上 | stale / fail-closed |
| 无实例返回 `ErrNoInstances` | 同上 | 空列表 |
| 每次调用都问 resolver | `gofy-interface-api/client/fixture_test.go` | 换实例重试的前提 |
| SSE 重连重新解析 | `gofy-interface-api/client/sse_reconnect_test.go` | 长连接 |
| 注册 → 发现 → 类型化调用 → 注销 | `demo-service/internal/integration/nacos` | 真实契约全链路 |

**还缺的只有**：`NewFromFiles → Resolve` 的串联、失败节流的次数断言、stale 越界的断言、
`load_balancer` 非法值在配置期报错。

### 3.4 为什么现在"没用上"

demo 的 `nacosCfg` 只去了三处：

- `main.go:59` `nacos.Sync` —— 拉远端配置（受 `enableNacosSyncConfig` 控制）
- `main.go:67` Watch —— 配置变更监听
- `options.go` `gofy.WithNacosRegistration(nacosCfg)` —— **服务注册**，用 `Register` 段，
  且 `enable_nacos_register: false`

全仓没有 `nacos.New(...)` 的非测试调用。`adapter/gofy.BindOptions` 只含
Global/Routes/Middleware/Registry/RateLimit/AuthValidator，没有 client/resolver 注入点。

order → product 走的是**进程内 innerapi**（`order/service/dependencies.go` 的
`productapi.ProductAPI`），不经过 HTTP，所以它**不能**用来验证服务发现（原稿 §8 的这一步
不成立）。生成的 `demoserviceclient` 已提供 `New` / `NewWithResolver` / `NewWithBaseURL`
三种构造器，只在 integration 测试里用过。

## 4. 推荐方案

### 阶段零（P0，必须先做）：修掉失败回源放大

#### 4.1 失败也要节流

现状：`entry.fetchedAt` 只在成功时写。Nacos 挂掉且 `now - fetchedAt >= CacheTTL` 之后，
**每次 `Resolve` 都会成为 leader、打一次 Nacos、等满 `nacos.Timeout`（默认 10s），再走
stale**。并发时 `inflight` 能合并同一批，但每个新请求仍要等这 10s。这不是"多查几次"，
是把注册中心故障直接放大成调用方全线超时。

改法：entry 拆成两个时间戳，语义照 swoolefy 但不复制它的越界行为。

```go
type entry struct {
    instances   []nacosconfig.Instance
    lastAttempt time.Time // 每次回源结束都写：节流用
    lastSuccess time.Time // 只在成功时写：stale 窗口用
    lastErr     error     // 最近一次失败原因，供节流窗口内直接报错
}
```

`Resolve` 的判定顺序（这一段是这次改动的主体）：

```go
now := time.Now()
if e := r.cache[service]; e != nil && now.Sub(e.lastAttempt) < r.cfg.CacheTTL {
    switch {
    case len(e.instances) > 0 && now.Sub(e.lastSuccess) <= r.cfg.StaleTTL:
        return baseURL(r.chooseLocked(service, e.instances)), nil // 命中或 stale 命中，不打 Nacos
    case e.lastErr == nil:
        return "", fmt.Errorf("%w: %q", ErrNoInstances, service)  // 权威空列表
    default:
        return "", fmt.Errorf("%w: %q: %w", ErrStaleExpired, service, e.lastErr)
    }
}
```

失败分支写 `lastAttempt = now; lastErr = err`，**不动** `instances` / `lastSuccess`。
成功分支三个字段一起写。

三个必须守住的点：

0. 判定与选址都在 `r.mu` 内完成（rr 游标要在锁内推进）；等待者拿到 leader 的共享结果后，
   **回到同一段判定**，而不是各自再算一遍 stale 窗口。

1. **stale 边界不能被节流窗口拉长**。swoolefy 的缓存直返路径不复查 `lastSuccessAt`，最多
   多用一个 cache_ttl；Go 这里在节流窗口内也要查 `lastSuccess`，超出就直接报错。这是有意的
   严格化，写进文档。
2. **节流窗口内不再回源**，即使已经超出 stale——回源只会再等一个超时，结果不会更好。
3. 新增 `ErrStaleExpired`，让调用方能区分"注册中心里没有实例"和"注册中心不可达且缓存过期"；
   `errors.Is(err, ErrNoInstances)` 的现有语义不变。

用例：fake Nacos 置 `fail=true`，`CacheTTL=50ms`、`StaleTTL=1h`，连打 100 次 `Resolve`
断言 `calls == 1` 且都返回旧地址；再把 `StaleTTL` 调到 1ms，断言返回 `ErrStaleExpired`
且不回源。

#### 4.1.2 空列表改为负缓存

现在空列表走 `delete(r.cache, service)`，等于把节流也一起删掉：服务正在发布、实例暂时为 0
时，每个请求都会打一次 Nacos。改为**存一条空 entry**（`lastErr = nil`），"成功空列表立即
生效"的语义不变（旧地址立刻被覆盖），只是重查节奏回到 `cache_ttl`。恢复延迟的上界因此是
`cache_ttl`，与正常缓存一致。

#### 4.2 回源超时与配置中心超时分离

`ListInstances` 用 `context.WithTimeout(ctx, c.cfg.Timeout)`，而 `nacos.Timeout` 默认
10s，是给配置中心 `Sync` 用的。服务发现位于请求路径上，10s 的失败代价不可接受。

- `DiscoveryConfig` 增加 `Timeout time.Duration`（yaml `timeout_ms`，默认 2000ms），
  `ConfigFromNacos` 映射到 `nacos.Config.Timeout`，`list()` 用它包一层 ctx。
- 不要靠调小 `NACOS_TIMEOUT_MS` 解决：它同时会缩短配置同步的超时，制造另一类故障。

#### 4.3 `load_balancer` 在配置期校验

现在拼错（如 `round-robiin`）一路无声通过 `normalize()`，直到 `nacos.New` 才返回
`ErrUnknownLoadBalancer`——接线后表现为启动失败，但错误里没有"哪个键"。

- `DiscoveryConfig` 增加 `Validate()`，由 `Config.Load` 调用，报错带上
  `nacos.discovery_service_client.load_balancer`。
- 同时把 `stale_ttl < cache_ttl` 从**静默改写**改为**报错**（仅当 `stale_ttl` 是显式配置
  时）。静默把 100s 改写成 180s 会让"我明明配了更严格的失败窗口"变成一个假象。
- 认哪些别名（`round-robin`、`roundrobin`、`weighted`）写进文档。

#### 4.4 `ConfigFromNacos` 的空值兜底

`ConfigFromNacos` 取的是 `cfg.Discovery.Namespace/Group`。生产路径传的是 `Load()` 归一过
的配置，没问题；但它是导出函数，手工构造 `nacos.Config{Namespace: "dev"}` 时会得到空
namespace，**静默查到 public 命名空间**。加一层 `firstNonEmpty(Discovery.X, cfg.X)` 兜底，
成本两行。

#### 4.5 时钟注入（可选，降级）

原稿把它列为缺口。实际测试已经用毫秒级 TTL 覆盖了 stale 的两侧，注入 clock 只对"边界毫秒"
的断言有价值。做法是给 `Resolver` 加一个未导出的 `now func() time.Time`（`nil` 时取
`time.Now`），包内测试直接赋值，**不要**为它改 `Config`、更不要改 `client.Resolver` 接口。

### 阶段一（P1）：装配根接线

#### 4.6 归属：resolver 在 `main` 建一次，不在 `factories` 里建

原稿说"在 `options.go` 或新增 `serviceclients.go` 完成一次接线"，和它自己"resolver 是进程级
单例"的要求冲突：`options.go` 里调用的是 `reload.factories(cfg)`，而 `factories` **每次
Reload 都会重新执行**（`reload.go:58-79`），在那里建 resolver 就是每代一个，缓存、rr 游标、
inflight 表全部丢失。

正确的持有链，与 `nacosCfg` 完全同构：

```
main.go: nacosCfg 加载后
  → serviceclients.New(cfg.Clients, nacosCfg)   // 一次，失败即 fatalf
  → appworker.Register(conns, clients)          // 消费者直接拿，不进容器
  → 将来有 service 需要时，再把同一个指针交给 reloader.factories
```

要点：

1. **消费者需要才注入，不为"以后可能用"提前塞进容器**。容器每代新建，把进程级客户端
   放进去会让人误以为它跟着 Reload 重建；当前唯一的消费者是 `group3`，所以只传给
   worker。出现 service 消费方时，再由 `main` 把同一个指针交给 `reloader`，容器只捕获
   指针、不重建。
2. **不要放进 `app/appio`**。那个包是"连接 + 代际"，`Reload` 会重建整套连接；resolver 的
   连接参数在启动期固定（`reload.go:29-32` 已明确 nacos 连接要重启才生效），放进去每代重建
   会丢掉缓存与游标。
3. **worker 与 script 也要拿得到**。`worker-run` / `script` 的 plan 都是
   `BuildServices: true`，会走 `options.go`，所以从 `main` 传下去即可。注意
   `planCommand("script")` 现在**没有** `LoadNacos`，脚本里会拿到零值 `nacosCfg`（指向
   127.0.0.1:8848）——加脚本消费方时必须同时把这一项补上。
4. **Close**。`gclient.Client.Close()` 已存在；`Resolver.Close()` 关闭 naming 客户端的
   空闲连接，`serviceclients.Clients.Close()` 统一关闭，`main` 在 `app.Execute` 返回后
   defer 调用。

#### 4.7 三种寻址方式

`gclient.New` 已经保证 BaseURL 与 Resolver 二选一（`client/client.go:129-149`），接线层
不要再判断，但**要在建 client 之前决定用哪种**：

| 条件 | 构造 | 用途 |
|---|---|---|
| `nacos.enabled` 为真 | `NewWithResolver`，另按需加一个指向 `inner_external_base_uri` 的降级 client | 生产按服务名寻址；本地开发靠降级 |
| 否则 `client.inner_external_base_uri` 非空 | `NewWithBaseURL` | 离线、CI、灰度直连 |
| 都不成立 | 不建 client，注入 `ErrNotConfigured` 占位实现 | 无注册中心的单机跑法 |

`client.inner_external_base_uri` 是**本服务自己**在"配置来源那套环境"里的地址，不是
下游地址表。它有两个使用方：

- **注册**：写进实例 metadata 的 `inner_external_base_uri`
  （`support/nacos.RegisterConfig.SetInnerExternalBaseURI`，会补 scheme、去尾斜杠），
  解析到本实例的调用方可以拿它替代 Pod IP——办公网连不上 Pod IP，但连得上这个地址；
- **本进程的客户端**：按服务名调用失败时改打它，重试一次。本地开发因此不必启动本地
  服务：拉 dev 的 `config.yaml`（里面就是 dev 的地址），打没起的本地实例失败后降级到
  dev 的部署。

降级的判定是"**没有响应到达**"：`APIError` / `HTTPError` / `ProtocolError` 说明服务端
答复了（哪怕是 500），不重试；调用方自己取消（`ctx.Err() != nil`）也不重试；但本客户端
自己的 deadline 到期**会**重试，那正是 Pod IP 被黑洞的情形。只包读操作——把写重试到另一
个实例不是兜底，是写两次。

一个值够用，是因为 demo 只按一个服务名调用，而那个服务名就是它自己。一个服务调多个下游
时兜底必须按下游分别配置，不能共用这一个值。

`nacos.enabled` 是框架既有的总开关（`support/nacos/config.go` 的 `Load`：显式 false 才关，
否则默认开），关掉它等于同时关掉拉取、注册与发现，不需要为发现再加一个开关。

第三条是原稿缺的一环：demo 的 `nacos.yaml` 存在（`nacos.Enabled` 默认 true），但地址是内网
IP；`nacos.New` 只校验端口，空地址会被 `normalize()` 补成 `127.0.0.1:8848`，**构造期无法
分辨"没配"和"配错"**。所以必须由装配根显式选路，否则本地一跑就是每次调用等 2s 超时。

#### 4.8 服务名来自契约

服务名用 `demoservice.ServiceName`（`examples/interface-service/appinterface/demoservice/nacos.go`），
不要在业务代码里写字符串。下游新增服务时，生成器已经把它写在 `client.go` 里。

#### 4.9 怎么证明链路通了

原稿的 `order → product` 不经过 HTTP（§3.4），证明不了任何事。实际采用的验证是：

- `worker/group3`（`worker --group=group3`，每 5s）作为**独立进程**，用
  `demoserviceclient` 走 resolver 调 `GET /api/v1/order/config`；
- 需要自发现时打开 `enable_nacos_register: true`，本进程注册后由 resolver 解析回自己；
- `internal/integration/nacos` 已有 `注册 → 发现 → 类型化调用 → 注销后不可发现` 的用例（§4.10）。

### 阶段二（P2）：adapter 提供入口，不新增 gofy Option

如果只有 demo 一个消费方，装配根手写即可。要复用时的最小做法：

- 在 `gofy-adapter/client/nacos` 里加 `NewFromConfig(cfg nacosconfig.Config) (*Resolver, error)`
  ——就是 `ConfigFromNacos` + `New` 两行，**不新建 `adapter/nacosclient` 包**：适配器层已经
  是"客户端侧"的归属地。
- **不要加 `gofy.WithServiceResolver`**。原稿已经正确地拒绝了 `BindOptions`，但 `gofy.Option`
  是同一个问题的另一面：把客户端依赖塞进服务端 App 容器，等于给业务代码开一条读全局的后门，
  与 `app/appio` 显式代际、`container` 构造注入的模型冲突。客户端依赖由组合根持有。

### 阶段三（P2）：补齐剩余测试与文档

1. `NewFromFiles → Resolve`：`TestNewFromFiles` 之后接着解析一次（fake Nacos 的 Addr 由
   `nacos.yaml` 提供）。
2. 失败节流：见 §4.1 的用例。
3. stale 越界：断言 `ErrStaleExpired`，且响应里不带旧地址。
4. `load_balancer` 非法值：在 `config.Load` 层断言错误信息带键名。
5. 文档：七个键 + `timeout_ms` 的含义、默认值、0 值语义、env 覆盖名（
   `NACOS_DISCOVERY_LOAD_BALANCER` / `_CACHE_TTL` / `_STALE_TTL` / `_HEALTHY_ONLY` /
   `_CLUSTERS`），以及"本地用 `client.inner_external_base_uri` 绕过/兜底 Nacos"的做法。

## 5. 关键设计取舍（校正）

- **Resolver 返回 baseURL，不返回实例对象**。`client.Resolver`（`client/resolver.go:11`）
  已经这样定义，生成的客户端也依赖它；改签名会波及整个接口层。metadata、权重需要时另开接口。
- **每次 attempt 重新 Resolve**。`client/call.go` 的 `send()` 已经这么做，等价于 swoolefy
  的换实例重试；不要改成"一次调用只解析一次"，否则连接失败无法换节点。
- **但要承认两边都不排除刚失败的地址**。默认 `random` + 2 个实例时，单次重试仍有 50% 打回
  原节点（`round_robin` 因为游标前进不受影响，`weight` 视权重）。原稿写"等价，保持"不够
  诚实：这是已知限制，彻底解决需要给 Resolver 加"排除地址"的能力，本期不做。
- **stale 只在失败时启用，成功空列表立即生效**。Go 侧语义保持，但实现从"删缓存"改为
  "负缓存 + 节流"（§4.1.2）——"立即生效"说的是地址立刻被覆盖，不是"每次调用都重查"。
- **fail-closed 优先于可用性**。超过 `stale_ttl` 一律报错，且节流窗口内不再回源；不把明显
  过期的地址继续交给调用方，也不用"再试一次"把故障时间乘以倍数。
- **不引入服务容器大改**。阶段一不改 gofy 框架、不改 `BindOptions`；先用一条真实调用证明
  链路，再决定是否上升到框架能力。

## 6. 风险与验证清单

| 风险 | 验证方式 |
|---|---|
| Nacos 挂掉后每请求多付一次回源超时 | 失败节流用例：`calls == 1`、P99 不随请求数增长 |
| 节流窗口把 stale 边界拉长 | 断言 `ErrStaleExpired`，且不返回旧地址 |
| resolver 在 Reload 时被重建 | 容器测试断言两次 `factories` 拿到同一 resolver 指针；启动日志打印一次 |
| namespace/group 配错导致查不到实例 | 启动时按 info 打一行 `discovery addr/namespace/group/clusters`；dev-nacos 真实验证 |
| 本地无 Nacos 导致每次调用等 2s | 三种寻址优先级（§4.7）；未配置时注入 `ErrNotConfigured` |
| 空列表期间把 Nacos 打成热点 | 负缓存用例：实例归零后 N 次调用只回源 1 次 |
| 非幂等请求换实例重试造成重复副作用 | 保留 `retryFor()` 的方法 + `Idempotency-Key` 判断，不得对所有方法开放 |
| 配置中心超时被误调小 | 发现超时独立成 `timeout_ms`，不复用 `NACOS_TIMEOUT_MS` |

## 7. 涉及文件（预估）

**阶段零**

- `gofy-adapter/client/nacos/resolver.go`（`entry` 双时间戳、`ErrStaleExpired`、`Close`、
  `list()` 超时）
- `gofy-adapter/client/nacos/resolver_test.go`（节流、越界、负缓存）
- `gofy/support/nacos/config.go`（`DiscoveryConfig.Timeout`、`Validate`）
- `gofy/support/nacos/*_test.go`
- `gofy-interface-api/client/encode.go`、`options.go`（`WithPathPrefix`）、
  `client.go`（默认不走环境代理）

**阶段一**

- `examples/demo-service/serviceclients/serviceclients.go`（新，三种寻址方式）
- `examples/demo-service/main.go`（构造一次并向下传）
- `examples/demo-service/worker/worker.go`、`worker/group3/*`（新分组：独立进程验证）
- `examples/demo-service/app/config/clients_config.go`（`clients` 段）
- `examples/demo-service/config.yaml`、`application.yaml`（`timeout_ms`）

**阶段二（可选）**

- `gofy-adapter/client/nacos/resolver.go`（`NewFromConfig`）

**阶段三**

- `gofy-adapter/client/nacos/resolver_test.go`、`examples/demo-service/internal/integration/nacos/`

## 8. 建议的落地顺序

1. **阶段零**：失败节流 + 独立超时 + 配置期校验。这几项不改架构，但决定后面接线是"可用"
   还是"放大故障"。
2. **阶段一**：装配根接线 + `script call-demo` 跑通一次真实调用。
3. **阶段三**：`yaml → Resolve` 串联 + 文档。现有故障注入用例直接复用，不要重写。
4. 观察是否有第二个应用需要同样接线，再决定阶段二。

## 9. 与 swoolefy 的差异清单（必须写进使用文档）

| 项 | swoolefy | gofy 现状 | 处理 |
|---|---|---|---|
| `cache_ttl: 0` | 每次调用都回源 | 视为未设置，回落 60s | 保持 Go 语义，文档写清 |
| `stale_ttl: 0` | 不兜底，立即 fail-closed | 视为未设置，回落 `max(3×cache,180s)` | 保持 Go 语义；显式禁用的写法另外约定（建议负值） |
| `stale_ttl < cache_ttl` | 不处理 | 静默改写为 `max(3×cache,180s)` | 改为配置期报错（§4.3） |
| `load_balancer` 未知值 | 当自定义类名反射 | `New` 返回 `ErrUnknownLoadBalancer` | 提前到配置期报错 |
| 失败回源节流 | 失败也更新 `lastFetchAt` | 只在成功时更新 | 本次修复（§4.1） |
| 缓存命中后的 stale 复查 | 不复查，可越界 `cache_ttl` | 复查，严格 fail-closed | 保留 Go 的更严格行为 |
| 空列表 | 覆盖为空列表 | 删缓存（每次回源） | 改为负缓存（§4.1.2） |
| `clusters` | 支持 | 支持 | 文档补齐第七个键 |
| 服务注册 `ephemeral` | 可配 | `application.yaml` 的 `ephemeral` 未被解析，注册固定 `Ephemeral: true` | 要么接上，要么从示例里删掉这个键 |
