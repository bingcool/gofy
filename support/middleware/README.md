# support/middleware

Gin 中间件集合：请求 ID、访问日志、异常恢复、Body 上限、CORS、JWT、限流、
安全响应头与请求头透传。业务通常不直接 `engine.Use`，而是通过根包的
`gofy.With*` 选项交给 App 按固定顺序安装。

本 README 重点说明这些中间件在 **gofy-interface-service** 场景下如何接入：
服务端如何保护契约路由，客户端如何在向下游调用时透传请求头与身份。

## 一、中间件清单

| 中间件 | 构造方式 | 根包选项 | 作用 |
| --- | --- | --- | --- |
| `RequestID` | `RequestID(support.DefaultRequestIDHeader)` | 自动安装 | 上游 `X-Trace-ID` 合法则复用，否则生成 UUID；写回响应头与 request context，下游 interface-service 客户端自动透传 |
| `AccessLog` | `AccessLog(logger, opts)` | `WithAccessLog(true)` | 结构化访问日志；跳过 `/healthz`、`/readyz`；query 中的 token/password 等敏感键脱敏；慢请求阈值 500ms |
| `Recovery` | `Recovery(logger, responder)` | `WithRecovery(true)`（默认开） | panic → 500 标准包络，堆栈只进日志不外泄 |
| `BodyLimit` | `BodyLimit(maxBytes)` | `WithMaxBodyBytes(n)` | `http.MaxBytesReader` 限制请求体，超限由绑定层映射为 413 |
| `CORS` | `CORS(cfg)` | `WithCORS(cfg)` | 裸域名展开 http/https、`*.example.com` 子域、路径白名单、非法来源 403；详见包内 cors.go |
| `JWTAuth` | `JWTAuth(cfg, responder)` | `WithJWTAuth(cfg)` | 校验 Bearer token，claims 写入 gin context 与 `frameworkcontext`；失败返回标准包络 |
| `RateLimit` | `RateLimit(cfg, responder)` | `WithRateLimit(cfg)` | 按 IP + 方法 + 路由分桶的滑动窗口；默认失败关闭（503） |
| `SecurityHeaders` | `SecurityHeaders(cfg)` | `WithSecurityHeaders(cfg)` | `X-Content-Type-Options`、`X-Frame-Options`、Referrer-Policy、HSTS、CSP、Permissions-Policy 与自定义头 |
| `HeaderPropagation` | `HeaderPropagation(trustedProxies)` | `WithHeaderPropagation(true)` | 把入口白名单头写入 context；身份头只采信可信代理，`Authorization`/`Cookie` 永不透传 |

`support.Middleware` 就是 `gin.HandlerFunc`，因此自定义 gin 中间件可以直接
传给 `gofy.WithMiddleware(...)` 或 `support.Router.Use(...)`。

## 二、App 的安装顺序`gofy.New` 在构建 HTTP server 时按下面顺序安装（`start.go`）：

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

### 请求 ID 与下游透传

`RequestID`（`req_trace_id.go`）保证「一次请求一个 trace」：

1. 上游请求带合法 `X-Trace-ID` 时原样复用；缺失或非法时生成 UUID（即使配置了
   自定义头，也优先沿用上游 `X-Trace-ID`）；
2. 写入 `gin.Context`（响应包络 `req_id`）、请求/响应头；
3. 写入 request context 的两层 key：
   - `support.ContextWithRequestID`：业务用
     `support.RequestIDFromContext(ctx)` 读取；
   - `headerpropagation` 的 `X-Trace-Id`：`gofy-interface-service/client` 会从
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

## 三、在 gofy-interface-service 服务端使用

gofy-interface-service 的契约路由通过 `adapter/gofy.BindGroup`（按分组）或
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
    gofy.WithModuleFactories(order.NewModuleFactory(cfg.Order)),
)
```

### 2. 契约路由级（adapter 的 BindOptions）

契约里只声明中间件的**逻辑名**：分组级 `group.Use("audit")`、路由级
`spec.Middleware = []string{"order-audit"}`（或链式 `.Use("order-audit")`）。
服务端用 `BindOptions.Middleware` 把名字映射成这里的中间件实例，
`adapter/gofy.BindGroup` 负责建组并解析分组名，内部 `BindAll` 解析路由名。

最终顺序：
`分组中间件（group.Use 顺序）→ Global → 路由中间件名（spec.Middleware 顺序）
→ Routes[OperationID] → Auth（契约声明 AuthRequired 时）→ RateLimit(spec)`。

```go
func (m *Module) Register(r *support.Router) error {
    h := controller.NewOrderController(m.svc, m.logger)
    opts := gofyadapter.BindOptions{
        // 只对契约路由生效的通用中间件。
        Global: []support.Middleware{
            middleware.RequestID(support.DefaultRequestIDHeader),
        },
        // 契约声明的逻辑名 → 具体中间件；名字缺失会在启动期报错。
        Middleware: map[string]support.Middleware{
            "audit":       middleware.Audit(m.logger, "group"),
            "order-audit": middleware.Audit(m.logger, "route"),
        },
        // 也可以绕过名字，直接按 OperationID 追加实例。
        Routes: map[string][]support.Middleware{
            "CreateOrder": {auditMiddleware},
        },
        // 契约声明 AuthRequired 的路由必须提供 Auth。
        Auth: m.auth,
        // 契约声明 RateLimit 时按 spec 生成限流中间件。
        RateLimit: func(spec endpoint.Spec) support.Middleware {
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

- `BindAll` 在启动期校验：`AuthRequired` 路由没有 `BindOptions.Auth` 会直接报错；
  签名不匹配、路径占位符缺 `uri` 字段、中间件名未注册等问题会聚合返回。
- 手动 `r.Group(prefix, mw...)` + `BindAll` 时分组中间件直接传实例，契约里的
  分组名不会被解析；路由级名字仍然解析。`Bind` 只安装显式传入的 `mw`。
- App 级 `WithJWTAuth` 已经对模块路由统一鉴权时，`BindOptions.Auth` 仍可放行
  （`c.Next()`）或做更细粒度的角色校验，避免重复解析 token。
- `BindOptions.Global` 的中间件比 App 级 CORS/JWT 更靠内：CORS 预检根本不会
  进入契约路由。

## 四、在 gofy-interface-service 客户端侧

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

router := support.NewRouter(engine, support.RouterConfig{Prefix: "/api/v1"})
router.Use(rateLimitMW, jwtMW) // 只作用于模块路由
```

gofy-interface-service 的 `adapter/gofy/bind_test.go` 就是用这种最小方式验证契约
绑定：`engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))` 后直接
调用 `gofyadapter.BindAll` / `BindGroup`。

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
```

覆盖 RequestID、访问日志脱敏与字段、Recovery 包络、CORS 来源/模式/路径/403、
JWT 校验与 Claims、限流键与失败关闭、安全头与透传头的可信代理判定。