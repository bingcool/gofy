# support/middleware

Gin 中间件集合：请求 ID、访问日志、异常恢复、Body 上限、CORS、JWT、限流、
安全响应头与请求头透传。业务通常不直接 `engine.Use`，而是通过根包的
`gofy.With*` 选项交给 App 按固定顺序安装。

本 README 重点说明这些中间件在 **项目契约模块（interface-service）** 场景下如何接入：
服务端如何保护契约路由，客户端如何在向下游调用时透传请求头与身份。

## 一、中间件清单

| 中间件 | 契约别名常量 | 构造方式 | 根包选项 | 作用 |
| --- | --- | --- | --- | --- |
| `RequestID` | `NameRequestID` = `request-id` | `RequestID(support.DefaultRequestIDHeader)` | 自动安装 | 上游 `X-Trace-ID` 合法则复用，否则生成 UUID；写回响应头与 request context，下游 interface-service 客户端自动透传 |
| `AccessLog` | `NameAccessLog` = `access-log` | `AccessLog(logger, opts)` | `WithAccessLog(true)`（默认）/ `WithAccessLogOptions(opts)` | 结构化访问日志；默认跳过 `/healthz`、`/readyz`；query 中的 token/password 等敏感键脱敏；慢请求阈值 500ms |
| `Recovery` | `NameRecovery` = `recovery` | `Recovery(logger, responder)` | `WithRecovery(true)`（默认开） | panic → 500 标准包络，堆栈只进日志不外泄 |
| `BodyLimit` | `NameBodyLimit` = `body-limit` | `BodyLimit(maxBytes)` | `WithMaxBodyBytes(n)` | `http.MaxBytesReader` 限制请求体，超限由绑定层映射为 413 |
| `CORS` | `NameCORS` = `cors` | `CORS(cfg)` | `WithCORS(cfg)` | 裸域名展开 http/https、`*.example.com` 子域、路径白名单、非法来源 403；详见包内 cors.go |
| `JWTAuth` | `NameJWTAuth` = `jwt-auth` | `JWTAuth(cfg, responder)` | `WithJWTAuth(cfg)` | 校验 Bearer token，claims 写入 gin context 与 `frameworkcontext`；失败返回标准包络 |
| `RateLimit` | `NameRateLimit` = `rate-limit` | `RateLimit(cfg, responder)` | `WithRateLimit(cfg)` | 按 IP + 方法 + 路由分桶的滑动窗口；默认失败关闭（503） |
| `SecurityHeaders` | `NameSecurityHeaders` = `security-headers` | `SecurityHeaders(cfg)` | `WithSecurityHeaders(cfg)` | `X-Content-Type-Options`、`X-Frame-Options`、Referrer-Policy、HSTS、CSP、Permissions-Policy 与自定义头 |
| `HeaderPropagation` | `NameHeaderPropagation` = `header-propagation` | `HeaderPropagation(trustedProxies)` | `WithHeaderPropagation(true)` | 把入口白名单头写入 context；身份头只采信可信代理，`Authorization`/`Cookie` 永不透传 |

`support.MiddlewareFunc` 就是 `gin.HandlerFunc`，因此自定义 gin 中间件可以直接
传给 `gofy.WithMiddleware(...)` 或 `support.Router.Use(...)`。

别名常量只是**名字**，不会自动注册，也不改变 App 级安装顺序：全局链仍由
`gofy.With*` 选项决定。App 构建 HTTP 层时会创建一份注册表，先把内建中间件注册
进去，再执行 `WithMiddlewareRegistrar` 注册应用层别名；契约路由按名字从 Router
携带的这份注册表取实例（见第三节第 3 小节）。业务自定义中间件同样在自己的文件里
导出 `NameXxx` 常量，保持「别名与实例一一对应」。

## 二、App 的安装顺序

`gofy.New` 在构建 HTTP server 时按下面顺序安装（`start.go`）：

```text
RequestID
  → OpenTelemetry Gin span（启用 tracing 时）
  → HeaderPropagation（WithHeaderPropagation(true) 时）
  → SecurityHeaders
  → AccessLog
  → CORS
  → Recovery
  → BodyLimit
  → WithMiddleware 追加的业务中间件
  → /healthz、/readyz 与 404/405 兜底
  → 内建别名逐个注册 + MiddlewareRegistrar 注册应用别名
  → 模块路由子 Router：RateLimit → JWTAuth
  → 契约路由（adapter 的 Global/Routes/Auth/RateLimit 中间件）
  → 业务 handler
```

要点：

- **健康检查与未知路径不经过鉴权和限流**；JWT/RateLimit 安装在模块子 Router 上。
- **CORS 在 JWT/限流之前**：预检请求由 CORS 统一回 204，不会要求 token。
- **Recovery 在业务中间件外层**：业务 panic 仍会得到标准 500 包络。
- 顺序不可随意调整：`RequestID` 必须最先建立 `req_id`，`HeaderPropagation` 要用
  `req_id` 或 OTel trace id 兜底 `X-Trace-Id`。

访问日志默认启用，配置固定为 `middleware.DefaultAccessLogOptions()`。需要改跳过
路径、慢请求阈值或字段开关时用 `WithAccessLogOptions`；传入的配置**按字面生效**，
不与默认值合并（切片为 nil 时仍由 `AccessLog` 补默认 SkipPaths / SensitiveQuery）：

```go
gofy.WithAccessLogOptions(middleware.AccessLogOptions{
    SkipPaths:     []string{"/healthz", "/readyz", "/metrics"},
    SlowThreshold: 300 * time.Millisecond,
    LogRequestID:  true,
    LogClientIP:   true,
    MaxPathLength: 512,
    // LogQuery 默认关闭。SensitiveQuery 是已知键名的黑名单，盖不住业务自定义参数；
    // 只有参数明确安全或有白名单约束时才显式打开。
})
```

### 请求 ID 与下游透传

`RequestID`（`req_trace_id.go`）保证「一次请求一个 trace」：

1. 上游请求带合法 `X-Trace-ID` 时原样复用；缺失或非法时生成 UUID（即使配置了
   自定义头，也优先沿用上游 `X-Trace-ID`）；
2. 写入 `gin.Context`（响应包络 `req_id`）、请求/响应头；
3. 写入 request context 的两层 key：
   - `support.ContextWithRequestID`：业务用
     `support.RequestIDFromContext(ctx)` 读取；
   - `headerpropagation` 的 `X-Trace-Id`：`gofy-interface-api/client` 会从
     同一个 context key 自动注入下游请求，不需要手工调用
     `client.ContextWithRequestID`。

```go
func (h *Handler) Do(c *gin.Context) {
    reqID := support.RequestIDFromContext(c.Request.Context())
    res, err := client.GetOrder(c.Request.Context(), req) // 自动携带 X-Trace-Id
    _ = reqID
}
```

即使没有启用 `WithHeaderPropagation`，这一层透传仍然生效；启用后
`HeaderPropagation` 会用上游 `X-Trace-Id` 或当前 req_id 继续补齐其余身份头。

## 三、在业务服务端使用

项目契约的路由通过 `adapter/gofy.BindGroup`（按分组）或
`BindAll`（按路由集合）绑定到 `support.Router`。中间件分两层：

### 1. App 级（全局，所有契约路由生效）

```go
app, err := gofy.New(
    gofy.WithName("order-gateway"),
    gofy.WithHTTP(cfg.HTTP),
    gofy.WithCORS(support.CORSConfig{
        AllowOrigins:        []string{"example.com"}, // 自动展开 http/https
        AllowOriginPatterns: []string{"*.example.com"},
        AllowPaths:          []string{"/api/*"},
        MaxAge:              24 * time.Hour,
    }),
    gofy.WithSecurityHeaders(support.SecurityHeadersConfig{
        ContentTypeOptions: "nosniff",
        FrameOptions:       "DENY",
        ReferrerPolicy:     "strict-origin-when-cross-origin",
    }),
    gofy.WithJWTAuth(support.JWTAuthConfig{
        Parser:    jwtParser,          // support/jwtauth.Parser，必填
        SkipPaths: []string{"/api/v1/public"},
    }, responder),
    gofy.WithRateLimit(support.RateLimitConfig{
        Store:  ratelimit.NewMemoryStore(),
        Limit:  100,
        Window: time.Minute,
    }, responder),
    gofy.WithMaxBodyBytes(8<<20),
    gofy.WithHeaderPropagation(true),
    // 应用层中间件（auth / audit / order-audit）在模块绑定路由之前注册，
    // 契约只写逻辑名，模块不再自己创建注册表。
    gofy.WithMiddlewareRegistrar(demoMiddlewareRegistrar(cfg)),
    gofy.WithModuleFactories(order.NewModuleFactory()),
)
```

### 2. 契约路由级（adapter 的 BindOptions）

契约里只声明中间件的**逻辑名**：分组级 `group.Use("audit", "auth")`、路由级
`spec.Middleware = []string{"order-audit"}`（或链式 `.Use("order-audit")`）。
App 启动时把这些名字对应的实例注册进共享注册表并随 Router 下发，
`adapter/gofy.BindGroup` 负责建组并解析分组名，内部 `BindAll` 解析路由名；
业务模块只绑定路由，不再创建或注入注册表。

认证没有特殊通道：`spec.Auth = AuthRequired` 只写入 OpenAPI 标记，adapter
不会自动附加认证中间件；需要认证时像其他中间件一样显式声明名字（上面
`group.Use` 里的 `"auth"`）。所有名字都必须显式注册，没有默认别名或兜底。

最终顺序：
`分组中间件（group.Use 顺序）→ Global → 路由中间件名（spec.Middleware 顺序）
→ Routes[OperationID] → RateLimit(spec)`。

```go
// main / options.go：注册应用层别名，启动和每次 Reload 都会用新配置执行一次。
func demoMiddlewareRegistrar(cfg *config.Config) gofy.MiddlewareRegistrar {
    return func(reg gofy.MiddlewareRegistration) error {
        return middleware.Register(reg.Registry, middleware.Deps{
            Logger:    reg.Runtime.Logger,
            JWT:       cfg.JWT,          // Auth 内部调用框架 JWTAuth
            Responder: reg.Responder,
        })
    }
}

// app/module/order/module.go：模块只声明契约，不再关心注册表。
func (m *Module) Register(r *support.Router) error {
    h := controller.NewOrderController(m.svc, m.logger)
    opts := gofyadapter.BindOptions{
        // 只对契约路由生效的通用中间件，不走名字。
        Global: []support.MiddlewareFunc{
            gofymiddleware.RequestID(support.DefaultRequestIDHeader),
        },
        // 也可以绕过名字，直接按 OperationID 追加实例。
        Routes: map[string][]support.MiddlewareFunc{
            "CreateOrder": {auditMiddleware},
        },
        // 契约声明 RateLimit 时按 spec 生成限流中间件。
        RateLimit: func(spec endpoint.Spec) support.MiddlewareFunc {
            return limiter.For(spec.OperationID)
        },
    }
    for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
        if err := gofyadapter.BindGroup(r, group, h, opts); err != nil {
            return err
        }
    }
    return nil
}
```

注意：

- 分组名与路由名从 Router 携带的 App 注册表解析；`BindOptions.Registry` 只是
  可选的显式注入，`BindOptions.Middleware` 仍是单次绑定的最高优先级覆盖。
- `BindAll` 在启动期校验：契约声明的每个名字（含认证 `"auth"`）都必须能在
  `BindOptions.Middleware`、`BindOptions.Registry` 或 App 注册表里查到；签名不
  匹配、路径占位符缺 `uri` 字段、名字未注册等问题会聚合返回。`AuthRequired`
  本身不参与校验，也不触发任何中间件安装。
- 手动 `r.Group(prefix, mw...)` + `BindAll` 时分组中间件直接传实例，契约里的
  分组名（含 `"auth"`）不会被解析；路由级名字仍然解析。`Bind` 只安装显式传入
  的 `mw`。
- 如果 App 级 `WithJWTAuth` 已经对模块路由统一鉴权，就不要再在契约里声明
  `"auth"`，否则 token 会被解析两次；两者按需选一个。
- `BindOptions.Global` 的中间件比 App 级 CORS/JWT 更靠内：CORS 预检根本不会
  进入契约路由。

### 3. 中间件别名注册表（Registry）

`registry.go` 提供一份显式的「别名 → 中间件实例」注册表，是契约名字与服务端
实现之间唯一的桥：

| API | 行为 |
| --- | --- |
| `NewRegistry()` | 创建空注册表；由 App 在每次装配（启动 / `Reload`）时创建，不是包级全局 |
| `Register(name, mw) error` | 别名去首尾空白后不能为空、实例不能为 nil、同名不可覆盖，违反任一条返回错误 |
| `MustRegister(name, mw)` | `Register` 的 panic 版本，用于启动期装配，别名写错立刻失败 |
| `Lookup(name) (support.MiddlewareFunc, bool)` | 按别名查实例；名字不存在、值为 nil 或注册表本身为 nil 都返回 `(nil, false)` |
| `Names() []string` | 已注册别名的排序副本，用于启动日志与排查 |

规则与取舍：

- **App 持有，模块只解析**：`gofy.WithMiddlewareRegistrar` 在模块绑定路由之前
  补充应用别名；注册表随 `support.RouterConfig.Middleware` 下发，模块用
  `r.Middleware(name)` 或 adapter 的名字解析取实例。
- **一一对应，不可覆盖**：重复注册返回 `middleware alias "x" is already registered`，
  避免两个中间件悄悄抢同一个名字；需要替换时新建一份注册表。
- **并发安全**：内部 `sync.RWMutex`，注册与查找可同时进行；热更新每次装配新建
  一份，旧路由继续持有旧实例，不会被半更新状态影响。
- **解析优先级**：`BindOptions.Middleware`（本次绑定的显式映射）→
  `BindOptions.Registry`（可选显式注入）→ Router 携带的 App 注册表。
- **缺失即启动失败**：都查不到时 adapter 报
  `middleware "x" is not registered in BindOptions.Middleware, BindOptions.Registry or the app middleware registry`，
  分组名带 `group "<prefix>"` 前缀、路由名带 OperationID 前缀，路由不会带着
  缺失的中间件上线。
- **adapter 不依赖具体类型**：`gofyadapter.MiddlewareRegistry` 只要求
  `Lookup(name string) (support.MiddlewareFunc, bool)`，因此可以注入自己的实现。
- **只服务契约路由**：App 级全局链仍由 `gofy.With*` 选项安装，注册表不接管它。
  把已在全局链安装的中间件（如 `NameCORS`）再注册给契约路由会执行两次，
  只在确实需要「按路由覆盖全局行为」时才这么做。

契约需要某个框架内建中间件时，在 registrar 里用它的别名常量注册即可：

```go
gofy.WithMiddlewareRegistrar(func(m gofy.MiddlewareRegistration) error {
    corsMW, err := gofymiddleware.CORS(cfg.CORS)
    if err != nil {
        return err
    }
    if corsMW == nil { // 未配置来源时 CORS 返回 (nil, nil)，表示不启用
        return nil
    }
    return m.Registry.Register(gofymiddleware.NameCORS, corsMW)
})
```

## 四、在契约客户端侧

服务端中间件与客户端通过固定 context key 对接，客户端本身不依赖 gofy：

- `HeaderPropagation` 捕获入口的 `X-Trace-Id`、`X-User-Id`、`X-User-Code`、
  `X-Tenant-Id`、`X-User-Name`、`X-Client-Ip`、`X-User-Agent`，写入 context key
  `gofy.header_propagation.headers`；
- `appinterface/*client` 发起下游调用时自动读取并注入这些头，业务只需传同一个
  `ctx`：

```go
res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: 42})
```

- `Authorization`、`Cookie` 不在白名单内，不会被转发；需要补充身份时用
  `client.ContextWithHeader(ctx, client.HeaderUserID, userID)`；
- request id 也可显式传递：`client.ContextWithRequestID(ctx, reqID)`；
- 调用方不是网关（没有入口透传头）时，客户端不会凭空生成身份头，
  `X-User-Agent` 默认写 `gofy-api-sdk`。

`JWTAuth` 解析成功后除了写 gin context，还会调用
`frameworkcontext.WithUser`；下游服务读取 `frameworkcontext.UserID(ctx)` 即可，
不需要在两个服务之间传递 token。

## 五、手动使用（测试/自定义 server）

不经过 `gofy.New` 时，可以自行安装同一组中间件：

```go
engine := gin.New()
engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))
engine.Use(middleware.HeaderPropagation(trustedProxies))
engine.Use(middleware.AccessLog(logger, middleware.DefaultAccessLogOptions()))
engine.Use(corsMW)       // middleware.CORS(cfg)
engine.Use(middleware.Recovery(logger, responder))
engine.Use(middleware.BodyLimit(8 << 20))

router := support.NewRouter(engine, support.RouterConfig{
    Prefix: "/api/v1",
    // 契约路由按名字解析时使用；不解析名字时可以为 nil。
    Middleware: reg,
})
router.Use(rateLimitMW, jwtMW) // 只作用于模块路由
```

gofy-interface-api 的 `adapter/gofy/fixture_test.go` 与
`adapter/gofy/middleware_registry_test.go` 就是用这种最小方式验证契约绑定：
`engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))` 后直接调用
`gofyadapter.BindAll` / `BindGroup`，后者覆盖 Router 注册表解析、显式映射覆盖、
认证别名显式解析、`AuthRequired` 不注入中间件，以及名字缺失时的启动期报错。

## 六、安全约定

- `AllowCredentials` 不能与 `AllowOrigins: ["*"]` 组合；
- 身份头只在 `WithTrustedProxies` 命中的直接对端转发时才采信，普通客户端无法
  伪造租户/用户头；
- 访问日志对敏感 query 键脱敏，JWT 错误不暴露 token 细节；
- 限流默认失败关闭，存储故障时返回 503 而不是放行；
- 安全响应头在 `New` 阶段校验，拒绝含换行的头值。

## 测试

```bash
go test ./support/middleware/
go test -race ./support/middleware/ -run Registry
```

覆盖 RequestID、访问日志脱敏与字段、Recovery 包络、CORS 来源/模式/路径/403、
JWT 校验与 Claims、限流键与失败关闭、安全头与透传头的可信代理判定，以及注册表
的注册/查找/重复别名/空名/nil 实例/MustRegister panic/Names 排序与并发读写。