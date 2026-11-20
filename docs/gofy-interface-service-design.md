# gofy-interface-service 详细技术方案（v1）

> **归档提示（2026-11-13）**：`gofy-interface-service` 仓库已删除，本文仅作历史设计记录保留，
> 不再按本文执行任何迁移步骤。仓库定位、目录结构与依赖规则以
> [拆分方案](./interface-api-split-design.md) 为准；旧路径到新路径的换算见其 §10 映射表。

> 状态：**待评审**（2026-11-04）
>
> 关联文档：[gofy Library 详细设计](./library-design.md)
>
> 参考实现：
> - `/Users/huangzengbing/Documents/wwwroot/swoolefy`（HTTP 能力、`docs/InterfaceApi.md`、`src/Script/ApiDoc`）
> - `/Users/huangzengbing/Documents/wwwroot/schedule-job/InterfaceApi`（契约布局、Support、生成器约定）
>
> **说明：** 用户给出的 `/Users/huangzengbing/Documents/wwwroot/interface-api-service` 在本机不存在。本文的契约约定以 `swoolefy/docs/InterfaceApi.md`、`schedule-job/docs/interface-api-layout.md`、`schedule-job/InterfaceApi/Support/README.md` 为准。仓库建立后，本文应迁移为新仓库的 `docs/design.md`。

---

## 目录

1. [结论与范围](#1-结论与范围)
2. [swoolefy HTTP 能力移植清单](#2-swoolefy-http-能力移植清单)
3. [gofy-interface-service 仓库设计](#3-gofy-interface-service-仓库设计)
4. [契约模型](#4-契约模型)
5. [路由描述符与服务端绑定](#5-路由描述符与服务端绑定)
6. [客户端 SDK](#6-客户端-sdk)
7. [OpenAPI / Swagger](#7-openapi--swagger)
8. [gofy 框架 HTTP 能力改造](#8-gofy-框架-http-能力改造)
9. [其他服务接入](#9-其他服务接入)
10. [测试策略](#10-测试策略)
11. [CI、版本与发布](#11-civersion-与发布)
12. [实施计划](#12-实施计划)
13. [验收标准](#13-验收标准)
14. [风险与开放问题](#14-风险与开放问题)
15. [附录](#15-附录)

---

## 1. 结论与范围

### 1.1 核心决策

| # | 决策 | 结果 |
| --- | --- | --- |
| D1 | `gofy-interface-service` 的定位 | **契约 + SDK + 文档工具仓库**，不是新的业务服务，也不是网关/BFF |
| D2 | 服务端实现位置 | 仍由各业务服务实现；契约仓库只提供接口、DTO、路由描述符、客户端和 OpenAPI |
| D3 | 契约来源 | 以 Go 结构体、接口、`endpoint.Route` 描述符为唯一事实来源；不维护 PHP 式注解 |
| D4 | 服务端路由 | 由 `apicore/adapter/gofy.BindAll` 根据契约描述符注册，路径/方法/鉴权元数据不再手写第二份 |
| D5 | DTO / Request / Response | 参考 InterfaceApi 的 `Request`、`Response`、`Dto`、`Const`、`Enum` 分层；Go 用 struct + tag 表达 |
| D6 | 响应信封 | 沿用 gofy：`{code,msg,data,req_id,details}`；`code == 0` 为成功，`req_id/details` 是兼容性扩展 |
| D7 | 列表/分页 | 列表 `data = {total,list}`；分页 `data = {total,list,page,page_size}` |
| D8 | 客户端 | 由契约仓库提供泛型 `client.Call`，并为每个契约生成强类型 Client |
| D9 | OpenAPI | 自研反射式生成器输出 OpenAPI 3.0.3 YAML/JSON；用 `kin-openapi` 校验；CI 做 golden diff |
| D10 | Swagger UI | 内嵌 `swaggest/swgui/v5emb`，不依赖 CDN；由业务服务挂载 `/docs`，默认仅非生产环境 |
| D11 | 契约纯净性 | `apicore/contracts/**` 只允许标准库和本模块包；`appinterface/**` 额外允许 `apicore/client/**`；两者都禁止导入 gin/gorm/gofy/zap 等运行时 |
| D12 | 版本 | Go module 语义化版本；业务契约集中在 `appinterface/{domain}service`，目录不带 `vN`；破坏性变更提升 module 主版本 |
| D13 | 不在本期范围 | cron、worker/process 管理、process pool、Swoole 协程运行时、daemon 管理 |
| D14 | 首个落地服务 | `examples/demo-service`，用它验证契约、Client、OpenAPI 和迁移路径 |

### 1.2 目标

1. 把 swoolefy 中**与 HTTP 相关、与 Swoole 运行时无关**的能力，按 gofy 的 library 形态重新实现，形成可复用的 `support`（HTTP 基础能力）与 `support/middleware` 包。
2. 新建 `gofy-interface-service`，让所有 Go 服务共享同一套 DTO、Request、Response、常量、枚举、错误码。
3. 服务端只实现业务接口；路由、绑定、校验、响应信封、OpenAPI 元数据由契约描述符统一驱动。
4. 调用方通过强类型 Go Client 调用，不手写 URL、JSON 字段和错误码判断。
5. 自动生成 Swagger/OpenAPI 文档，CI 可校验文档与契约一致。
6. 契约变更可审计：引用边界检查、兼容性检查、生成物 diff。

### 1.3 非目标

- 不实现独立的“接口服务”进程。业务接口仍由 order-service、user-service 等自身提供，避免多一跳网络和重复业务逻辑。
- 不把 PHP 的 `ArrayDto` 魔术方法、`__get/__set`、`ArrayAccess` 搬到 Go；Go 用显式字段和 tag。
- 不复制 swoolefy 的动态 Controller/Action 分发、`denyActions`、路由元数据缓存；Go 在启动期静态注册路由。
- 不把 cron、worker、process、进程池、热重启纳入本方案；这些要么已移除，要么由部署层负责。
- 不在 v1 内置服务发现 SDK；先定义 `Resolver` 接口，Nacos/K8s 适配器后续单独模块提供。
- 不要求所有服务一次性迁移；契约按模块逐步接管。

### 1.4 与 InterfaceApi 的关系

| 维度 | interface-api-service（PHP） | gofy-interface-service（Go） |
| --- | --- | --- |
| 契约载体 | `*ApiInterface` + PHP Attribute | `Service` 接口 + `endpoint.Route` 描述符 + struct tag |
| DTO | `ArrayDto` + 反射填充 | 普通 struct + `json/form/uri/validate/default/doc` tag |
| 校验 | `#[ValidationRule]` | `go-playground/validator` tag |
| 响应 | `BaseResponse` 子类声明 `data` | Response struct 即 `data`，信封由 gofy Responder 生成 |
| 客户端 | 生成 `*Api` 继承 `BaseClientApi` | 泛型 `client.Call` + 生成强类型方法 |
| 文档 | `generate-openapi.php` | 反射式 `apicore/cmd/gofy-openapi` |
| 引用边界 | 只允许 `InterfaceApi\` + PHP 内建 | 公共层只允许标准库 + 本模块 `apicore/contracts/**`；业务层额外允许 `apicore/client/**`、`appinterface/**` |
| 服务发现 | Nacos | `client.Resolver` 接口，Nacos 适配器后置 |
| 版本 | 仓库版本 + 目录 | module semver；业务契约目录不带 `vN` |

---

## 2. swoolefy HTTP 能力移植清单

### 2.1 移植原则

1. **只移植 HTTP 语义**：路由、请求解析、校验、响应、中间件、上传、流式、健康检查、文档。
2. **不移植 Swoole 运行时细节**：协程、连接 fd、`Swoole\Http\Request/Response`、worker 生命周期。
3. **优先复用 Go 标准库与既有依赖**：gin、validator、creasty/defaults、mime、net/http。
4. **框架能力进 gofy，契约能力进 gofy-interface-service**：不要把业务 DTO 或 OpenAPI 生成器塞进 gofy 核心。
5. **每个移植项必须有测试**：不能只复制接口签名。

### 2.2 总表

阶段说明：P0 = 契约仓库落地前必须完成；P1 = 与契约仓库同期或紧随；P2 = 第二阶段；P3 = 可选增强。

| # | swoolefy 能力 | swoolefy 位置 | gofy 现状 | 移植方案 | gofy 目标包 | 阶段 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 路由分组、组中间件 | `src/Http/Route.php` `group()` | `Router.Group(prefix, mw...)` 已有 | 保持；补充组级 after 语义 | `support` | 已有 |
| 2 | before / after 路由中间件 | `HttpRoute::handleBeforeRouteMiddles` / `handleAfterRouteMiddles` | 只有普通 gin 中间件 | `Router.After(...)`：在 handler 返回后、响应渲染前执行；用于审计、耗时、结果加工 | `support` | P2 |
| 3 | 路由选项（DB debug、OTel、限流、route meta cache） | `src/Http/RouteOption.php` | 无 | 新增 `support.RouteConfig` / `RouteOption`；meta cache 不移植 | `support` | P2 |
| 4 | Controller `_beforeAction` / `_afterAction` | `HttpRoute::invoke` | 无 | 用 before/after 中间件和 handler hook 等价实现；不复制 Controller 基类 | `support` | P2 |
| 5 | `denyActions` | `HttpRoute::$denyActions` | 无 | 不移植：Go 路由静态注册，不存在按 URL 反射调用任意方法 | — | 不移植 |
| 6 | 路由元数据缓存 | `parseDispatchControllerMeta` | 无 | 不移植：启动期注册，天然只解析一次 | — | 不移植 |
| 7 | 入参绑定：JSON / form / query / route | `HttpRoute::bindActionParams`、`RequestParseTrait` | `ShouldBindUri` + `ShouldBind` + validator 已有 | 保持；补 header/cookie 绑定 | `support` | P0 |
| 8 | 嵌套 DTO 与数组集合校验 | `RequestValidate` | validator 支持嵌套 struct；slice 需 `dive` | 文档化 `dive` 规则；不支持 PHP 的 `foo.*.bar` 通配 map 语法 | `support` + 契约规范 | P0 |
| 9 | 校验消息定制 | `#[ValidationRule(message: ...)]` | `message:"rule:文本,..."` 已有 | 保持；契约统一使用 `message` tag | `support` | 已有 |
| 10 | `StringToInt` | `src/Annotation/StringToInt.php` | query/form 绑定已自动转 int；JSON 字符串数字不转 | 新增 `types.Int64String` / `types.Uint64String`，同时支持 JSON 数字与字符串 | 契约 `types` | P0 |
| 11 | `IntToString`（大整数防精度丢失） | `src/Annotation/IntToString.php` | 无 | 同上；对外 ID 建议统一用字符串类型 | 契约 `types` | P0 |
| 12 | Header / Cookie / Raw Body 读取 | `RequestParseTrait` | gin 可读，但绑定器未统一处理 header/cookie | 扩展 `bindAndValidate`：`ShouldBindHeader`；提供 `support.RawBody(c)`；Cookie 用 `c.Cookie` | `support` | P1 |
| 13 | 上传文件 | `src/Http/UploadedFile.php`、`RequestParseTrait::file/files` | gin 支持 `*multipart.FileHeader`，无安全保存/校验工具 | 新增 `upload.File`：大小、MIME、扩展名校验，安全文件名，禁止路径穿越 | `support/upload` | P1 |
| 14 | MIME 映射 | `src/Http/MimeType.php`（811 行） | 标准库 `mime` + 已有 `gabriel-vasile/mimetype` 间接依赖 | 不复制 811 行表；用 `mime.TypeByExtension` + `mimetype.Detect` | `support/upload` | P1 |
| 15 | 响应信封 `{code,msg,data}` | `BaseResponse`、`ResponseCode` | `support.Responder` 已实现 `{code,msg,data,req_id,details}` | 保持；契约 Response 只描述 `data` | `support` | 已有 |
| 16 | 重定向 | `ResponseOutput::redirect` | 无 | 新增 `support.Redirect`（实现 `Renderer`） | `support` | P1 |
| 17 | 自定义 Header / Cookie | `ResponseOutput::withHeader/withCookie` | gin 原生可做，无统一 helper | 新增响应 helper；Cookie 支持 `SameSite`、`Partitioned` | `support` | P1 |
| 18 | 文件下载 / inline | `DownloadResponse` 注解 | 无 | `support.Download{Reader,Filename,ContentType,Inline}` + `Content-Disposition` 安全编码 | `support` | P1 |
| 19 | SSE | `src/Http/EventStream.php` | 无 | `support.EventStream`：`event/id/data/retry/comment/heartbeat`，自动 flush | `support/sse` | P1 |
| 20 | 分块输出 | `src/Http/HttpChunkedResponse.php` | 无 | `support.ChunkWriter`：`Write/WriteLine/WriteJSON/WriteFile` | `support/chunked` | P1 |
| 21 | 普通文本/原始响应 | `ResponseOutput::echo` | `c.String` 可用 | `support.Raw{ContentType,Body}` | `support` | P1 |
| 22 | 返回值归一化 | `src/Http/ActionResultNormalizer.php` | 泛型 handler 已强类型 | 不移植 PHP 的数组/对象归一化；`nil` 响应输出 `data:null` | — | 不移植 |
| 23 | CORS | `src/Http/Middleware/CorsMiddleware.php` | 精确 origin、credentials、expose、max-age 已有 | 补 `AllowOriginPatterns`（`https://*.example.com`）、`AllowPaths`、预检 204 统一 | `support/middleware` | P2 |
| 24 | 安全响应头 | `SecurityHeadersMiddleware.php` | 无 | 新增：`nosniff`、`DENY/SAMEORIGIN`、`Referrer-Policy`、`Permissions-Policy`、CSP、HSTS（仅 TLS） | `support/middleware` | P1 |
| 25 | Bearer 鉴权 / 可选鉴权 | `AuthenticateMiddleware`、`OptionalAuthenticateMiddleware` | 示例服务手写 | 新增 `Authenticator` 接口 + `Bearer` / `OptionalBearer`；Principal 放 context | `support/auth` | P1 |
| 26 | Locale / i18n 协商 | `LocaleMiddleware.php` | 无 | 新增 `Locale`：`Accept-Language`、`?lang=`、cookie、`X-Locale`，只做协商与 context，不内置翻译 | `support/middleware` | P2 |
| 27 | 限流与限流响应头 | `RateLimit/*`、`AbstractRateLimiterMiddleware` | 无 | `Limiter` 接口 + 内存滑窗实现；Redis 适配器独立模块；`X-RateLimit-*`、429 信封 | `support/ratelimit` | P2 |
| 28 | 健康检查（DB/Redis/文件存储） | `src/Http/Health/*` | `/healthz`、`/readyz` 仅 DB ping | `health.Registry` + `Check` 接口；并发检查、超时、详情开关、路径别名；内置 DB Check | `support/health` | P2 |
| 29 | 进程健康检查 | `Health/Check/ProcessHealthCheck.php` | 无 | 不移植：进程管理不在范围 | — | 不移植 |
| 30 | 日志参数脱敏 | `src/Http/LogParamSanitizer.php` | 无 | 新增 `logger.Sanitize`：password/token/secret/cookie/authorization 等 | `logger` | P2 |
| 31 | 忽略路径配置 | `src/Http/IgnoreRouteConfig.php` | accesslog 只有精确 `SkipPaths` | 新增 `routematch.Matcher`：精确、前缀 `/*`、`:param`；供 accesslog/auth/ratelimit 共用 | `support/routematch` | P2 |
| 32 | OpenTelemetry | `src/Http/OpenTelemetry/*` | 无 | 可选模块：trace id 注入响应头与日志；不进入核心依赖 | `contrib/otel` | P3 |
| 33 | API 文档注解与生成 | `src/Annotation/Api*`、`src/Script/ApiDoc` | 无 | 契约描述符 + struct tag + 反射生成 OpenAPI 3.0.3；Swagger UI 内嵌 | 新仓库 `docgen` | P0 |
| 34 | 请求辅助（isMobile/getBrowser/previousUrl） | `RequestParseTrait` | 无 | 不移植：业务价值低且依赖 UA/会话 | — | 不移植 |
| 35 | `dump()` 调试输出 | `ResponseOutput::dump` | 无 | 不移植：Go 用日志/调试器 | — | 不移植 |
| 36 | 静态文件 | `HttpServer` 路由 | `Router.Static` 已有 | 保持 | `support` | 已有 |

### 2.3 不移植清单及原因

| 能力 | 原因 |
| --- | --- |
| cron 定时任务 | 已从 gofy 移除；由外部调度系统负责 |
| worker / process 管理、进程池 | 不属于 HTTP 能力；由容器/部署层负责 |
| Swoole 协程与连接 fd | Go 用 goroutine + `context.Context` |
| 动态 Controller/Action 分发、denyActions | Go 静态路由，不存在反射任意方法调用 |
| 路由元数据缓存 | 启动期注册，天然只解析一次 |
| PHP `ArrayDto` 魔术访问 | Go 用显式字段；反射填充只存在于 apicore/client/docgen |
| `dump()`、previousUrl、isMobile、getBrowser | Swoole/PHP 场景专用或价值低 |
| 811 行 MIME 静态表 | 标准库 + `mimetype` 已覆盖 |
| 进程健康检查 | 进程管理不在范围；容器探针由编排系统负责 |

### 2.4 gofy 侧落地包与优先级

```text
gofy/
  support/
    result.go            # Renderer、Redirect、Raw、Download、Cookie helper
    route_config.go      # RouteConfig / RouteOption（P2）
    router.go            # 增加 After（P2）
    upload/
      file.go            # 上传校验与安全保存
    sse/
      stream.go          # SSE
    chunked/
      writer.go          # 分块
    middleware/
      security.go        # 安全响应头
      locale.go          # 语言协商
      ratelimit.go       # 限流中间件
    auth/
      bearer.go          # 鉴权接口与中间件
    ratelimit/
      limiter.go         # 接口 + 内存实现
    health/
      registry.go        # 健康检查注册表
      check_db.go        # GORM DB Check
    routematch/
      matcher.go         # 忽略路径匹配
  logger/
    sanitize.go          # 日志脱敏
  contrib/otel/
    middleware.go        # 可选 OpenTelemetry
```

> `support` 现有包不引入 Redis、OTel、Swagger 依赖；限流只定义接口和内存实现。Redis 适配器、OTel 采集器放独立模块，避免核心 `go.mod` 膨胀。

---

## 3. gofy-interface-service 仓库设计

### 3.1 定位

`gofy-interface-service` 是 **Go 契约仓库**，与 PHP 的 `interface-api-service` 对齐：

- 存放所有跨服务契约：DTO、Request、Response、Const、Enum、错误码、路由描述符、Service 接口。
- 存放生成的强类型 Client。
- 存放 OpenAPI 生成器、Swagger UI 包、引用边界检查器。
- **不存放业务实现**；业务服务通过 `apicore/adapter/gofy` 把契约绑定到自己的 gofy 应用。

**公共层与业务层的修改边界：**

本仓库会被新项目作为依赖下载使用。`apicore/` 是契约核心，目录名即约束：业务侧定义契约时不得修改。下载后只有 `appinterface/**` 允许增改；`apicore/contracts/**`、`apicore/client/**`、`apicore/adapter/**`、`apicore/cmd/**`、`docgen/**`、`docsui/**`、`openapi/**` 属于共享实现，保持只读，随 module 版本升级。新增服务时在 `appinterface/` 下建目录（如 `demoservice`、`productservice`），在该目录内定义 DTO、枚举、常量、请求、响应、路由和 Service 接口，并在 `{domain}serviceclient/` 子包内定义强类型客户端。

契约仓库只描述线上协议；Redis、锁、对象存储、短信等运行时能力不属于契约层，业务在 service 层使用自己的实现并把结果映射为契约 DTO。禁止为了让 DTO 复用某个运行时类型而把这类依赖引入契约仓库。

可选模式（不推荐作为 v1）：

- **Catalog 服务**：只读部署，挂载所有 OpenAPI 规范，提供统一 `/docs` 门户。它不承载业务接口，只解决“文档分散”问题。该模式放 P3。

### 3.2 目录结构

```text
gofy-interface-service/
├── go.mod                          # module github.com/bingcool/gofy-interface-service
├── go.sum
├── README.md
├── Makefile
├── .golangci.yml
├── .github/
│   └── workflows/
│       └── ci.yml
├── apicore/                        # 契约核心（业务侧只读，随 module 版本升级）
│   ├── contracts/                  # 公共契约层（不可修改）：只依赖标准库和 apicore/contracts/**
│   │   └── common/
│   │       ├── envelope/
│   │       │   └── envelope.go         # Envelope[T]、ErrorEnvelope
│   │       ├── paging/
│   │       │   └── paging.go           # PageRequest、ListData[T]、PageData[T]
│   │       ├── errcode/
│   │       │   ├── errcode.go          # Code、Fault、公共错误码
│   │       │   └── ranges.go           # 业务码段分配表
│   │       ├── types/
│   │       │   ├── int64_string.go     # Int64String / Uint64String
│   │       │   ├── date.go             # Date（yyyy-MM-dd）
│   │       │   └── decimal.go          # Decimal（字符串定点数，可选）
│   │       ├── endpoint/
│   │       │   └── endpoint.go         # Spec、Route[Req,Res]、Descriptor、New()
│   │       └── enum/
│   │           └── enum.go             # 枚举辅助：Values、Labels、校验
│   ├── adapter/
│   │   └── gofy/
│   │       ├── bind.go                 # Bind、BindGroup、BindAll、BindSSE、BindDownload、BindChunked
│   │       ├── errors.go               # errcode.Fault -> *apperr.Error
│   │       └── options.go              # BindOptions、MiddlewareRegistry、路由级中间件
│   ├── client/
│   │   ├── client.go                   # Client、Config
│   │   ├── call.go                     # Call[Req,Res]
│   │   ├── encode.go                   # path/query/json/multipart 构造
│   │   ├── decode.go                   # 信封解析
│   │   ├── errors.go                   # APIError、HTTPError、ProtocolError
│   │   ├── retry.go                    # 重试与退避
│   │   ├── sse.go                      # SSE / 下载 / 分块客户端
│   │   ├── resolver.go                 # Resolver 接口、StaticResolver
│   │   └── nacos/
│   │       └── resolver.go             # Nacos 服务发现、缓存/陈旧回退、负载均衡
│   └── cmd/
│       ├── gofy-openapi/
│       │   └── main.go                 # 生成/校验 OpenAPI
│       ├── gofy-client/
│       │   └── main.go                 # 生成/检查强类型 Client
│       └── refcheck/
│           └── main.go                 # 契约引用边界检查
├── appinterface/                      # 业务契约层（唯一可扩展目录）
│   ├── routes.go                   # 显式聚合所有服务 Routes()，无 init()
│   └── demoservice/
│       ├── routes.go               # Routes() 聚合，委托 orderroute
│       ├── service.go              # Service 接口
│       ├── demoserviceclient/      # package demoserviceclient，强类型客户端
│       │   ├── client.go           # Client 类型与三种寻址方式
│       │   ├── order_client.go     # order_route.go 的方法
│       │   └── config_client.go    # config_route.go 的方法
│       ├── nacos.go                # ServiceName（Nacos 注册服务名）
│       ├── base_url.go             # BaseURL（固定地址模式的本地默认地址）
│       ├── errors.go               # 转发 ordererror，保持 demoservice 对外 API
│       ├── doc.go                  # 包说明
│       ├── orderroute/             # package orderroute
│       │   ├── order_route.go       # 订单路由描述符（按业务路由拆分文件）
│       │   ├── config_route.go      # GetConfig 描述符
│       │   └── routes.go           # Routes() 聚合
│       ├── ordererror/             # package ordererror
│       │   └── error.go            # 订单错误码与 Fault（叶子包，避免循环依赖）
│       ├── orderconst/             # package orderconst
│       │   └── const.go            # 订单业务常量（MaxOrderItems、OrderNoPrefix）
│       ├── orderdto/               # package orderdto
│       │   └── dto.go              # OrderDTO、OrderItemDTO
│       ├── orderenum/              # package orderenum
│       │   └── enum.go             # OrderStatus、PayType
│       ├── orderrequest/           # package orderrequest
│       │   └── request.go          # GetOrderRequest、CreateOrderRequest、ListOrdersRequest、GetConfigRequest
│       └── orderresponse/          # package orderresponse
│           └── response.go         # GetOrderResponse、CreateOrderResponse、GetConfigResponse
├── docgen/
│   ├── openapi.go                  # 文档构建入口
│   ├── operation.go                # Operation 生成
│   ├── schema.go                   # 类型 -> Schema
│   ├── naming.go                   # 稳定组件名
│   ├── constraints.go              # validator tag -> JSON Schema
│   ├── errors.go                   # 默认错误响应
│   └── testdata/                   # golden YAML
├── docsui/
│   └── docsui.go                   # 内嵌 Swagger UI handler / gofy Module
├── openapi/
│   ├── embed.go                    # //go:embed *.yaml
│   ├── demoservice.yaml            # 生成物
│   └── demoservice.json            # 生成物
├── internal/
│   ├── testserver/                 # httptest 服务端
│   └── lint/                       # tag/枚举一致性检查
└── docs/
    ├── design.md                   # 本文迁移后的副本
    ├── contract-conventions.md     # 契约编写规范
    └── migration-from-interface-api.md
```

### 3.3 模块与依赖规则

**依赖方向：**

```text
apicore/contracts/**   -> 标准库 + apicore/contracts/**
appinterface/**           -> 标准库 + apicore/contracts/** + apicore/client/** + appinterface/**
apicore/adapter/gofy   -> appinterface/** + apicore/contracts/** + github.com/bingcool/gofy
apicore/client         -> apicore/contracts/** + 标准库（net/http、encoding/json）
docgen                 -> appinterface/** + apicore/contracts/** + kin-openapi（仅工具链）
docsui                 -> 标准库 + swgui +（可选）gofy
apicore/cmd/*          -> 上述包
```

**硬性规则：**

1. `apicore/contracts/**` 不得导入 `github.com/gin-gonic/gin`、`gorm.io/gorm`、`github.com/bingcool/gofy`、`go.uber.org/zap`、任何第三方包。
2. `appinterface/**` 只允许标准库、本模块 `apicore/contracts/**`、`apicore/client/**`、`appinterface/**`；不得导入 gin/gorm/gofy、`apicore/adapter/gofy`、`docgen`、`docsui`。
3. `apicore/contracts/**` 不得包含业务实现、数据库访问、日志、HTTP 客户端；业务契约集中在 `appinterface/{domain}service/`。
4. `apicore/adapter/gofy` 是唯一把契约接到 gofy 路由的包。
5. `apicore/client` 不依赖 gofy；它只依赖 `net/http` 和契约。
6. `docgen` / `docsui` / `apicore/cmd` 是工具链，不被业务服务 import。
7. 两层都禁止 `init()` 副作用；契约聚合使用显式 `Routes()`。
8. 只有 `appinterface/**` 是业务方可编辑区域，其余目录只随 module 版本升级。

**为什么这样分层：**

- 契约包会被所有服务 import，必须最小依赖、可长期稳定。
- 服务端适配器集中处理 gofy 绑定差异，契约本身不感知框架。
- 客户端不依赖 gofy，未来可用于非 gofy 的 Go 服务。
- 文档生成器依赖较重（kin-openapi、swgui），隔离在工具链，避免污染业务二进制。

### 3.4 包职责表

| 包 | 职责 | 不做什么 |
| --- | --- | --- |
| `apicore/contracts/common/envelope` | 定义成功/错误信封、泛型容器 | 不负责序列化策略 |
| `apicore/contracts/common/paging` | 分页请求、列表/分页 data | 不做数据库分页 |
| `apicore/contracts/common/errcode` | 公共错误码、业务码段、`Fault` | 不依赖 `apperr` |
| `apicore/contracts/common/types` | 大整数、日期、定点数等 JSON/表单类型 | 不做业务校验 |
| `apicore/contracts/common/endpoint` | 路由描述符、请求/响应类型反射入口 | 不导入 gofy/gin |
| `apicore/contracts/common/enum` | 枚举值/标签辅助 | 不生成代码 |
| `apicore/contracts/common/*` | 公共契约原语（信封、分页、错误码、类型、路由描述符、枚举辅助） | 不写业务逻辑 |
| `appinterface/{domain}service` | 业务 DTO、Request、Response、枚举、常量、Service 接口、路由描述符；强类型客户端在其 `{domain}serviceclient` 子包 | 不写业务逻辑 |
| `apicore/adapter/gofy` | 注册路由、映射错误、安装中间件、特殊响应 | 不定义业务类型 |
| `client` | HTTP 调用、编码/解码、重试、错误 | 不做服务端校验 |
| `docgen` | 反射生成 OpenAPI | 不运行服务 |
| `docsui` | 挂载 Swagger UI | 不生成规范 |
| `apicore/cmd/refcheck` | apicore/contracts/appinterface 引用边界检查 | 不改代码 |
| `apicore/cmd/gofy-client` | 生成强类型客户端 | 不手写业务客户端 |
| `apicore/cmd/gofy-openapi` | 生成/校验 OpenAPI | 不启动 HTTP 服务 |

### 3.5 构建与工具链

`Makefile` 目标：

```makefile
.PHONY: test vet lint refcheck generate generate-client docs check

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	gofmt -l . && go run ./apicore/cmd/refcheck

refcheck:
	go run ./apicore/cmd/refcheck

generate:
	go run ./apicore/cmd/gofy-openapi -all -out ./openapi

generate-client:
	go run ./apicore/cmd/gofy-client -all

docs:
	go run ./apicore/cmd/gofy-openapi -all -out ./openapi -serve :8090

check: vet refcheck generate generate-client
	git diff --exit-code -- openapi client
```

`go.mod` 基线：

```text
module github.com/bingcool/gofy-interface-service

go 1.26.0

require (
    github.com/bingcool/gofy v0.x.y
    github.com/getkin/kin-openapi v0.152.0
    github.com/swaggest/swgui v1.8.9
    golang.org/x/tools v0.x.y        // gofy-client 使用 go/packages
)
```

> 契约包本身不需要以上第三方依赖；它们只被 `adapter`、`docgen`、`cmd` 使用。若后续需要进一步隔离，可把工具链拆到 `tools/go.mod`，但 v1 保持单模块以降低发布复杂度。

---

## 4. 契约模型

### 4.1 命名与版本

| 项 | 规则 |
| --- | --- |
| module path | `github.com/bingcool/gofy-interface-service` |
| 公共契约层 | `apicore/contracts/**`，随 module 版本升级，业务方不得修改 |
| 业务契约层 | `appinterface/{domain}service/**`，唯一可扩展目录，如 `appinterface/demoservice` |
| Go 包名 | `{domain}service`，如 `demoservice` |
| 载荷子包 | `appinterface/{domain}service/{domain}dto`、`{domain}enum`、`{domain}request`、`{domain}response`、`{domain}const`，包名带领域前缀（如 `orderdto`），禁止使用 `dto`、`request`、`const` 等通用包名 |
| 路由子包 | `appinterface/{domain}service/{domain}route`（如 `orderroute`），按业务路由拆文件，`{domain}service.Routes()` 只做聚合 |
| 错误子包 | `appinterface/{domain}service/{domain}error`（如 `ordererror`），叶子包存放错误码与 Fault，供路由和 `{domain}service` 共用 |
| 嵌套 | 载荷子包不得 import 上层 `{domain}service`；`demoservice` 引用子包，子包之间按 `enum <- dto <- response` 单向依赖；`orderroute` 只依赖载荷子包与 `ordererror` |
| 版本 | 通过 module 语义化版本管理；破坏性变更提升 module 主版本，目录不按 `vN` 分层 |
| JSON 字段 | 统一 `snake_case`，与现有 order-service 一致 |
| URL | 路由分组 `Prefix` 携带完整部署前缀（如 `/api/v1/order`），组内 `Spec.Path` 只写相对路径（如 `/orders`）；完整路由 = 分组前缀 + `Spec.Path` |
| 枚举值 | 对外字符串值一经发布不可改名；新增值必须保证旧客户端可忽略 |

**子包命名约定：**

- 一个文件里同时引入 `demoservice` 和 `productservice` 时，若子包都叫 `dto`/`request`，就必须起 import 别名；带领域前缀的 `orderdto`/`productdto` 不会冲突，调用处也不需要别名。
- 子包主要承载载荷类型；路由描述符放在 `{domain}route` 子包（如 `orderroute`），每新增一组业务路由就新增一个文件，并在该子包的 `Routes()` 中登记。
- 业务常量放在 `{domain}const` 子包（如 `orderconst`），不要留在 `{domain}service` 顶层，避免多个服务的 `const.go` 语义混乱。
- `{domain}service` 保留聚合的 `Service` 接口和 `Routes()`，因此 `demoservice.Service`、`demoservice.Routes()` 仍是服务端统一入口；强类型客户端单独放在 `{domain}serviceclient`，见 `demoserviceclient.New()`。路由分组各自有窄接口（如 `OrdersService`、`ConfigService`，`Service` 内嵌二者），handler 可以整体实现 `Service`，也可以按分组拆分、各自断言窄接口后用 `BindGroup` 分别绑定。
- 错误码与 Fault 放在 `{domain}error` 叶子包（如 `ordererror`），避免 `{domain}route` 反向依赖 `{domain}service`；`demoservice/errors.go` 转发这些值，对外 API 不变。
- docgen 以 `appinterface/<service>/` 为锚点生成组件名，嵌套子包不改变对外 schema 名：`orderdto.OrderDTO` 仍然是 `DemoserviceOrderDTO`。

**版本约定：**

- `appinterface/demoservice` 是当前订单服务的业务契约，不携带 `v1`/`v2` 目录。
- Git tag `v1.4.0` 是**契约仓库代码版本**。
- 对外 API 破坏性变更时提升 module 主版本（如 `v2.0.0`），公共层与业务层一起升级；旧主版本继续提供修复分支。
- 同一主版本内只做加法：新增服务目录到 `appinterface/`、新增字段、新增枚举值。

### 4.2 DTO

DTO 只描述跨服务传输的数据，不包含业务方法、数据库模型、ORM tag。

```go
package orderdto

import (
	"time"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderenum"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/types"
)

// OrderDTO 订单详情。
type OrderDTO struct {
	ID        types.Uint64String    `json:"id" doc:"订单ID" example:"1234567890123456789"`
	UserID    types.Uint64String    `json:"user_id" doc:"用户ID" example:"10001"`
	Amount    int64                 `json:"amount" doc:"金额，单位：分" example:"1999"`
	Status    orderenum.OrderStatus `json:"status" doc:"订单状态" enum:"pending,paid,cancelled"`
	Remark    string                `json:"remark,omitempty" doc:"备注" example:"尽快发货"`
	CreatedAt time.Time             `json:"created_at" doc:"创建时间" format:"date-time"`
	PaidAt    *time.Time            `json:"paid_at,omitempty" doc:"支付时间；未支付为 null" format:"date-time"`
}
```

**约定：**

- 金额使用最小货币单位 `int64`（分），不使用浮点。
- 对外 ID 使用 `types.Uint64String`，JSON 输出字符串，避免 JavaScript `2^53-1` 精度问题。
- 可空字段用指针 + `omitempty`；OpenAPI 生成 `nullable: true`。
- 时间使用 `time.Time`，RFC3339；仅日期用 `types.Date`。
- 不把 GORM model 直接当 DTO；model 与 DTO 之间显式转换。
- DTO 不实现 `MarshalJSON`（除 `types` 包中的基础类型），避免文档生成器无法推断。

### 4.3 Request

Request 只描述一次 HTTP 调用的入参，按来源拆分 tag：

| 来源 | tag | 说明 |
| --- | --- | --- |
| JSON body | `json` | `POST/PUT/PATCH` |
| Query | `form` | `GET/HEAD/DELETE`，以及 POST 的 query |
| Path | `uri` | 路径参数 |
| Header | `header` | 需要 gofy 绑定器扩展（P1） |
| Cookie | `cookie` | 需要 gofy 绑定器扩展（P1） |
| 文件 | `form` + `*multipart.FileHeader` | multipart |
| 校验 | `validate` | `go-playground/validator` |
| 默认值 | `default` | `creasty/defaults` |
| 错误消息 | `message` | `rule:消息,...` |
| 文档 | `doc` / `example` / `enum` / `format` / `deprecated` | 仅供 docgen |

示例：

```go
package orderrequest

import (
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderenum"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/paging"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/types"
)

// GetOrderRequest 查询订单。
type GetOrderRequest struct {
	ID types.Uint64String `uri:"id" json:"id" validate:"required" message:"required:订单ID不能为空" doc:"订单ID" example:"1234567890123456789"`
}

// ListOrdersRequest 分页查询订单。
type ListOrdersRequest struct {
	paging.PageRequest

	UserID types.Uint64String    `form:"user_id" json:"user_id" validate:"required" message:"required:用户ID不能为空" doc:"用户ID"`
	Status orderenum.OrderStatus `form:"status" json:"status" validate:"omitempty,oneof=pending paid cancelled" message:"oneof:订单状态必须是 pending、paid、cancelled" doc:"订单状态" enum:"pending,paid,cancelled"`
	// 时间范围使用字符串，避免 query 中的时区歧义；格式 RFC3339。
	CreatedFrom string `form:"created_from" json:"created_from" validate:"omitempty,datetime=2006-01-02T15:04:05Z07:00" message:"datetime:创建时间起必须是 RFC3339 格式" doc:"创建时间起（RFC3339）"`
	CreatedTo   string `form:"created_to" json:"created_to" validate:"omitempty,datetime=2006-01-02T15:04:05Z07:00" message:"datetime:创建时间止必须是 RFC3339 格式" doc:"创建时间止（RFC3339）"`
}

// CreateOrderRequest 创建订单。
type CreateOrderRequest struct {
	UserID types.Uint64String `json:"user_id" validate:"required" message:"required:用户ID不能为空" doc:"用户ID"`
	Amount int64              `json:"amount" validate:"required,gt=0" message:"required:金额不能为空,gt:金额必须大于0" doc:"金额，单位：分" example:"1999"`
	Remark string             `json:"remark,omitempty" validate:"max=255" message:"max:备注最多255个字符" doc:"备注" example:"尽快发货"`
	Items  []CreateOrderItem  `json:"items" validate:"required,min=1,max=100,dive" message:"required:订单明细不能为空,min:订单明细至少1条,max:订单明细最多100条" doc:"订单明细"`
}

// GetConfigRequest 查询订单模块配置；没有字段，保留类型是为了让路由与适配器保持强类型。
type GetConfigRequest struct{}
```

**分页基类：**

```go
package paging

// PageRequest 所有分页请求的公共字段。
// 统一使用 page / page_size，避免各服务出现 size/limit/per_page 多种写法。
type PageRequest struct {
	Page     int `form:"page" json:"page" default:"1" validate:"min=1" doc:"页码，从 1 开始" example:"1"`
	PageSize int `form:"page_size" json:"page_size" default:"20" validate:"min=1,max=100" doc:"每页条数，最大 100" example:"20"`
}

func (p PageRequest) Offset() int {
	return (p.Page - 1) * p.PageSize
}
```

**关于 `StringToInt`：**

- Query/form 绑定到 int 时，gin 已经通过 `strconv` 转换字符串。
- JSON body 中的 `"amount": "1999"` 默认会失败；如果调用方确实需要字符串数字，使用 `types.Int64String` / `types.Uint64String`。
- 新契约不要依赖“字符串数字自动转 int”的隐式行为；类型声明即契约。

**关于嵌套集合：**

```go
type CreateOrderItem struct {
	SkuID types.Uint64String `json:"sku_id" validate:"required" doc:"SKU ID"`
	Num   int32              `json:"num" validate:"required,gt=0,lte=999" doc:"数量"`
}
```

Go 不支持 PHP 的 `items.*.sku_id` 通配规则；validator 的 `dive` 已覆盖“逐项校验”。如果需要“数组中每项的某个字段必填”，在 `CreateOrderItem` 上声明 `required` 即可。

### 4.4 Response、Envelope、List、Page

**核心原则：契约 Response 只描述 `data`，不重复声明 `code`/`msg`。**

服务端实际响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "order": {
      "id": "1234567890123456789",
      "user_id": "10001",
      "amount": 1999,
      "status": "paid"
    }
  },
  "req_id": "6f1c0b1e-..."
}
```

契约：

```go
// GetOrderResponse 查询订单返回。
type GetOrderResponse struct {
	Order *OrderDTO `json:"order" doc:"订单详情"`
}
```

错误响应：

```json
{
  "code": 40401,
  "msg": "order not found",
  "data": null,
  "req_id": "6f1c0b1e-...",
  "details": {}
}
```

**通用信封：**

```go
package envelope

// Envelope 是 gofy 成功响应的完整形状，用于文档与客户端解析。
type Envelope[T any] struct {
	Code    int               `json:"code"`
	Msg     string            `json:"msg"`
	Data    T                 `json:"data"`
	ReqID   string            `json:"req_id,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// ErrorEnvelope 是错误响应的完整形状。
type ErrorEnvelope struct {
	Code    int               `json:"code"`
	Msg     string            `json:"msg"`
	Data    any               `json:"data"`
	ReqID   string            `json:"req_id,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}
```

**列表：**

```go
// ListData 列表 data。total 是全量条数，不是当前页条数。
type ListData[T any] struct {
	Total int64 `json:"total" doc:"总条数" example:"125"`
	List  []T   `json:"list" doc:"当前页数据"`
}
```

**分页：**

```go
// PageData 分页 data。page/page_size 回显请求值。
type PageData[T any] struct {
	Total    int64 `json:"total" doc:"总条数" example:"125"`
	List     []T   `json:"list" doc:"当前页数据"`
	Page     int   `json:"page" doc:"页码" example:"1"`
	PageSize int   `json:"page_size" doc:"每页条数" example:"20"`
}
```

**使用：**

列表响应直接使用 `paging.ListData[OrderDTO]` 作为 `endpoint.New` 的 `Res` 类型：

```go
var ListOrders = endpoint.New[ListOrdersRequest, paging.ListData[OrderDTO]](...)
```

如团队希望保留具名响应类型，使用普通 struct 嵌入（不要使用泛型类型别名，避免泛型类型别名在不同工具链间的差异）：

```go
type ListOrdersResponse struct {
	paging.ListData[OrderDTO]
}
```

嵌入会按 JSON 规则展开 `total`/`list`，文档生成器同样展开嵌入字段。

**无响应体：**

```go
type CancelOrderRequest struct {
	ID types.Uint64String `uri:"id" json:"id" validate:"required" doc:"订单ID"`
}
```

对应 `endpoint.Spec{NoContent: true}`，OpenAPI 返回 204，Client 返回 `error`。

### 4.5 常量

常量分三类，禁止跨层复用：

| 类型 | 位置 | 示例 |
| --- | --- | --- |
| 协议/框架常量 | `apicore/contracts/common` | 分页默认值、请求头名 |
| 业务常量 | `appinterface/{domain}service/{domain}const/const.go` | 最大订单明细数、订单号前缀 |
| 错误码 | `apicore/contracts/common/errcode` + 业务码段 | `ErrOrderNotFound` |

```go
package orderconst

const (
	// MaxOrderItems 单笔订单最大明细数。
	MaxOrderItems = 100

	// OrderNoPrefix 订单号前缀。
	OrderNoPrefix = "ORD"
)
```

**禁止：**

- 用 `iota` 定义需要跨服务传输的数值常量（值会随插入顺序变化）。
- 在 DTO 中硬编码魔法值；枚举/常量必须命名。
- 把错误码和 HTTP status 混在一个未类型化的 int 中。

### 4.6 枚举

Go 没有原生枚举，用“命名类型 + 常量 + 辅助函数”实现，文档生成器通过 `enum` tag 读取可选值。

**字符串枚举（推荐）：**

```go
package orderenum

type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusPaid      OrderStatus = "paid"
	OrderStatusCancelled OrderStatus = "cancelled"
)

var AllOrderStatus = []OrderStatus{
	OrderStatusPending,
	OrderStatusPaid,
	OrderStatusCancelled,
}

var OrderStatusLabels = map[OrderStatus]string{
	OrderStatusPending:   "待支付",
	OrderStatusPaid:      "已支付",
	OrderStatusCancelled: "已取消",
}

func (s OrderStatus) Valid() bool {
	_, ok := OrderStatusLabels[s]
	return ok
}

func ParseOrderStatus(v string) (OrderStatus, bool) {
	s := OrderStatus(v)
	return s, s.Valid()
}

func (s OrderStatus) String() string {
	if label, ok := OrderStatusLabels[s]; ok {
		return label
	}
	return string(s)
}
```

字段声明：

```go
Status OrderStatus `json:"status" form:"status" validate:"omitempty,oneof=pending paid cancelled" doc:"订单状态" enum:"pending,paid,cancelled"`
```

**整数枚举：**

```go
type PayType int

const (
	PayTypeUnknown PayType = 0
	PayTypeWechat  PayType = 1
	PayTypeAlipay  PayType = 2
)

var PayTypeLabels = map[PayType]string{
	PayTypeWechat: "微信",
	PayTypeAlipay: "支付宝",
}
```

字段声明：

```go
PayType PayType `json:"pay_type" validate:"omitempty,oneof=1 2" doc:"支付方式" enum:"1,2" enumLabels:"1=微信,2=支付宝"`
```

**规则：**

1. 对外字符串枚举值不可改名；新增值必须保证旧客户端可忽略。
2. 整数枚举从 1 开始，0 保留给“未知/未设置”。
3. `enum` tag 必须与 `All*` / `*Labels` 一致；`internal/lint` 在测试中校验。
4. 客户端遇到未知枚举值**不得报错**；服务端校验用 `oneof`。
5. 文档生成器输出 `enum`，标签输出 `x-enum-labels`。

### 4.7 错误码

**公共错误码必须与 gofy `apperr` 现有约定一致**，否则同一个错误在服务端和客户端会得到不同 code。

| code | HTTP | 含义 | gofy 对应 |
| --- | --- | --- | --- |
| 0 | 200 | 成功 | `support` 默认 |
| 40000 | 400 | 请求错误 | `apperr.BadRequest` |
| 40001 | 400 | 参数非法 | `apperr.InvalidArgument` |
| 40100 | 401 | 未认证 | `apperr.Unauthorized` |
| 40300 | 403 | 无权限 | `apperr.Forbidden` |
| 40400 | 404 | 资源不存在 | `apperr.NotFound` |
| 40900 | 409 | 冲突 | `apperr.Conflict` |
| 41300 | 413 | 请求体过大 | `support.bindError` |
| 42900 | 429 | 限流 | `apperr.TooManyRequests` |
| 50000 | 500 | 服务内部错误 | `apperr.Internal` |
| 50300 | 503 | 服务不可用 | `apperr.Unavailable` |

**业务码段分配：**

| 区段 | 归属 | 示例 |
| --- | --- | --- |
| 0 | 成功 | — |
| 40000–40999 | 通用客户端错误 | 40001 |
| 50000–50999 | 通用服务端错误 | 50000 |
| 10000–19999 | order | 40401 订单不存在 |
| 20000–29999 | user | 20401 用户不存在 |
| 30000–39999 | payment | 30401 支付失败 |
| 90000–99999 | 平台/基础设施 | 90401 配置缺失 |

`apicore/contracts/common/errcode/ranges.go` 维护该表；CI 检查同一 code 不被两个业务域声明。

**纯契约错误类型（不依赖 gofy）：**

```go
package errcode

type Code int

type Fault struct {
	Code    Code
	Status  int
	Message string
}

func New(code Code, status int, message string) Fault {
	return Fault{Code: code, Status: status, Message: message}
}
```

**业务错误：**

```go
package demoservice

import "github.com/bingcool/gofy-interface-service/apicore/contracts/common/errcode"

const (
	ErrCodeOrderNotFound errcode.Code = 40401
	ErrCodeOrderClosed   errcode.Code = 40402
)

var (
	ErrOrderNotFound = errcode.New(ErrCodeOrderNotFound, 404, "order not found")
	ErrOrderClosed   = errcode.New(ErrCodeOrderClosed, 409, "order already closed")
)
```

**服务端映射：**

```go
package gofyadapter

import (
	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/errcode"
)

func Error(f errcode.Fault) *apperr.Error {
	return apperr.New(int(f.Code), f.Status, f.Message)
}

func ErrorWithDetails(f errcode.Fault, details map[string]string) *apperr.Error {
	return Error(f).WithDetails(details)
}
```

**客户端判断：**

```go
if demoserviceclient.IsOrderNotFound(err) {
	// 处理订单不存在：返回空结果或提示用户
}
```

```go
func IsOrderNotFound(err error) bool {
	return client.CodeOf(err) == int(ErrCodeOrderNotFound)
}
```

### 4.8 基础类型

**大整数：**

```go
package types

// Uint64String 在 JSON 中表现为字符串，避免 JavaScript 精度丢失；
// 在 query/form/uri 中可按普通 uint64 解析。
type Uint64String uint64

func (v Uint64String) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(v), 10) + `"`), nil
}

func (v *Uint64String) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return fmt.Errorf("types: invalid uint64 %q", s)
	}
	*v = Uint64String(n)
	return nil
}

func (v Uint64String) String() string { return strconv.FormatUint(uint64(v), 10) }
func (v Uint64String) Uint64() uint64 { return uint64(v) }

// OpenAPIType 供 docgen 输出 string 类型。
func (Uint64String) OpenAPIType() (typ, format string) { return "string", "uint64" }
```

`Int64String` 同理。

**日期：**

```go
package types

type Date struct {
	time.Time
}

const dateLayout = "2006-01-02"

func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Format(dateLayout) + `"`), nil
}

func (d *Date) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

func (Date) OpenAPIType() (typ, format string) { return "string", "date" }
```

**定点数：**

需要精确小数时使用字符串定点数（如金额单位不是分）：

```go
type Decimal string

func (Decimal) OpenAPIType() (typ, format string) { return "string", "decimal" }
```

> 能用整数最小单位就不要用 `Decimal`；整数更简单、更快、无舍入。

### 4.9 标签规范

| tag | 作用 | 示例 |
| --- | --- | --- |
| `json` | JSON 字段名 | `json:"user_id"` |
| `form` | query/form 字段名 | `form:"page_size"` |
| `uri` | 路径参数名 | `uri:"id"` |
| `header` | 请求头 | `header:"X-Tenant-ID"` |
| `cookie` | Cookie | `cookie:"session"` |
| `validate` | 校验规则 | `validate:"required,min=1"` |
| `default` | 默认值 | `default:"20"` |
| `message` | 错误消息，同时作为响应 `msg` 返回 | `message:"required:用户ID不能为空"` |
| `doc` | 字段说明 | `doc:"订单ID"` |
| `example` | 示例 | `example:"10001"` |
| `enum` | 枚举值 | `enum:"pending,paid"` |
| `enumLabels` | 枚举标签 | `enumLabels:"1=微信,2=支付宝"` |
| `format` | OpenAPI format | `format:"date-time"` |
| `deprecated` | 废弃标记 | `deprecated:"true"` |

**校验 tag 到 OpenAPI 的映射：**

| validate | OpenAPI |
| --- | --- |
| `required` | `required: [field]` |
| `min` / `max`（数字） | `minimum` / `maximum` |
| `min` / `max`（字符串） | `minLength` / `maxLength` |
| `len` | `minLength` = `maxLength` |
| `oneof` | `enum` |
| `email` | `format: email` |
| `url` | `format: uri` |
| `uuid` | `format: uuid` |
| `gte` / `lte` / `gt` / `lt` | `minimum` / `maximum` + `exclusive*` |
| `dive` | 仅用于数组元素，不直接映射 |
| 其他 | 写入 `description`，不猜测 |

### 4.10 示例：demoservice 完整契约

**service.go：**

```go
package demoservice

import (
	"context"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderdto"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/paging"
)

// Service 是订单契约的服务端接口。业务实现必须满足该接口。
type Service interface {
	GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error)
	ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error)
	CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error)
	CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error
	GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error)
}
```

**orderroute/order_route.go：**

```go
package orderroute

import (
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderdto"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/ordererror"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/paging"
)

var (
	// GetOrder GET /orders/{id}
	GetOrder = endpoint.New[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse](endpoint.Spec{
		OperationID: "GetOrder",
		Method:      "GET",
		Path:        "/orders/{id}",
		Summary:     "查询订单详情",
		Description: "按订单ID查询订单；订单不存在返回 40401。",
		Tags:        []string{"demoservice"},
		// 契约声明多少就限多少：服务端按 Key 生成限流中间件。没有声明
		// RateLimit 的路由不挂限流。
		RateLimit:   &endpoint.RateLimit{Limit: 120, WindowSeconds: 60, Key: endpoint.RateKeyUser},
		Errors: []endpoint.ErrorSpec{
			{Code: int(ordererror.ErrCodeOrderNotFound), Status: 404, Message: "order not found"},
		},
	})

	// ListOrders GET /orders
	ListOrders = endpoint.New[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](endpoint.Spec{
		OperationID: "ListOrders",
		Method:      "GET",
		Path:        "/orders",
		Summary:     "分页查询订单",
		Tags:        []string{"demoservice"},
		RateLimit:   &endpoint.RateLimit{Limit: 100, WindowSeconds: 60, Key: endpoint.RateKeyUser},
	})

	// CreateOrder POST /orders
	CreateOrder = endpoint.New[orderrequest.CreateOrderRequest, orderresponse.CreateOrderResponse](endpoint.Spec{
		OperationID: "CreateOrder",
		Method:      "POST",
		Path:        "/orders",
		Summary:     "创建订单",
		Tags:        []string{"demoservice"},
		RateLimit:   &endpoint.RateLimit{Limit: 60, WindowSeconds: 60, Key: endpoint.RateKeyUser},
		Errors: []endpoint.ErrorSpec{
			{Code: 40001, Status: 400, Message: "invalid argument"},
		},
	})

	// CancelOrder DELETE /orders/{id}
	CancelOrder = endpoint.New[orderrequest.CancelOrderRequest, endpoint.NoBody](endpoint.Spec{
		OperationID: "CancelOrder",
		Method:      "DELETE",
		Path:        "/orders/{id}",
		Summary:     "取消订单",
		Tags:        []string{"demoservice"},
		NoContent:   true,
		RateLimit:   &endpoint.RateLimit{Limit: 60, WindowSeconds: 60, Key: endpoint.RateKeyUser},
		Errors: []endpoint.ErrorSpec{
			{Code: int(ordererror.ErrCodeOrderNotFound), Status: 404, Message: "order not found"},
			{Code: int(ordererror.ErrCodeOrderClosed), Status: 409, Message: "order already closed"},
		},
	})
)
```

**orderroute/config_route.go：**

```go
package orderroute

import (
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/endpoint"
)

// GetConfig GET /config
var GetConfig = endpoint.New[orderrequest.GetConfigRequest, orderresponse.GetConfigResponse](endpoint.Spec{
	OperationID: "GetConfig",
	Method:      "GET",
	Path:        "/config",
	Summary:     "查询订单模块配置",
	Tags:        []string{"demoservice"},
})
```

**orderroute/routes.go 与 demoservice/routes.go：**

```go
package orderroute

// Routes 返回订单服务的所有路由描述符。
func Routes() []endpoint.Descriptor {
	return []endpoint.Descriptor{
		GetOrder,
		ListOrders,
		CreateOrder,
		CancelOrder,
		GetConfig,
	}
}
```

```go
package demoservice

// Routes 返回订单服务的所有路由描述符，委托给 orderroute 聚合。
func Routes() []endpoint.Descriptor {
	return orderroute.Routes()
}
```

**response.go：**

```go
package orderresponse

import "github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderdto"

// GetOrderResponse 查询订单返回。
type GetOrderResponse struct {
	Order *orderdto.OrderDTO `json:"order" doc:"订单详情"`
}

// CreateOrderResponse 创建订单返回。
type CreateOrderResponse struct {
	Order *orderdto.OrderDTO `json:"order" doc:"已创建的订单"`
}
```

**orderconst/const.go：**

```go
package orderconst

const (
	// MaxOrderItems 单笔订单最大明细数。
	MaxOrderItems = 100

	// OrderNoPrefix 订单号前缀。
	OrderNoPrefix = "ORD"
)
```

**enum.go：**

```go
package orderenum

type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusPaid      OrderStatus = "paid"
	OrderStatusCancelled OrderStatus = "cancelled"
)

var AllOrderStatus = []OrderStatus{
	OrderStatusPending,
	OrderStatusPaid,
	OrderStatusCancelled,
}

var OrderStatusLabels = map[OrderStatus]string{
	OrderStatusPending:   "待支付",
	OrderStatusPaid:      "已支付",
	OrderStatusCancelled: "已取消",
}

func (s OrderStatus) Valid() bool {
	_, ok := OrderStatusLabels[s]
	return ok
}
```

---

## 5. 路由描述符与服务端绑定

### 5.1 为什么不用代码生成路由

InterfaceApi 的服务端路由仍写在 PHP `Router` 文件里，接口注解只用于生成 Client。Go 方案更进一步：

- **路由描述符是唯一事实来源**：路径、方法、鉴权、限流、文档、Client 都从它派生。
- **服务端绑定由适配器完成**：避免“契约写 `/orders/{id}`，路由写 `/orders/:id`”的漂移。
- **不需要代码生成路由文件**：`BindAll` 反射调用实现方法，启动期一次完成。
- **编译期仍有类型检查**：业务实现声明 `var _ demoservice.Service = (*Handler)(nil)`；按路由分组拆成多个 handler 时，各自断言窄接口（`demoservice.OrdersService`、`demoservice.ConfigService`）。

### 5.2 endpoint 包 API

```go
package endpoint

import "reflect"

// 认证不在契约里声明：2026-11-19 起 AuthMode 已删除，"需要登录"由分组或路由
// 上的 Use("auth") 表达，见本文档的中间件章节。

// RateKey 描述限流主体。
type RateKey string

const (
	RateKeyIP      RateKey = "ip"
	RateKeyUser    RateKey = "user"
	RateKeyAPIKey  RateKey = "api_key"
	RateKeyRoute   RateKey = "route"
)

// RateLimit 是路由级限流元数据；具体实现由服务端中间件提供。
type RateLimit struct {
	Limit         int
	WindowSeconds int
	Key           RateKey
}

// ErrorSpec 描述一个可预期错误，用于 OpenAPI。
type ErrorSpec struct {
	Code    int
	Status  int
	Message string
}

// ResponseKind 描述特殊响应类型。
type ResponseKind string

const (
	KindJSON     ResponseKind = ""
	KindSSE      ResponseKind = "sse"
	KindChunked  ResponseKind = "chunked"
	KindDownload ResponseKind = "download"
)

// NoBody 用于无响应体/流式/下载路由的占位响应类型。
type NoBody struct{}

// Spec 是路由的全部元数据。
type Spec struct {
	OperationID         string
	Method              string
	Path                string
	Summary             string
	Description         string
	Tags                []string
	Middleware          []string // 路由级中间件逻辑名，按声明顺序执行
	RateLimit           *RateLimit
	NoContent           bool
	ResponseKind        ResponseKind
	RequestContentType  string // 默认 application/json；上传为 multipart/form-data
	ResponseContentType string // 默认 application/json；下载/流式可覆盖
	Errors              []ErrorSpec
	Deprecated          bool
}

// orderroute 定义 ServiceTag = "demoservice"，各路由统一用它填充 Spec.Tags；
// docgen 用它做 -service 筛选、PREFIX 前缀映射与 Swagger 分组。

// Route 是强类型路由描述符。
type Route[Req, Res any] struct {
	spec Spec
}

func New[Req, Res any](spec Spec) *Route[Req, Res] {
	return &Route[Req, Res]{spec: spec}
}

// HTTP 方法构造器：方法/路径作参数，其余元数据在 build 回调里设置。
func Get[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Post[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Put[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Patch[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Delete[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Head[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]
func Options[Req, Res any](path string, build func(*Spec)) *Route[Req, Res]

func (r *Route[Req, Res]) Spec() Spec { return r.spec }

// Use 追加路由级中间件逻辑名（等价于 spec.Middleware），返回路由本身便于链式声明。
func (r *Route[Req, Res]) Use(names ...string) *Route[Req, Res]

func (r *Route[Req, Res]) RequestType() reflect.Type {
	return reflect.TypeOf((*Req)(nil)).Elem()
}

func (r *Route[Req, Res]) ResponseType() reflect.Type {
	return reflect.TypeOf((*Res)(nil)).Elem()
}

// Descriptor 是异构路由集合的元素类型。
type Descriptor interface {
	Spec() Spec
	RequestType() reflect.Type
	ResponseType() reflect.Type
}
```

**约束：**

- `Spec.Method` 必须是 `GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS`。
- `Spec.Path` 必须以 `/` 开头，路径参数用 `{name}`。
- `Spec.OperationID` 必须与 `Service` 接口方法名一致。
- 同一模块内 `(Method, Path)` 不得重复；`config.Validate` 在启动期检查。
- `NoContent` 与 `ResponseKind` 互斥；`ResponseKind != ""` 时 `ResponseType` 必须是 `endpoint.NoBody`。

### 5.3 服务端绑定

**显式绑定（推荐用于少量路由/需要精细控制）：**

```go
func Bind[Req, Res any](
	r *support.Router,
	rt *endpoint.Route[Req, Res],
	h support.Handler[Req, Res],
	mw ...support.MiddlewareFunc,
)
```

**按接口批量绑定：**

```go
// MiddlewareRegistry 是服务端注入的「别名 → 中间件实例」查询接口，
// 由 support/middleware.Registry 实现；adapter 只依赖这一个方法。
type MiddlewareRegistry interface {
	Lookup(name string) (support.MiddlewareFunc, bool)
}

type BindOptions struct {
	// Global 应用到所有契约路由的中间件（RequestID/AccessLog 等由 gofy 根包负责）。
	Global []support.MiddlewareFunc

	// Routes 按 OperationID 追加路由级中间件。
	Routes map[string][]support.MiddlewareFunc

	// Middleware 是本次绑定的显式别名映射，优先级最高，用于测试桩
	// 或单个模块的定制覆盖；未注册或值为 nil 的名字在启动期报错。
	Middleware map[string]support.MiddlewareFunc

	// Registry 是可选的显式注册表。通常不需要：App 启动时把别名注册进
	// Router 携带的共享注册表（support.RouterConfig.Middleware），adapter
	// 直接从这里解析。
	Registry MiddlewareRegistry

	// RateLimit 按契约声明构造限流中间件，只对声明了 Spec.RateLimit 的路由
	// 调用，返回值装在路由中间件链最前面；返回 nil 表示该路由不挂限流。
	// 第二个参数是 Router 的 Responder，限流拒绝与 App 其它错误共用信封。
	RateLimit func(endpoint.Spec, support.Responder) support.MiddlewareFunc

	// Strict 为 true 时，实现类型缺少方法、签名不符会返回错误（默认 true）。
	Strict bool
}

func BindAll(
	r *support.Router,
	routes []endpoint.Descriptor,
	impl any,
	opts BindOptions,
) error
```

**路由分组（Gin 风格，分组声明在路由文件里）：**

```go
// appinterface/demoservice/orderroute/order_route.go
var Orders = endpoint.NewRouteGroup("/orders", func(group *endpoint.Group) {
	// 中间件（含认证）都按名字显式声明；装上 "auth" 就是"本组需要登录"。
	group.Use("audit", "auth")
	// 路由在分组回调里内联声明，并赋值给包级变量供客户端/测试复用。
	GetOrder = endpoint.Get[Req, Res]("/orders/{id}", func(spec *endpoint.Spec) {
		spec.OperationID = "GetOrder"
	})
	// ListOrders / CreateOrder / CancelOrder 同理…
	group.Add(GetOrder, ListOrders, CreateOrder, CancelOrder)
})

// config_route.go 也可以把路由直接内联在分组回调里，再赋值给包级变量
// 供客户端复用：
// var Config = endpoint.NewRouteGroup("/config", func(group *endpoint.Group) {
//     GetConfig = endpoint.New[Req, Res](endpoint.Spec{...})
//     group.Add(GetConfig)
// })

// 服务端也可以按 Gin 习惯手动建组：分组名不会被解析，契约里的
// "audit"/"auth" 需要在这里显式传实例；路由级名字仍由 Router 的
// App 注册表 / opts.Registry / opts.Middleware 解析。
orders := router.Group(orderroute.Orders.Prefix, auditMiddleware, authMiddleware)
err := gofyadapter.BindAll(orders, orderroute.Orders.Routes, impl, opts)
```

**分组与路由中间件（契约声明名字，服务端提供实现）：**

```go
// 路由文件：分组级与路由级都只声明逻辑名，顺序即执行顺序；
// 认证同样是名字（"auth"），契约里没有第二个地方能声明它。
var Orders = endpoint.NewRouteGroup("/orders", func(group *endpoint.Group) {
	group.Use("audit", "auth")

	CreateOrder = endpoint.Post[Req, Res]("/orders", func(spec *endpoint.Spec) {
		spec.OperationID = "CreateOrder"
		spec.Middleware = []string{"order-audit"}
	})
	// 等价链式写法：endpoint.Post[Req, Res]("/orders", build).Use("order-audit")
	group.Add(GetOrder, ListOrders, CreateOrder, CancelOrder)
})

// 服务端：App 启动时执行 registrar，把别名与实例注册进本次装配的共享注册表
gofy.WithMiddlewareRegistrar(func(m gofy.MiddlewareRegistration) error {
	if err := m.Registry.Register("audit", auditMiddleware); err != nil {
		return err
	}
	if err := m.Registry.Register("order-audit", orderAuditMiddleware); err != nil {
		return err
	}
	return m.Registry.Register("auth", authMiddleware)
})

// 模块只绑定路由：BindGroup 从 Router 携带的注册表解析分组名，
// 内部 BindAll 再解析每条路由声明的名字；没有任何隐式注入
err := gofyadapter.BindGroup(router, orderroute.Orders, impl, gofyadapter.BindOptions{})
```

- `endpoint.Group` 包含 `Prefix`、`Routes`、`Middleware []string`；前缀、路由、
  分组与中间件名都放在同一个路由文件里，每个文件直接导出自己的分组；
- `Spec.Middleware []string` 是路由级名字，`Route.Use(names...)` 是它的链式写法；
  空名字会被忽略，重复名字按声明顺序重复执行；
- 契约不依赖 gofy，服务端把别名与实例注册进 `support/middleware.Registry`，由 App
  在启动时挂到 Router 上（`support.RouterConfig.Middleware`）；解析优先级为
  `BindOptions.Middleware`（单次覆盖）→ `BindOptions.Registry`（可选显式注入）
  → Router 携带的 App 注册表，都查不到（或值为 nil）的名字在启动期报错，错误带上
  分组前缀或 OperationID；
- 手动 `router.Group(prefix, mw...)` + `BindAll` 时分组名不会被解析（分组中间件
  已直接传实例），路由级名字仍会解析；`Bind` 只安装显式传入的 `mw`；
- 分组 `Prefix` 可以携带完整部署前缀（推荐，如 `/api/v1/order`），此时组内
  `Spec.Path` 只写相对路径，完整路由 = `Prefix` + `Spec.Path`；分组 `Prefix`
  也可以只是分组名，`BindGroup`/`BindAll` 会通过 `Router.GroupPrefix()` 把
  完整 `Spec.Path` 裁剪成组内相对路径后注册；
- `Group.Add` 在声明阶段把 `Prefix` 写入每条描述符的 `Spec.Prefix`，客户端与
  docgen 都经 `endpoint.JoinPath` 从同一来源拼出完整路径，调用方不再逐次传
  前缀；`docgen.Config.Prefixes` 命中标签时替换该前缀并保留组内剩余路径
  （CLI `-prefix` 覆盖），`BindGroup` 启动期断言 stamped 前缀与分组一致；
- 中间件顺序：分组中间件（按 `group.Use` 顺序）→ `RateLimit`（契约声明时）→
  `BindOptions.Global` → 路由中间件名（按 `spec.Middleware` 顺序）→
  `Routes[OperationID]`；限流放在路由链最前面，超额请求在进入其余路由中间件
  和 handler 之前就被拒绝；
- 所有中间件都必须显式声明并注册，不存在默认别名或内建兜底：契约没有认证字段，
  需要认证时写 `group.Use("auth")` 或 `spec.Middleware = []string{"auth"}`，
  没写就是公开路由。

`BindAll` 的行为：

1. 校验路由集合：方法/路径/OperationID 合法、无重复。
2. 用反射检查 `impl` 是否实现每个 `OperationID` 方法：
   - JSON：`func(context.Context, *Req) (*Res, error)`
   - `NoContent`：`func(context.Context, *Req) error`
   - SSE/Download/Chunked 使用专用绑定函数，不进入普通 JSON 分支。
3. 解析 `Spec.Middleware` 逻辑名（`opts.Middleware` → `opts.Registry` → Router 的
   App 注册表），缺失即记入聚合错误；认证也走同一条路径，没有默认别名。
4. 对声明了 `Spec.RateLimit` 的路由调用 `opts.RateLimit`，把返回的中间件装在
   路由链最前面；没有声明的路由不限流。
5. 调用 gofy 的 `support.Any(r, method, path, h, mw...)` 注册。
6. 返回聚合错误，列出所有缺失/签名不符的方法，而不是遇到第一个就退出。

`Spec.Auth` 不参与中间件装配，只供 docgen 生成 OpenAPI 的 `security` 标记。

**中间件别名注册表（`support/middleware.Registry`）：**

契约只写别名，实例由服务端注册。注册表实现在 gofy 的
`support/middleware/registry.go`，adapter 通过 `MiddlewareRegistry` 接口消费，
因此契约层和 adapter 都不认识具体中间件类型：

```go
type Registry struct {
	// mu sync.RWMutex; entries map[string]support.MiddlewareFunc
}

func NewRegistry() *Registry
func (r *Registry) Register(name string, mw support.MiddlewareFunc) error
func (r *Registry) MustRegister(name string, mw support.MiddlewareFunc)
func (r *Registry) Lookup(name string) (support.MiddlewareFunc, bool)
func (r *Registry) Names() []string
```

- **显式实例，无包级全局**：App 在每次装配（启动 / `Reload`）时新建一份，通过
  `WithMiddlewareRegistrar` 注册应用别名，再随 `support.Router` 交给 adapter；
  模块不再自己创建或注入，Nacos 热更新和测试各自持有一份，互不污染；
- **别名与实例一一对应**：`Register` 去掉首尾空白后校验别名非空、实例非 `nil`、
  别名未被占用，重复注册返回 `middleware alias "x" is already registered`，
  不静默覆盖；`MustRegister` 是 panic 版本，用于启动期装配；
- **并发安全**：内部 `sync.RWMutex`；`Names()` 返回排序副本，供启动日志与排查；
  `Lookup` 对 `nil` 注册表返回 `(nil, false)`，typed-nil 注入不会 panic；
- **解析优先级**：`BindOptions.Middleware`（本次绑定的显式覆盖）→
  `BindOptions.Registry`（可选）→ Router 的 App 注册表；都查不到时启动期报
  `middleware "x" is not registered in BindOptions.Middleware, BindOptions.Registry or the app middleware registry`；
- **别名常量与中间件同文件**：框架内建导出 `NameAccessLog`、`NameBodyLimit`、
  `NameCORS`、`NameHeaderPropagation`、`NameJWTAuth`、`NameRateLimit`、
  `NameRecovery`、`NameRequestID`、`NameSecurityHeaders`；业务中间件在自己的文件里
  导出 `NameXxx`（demo-service：`NameAuth`、`NameAudit`、`NameOrderAudit`）；
  认证别名（demo 的 `"auth"`）与其他别名完全一样，必须在契约里显式声明并在
  注册表里注册，没有默认别名或框架兜底；
- **只服务契约路由**：App 级全局链仍由 `gofy.With*` 选项安装，注册表不接管它。
  把全局链里已安装的中间件再注册给契约路由会执行两次，只在确实需要「按路由
  覆盖全局行为」时才这么做。

**业务实现：**

```go
package order

import (
	"context"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderdto"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/paging"
)

type Handler struct {
	svc *service.OrderService
}

var _ demoservice.Service = (*Handler)(nil)

func (h *Handler) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	// 调用 service；不存在时返回 gofyadapter.Error(demoservice.ErrOrderNotFound)
}

func (h *Handler) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	// 调用 service 分页查询；把 model 转成 OrderDTO
}

func (h *Handler) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	// 调用 service 创建订单；校验失败返回 apperr.InvalidArgument
}

func (h *Handler) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error {
	// 调用 service 取消订单；状态不允许时返回 gofyadapter.Error(demoservice.ErrOrderClosed)
}

func (h *Handler) GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error) {
	// 返回模块配置；示例实现输出固定值
}
```

**模块注册：**

```go
func (m *Module) Register(r *support.Router) error {
	// 应用层中间件（含认证 "auth"）由 App 启动时的 WithMiddlewareRegistrar
	// 注册进共享注册表，随 support.Router 下发；模块只绑定路由，不创建注册表、
	// 不构造认证中间件。bindOptions 由装配根（app/container）构造后注入。
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, m.handler, m.bindOptions); err != nil {
			return err
		}
	}
	return nil
}
```

限流完全由契约驱动：路由声明 `Spec.RateLimit` 就限流，`Limit` /
`WindowSeconds` 就是生效阈值，`Key` 决定计数主体，`RedisAlias` 指定计数
使用的 Redis 命名实例（缺省 `ratelimit`）；没有声明就不挂限流。demo 的实现
是 `app/middleware.RateLimiter`，装配根只把回调交给绑定选项：

```go
// app/container/container.go
func (c *Container) BindOptions() gofyadapter.BindOptions {
	return gofyadapter.BindOptions{RateLimit: appmiddleware.RateLimiter{}.Callback()}
}
```

adapter 调用回调时把 Router 的 Responder 作为第二个参数注入，`RateLimiter`
用 `RateLimiter.Responder`（非空时）或注入值渲染 429/503，因此限流错误与
其它接口共用同一 code/msg/req_id 信封。

中间件在首次请求时用 `redisstore.New(redis.NewIO(ctx, alias))` 构建
`ratelimit.Store` 并缓存：模块工厂完成路由绑定时 `app/appio` 尚未打开 Redis，
而 `Reload` 时新代中间件必须绑定到新代连接。解析失败不缓存，失败请求按 503
处理。

`RateLimiter` 按 `RateKey` 生成不同的计数主体，统一使用框架
`middleware.RateLimit` + `ratelimit.Store`：

| `RateKey` | 计数主体 | 身份缺失时 |
| --- | --- | --- |
| `user` | `frameworkcontext.UserID` | 退回客户端 IP |
| `ip`（缺省/未知 Key 同此） | 客户端 IP | — |
| `api_key` | `X-API-Key` 请求头 | 退回客户端 IP |
| `route` | 整条路由，不区分调用方 | — |

- 分桶键始终带方法 + 路由（`<主体>\|<subject>\|<method>\|<route>`），一条热点
  路由不会耗尽另一条路由的额度；
- 认证声明在分组层，先于限流执行，所以 `user` 主体能读到已认证身份；
- 限流中间件装在路由中间件链最前面（分组中间件之后），超额请求在进入其余
  路由中间件和 handler 之前就被拒绝；
- 契约里的 `Limit` / `WindowSeconds` 非法、Redis 实例解析失败或计数失败时
  都返回 503，不会静默放行；
- 计数写在契约 `RedisAlias` 指定的实例上（缺省 `ratelimit`），Redis 键使用
  `redisstore.DefaultPrefix`，多实例共享同一份计数；换存储只改
  `RateLimiter.RedisClient` 或中间件实现，契约和路由绑定都不用动。

契约里 `group.Use("audit", "auth")` 声明了分组级中间件，`spec.Middleware`
声明了路由级中间件；它们都在启动期从 App 注册表解析，缺失即报错。

### 5.4 中间件安装顺序

最终 gin 链路：

```text
RequestID
  -> AccessLog
  -> Recovery
  -> BodyLimit
  -> CORS
  -> 业务全局中间件（WithMiddleware）
  -> 路由分组中间件（router.Group(prefix, mw...)，来自 group.Use 名字）
  -> RateLimit（如果契约声明 Spec.RateLimit）
  -> BindOptions.Global
  -> 契约路由中间件名（Spec.Middleware，按声明顺序）
  -> 契约路由中间件（BindOptions.Routes[OperationID]）
  -> 参数绑定 + 校验
  -> handler
  -> after 中间件（P2）
  -> Responder / Renderer
```

**规则：**

- `RequestID` 必须最外层，保证错误响应和日志都有 `req_id`。
- `Recovery` 必须在业务中间件之外，panic 也要返回信封。
- `AccessLog` 在 `Recovery` 外层，才能记录 500。
- 鉴权在参数绑定前，未认证请求不解析 body。
- 限流在路由链最前面、分组中间件之后：用户级限流依赖分组层的认证，因此认证
  应声明在 `group.Use`，而不是 `spec.Middleware`。
- 契约路由的中间件由 `BindAll` 安装，避免业务模块重复写。

### 5.5 特殊响应绑定

JSON 之外的响应不使用 `support.Handler[Req,Res]`，避免契约包导入 gofy 类型。

```go
// SSE：handler 向流写入事件，返回后结束。
func BindSSE[Req any](
	r *support.Router,
	rt *endpoint.Route[Req, endpoint.NoBody],
	h func(ctx context.Context, req *Req, stream *sse.Stream) error,
	mw ...support.MiddlewareFunc,
)

// 下载：返回文件流；Content-Disposition 由 gofy 生成。
func BindDownload[Req any](
	r *support.Router,
	rt *endpoint.Route[Req, endpoint.NoBody],
	h func(ctx context.Context, req *Req) (*support.Download, error),
	mw ...support.MiddlewareFunc,
)

// 分块：handler 直接写 chunk。
func BindChunked[Req any](
	r *support.Router,
	rt *endpoint.Route[Req, endpoint.NoBody],
	h func(ctx context.Context, req *Req, w *chunked.Writer) error,
	mw ...support.MiddlewareFunc,
)
```

> **实现状态（当前）：** Gofy Adapter 只实现 JSON 信封与 `NoContent`（204）两种响应。
> `apicore/adapter/gofy.BindAll` 对 `KindSSE`/`KindDownload`/`KindChunked` 会在启动时
> 返回聚合错误（`response kind ... is not supported by the gofy adapter`），不会静默
> 注册成 JSON。客户端 `client.StreamSSE`/`DownloadFile`/`StreamChunked` 已实现并通过
> 测试，可用于调用其他服务的这类路由，但这**不代表服务端端到端支持**。
>
> 将来实现服务端流式能力时，以下验收点必须同时满足（当前不在范围内）：
>
> - 客户端取消时服务端写流能够退出；
> - 服务端错误不会在已经开始写流后尝试写第二个 JSON Envelope；
> - `WriteTimeout` 与长连接配置有明确约定；
> - Trace Span、RequestID 和 AccessLog 能记录流式请求的结束原因与持续时间；
> - Download 的 `Content-Disposition`、文件名和 `Content-Type` 有明确安全策略。

契约侧：

```go
var WatchOrders = endpoint.New[WatchOrdersRequest, endpoint.NoBody](endpoint.Spec{
	OperationID:         "WatchOrders",
	Method:              "GET",
	Path:                "/orders/watch",
	Summary:             "订阅订单变更",
	Tags:                []string{"demoservice"},
	ResponseKind:        endpoint.KindSSE,
	ResponseContentType: "text/event-stream",
})

var DownloadInvoice = endpoint.New[DownloadInvoiceRequest, endpoint.NoBody](endpoint.Spec{
	OperationID:         "DownloadInvoice",
	Method:              "GET",
	Path:                "/orders/{id}/invoice",
	Summary:             "下载发票",
	Tags:                []string{"demoservice"},
	ResponseKind:        endpoint.KindDownload,
	ResponseContentType: "application/pdf",
})
```

**OpenAPI：**

- SSE：`200`，`content: text/event-stream`，schema `type: string`。
- Download：`200`，`content: application/octet-stream`（或声明类型），`headers: Content-Disposition`。
- Chunked：`200`，`content: application/octet-stream`，schema `type: string, format: binary`。

### 5.6 启动期校验清单

`BindAll` / `config.Validate` 失败即启动失败，错误信息包含 `OperationID` 和原因：

1. 路由方法不在允许列表。
2. 路径不以 `/` 开头。
3. `OperationID` 为空或重复。
4. 同模块 `(Method, Path)` 重复。
5. `NoContent` 与 `ResponseKind` 同时设置。
6. `ResponseKind != ""` 但 `ResponseType` 不是 `endpoint.NoBody`。
7. `impl` 缺少方法。
8. 方法签名不符（参数个数、参数类型、返回类型）。
9. 路径占位符在 Request 的 `uri` tag 中不存在。
10. `Spec.Path` 中的占位符重复或为空。
11. `RequestContentType=multipart/form-data` 但 Request 没有文件字段（警告，不阻止）。
12. 中间件别名（含认证 `"auth"`）既不在 `BindOptions.Middleware`、`BindOptions.Registry`
    也未注册到 App 注册表，或解析结果为 `nil`：分组名由 `BindGroup` 报（错误带
    分组前缀），路由名由 `BindAll` 报（错误带 `OperationID`）。
13. （2026-11-19 删除）认证声明校验随 `AuthValidator` 一并移除：它只比对名字，
    证明不了实现会拒绝匿名。现在没有认证声明，也就不存在"声明与实现不一致"这一
    启动失败项。

---

## 6. 客户端 SDK

### 6.1 设计目标

- 调用方只看到强类型方法，不手写 URL/JSON。
- 自动处理信封、业务码、错误详情、`req_id`。
- 自动替换路径参数、编码 query、序列化 body。
- 默认只重试幂等请求；支持超时、上下文取消。
- 不依赖 gofy，可用于任何 Go 服务。
- 支持 SSE、下载、分块。
- 为服务发现留接口，不绑定 Nacos。

### 6.2 Client API

```go
package client

import (
	"net/http"
	"time"
)

type Config struct {
	// BaseURL 固定地址，如 http://order-service:8080；为空时使用 Resolver。
	BaseURL string

	// Service 是服务名，Resolver 模式下必填。
	Service string

	// HTTPClient 可注入自定义 Transport；为空时使用安全默认值。
	HTTPClient *http.Client

	// Timeout 是单次请求超时；默认 30s。SSE/下载不受此限制，使用 Context。
	Timeout time.Duration

	// MaxResponseBytes 限制普通 JSON 响应体大小；默认 10 MiB。
	MaxResponseBytes int64

	// Retry 是幂等请求的最大重试次数；默认 1（总共 2 次）。
	Retry int

	// Resolver 是服务发现接口；BaseURL 为空时使用。
	Resolver Resolver

	// TokenSource 返回 Bearer Token；为空则不设置 Authorization。
	TokenSource func(ctx context.Context) (string, error)

	// UserAgent 默认 gofy-interface-service/<version>。
	UserAgent string

	// RequestIDHeader 默认 X-Trace-ID。
	RequestIDHeader string
}

type Client struct {
	// 内部字段
}

func New(cfg Config) (*Client, error)

// Call 是泛型调用入口。
func Call[Req, Res any](
	ctx context.Context,
	c *Client,
	rt *endpoint.Route[Req, Res],
	req *Req,
	opts ...CallOption,
) (*Res, error)

type CallOption func(*callOptions)

func WithHeader(key, value string) CallOption
func WithHeaders(h http.Header) CallOption
func WithBearerToken(token string) CallOption
func WithRequestID(id string) CallOption
func WithQuery(key, value string) CallOption
func WithTimeout(d time.Duration) CallOption
func WithConnectTimeout(d time.Duration) CallOption
func WithHTTPClient(cl *http.Client) CallOption

// 请求头透传：context key 与 gofy/support/headerpropagation 保持一致。
const ContextKey = "gofy.header_propagation.headers"

const (
	HeaderTraceID  = "X-Trace-Id"
	HeaderUserID   = "X-User-Id"
	HeaderUserCode = "X-User-Code"
	HeaderTenantID = "X-Tenant-Id"
	HeaderUserName = "X-User-Name"
	HeaderClientIP = "X-Client-Ip"
	HeaderUserAgent = "X-User-Agent"

	DefaultPropagatedUserAgent = "gofy-api-sdk"
)

func ContextWithHeaders(ctx context.Context, headers http.Header) context.Context
func ContextWithHeader(ctx context.Context, name, value string) context.Context
func HeadersFromContext(ctx context.Context) http.Header
func HeaderFromContext(ctx context.Context, name string) string
```

**强类型 Client：**

```go
package demoserviceclient

import (
	"context"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderdto"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderroute"
	"github.com/bingcool/gofy-interface-service/apicore/client"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/paging"
)

type Client struct {
	c *client.Client
}

func New(c *client.Client) *Client {
	return &Client{c: c}
}

func (c *Client) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest, opts ...client.CallOption) (*orderresponse.GetOrderResponse, error) {
	return client.Call(ctx, c.c, orderroute.GetOrder, req, opts...)
}

func (c *Client) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest, opts ...client.CallOption) (*paging.ListData[orderdto.OrderDTO], error) {
	return client.Call(ctx, c.c, orderroute.ListOrders, req, opts...)
}

func (c *Client) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest, opts ...client.CallOption) (*orderresponse.CreateOrderResponse, error) {
	return client.Call(ctx, c.c, orderroute.CreateOrder, req, opts...)
}

func (c *Client) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest, opts ...client.CallOption) error {
	_, err := client.Call(ctx, c.c, orderroute.CancelOrder, req, opts...)
	return err
}

func (c *Client) GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest, opts ...client.CallOption) (*orderresponse.GetConfigResponse, error) {
	return client.Call(ctx, c.c, orderroute.GetConfig, req, opts...)
}
```

> 以上方法由 `apicore/cmd/gofy-client` 生成，文件头写 `// Code generated by gofy-client. DO NOT EDIT.`。手写等价代码也允许，但 CI 的 `-check` 会以生成结果为准。

### 6.3 请求构造规则

| 项 | 规则 |
| --- | --- |
| 路径 | 取 `Spec.Path`，对每个 `{name}` 用 Request 中 `uri:"name"` 字段替换；值 `url.PathEscape`；缺失字段返回配置错误 |
| Query | `GET/HEAD/DELETE/OPTIONS` 使用 `form` tag；slice 重复 key；指针非 nil 才发送；空字符串/零值默认跳过，`form:",omitempty"` 可显式控制 |
| JSON body | `POST/PUT/PATCH` 默认 `application/json`；`json.Marshal` |
| Multipart | `Spec.RequestContentType == multipart/form-data` 时，`*multipart.FileHeader`/`upload.File` 字段写文件 part，其他字段写表单 |
| Header | `header` tag；`CallOption` 覆盖 |
| Cookie | `cookie` tag；以 `Cookie` 头发送 |
| 透传头 | 从 ctx 的 `gofy.header_propagation.headers` 读取白名单；在 Request `header` tag 和 `CallOption` 之前写入，后两者可覆盖 |
| 认证 | `CallOption` > `Client.TokenSource` |
| 请求 ID | `CallOption` > 从 ctx 读取 > 不发送（由服务端生成） |
| Content-Type | 有 body 时设置；无 body 不设置 |
| Accept | JSON 默认 `application/json`；SSE `text/event-stream`；下载 `*/*` |
| User-Agent | 默认 `gofy-interface-service/<version>` |
| X-User-Agent | 透传上下文有值时沿用，否则默认 `gofy-api-sdk` |

**Path 参数示例：**

```go
type GetOrderRequest struct {
	ID types.Uint64String `uri:"id" json:"id"`
}
```

`/orders/{id}` -> `/orders/1234567890123456789`。

**请求头透传：** gofy 服务端启用 `WithHeaderPropagation(true)` 后，会在入口把白名单头
写入 request context（key `gofy.header_propagation.headers`，值为 `http.Header`）。
客户端把 handler 的 `ctx` 原样传给 `Call` 即可，不需要额外参数：

```go
res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: 42})
```

- 白名单：`X-Trace-Id`、`X-User-Id`、`X-User-Code`、`X-Tenant-Id`、
  `X-User-Name`、`X-Client-Ip`、`X-User-Agent`；其余头（包括 `Authorization`、
  `Cookie`）即使写入 context 也会被 `ContextWithHeaders` / `HeadersFromContext` 过滤；
- 身份头是否可信由服务端决定：只有 `WithTrustedProxies` 命中的代理传来的
  `X-User-*` / `X-Tenant-Id` 才会进入 context，客户端只负责转发；
- 合并顺序保证可覆盖：单次调用用 `client.WithHeader` 可以改掉透传值；
- 人工补充身份头：`ctx = client.ContextWithHeader(ctx, client.HeaderUserID, id)`；
- 网关应用用 `JWTAuth` 校验后，gofy 的 `support/frameworkcontext` 会把本地身份
  回写到同一组透传头，下游客户端无需感知认证方式；
- 客户端不 import gofy，服务端也不 import 本仓库，唯一的耦合是固定字符串 key，
  修改时必须两个仓库同时发布。

### 6.4 响应解析与错误

**成功：**

1. 读取响应体，限制 `MaxResponseBytes`。
2. 2xx 且 body 非空：解析信封。
3. `code == 0`：把 `data` 反序列化为 `*Res`。`data` 为 `null` 时返回 `nil, nil`（NoContent 除外）。
4. 2xx 且 body 为空：如果 `NoContent`，返回 `nil, nil`；否则返回 `ProtocolError`。

**错误：**

```go
type APIError struct {
	Status  int
	Code    int
	Message string
	ReqID   string
	Details map[string]string

	Raw []byte
}

func (e *APIError) Error() string
func CodeOf(err error) int
func RequestIDOf(err error) string
```

| 场景 | 返回 |
| --- | --- |
| 非 2xx，body 是合法信封 | `*APIError`（带 HTTP status、业务 code、details、req_id） |
| 非 2xx，body 非 JSON | `*HTTPError{Status, BodyPrefix}` |
| 2xx，code != 0 | `*APIError` |
| 2xx，JSON 非法 | `*ProtocolError` |
| 网络错误/超时 | 原始 error（可用 `errors.Is` 判断 `context.DeadlineExceeded`） |

**规则：**

- 客户端以 `code` 为准，不以 HTTP status 为准；HTTP status 仅用于传输层判断。
- 未知字段忽略；契约新增字段不得导致旧客户端失败。
- `details` 用于字段级校验错误，如 `{"user_id":"用户ID不能为空"}`；同一文案也会出现在信封 `msg` 中（多字段失败用 `; ` 拼接），客户端直接展示 `msg` 即可。

### 6.5 重试、超时、幂等

| 方法 | 默认重试 |
| --- | --- |
| `GET` / `HEAD` / `OPTIONS` | 1 次 |
| `DELETE` | 1 次（业务需保证幂等） |
| `POST` / `PUT` / `PATCH` | 0 次 |

**可重试错误：**

- 连接错误、DNS 错误、连接重置。
- `502`、`503`、`504`。
- `429` 且带 `Retry-After`（不超过 3 秒）。
- 不重试：`4xx`（除 429）、业务 `code != 0`、请求体已部分发送且无法重放。

**退避：**

```text
delay = min(2^attempt * 100ms, 2s) + jitter(0~100ms)
```

**超时与单次覆盖：**

- `Client.Timeout` 控制 `Call` 的总超时；`CallOption.WithTimeout` 覆盖单次 `Call`。
- `CallOption.WithConnectTimeout` 只限制 TCP 建连：克隆配置的 `*http.Transport` 并替换 `DialContext`，相同值缓存复用连接池；不改变 TLS 握手超时。配置的 `*http.Client` 必须使用 `*http.Transport`（未设置 Transport 时按 `http.DefaultTransport` 处理），否则调用返回错误。
- `CallOption.WithHTTPClient` 用传入的 `*http.Client` 执行本次调用，优先级高于 `WithConnectTimeout`，用于单接口的 TLS、代理或埋点需求。
- SSE/下载/分块使用调用方 `context`，不设总超时；`WithConnectTimeout`/`WithHeader`/`WithHTTPClient` 对流式调用同样生效。

### 6.6 SSE、下载、分块

```go
// SSE：返回事件通道；ctx 取消时关闭。
type Event struct {
	Event string
	ID    string
	Data  []byte
	Retry int
}

func StreamSSE[Req any](
	ctx context.Context,
	c *Client,
	rt *endpoint.Route[Req, endpoint.NoBody],
	req *Req,
	opts ...CallOption,
) (<-chan Event, <-chan error, error)

// 下载：返回流和文件名；调用方负责关闭。
type Download struct {
	Body        io.ReadCloser
	Filename    string
	ContentType string
	Size        int64
}

func DownloadFile[Req any](
	ctx context.Context,
	c *Client,
	rt *endpoint.Route[Req, endpoint.NoBody],
	req *Req,
	opts ...CallOption,
) (*Download, error)

// 分块：返回带缓冲的 reader。
func StreamChunked[Req any](...) (io.ReadCloser, error)
```

**SSE 解析规则：**

- 按行解析 `event:`、`id:`、`data:`、`retry:`；空行分发事件。
- 多行 `data:` 用 `\n` 拼接。
- 注释行 `:` 忽略。
- 服务端 `heartbeat` 注释不产生事件。
- 连接断开且 ctx 未取消：按 `Retry` 配置重连（默认 1 次），从 `Last-Event-ID` 续传。
  服务端正常返回（EOF）与传输中断同等处理；重试预算用尽后向错误 channel 投递
  `ErrStreamEnded`，不再静默关闭。
- 三个流式 helper 只接受对应 `ResponseKind` 的路由，接错在发请求前返回
  `ErrResponseKindMismatch`。2xx 还校验 `Content-Type`：SSE 必须是
  `text/event-stream`；下载/分块在声明 `ResponseContentType` 时精确匹配
  （支持 `*/*`、`type/*` 通配），未声明时拒绝 JSON 类型，避免错误信封被当成数据。

### 6.7 服务发现

```go
type Resolver interface {
	Resolve(ctx context.Context, service string) (baseURL string, err error)
}

type StaticResolver struct {
	BaseURL string
}

func (r StaticResolver) Resolve(context.Context, string) (string, error) {
	return r.BaseURL, nil
}
```

**规则：**

- `Config.BaseURL` 非空时优先，不使用 Resolver；固定模式下重试退避后请求同一地址。
- 生成客户端位于 `appinterface/{domain}service/{domain}serviceclient`，提供
  `NewWithBaseURL`（域名或 IP，缺省 scheme 补 http）、`NewWithResolver`（服务发现）
  和 `New(*client.Client)`（自定义 Client），对应 interface-api-service
  `makeService()` 的三种模式。
- Resolver 结果按 `Service` 缓存，默认 TTL 30s。
- 连接错误时主动失效缓存，并在下一次重试前重新解析（`send` 在循环内取 base URL），
  因此重试可能落到另一个实例。
- 内置 `StaticResolver`（固定地址）与 `apicore/client/nacos`（Nacos 命名服务发现）。
- `apicore/client/nacos.Resolver` 调用 `instance/list`，按 `cache_ttl` 缓存实例列表；Nacos
  不可达时在 `stale_ttl` 内继续使用最后一次成功结果。负载均衡在 Resolver 内完成：
  `random`（默认）、`round_robin`、`weight`。
- 注册信息（`service_register` / `discovery_service_client`）由
  `github.com/bingcool/gofy/support/nacos` 解析；客户端通过
  `demoserviceclient.NewWithResolver` 使用 `demoservice.ServiceName`（`demo-service`）。

### 6.8 生成强类型客户端

`apicore/cmd/gofy-client`：

1. 用 `golang.org/x/tools/go/packages` 加载 `./apicore/contracts/...`。
2. 找到每个模块的 `Service` 接口和 `Routes()` 返回的路由描述符。
3. 按 `OperationID` 匹配接口方法和路由。
4. 生成到 `appinterface/{domain}service/{domain}serviceclient/`（不带版本目录，例如 `appinterface/demoservice/demoserviceclient/`）：`client.go` 放 `Client` 类型和三种寻址方式，每个路由文件生成一个 `*_client.go`（`order_route.go` → `order_client.go`、`config_route.go` → `config_client.go`）。
5. 文件头：`// Code generated by gofy-client. DO NOT EDIT.`
6. `-check` 模式只校验生成物是否最新，不写文件。

**生成规则：**

| 契约 | 生成方法 |
| --- | --- |
| JSON | `return client.Call(ctx, c.c, contract.X, req)` |
| NoContent | `_, err := client.Call(...); return err` |
| SSE | `return client.StreamSSE(ctx, c.c, contract.X, req)` |
| Download | `return client.DownloadFile(ctx, c.c, contract.X, req)` |
| Chunked | `return client.StreamChunked(ctx, c.c, contract.X, req)` |

**失败即停：**

- `OperationID` 在 `Routes()` 中不存在，或接口方法没有对应路由。
- 接口方法签名与路由 `Req/Res` 不匹配。
- 同一 `OperationID` 出现多次。
- 生成目录已存在手写文件（非 generated 标记）且内容不同。

> **实现状态（v0.1）：** `apicore/cmd/gofy-client` 尚未实现。`appinterface/demoservice/demoserviceclient/` 目前是按相同规则手写的等价包装
> （每个方法一行 `client.Call`，并提供 `IsOrderNotFound`/`IsOrderClosed` 便捷判断）。
> 通用 `client.Call[Req,Res]`/`StreamSSE`/`DownloadFile`/`StreamChunked` 均已可直接使用；
> 后续加入生成器时，手写文件需带 generated 标记或迁移到生成目录。

---

## 7. OpenAPI / Swagger

### 7.1 方案对比与选型

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| swaggo/swag 注解 | 生态成熟、Swagger UI 现成 | 注解与契约重复；泛型 handler 支持弱；字段文档要写两遍 | 不采用 |
| Huma | 类型驱动、运行时生成 OpenAPI 3.1 | 与 gofy/gin 绑定方式冲突；引入新的 handler 模型 | 不采用 |
| go-swagger | 结构体注释生成 | 需要大量注释；与泛型不兼容 | 不采用 |
| **自研反射式生成器** | 契约单一事实来源；与 `endpoint.Route` 完全一致；可 golden diff | 需要自己实现 schema 映射；泛型命名要稳定 | **采用** |
| kin-openapi 仅做校验 | 稳定、成熟 | 不负责从 Go 类型生成 | 作为校验器 |

**决策：** 自研 `docgen`，输出 OpenAPI **3.0.3**（Swagger UI、代码生成器兼容性最好），用 `kin-openapi` 做加载与校验。

### 7.2 生成器架构

```text
apicore/cmd/gofy-openapi
  -> appinterface.Routes()          // 显式聚合
  -> docgen.Build(options)           // 纯函数
       -> operation.Build(route)     // 路径、参数、body、响应
       -> schema.ForType(req/res)    // 递归类型
       -> constraints.FromTags()     // validate/default/doc/enum
       -> naming.StableName()        // 组件名
  -> YAML / JSON 输出
  -> kin-openapi Validate
```

**输入：**

- `[]endpoint.Descriptor`。
- 全局配置：`title`、`version`、`description`、`servers`、`securitySchemes`。
- 路由过滤：`-service demoservice` 只生成该模块，`-all` 生成全量。
- 部署前缀映射：`-prefix demoservice=/api/v1/order`，用配置值替换描述符
  `Spec.Prefix`（`Group.Add` 写入的分组前缀）并保留组内剩余路径，便于按另一
  基址发布文档。
- 输出目录。

**CLI：**

```bash
# 生成全量
go run ./apicore/cmd/gofy-openapi -all -out ./openapi

# 只生成订单模块，并声明部署前缀
go run ./apicore/cmd/gofy-openapi -service demoservice -prefix demoservice=/api/v1/order -out ./openapi

# 校验已有规范
go run ./apicore/cmd/gofy-openapi -validate ./openapi/demoservice.yaml

# 本地预览（内嵌 Swagger UI）
go run ./apicore/cmd/gofy-openapi -all -serve :8090
```

**输出：**

- `openapi/demoservice.yaml`
- `openapi/demoservice.json`
- 可选 `openapi/all.yaml`（多模块聚合）。

### 7.3 Schema 映射规则

| Go 类型 | OpenAPI |
| --- | --- |
| `string` | `type: string` |
| `bool` | `type: boolean` |
| `int` / `int32` | `type: integer`, `format: int32` |
| `int64` | `type: integer`, `format: int64` |
| `uint64` | `type: integer`, `format: int64`, `minimum: 0` |
| `float32` | `type: number`, `format: float` |
| `float64` | `type: number`, `format: double` |
| `time.Time` | `type: string`, `format: date-time` |
| `*T` | `T` + `nullable: true` |
| `[]T` | `type: array`, `items: T` |
| `[N]T` | `type: array`, `minItems/maxItems: N` |
| `map[string]T` | `type: object`, `additionalProperties: T` |
| 匿名嵌入 struct | 展开字段（与 JSON 行为一致） |
| `json:"-"` | 跳过 |
| `types.Uint64String` | `type: string`, `format: uint64` |
| `types.Date` | `type: string`, `format: date` |
| `types.Decimal` | `type: string`, `format: decimal` |
| `endpoint.NoBody` | 无 schema |
| 实现 `OpenAPIType() (typ, format string)` | 使用返回值 |

**必填规则：**

1. 字段带 `validate:"required"` -> 加入 `required`。
2. 非指针字段且没有 `omitempty` -> 默认必填（与 Go JSON 行为一致）。
3. 指针字段默认非必填。
4. 嵌入的 `PageRequest` 中 `default` 字段仍按必填处理（`page`/`page_size` 有默认值，但客户端应显式传；文档中可标 required=false + default）。

**默认值：**

- `default` tag 解析为 JSON 标量；解析失败则忽略并在生成日志中警告。

**枚举：**

- `enum:"a,b,c"` -> `enum: [a,b,c]`。
- `enumLabels:"1=微信,2=支付宝"` -> `x-enum-labels: {"1":"微信","2":"支付宝"}`。
- `enum` tag 为空但 `validate` 含 `oneof=` -> 从 `oneof` 提取。

**描述与示例：**

- `doc` -> `description`；缺失时用字段名。
- `example` -> `example`；尝试按字段类型解析，失败则按字符串。

**递归与复用：**

- 组件名稳定算法：
  ```text
  schemaName(t):
    基础名 = PascalCase(包名去掉 apicore/contracts/ 前缀) + PascalCase(类型名)
    泛型实例 = 基础名 + "Of" + 每个类型参数名，用 "And" 连接
    非法字符替换为 "_"
    冲突时追加 8 位 FNV hash
  ```
- 示例：`appinterface/demoservice/orderdto.OrderDTO` -> `DemoserviceOrderDTO`；`common/paging.ListData[orderdto.OrderDTO]` -> `CommonPagingListDataOfDemoserviceOrderDTO`。嵌套子包不参与前缀，服务目录是唯一锚点。
- **注意：** 实例化泛型的 `reflect.Type.Name()` 形如 `ListData[main.Order]`，包含类型参数和包路径，不能直接当组件名。`naming.StableName` 必须递归拆解类型参数、去掉包路径、做 PascalCase 与字符清洗，再拼接。
- 用 `seen map[reflect.Type]string` 处理递归；已见类型输出 `$ref`。
- 组件按名称排序输出，保证确定性。

**不支持的 Go 构造（生成失败并给出文件/类型）：**

- `interface{}` / `any` 作为字段类型（要求明确类型）。
- 函数、channel、complex。
- 非字符串 key 的 map。
- 匿名 struct（要求命名类型）。
- 带循环且无法通过 `$ref` 表达的值类型（理论上都能表达，但需测试）。

### 7.4 Operation 生成规则

| OpenAPI 字段 | 来源 |
| --- | --- |
| `operationId` | `Spec.OperationID` |
| `summary` | `Spec.Summary`，缺失用 `Method + " " + Path` |
| `description` | `Spec.Description` |
| `tags` | `Spec.Tags` |
| `deprecated` | `Spec.Deprecated` |
| `security` | 不生成：认证由中间件安装，docgen 看不到 |
| `parameters` | Path（`uri`）、Query（`form`，仅 GET/HEAD/DELETE）、Header（`header`）、Cookie（`cookie`） |
| `requestBody` | POST/PUT/PATCH 的 JSON 或 multipart |
| `responses.200` | JSON：`Envelope[Res]`；NoContent：无 body；SSE/Download/Chunked：对应 content |
| `responses.400` | 参数校验失败信封 |
| `responses.401` | 有鉴权时 |
| `responses.403` | 声明了 403 错误时 |
| `responses.404/409/...` | `Spec.Errors` |
| `responses.429` | 有 `RateLimit` 时 |
| `responses.500` | 始终添加 |

**Path 参数：**

```yaml
parameters:
  - name: id
    in: path
    required: true
    schema:
      type: string
      format: uint64
```

**Query 参数：**

- 标量直接展开。
- slice：`style: form`, `explode: true`。
- 嵌套 struct：生成失败，要求拆成标量。
- `PageRequest` 嵌入字段展开为 `page`、`page_size`。

**请求体：**

```yaml
requestBody:
  required: true
  content:
    application/json:
      schema:
        $ref: '#/components/schemas/DemoserviceCreateOrderRequest'
```

**成功响应（JSON）：**

```yaml
responses:
  "200":
    description: OK
    content:
      application/json:
        schema:
          allOf:
            - $ref: '#/components/schemas/CommonEnvelope'
            - type: object
              properties:
                data:
                  $ref: '#/components/schemas/DemoserviceGetOrderResponse'
```

> 为避免泛型组件名不稳定，成功信封不生成 `EnvelopeOfX` 组件，而是用 `allOf` 内联 `data`。`CommonEnvelope` 只声明 `code/msg/req_id/details`。

### 7.5 错误响应

**统一错误 schema：**

```yaml
CommonErrorEnvelope:
  type: object
  required: [code, msg, data]
  properties:
    code:
      type: integer
      example: 40401
    msg:
      type: string
      example: order not found
    data:
      nullable: true
      example: null
    req_id:
      type: string
      example: 6f1c0b1e-7d2a-4b9c-8e1f-2a3b4c5d6e7f
    details:
      type: object
      additionalProperties:
        type: string
```

**按状态码生成示例：**

```yaml
"404":
  description: order not found
  content:
    application/json:
      schema:
        $ref: '#/components/schemas/CommonErrorEnvelope'
      example:
        code: 40401
        msg: order not found
        data: null
        req_id: 6f1c0b1e-7d2a-4b9c-8e1f-2a3b4c5d6e7f
```

**规则：**

- 同一状态码多个错误时，`description` 用 `; ` 连接，`examples` 列出每个业务码。
- 未声明的状态码不生成（500 除外）。
- `401` 的示例 code 为 `40100`；`429` 为 `42900`。

### 7.6 输出与确定性

1. 路由按 `Path` 字典序、再按 `Method` 排序。
2. 组件按名称排序。
3. 字段顺序保持 struct 声明顺序（便于 review）。
4. YAML 使用 2 空格缩进，不输出时间戳、生成器版本等易变信息。
5. 每次生成前删除旧文件？**不删除**，由 CI diff 判断；本地覆盖写。
6. 输出文件头加注释：

```yaml
# Code generated by gofy-openapi. DO NOT EDIT.
```

7. JSON 输出用于程序消费；YAML 用于 review 和 Swagger UI。

### 7.7 Swagger UI

`docsui` 包：

```go
package docsui

import "net/http"

type Config struct {
	Title      string
	Spec       []byte
	SpecPath   string // 默认 /openapi.json
	DocsPath   string // 默认 /docs
	Enabled    bool
	BasicAuth  *BasicAuth // 可选
}

type BasicAuth struct {
	Username string
	Password string
}

func Handler(cfg Config) (http.Handler, error)
```

实现：

- 使用 `github.com/swaggest/swgui/v5emb` 内嵌 Swagger UI 5 资源，不依赖 CDN。
- `/docs` 返回 Swagger UI HTML；`/openapi.json` 返回规范。
- `Enabled=false` 时返回 404 handler（零值即关闭）；`Spec` 为空时返回错误。
- `DocsPath`/`SpecPath` 会自动归一化（补前导 `/`、去尾部 `/`），两者相同则返回错误。
- `/docs` 会重写到 `/docs/`，保证 UI 相对资源路径正确；`/docs/*` 由同一 handler 提供。
- 支持 `BasicAuth`（常量时间比较，认证通过后剥离 `Authorization` 头再交给 UI）；生产环境建议直接不挂载，或放在内网网关后。
- 不修改 CSP；如果服务启用了严格 CSP，需要为 `/docs` 放行内联脚本/样式。

**业务服务挂载：**

```go
docs, err := docsui.Handler(docsui.Config{
	Title:   "Order Service API",
	Spec:    openapi.DemoServiceYAML, // //go:embed 的字节
	Enabled: true,
})
if err != nil {
	return err
}

mux := http.NewServeMux()
mux.Handle("/docs", docs)
mux.Handle("/docs/", docs)
mux.Handle("/openapi.json", docs)
```

作为 gofy 模块（放在 `apicore/adapter/gofy/docs.go`，避免 `docsui` 依赖 gin/gofy）。文档需要双重显式开启：
既注册模块，又设置 `cfg.Enabled = true`；零值配置不会发布任何文档路由，生产环境只需关掉这一个开关：

```go
package gofyadapter

import (
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy-interface-service/docsui"
	"github.com/gin-gonic/gin"
)

type docsModule struct {
	cfg docsui.Config
}

func DocsModule(cfg docsui.Config) support.Module {
	return &docsModule{cfg: cfg}
}

func (m *docsModule) Name() string { return "docs" }

func (m *docsModule) Register(r *support.Router) error {
	if !m.cfg.Enabled {
		return nil
	}
	h, err := docsui.Handler(m.cfg)
	if err != nil {
		return err
	}
	docsPath := m.cfg.EffectiveDocsPath()
	r.GET(docsPath, gin.WrapH(h))
	if docsPath != "/" {
		r.GET(docsPath+"/*any", gin.WrapH(h))
	}
	r.GET(m.cfg.EffectiveSpecPath(), gin.WrapH(h))
	return nil
}
```

> 文档模块只应在非生产环境启用；用 `Runtime.Env` 决定 `Enabled`，或由 `main.go` 决定是否 `WithModules`。

### 7.8 CI 校验

```bash
# 1. 生成
go run ./apicore/cmd/gofy-openapi -all -out ./openapi

# 2. 校验 YAML/JSON 合法、无未解析 $ref
go run ./apicore/cmd/gofy-openapi -validate ./openapi/demoservice.yaml

# 3. 确认生成物与仓库一致
git diff --exit-code -- openapi/

# 4. 契约引用边界
go run ./apicore/cmd/refcheck

# 5. 客户端生成物一致
go run ./apicore/cmd/gofy-client -all -check
```

**可选：** 用 `oasdiff` 做破坏性变更检测：

```bash
oasdiff breaking openapi/demoservice.base.yaml openapi/demoservice.yaml
```

v1 至少做生成物 diff；破坏性检测放 P3。

---

## 8. gofy 框架 HTTP 能力改造

### 8.1 总览

本节是把第 2 节标记为 P1/P2/P3 的能力落到 gofy 代码的设计。原则：

- 新能力默认不改变现有 API 行为。
- 新包不引入重依赖；Redis、OTel、Swagger 放独立模块。
- 每个能力有单元测试 + 至少一个 httptest 集成测试。
- 文档同步更新 `docs/library-design.md`。

### 8.2 After 中间件与路由选项

**After 中间件：**

```go
// After 注册在 handler 返回后、响应渲染前执行的中间件。
// 与普通中间件不同，After 不包裹 handler，而是由 dispatch 在 handler 之后调用。
func (r *Router) After(mw ...AfterMiddleware)

type AfterMiddleware func(ctx *AfterContext)

type AfterContext struct {
	C       *gin.Context
	Result  any
	Err     error
	Elapsed time.Duration
}
```

实现要点：

- `Router` 增加 `after []AfterMiddleware`，`Group` 继承父级。
- 泛型注册函数在 handler 返回、Responder 调用前执行 `after`。
- `AfterMiddleware` 不能修改响应体，只能记录/观测；如需改写结果，使用普通中间件或 handler 包装。
- `recover` 后也执行，`Err` 为 panic 转换后的错误。

**路由选项：**

```go
type RouteConfig struct {
	DBDebug       bool
	OpenTelemetry bool
	RateLimit     *RateLimitConfig
}

func Route[Req, Res any](
	r *Router,
	method, path string,
	h Handler[Req, Res],
	cfg RouteConfig,
	mw ...Middleware,
)
```

- `DBDebug` 写入 `gin.Context` / `context.Context`，由 GORM logger 读取。
- `OpenTelemetry` 由 OTel 中间件读取。
- `RateLimit` 由限流中间件读取；与契约 `Spec.RateLimit` 合并，显式配置优先。
- 不实现 route meta cache。

### 8.3 请求绑定扩展

**Header/Cookie 绑定：**

```go
func (e *engine) bindAndValidate(c *gin.Context, req any) error {
	if err := defaults.Set(req); err != nil { ... }
	if err := c.ShouldBindUri(req); err != nil { ... }
	if err := c.ShouldBindHeader(req); err != nil { ... }
	if err := c.ShouldBind(req); err != nil { ... }
	if err := e.validator.Struct(req); err != nil { ... }
	return nil
}
```

注意：

- `ShouldBind` 对 GET 使用 query，对 POST 使用 body；`ShouldBindHeader` 额外绑定 `header` tag。
- Cookie 绑定不通过 `ShouldBind`；新增 `support.BindCookies(c, req)` 或让契约用 `header:"Cookie"` 手动解析。v1 建议 Cookie 用 `c.Cookie(name)`，不进入 DTO。
- 重复绑定同一字段时，后绑定的覆盖前者；文档约定不同来源使用不同字段。

**Raw Body：**

```go
func RawBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	return io.ReadAll(c.Request.Body)
}
```

- 仅在 handler 内调用；绑定器已读过 body 时返回空，需在绑定前使用 `c.GetRawData()` 缓存。

### 8.4 上传

```go
package upload

type File struct {
	Header *multipart.FileHeader
}

type Options struct {
	MaxBytes    int64
	AllowedMIME []string
	AllowedExt  []string
	Dir         string
	Perm        os.FileMode
}

func New(header *multipart.FileHeader) *File
func (f *File) Open() (multipart.File, error)
func (f *File) Size() int64
func (f *File) Filename() string
func (f *File) MIME() (string, error)
func (f *File) Ext() string
func (f *File) Validate(opts Options) error
func (f *File) Save(dir string) (path string, err error)
```

**安全要求：**

1. 文件名只保留 base，去除路径分隔符；生成随机文件名（保留扩展名）。
2. 目录必须存在且是目录；不自动 `MkdirAll` 任意路径。
3. 文件权限默认 `0600`；目录 `0700`。
4. `MaxBytes` 默认 8 MiB；与 `WithMaxBodyBytes` 共同生效。
5. MIME 检测优先读文件头（`mimetype.Detect`），不信任客户端 `Content-Type`。
6. 扩展名白名单与 MIME 白名单同时校验。
7. 拒绝 `..`、绝对路径、NUL 字节。
8. 保存后返回相对路径，不返回用户可控的原始文件名。

**绑定：**

```go
type UploadRequest struct {
	File *multipart.FileHeader `form:"file" validate:"required" doc:"文件"`
}
```

gin 的 multipart 绑定支持 `*multipart.FileHeader`；测试覆盖。

### 8.5 下载、重定向、原始响应

```go
package support

// Renderer 让 handler 返回值绕过 JSON 信封。
type Renderer interface {
	Render(c *gin.Context) error
}

type Redirect struct {
	URL    string
	Status int // 默认 302
}

type Raw struct {
	ContentType string
	Body        []byte
}

type Download struct {
	Reader      io.ReadCloser
	Filename    string
	ContentType string // 为空时用 application/octet-stream
	Inline      bool
	Size        int64 // 可选，用于 Content-Length
}
```

**Download.Render：**

1. 设置 `Content-Type`。
2. 设置 `Content-Disposition`：
   - `attachment; filename="safe.pdf"; filename*=UTF-8''...`（RFC 5987）。
   - `Inline` 时用 `inline`。
3. 设置 `Content-Length`（`Size > 0`）。
4. `io.Copy` 到 `c.Writer`。
5. 关闭 `Reader`（如果实现了 `io.Closer`）。

**安全：**

- 文件名去除路径和控制字符。
- 不把用户输入直接放入 header；使用 `mime.FormatMediaType`。
- 禁止目录遍历；下载源由业务控制。

### 8.6 SSE 与分块

**SSE：**

```go
package sse

type Stream struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

type Event struct {
	ID    string
	Event string
	Data  any
	Retry int
}

func New(w http.ResponseWriter) (*Stream, error)
func (s *Stream) Send(e Event) error
func (s *Stream) Comment(text string) error
func (s *Stream) Heartbeat() error
func (s *Stream) Close() error
```

**响应头：**

```text
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

**规则：**

- 每条消息以 `\n\n` 结束。
- `data` 为字符串时按行拆分；为对象时 JSON 序列化。
- 每次写后 `Flush`。
- 客户端断开时 `Write` 返回错误，handler 应退出。
- 服务端 `WriteTimeout` 必须为 0 或足够大；文档提示用 `WithServerConfigurator` 调整。
- 禁止在 SSE 路由使用 `AlwaysOK` 影响状态码；SSE 固定 200。

**分块：**

```go
package chunked

type Writer struct {
	w http.ResponseWriter
}

func New(w http.ResponseWriter, contentType string) (*Writer, error)
func (w *Writer) Write(p []byte) (int, error)
func (w *Writer) WriteLine(line string) error
func (w *Writer) WriteJSON(v any) error
func (w *Writer) WriteFile(path string, bufSize int) error
func (w *Writer) Close() error
```

**规则：**

- 不设置 `Content-Length`；由 net/http 使用 chunked transfer。
- `WriteFile` 分块读取，默认 2 MiB。
- 写失败立即返回；不吞错误。

### 8.7 安全响应头

```go
package middleware

type SecurityHeadersOptions struct {
	ContentTypeNosniff    bool   // 默认 true
	FrameOptions          string // 默认 "DENY"
	ReferrerPolicy        string // 默认 "no-referrer"
	PermissionsPolicy     string // 默认 "geolocation=(), microphone=(), camera=()"
	ContentSecurityPolicy string // 默认空，不覆盖业务
	HSTSMaxAge            time.Duration // 默认 0，仅 TLS 生效
	HSTSIncludeSubdomains bool
	HSTSPreload           bool
	SkipPaths             []string
}

func SecurityHeaders(opts SecurityHeadersOptions) gin.HandlerFunc
```

**规则：**

- `HSTS` 只在 `c.Request.TLS != nil` 且 `MaxAge > 0` 时设置。
- CSP 默认不设置，避免破坏业务页面；需要时显式配置。
- 不覆盖业务已设置的同名 header（用 `Header().Set` 还是 `Add`？统一 `Set`，文档说明）。
- `SkipPaths` 支持 `routematch.Matcher`。

### 8.8 鉴权

```go
package auth

type Principal struct {
	Subject string
	Claims  map[string]any
}

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

type Options struct {
	Header      string // 默认 Authorization
	Prefix      string // 默认 "Bearer "
	Optional    bool
	SkipPaths   []string
	ContextKey  string // 默认 "gofy.principal"
}

func Bearer(a Authenticator, opts Options) support.MiddlewareFunc
func OptionalBearer(a Authenticator, opts Options) support.MiddlewareFunc
func PrincipalFrom(c *gin.Context) (Principal, bool)
```

**规则：**

- `Authenticator` 返回 `apperr.Unauthorized` 或自定义错误；中间件直接交给 Responder。
- `OptionalBearer` 无 token 时放行，`Principal` 为零值；token 非法仍返回 401。
- 不在核心实现 JWT；业务提供 `Authenticator`，或使用独立 `gofy-jwt` 模块。
- Principal 写入 `gin.Context` 和 `request.Context()`，便于 service 层读取。

### 8.9 Locale

```go
package middleware

type LocaleOptions struct {
	QueryParam   string   // 默认 "lang"
	CookieName   string   // 默认 "lang"
	HeaderName   string   // 默认 "Accept-Language"
	Default      string   // 默认 "zh-CN"
	Supported    []string // 为空表示不限制
}

func Locale(opts LocaleOptions) gin.HandlerFunc
func LocaleFrom(c *gin.Context) string
```

**协商顺序：**

1. Query `?lang=`
2. Cookie `lang`
3. `Accept-Language`（按 q 值排序）
4. `X-Locale`（可选）
5. `Default`

**规则：**

- 只做协商，不加载翻译文件。
- 支持 `zh-CN`、`zh`、`en-US` 的降级匹配。
- 不在响应中自动设置 `Content-Language`；业务可选。

### 8.10 限流

```go
package ratelimit

type Decision struct {
	Allowed   bool
	Limit     int
	Remaining int
	ResetAt   time.Time
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error)
}

type MemoryLimiter struct {
	// 滑动窗口计数
}

func NewMemoryLimiter() *MemoryLimiter
```

**中间件：**

```go
package middleware

type RateLimitOptions struct {
	Limiter   ratelimit.Limiter
	Limit     int
	Window    time.Duration
	KeyFunc   func(*gin.Context) string
	SkipPaths []string
	Headers   bool // 默认 true
}

func RateLimit(opts RateLimitOptions) gin.HandlerFunc
```

**响应头：**

```text
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 42
X-RateLimit-Reset: 1730000000
Retry-After: 30
```

**规则：**

- 超限返回 `apperr.TooManyRequests("rate limit exceeded")`，HTTP 429。
- 内存实现使用分片锁 + 滑动窗口，避免全局锁。
- 多实例部署必须使用 Redis 等共享存储；核心只提供接口。
- `KeyFunc` 默认 `c.ClientIP()`；用户级用 `auth.PrincipalFrom(c).Subject`。
- 跳过健康检查和文档路径。

### 8.11 健康检查

```go
package health

type Check interface {
	Name() string
	Check(ctx context.Context) error
}

type Registry struct {
	// 注册 liveness / readiness 检查
}

type Options struct {
	Timeout        time.Duration
	IncludeDetails bool
	LivenessPaths  []string
	ReadinessPaths []string
}

func NewRegistry(opts Options) *Registry
func (r *Registry) AddLiveness(checks ...Check)
func (r *Registry) AddReadiness(checks ...Check)
func (r *Registry) Handler() http.Handler
```

**响应：**

```json
{
  "status": "ok",
  "checks": {
    "mysql": {"status": "ok", "latency_ms": 1.2}
  }
}
```

**规则：**

- 并发执行检查，总超时由 `Options.Timeout` 控制。
- 任一 readiness 检查失败 -> 503；liveness 只反映进程存活，不检查外部依赖。
- `IncludeDetails=false` 时错误信息只输出 `"failed"`，避免泄露 DSN/地址。
- 路径别名：`/healthz`、`/livez`；`/readyz`、`/ready`。
- 内置 `CheckDB`（GORM `PingContext`）；Redis/存储检查由业务或独立模块提供。
- 与现有 `WithHealthEndpoints` 兼容：未注册 Registry 时保持旧行为。

### 8.12 日志脱敏

```go
package logger

type SanitizeOptions struct {
	Keys       []string // 默认 password/token/secret/authorization/cookie/access_token/refresh_token
	Mask       string   // 默认 "***"
	MaxDepth   int      // 默认 4
	MaxString  int      // 默认 1024
}

func Sanitize(value any, opts SanitizeOptions) any
func SanitizeFields(fields []zap.Field, opts SanitizeOptions) []zap.Field
```

**规则：**

- 大小写不敏感匹配 key。
- 支持 map、struct、slice；不修改原对象。
- 超长字符串截断。
- AccessLog 不记录 body；如需记录，必须经过 `Sanitize`。
- 不记录 `Authorization`、`Cookie`、`Set-Cookie`。

### 8.13 CORS 增强

扩展 `support.CORSConfig`：

```go
type CORSConfig struct {
	AllowOrigins        []string
	AllowOriginPatterns []string // 如 https://*.example.com
	AllowPaths          []string // 仅这些路径启用 CORS；为空表示全部
	AllowMethods        []string
	AllowHeaders        []string
	ExposeHeaders       []string
	AllowCredentials    bool
	MaxAge              time.Duration
}
```

**规则：**

- `AllowOriginPatterns` 使用 `path.Match` 的域名通配，禁止 `*` 与 credentials 组合（通配符不算精确 `*`，但需文档说明风险）。
- 预检统一返回 204，不进入业务 handler。
- `Vary: Origin` 始终设置。
- `AllowPaths` 用 `routematch.Matcher`；未匹配路径不处理 CORS。
- 保持现有 `Validate` 行为，新增字段做校验。

### 8.14 OpenTelemetry（P3）

```go
package otel

type Config struct {
	ServiceName string
	Tracer      trace.Tracer
	Propagator  propagation.TextMapPropagator
}

func Middleware(cfg Config) support.MiddlewareFunc
```

**规则：**

- 独立模块 `contrib/otel`，不进入 gofy 核心 `go.mod`。
- 从 `traceparent` 提取上下文；生成 span；把 trace id 写入 `X-Trace-ID` 响应头和日志字段。
- 不自动上报 metrics；只做 HTTP span。
- 契约仓库不依赖 OTel。

### 8.15 兼容性

- 新包全部是加法；现有 `support.GET/POST/...` 签名不变。
- `bindAndValidate` 增加 `ShouldBindHeader` 可能影响同一字段同时有 `header` 和 `form` tag 的场景；文档约定不混用。
- `WithAlwaysOK` 不影响 `NoContent`（保持 204）、SSE、下载。
- 现有 `WithHealthEndpoints` 行为保持：未使用新 Registry 时仍只做 DB ping。
- gofy 最低 Go 版本为 1.26；不引入 1.27 特性。

---

## 9. 其他服务接入

### 9.1 服务端接入步骤

以 order-service 为例：

**1. 添加依赖：**

```go
require github.com/bingcool/gofy-interface-service v0.1.0
```

本地联调：

```go
replace github.com/bingcool/gofy-interface-service => ../gofy-interface-service
```

**2. 删除本地重复 DTO：**

`app/module/order/dto/order.go` 删除，改用 `appinterface/demoservice/orderdto` 和 `apicore/contracts/common/paging`。

**3. 实现契约接口：**

```go
var _ demoservice.Service = (*Handler)(nil)
```

Handler 方法直接使用契约 Request/Response。

**4. 模块注册：**

```go
func (m *Module) Register(r *support.Router) error {
	// 应用层别名由 App 启动时的 WithMiddlewareRegistrar 注册进共享注册表；
	// 模块只绑定路由，认证与审计都通过契约声明的名字解析。
	opts := gofyadapter.BindOptions{
		RateLimit: m.rateLimitMiddleware,
	}
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, m.handler, opts); err != nil {
			return err
		}
	}
	return nil
}
```

**5. 错误映射：**

```go
return nil, gofyadapter.Error(demoservice.ErrOrderNotFound)
```

**6. 挂载文档（非生产）：**

```go
if rt.Env != "prd" {
	appOpts = append(appOpts, gofy.WithModules(gofyadapter.DocsModule(docsui.Config{
		Title:   "Order Service API",
		Spec:    openapi.DemoServiceYAML,
		Enabled: rt.Env != "prd",
	})))
}
```

**7. 验证：**

```bash
go test ./...
curl -s localhost:8080/docs
curl -s localhost:8080/openapi.json | jq '.paths | keys'
```

### 9.2 调用方接入步骤

```go
import (
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/apicore/client"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/types"
)

c, err := client.New(client.Config{
	BaseURL: "http://order-service:8080",
	Timeout: 5 * time.Second,
})
if err != nil {
	return err
}

orderClient := demoserviceclient.New(c)
resp, err := orderClient.GetOrder(ctx, &orderrequest.GetOrderRequest{
	ID: types.Uint64String(orderID),
})
if err != nil {
	if demoserviceclient.IsOrderNotFound(err) {
		// 处理不存在
	}
	return err
}
```

**调用方规则：**

- 不在调用方重复定义 Request/Response。
- 不在调用方拼 URL 或解析 JSON 信封。
- 不根据 HTTP status 判断业务错误；用 `client.CodeOf(err)`。
- 透传 `context` 和请求 ID。
- 对非幂等接口不要盲目重试；使用业务幂等键。

### 9.3 本地联调

多仓库联调推荐 `go.work`：

```text
go.work
  use (
      ./gofy
      ./gofy-interface-service
      ./order-service
  )
```

或单仓库 `replace`。**提交前移除 `replace`**，改用正式 tag，避免 CI 拉到本地路径。

### 9.4 PHP InterfaceApi 迁移映射

| PHP 概念 | Go 对应 | 说明 |
| --- | --- | --- |
| `*ApiInterface` | `Service` 接口 | 方法名 = `OperationID` |
| `#[RouteGroup(prefix)]` | `endpoint.NewRouteGroup(prefix, ...)` | `prefix` 写完整部署前缀，组内 `Spec.Path` 写相对路径 |
| `#[Route(method,path)]` | `endpoint.New(Spec{Method,Path})` | 方法/路径唯一 |
| `#[ApiController]` | 包注释 / `Spec.Tags` | 仅文档 |
| `#[ApiOperation]` | `Spec.Summary/Description` | 仅文档 |
| `#[ApiProperty]` | `doc` tag | 仅文档 |
| `#[ArrayList(itemClass)]` | `[]T` | Go 类型即元素类型 |
| `#[ValidationRule]` | `validate` tag | 规则子集映射 |
| `StringToInt` | `types.Int64String` / `Uint64String` | 或 query/form 原生 int |
| `IntToString` | `types.Int64String` / `Uint64String` | JSON 输出字符串 |
| `BaseRequest` | 普通 Request struct | 无 requestInput |
| `BaseResponse` | Response struct 即 `data` | 信封由 gofy 生成 |
| `BasePageRequest` | `paging.PageRequest` | `page`/`page_size` |
| `AbstractListDataDto` | `paging.ListData[T]` | `total`+`list` |
| `AbstractPageDataDto` | `paging.PageData[T]` | 加 `page`/`page_size` |
| `BaseIntEnum` / `BaseStringEnum` | 命名类型 + 常量 + Labels | 无继承 |
| `BaseClientApi` | `client.Call` | 泛型 + 重试 |
| `NacosServiceDiscovery` | `client.Resolver` | 适配器后置 |
| `StreamResponse` | `Spec.ResponseKind=KindSSE` | 专用绑定 |
| `ChunkedResponse` | `Spec.ResponseKind=KindChunked` | 专用绑定 |
| `DownloadResponse` | `Spec.ResponseKind=KindDownload` | 专用绑定 |
| `generate-client.php` | `apicore/cmd/gofy-client` | 生成强类型方法 |
| `generate-openapi.php` | `apicore/cmd/gofy-openapi` | 反射生成 |
| `reference-check.php` | `apicore/cmd/refcheck` | 导入边界检查 |
| `interface-api.json` serviceName | `Config.Service` / `Resolver` | 服务名 |

**行为差异：**

- PHP 的 `code/msg` 在 `BaseResponse` 上；Go 的 Response 只描述 `data`。
- PHP 的 `trace_id` 在响应中；gofy 使用 `req_id`，并可选 `X-Trace-ID`。
- PHP 的 Client 返回 Response 对象；Go 的 `Call` 返回 `*Res`，错误通过 `error`。
- PHP 的枚举是类；Go 是命名类型 + 常量，必须显式 `Valid()`。
- PHP 的 `ArrayDto` 支持动态属性；Go 不支持，字段必须声明。

---

## 10. 测试策略

### 10.1 单元测试

| 包 | 测试重点 |
| --- | --- |
| `endpoint` | `Spec` 校验、泛型类型反射、重复路由 |
| `apicore/adapter/gofy` | `BindAll` 方法映射、签名不符、NoContent、中间件名缺失、错误映射 |
| `apicore/client/encode` | path 替换、query 编码、slice 重复 key、multipart |
| `apicore/client/decode` | 成功/错误信封、空 body、非法 JSON、未知字段、大整数 |
| `apicore/client/retry` | 幂等/非幂等、429/503、退避、context 取消 |
| `docgen/schema` | 所有类型映射、指针、嵌套、泛型、递归、嵌入 |
| `docgen/naming` | 稳定组件名、冲突 hash |
| `docgen/constraints` | validate tag 映射 |
| `docgen/operation` | 参数位置、请求体、响应、错误码、鉴权/限流 |
| `refcheck` | 合法/非法导入、测试 fixture |
| `docsui` | `/docs`、`/openapi.json`、BasicAuth |
| gofy `support/upload` | 大小、MIME、扩展名、路径穿越、随机文件名 |
| gofy `support/sse` | event/id/data/retry、多行 data、heartbeat、flush |
| gofy `support/chunked` | 分块写、JSON、文件、错误 |
| gofy `support` Download | Content-Disposition、RFC5987、关闭 reader |
| gofy `middleware` Security | 每个 header、HSTS 仅 TLS、skip |
| gofy `auth` | bearer、optional、invalid、principal |
| gofy `ratelimit` | 允许/拒绝、窗口重置、headers |
| gofy `health` | 并发、超时、详情开关、503 |
| gofy `logger` | 脱敏 key、嵌套、截断、不修改原对象 |

### 10.2 黄金文件测试

- `docgen/testdata/*.golden.yaml`：固定契约 -> 固定 OpenAPI。
- `apicore/cmd/gofy-openapi -update` 更新 golden；review diff。
- `apicore/cmd/gofy-client -check` 校验生成物。
- CI 中 `git diff --exit-code`。

### 10.3 契约测试

1. **编译期契约**：每个业务 Handler 声明编译期断言——整体实现用 `var _ demoservice.Service = (*Handler)(nil)`，按分组拆分用 `var _ demoservice.OrdersService = (*OrdersHandler)(nil)` 等窄接口。
2. **路由一致性**：`BindAll` 启动期校验，测试用无效实现验证失败信息。
3. **OpenAPI 一致性**：生成器从 `Routes()` 读取，不存在第二份路径。
4. **Client/Server 回环**：
   - 用 `httptest` 启动一个按契约注册的 gofy 应用。
   - 用生成的 Client 调用。
   - 覆盖成功、400 校验、401、404 业务错误、429、500。
5. **枚举/标签一致性**：`internal/lint` 校验 `enum` tag 与 `All*`/`Labels` 一致。

### 10.4 E2E（order-service）

- 保留现有 MySQL 集成测试（使用 `config.yaml` 中的 literal DSN）。
- 迁移到契约后，原有 API 行为不变：
  - `POST /api/v1/order/orders`
  - `GET /api/v1/order/orders`
  - `GET /api/v1/order/orders/:id`
  - `DELETE /api/v1/order/orders/:id`
- 新增：
  - `/docs` 可加载。
  - `/openapi.json` 包含 4 个路径。
  - Client 调用成功。
  - 业务错误码正确。
**路径策略（已锁定）：**

- 路由分组 `Prefix` 写**完整部署前缀**，如 `orderroute.RoutePrefix = "/api/v1/order"`；组内 `Spec.Path` 只写相对路径，如 `/orders`、`/orders/{id}`、`/config`。
- 完整路由 = 分组 `Prefix` + `Spec.Path`。order-service 的 `order` 模块 `Prefix()` 返回 `""`，App 不设置 `WithAPIPrefix`，也不再叠加模块名，因此最终是 `/api/v1/order/orders`，与迁移前一致。
- 契约即部署契约：同一份路由描述符挂到其他服务时，需要改分组 `Prefix` 才能换前缀；契约层不再依赖 gofy 的 `apiPrefix`。
- OpenAPI 默认直接使用 `Group.Add` 写入描述符的 `Spec.Prefix`；需要按另一基址发布时用 `apicore/cmd/gofy-openapi -prefix demoservice=/api/v1/order`，配置值替换该前缀并保留组内剩余路径。生成出的 `paths` 是完整服务端路径，`servers` 只写协议/域名。
- `apicore/contracts/README.md` 固定该规则；`internal/lint` 检查组内 `Spec.Path` 不以 `/api/` 开头、不包含模块名，保证前缀只出现在分组声明处。

**order-service 迁移前后对照：**

| 迁移前 | 迁移后 |
| --- | --- |
| `support.POST(r, "/orders", ...)` | 分组 `Prefix = "/api/v1/order"`，`Spec.Path = "/orders"`，模块 `Prefix() = ""` |
| 最终 `POST /api/v1/order/orders` | 最终 `POST /api/v1/order/orders` |
| 本地 `dto.ListOrderRequest{Page, Size}` | `paging.PageRequest{Page, PageSize}` |

> 注意：现有 order-service 使用 `size` 作为每页条数，契约统一为 `page_size`。这是对外字段变更；order-service 必须在同一次发布中切换，或保留一层兼容参数（`size` -> `page_size`）并标记废弃。

### 10.5 性能与稳定性

- `docgen` 在 100 个路由、500 个 schema 下生成时间 < 2s。
- `client.Call` 反射仅用于 path/query 构造；如需极致性能，生成方法可缓存字段元数据（`sync.Map`）。
- 限流内存实现并发基准测试。
- SSE 长连接压测：1000 连接、心跳 15s、无 goroutine 泄漏。

---

## 11. CI、版本与发布

### 11.1 GitHub Actions

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: gofmt -l . && test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go test -race ./...

  contracts:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go run ./apicore/cmd/refcheck
      - run: go run ./apicore/cmd/gofy-openapi -all -out ./openapi
      - run: go run ./apicore/cmd/gofy-openapi -validate ./openapi/demoservice.yaml
      - run: go run ./apicore/cmd/gofy-client -all -check
      - run: git diff --exit-code -- openapi client

  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go build ./...
```

### 11.2 版本策略

| 变更 | 版本 |
| --- | --- |
| 新增接口、可选字段、新枚举值 | minor |
| 修复文档/生成器、不改契约 | patch |
| 删除/重命名字段、改类型、改路径、改错误码 | 提升 module 主版本（如 `v2.0.0`），旧主版本保留至少 2 个 minor 或 90 天 |
| 修改公共包 / `adapter` 行为 | minor（若影响调用方则 major） |
| 修改生成器输出格式 | minor + golden 更新说明 |

**发布流程：**

1. 合并到 `main`。
2. 打 tag `v0.1.0`。
3. 业务服务升级依赖，跑契约测试。
4. 旧版本契约保留，不删除生成物。

### 11.3 兼容性检查

- **OpenAPI diff**：生成旧版本与新版本规范，使用 `oasdiff breaking` 检测。
- **Go API diff**：`golang.org/x/exp/apidiff` 或 `go tool api` 检查导出符号。
- **JSON 兼容**：新增字段允许；删除/改类型禁止；枚举新增允许，删除禁止。
- **客户端兼容**：旧客户端必须能解析新服务端响应（忽略未知字段）。
- **服务端兼容**：新服务端必须接受旧客户端请求（新增字段可选、默认值合理）。

---

## 12. 实施计划

### 12.1 阶段划分

| 阶段 | 内容 | 产出 | 预估 |
| --- | --- | --- | --- |
| P0-1 | 创建仓库骨架、`apicore/contracts/common`、`endpoint`、`appinterface/demoservice` | 可编译的契约包 | 2 天 |
| P0-2 | `client` 泛型调用、错误、重试、编码/解码 | Client 单测通过 | 3 天 |
| P0-3 | `apicore/adapter/gofy` BindAll + order-service 迁移 | order-service 使用契约，e2e 通过 | 3 天 |
| P0-4 | `docgen` + `docsui` + `apicore/cmd/gofy-openapi` | OpenAPI 3.0.3 + Swagger UI | 4 天 |
| P0-5 | `apicore/cmd/gofy-client`、`refcheck`、CI、golden | CI 全绿 | 3 天 |
| P1-1 | gofy：上传、下载、重定向、Raw、SSE、Chunked | 新包 + 测试 | 4 天 |
| P1-2 | gofy：安全头、Bearer 鉴权、header/cookie 绑定 | 中间件 + 测试 | 2 天 |
| P2-1 | gofy：CORS 增强、Locale、限流、健康检查、日志脱敏、routematch | 中间件 + 测试 | 5 天 |
| P2-2 | 契约：流式/下载接口示例与 Client | SSE/下载 E2E | 2 天 |
| P3-1 | OTel 模块、Nacos/K8s Resolver、破坏性检查、Catalog | 可选模块 | 5 天 |

总计约 4–5 周（单人）。

### 12.2 里程碑

| 里程碑 | 验收 |
| --- | --- |
| M1 契约可用 | order-service 编译、测试通过，无本地重复 DTO |
| M2 调用可用 | 另一个服务通过 Client 调用 order-service |
| M3 文档可用 | `/docs` 打开，OpenAPI 校验通过，CI diff 为空 |
| M4 HTTP 能力补齐 | 上传/下载/SSE/安全头/鉴权测试通过 |
| M5 生产可用 | 限流/健康检查/脱敏/版本策略落地 |

---

## 13. 验收标准

### 13.1 契约仓库

- [ ] `go test -race ./...` 全绿。
- [ ] `go vet ./...` 无输出。
- [ ] `gofmt -l .` 无输出。
- [ ] `go run ./apicore/cmd/refcheck` 通过；故意引入 gin 导入时失败。
- [ ] `go run ./apicore/cmd/gofy-openapi -all` 生成成功，`kin-openapi` 校验通过。
- [ ] `go run ./apicore/cmd/gofy-client -all -check` 通过。
- [ ] `git diff --exit-code -- openapi client` 为空。
- [ ] `appinterface/demoservice` 覆盖 order-service 现有 4 个接口。
- [ ] 每个业务 Handler 有编译期断言：整体实现断言 `demoservice.Service`，按分组拆分则断言对应的窄接口（`OrdersService` / `ConfigService`）。
- [ ] 枚举 tag 与常量一致。
- [ ] 错误码无重复、无越界。

### 13.2 gofy 框架

- [ ] 现有测试全绿，公开 API 无破坏性变更。
- [ ] 上传：路径穿越、超大文件、伪造 MIME 均被拒绝。
- [ ] SSE：事件格式、心跳、客户端断开、无 goroutine 泄漏。
- [ ] Chunked：分块写、文件写、错误传播。
- [ ] Download：中文文件名、inline/attachment、Content-Length。
- [ ] 安全头：HSTS 仅 TLS；CSP 可配置；skip 路径生效。
- [ ] 鉴权：required/optional/无效 token；Principal 可读。
- [ ] 限流：允许/拒绝/重置/headers；并发安全。
- [ ] 健康检查：DB 失败 503、超时、详情隐藏。
- [ ] 日志脱敏：嵌套结构、大小写、截断。
- [ ] CORS：通配子域、credentials 校验、预检 204。

### 13.3 业务服务

- [ ] order-service 不再有本地 `dto` 包。
- [ ] 原 4 个接口行为不变（状态码、字段、错误码）。
- [ ] `/docs` 可访问，`/openapi.json` 包含 4 个路径。
- [ ] Client 调用成功；404 映射为 `ErrCodeOrderNotFound`。
- [ ] `DELETE` 无响应体返回 204。
- [ ] `go test ./...` 通过。
- [ ] README 更新接入说明。

---

## 14. 风险与开放问题

### 14.1 风险

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R1 | 自研 docgen 工作量大 | 文档生成延期 | 先支持 demoservice 所需类型；golden 测试；逐步扩展 |
| R2 | 泛型类型名不稳定 | OpenAPI diff 噪声、客户端生成混乱 | 固定命名算法 + hash 冲突；golden 覆盖 |
| R3 | 契约与实现漂移 | 运行时 404/签名错误 | `BindAll` 启动校验；编译期 `var _ Service`；契约测试 |
| R4 | 信封差异（PHP 无 req_id/details） | 旧调用方解析失败 | 新字段可忽略；文档声明；客户端容错 |
| R5 | 大整数精度 | JS/弱类型客户端丢精度 | 对外 ID 用字符串类型；文档强制 |
| R6 | 错误码冲突 | 调用方误判 | 码段分配表 + CI 唯一性检查 |
| R7 | 文档暴露内部接口 | 安全风险 | 默认仅非生产挂载；BasicAuth/网关保护 |
| R8 | 工具链依赖进入业务二进制 | 体积增大 | 契约包纯净；`docgen/docsui` 不被业务 import |
| R9 | SSE 长连接与超时 | 连接被网关断开 | 心跳；`X-Accel-Buffering: no`；文档说明 WriteTimeout |
| R10 | Path 前缀策略混用 | 路由不一致 | 在 `apicore/contracts/README.md` 固定一种；lint 检查 |
| R11 | 删除/改字段破坏旧客户端 | 线上故障 | 版本策略；兼容性检查；灰度 |
| R12 | Redis 限流未内置 | 多实例限流失效 | 核心只提供接口；生产必须接共享存储 |

### 14.2 开放问题（建议默认值）

| # | 问题 | 建议 |
| --- | --- | --- |
| Q1 | 仓库最终地址 | `github.com/bingcool/gofy-interface-service` |
| Q2 | 首个接入服务 | order-service |
| Q3 | Path 前缀模式 | 方案 A：契约写相对路径，gofy 负责前缀 |
| Q4 | 对外 ID 类型 | 字符串（`types.Uint64String`），从 v1 开始 |
| Q5 | 错误码段分配人 | 架构组维护 `ranges.go`，PR 评审 |
| Q6 | 文档是否上生产 | 默认否；如需，放内网网关 + BasicAuth |
| Q7 | 服务发现 | v1 固定地址；v2 接 Nacos/K8s |
| Q8 | JWT 实现 | 业务提供 `Authenticator`；不内置 |
| Q9 | Redis 适配器 | 独立模块 `gofy-contrib/redis`，不进入核心 |
| Q10 | OpenAPI 版本 | 3.0.3；3.1 后续评估 |

---

## 15. 附录

### 附录 A：契约仓库公开 API 清单

| 包 | 导出符号 |
| --- | --- |
| `apicore/contracts/common/envelope` | `Envelope[T]`、`ErrorEnvelope` |
| `apicore/contracts/common/paging` | `PageRequest`、`Offset()`、`ListData[T]`、`PageData[T]` |
| `apicore/contracts/common/errcode` | `Code`、`Fault`、`New`、公共码常量 |
| `apicore/contracts/common/types` | `Int64String`、`Uint64String`、`Date`、`Decimal` |
| `apicore/contracts/common/endpoint` | `Spec`、`Route[Req,Res]`、`Descriptor`、`New`、`NewRouteGroup`、`Group`、`Get`/`Post`/`Put`/`Patch`/`Delete`、`NoBody`、`RateKey`、`ResponseKind`、`ErrorSpec` |
| `apicore/contracts/common/enum` | `Values`、`Labels`、`Valid` 辅助 |
| `appinterface/{domain}service` | `Service`、DTO、Request、Response、Const、Enum、`Routes()` |
| `appinterface/{domain}service/{domain}serviceclient` | `Client`、`New`、`NewWithBaseURL`、`NewWithResolver`、各路由方法 |
| `apicore/adapter/gofy` | `Bind`、`BindGroup`、`BindAll`、`BindSSE`、`BindDownload`、`BindChunked`、`BindOptions`、`MiddlewareRegistry`、`Error` |
| `client` | `New`、`Config`、`Call`、`StreamSSE`、`DownloadFile`、`StreamChunked`、`APIError`、`CodeOf`、`RequestIDOf`、`Resolver`、`StaticResolver`、`CallOption`、`WithTimeout`、`WithConnectTimeout`、`WithHTTPClient`、`WithHeader(s)` |
| `docgen` | `Build`、`Options`、`Validate` |
| `docsui` | `Handler`、`Config`、`BasicAuth` |
| `apicore/cmd/*` | CLI |

### 附录 B：OpenAPI 生成规则速查

| 输入 | 输出 |
| --- | --- |
| `Spec.Summary` | `summary` |
| `Spec.Description` | `description` |
| `Spec.Tags` | `tags` |
| `Spec.Errors` 里的 401 | `401` 响应（不再按认证字段推导） |
| `Spec.RateLimit` | 429 + `X-RateLimit-*` 文档 |
| `Spec.NoContent` | 204 无 body |
| `Spec.ResponseKind=KindSSE` | `text/event-stream` |
| `Spec.ResponseKind=KindDownload` | `application/octet-stream` + `Content-Disposition` |
| `Spec.ResponseKind=KindChunked` | `application/octet-stream` + binary |
| `Spec.Errors` | 对应状态码 + 示例 |
| `uri` tag | path parameter，required |
| `form` tag + GET/HEAD/DELETE | query parameter |
| `header` tag | header parameter |
| `cookie` tag | cookie parameter |
| `validate:"required"` | `required` |
| `validate:"min/max"` | `minimum/maximum` 或 `minLength/maxLength` |
| `default` | `default` |
| `doc` | `description` |
| `example` | `example` |
| `enum` | `enum` |
| `deprecated:"true"` | `deprecated: true` |
| `*T` | `nullable: true` |
| `[]T` | `type: array` |
| `map[string]T` | `additionalProperties` |
| 嵌入 struct | 展开字段 |
| `json:"-"` | 跳过 |

### 附录 C：决策记录

| ID | 决策 | 理由 |
| --- | --- | --- |
| C1 | 契约仓库不承载业务实现 | 避免网络多一跳、重复逻辑、部署耦合 |
| C2 | 路由描述符是唯一事实来源 | 消除“契约/路由/文档”三份漂移 |
| C3 | 自研反射式 OpenAPI 生成器 | 契约单一事实来源；泛型与 tag 可控；可 golden diff |
| C4 | 契约包零第三方依赖 | 所有服务可安全 import；长期稳定 |
| C5 | Client 用泛型 `Call` + 生成强类型方法 | 类型安全 + 低样板 |
| C6 | 对外 ID 用字符串 | 避免 JS 精度问题；与 InterfaceApi `IntToString` 对齐 |
| C7 | 分页统一 `page`/`page_size` | 跨服务一致；与 InterfaceApi 对齐 |
| C8 | 错误码沿用 gofy `apperr` 公共码 | 避免同一错误两套 code |
| C9 | 文档 UI 内嵌资源 | 中国网络/离线环境不依赖 CDN |
| C10 | v1 不内置服务发现 | 先固定地址；接口预留 Resolver |
| C11 | 不移植 cron/worker/process | 不属于 HTTP 能力，且 gofy 已移除 |
| C12 | 不实现动态 Action/denyActions | Go 静态路由，无此攻击面 |
