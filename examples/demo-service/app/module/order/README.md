# app/module/order

演示模块：用 `gofy` 的模块机制挂载一组订单 HTTP 路由，同时展示
controller / service / repository 的分层边界。契约来自
`gofy/gofy-interface-service`，本模块只负责实现。

## 目录结构

```
order/
├── module.go                  # 模块装配、配置、路由注册
├── module_test.go             # 基于 gofytest 的接口测试
├── controller/
│   └── order_controller.go    # 适配 demoservice.Service 契约
├── service/
│   └── order_service.go       # 业务用例，返回 DTO
├── repository/
│   └── order_repository.go    # 持久化接口 + GORM 实现
└── model/
    └── order.go               # 持久化模型（orders 表）
```

分层约定：

| 层 | 职责 | 依赖限制 |
| --- | --- | --- |
| controller | 实现契约接口，做错误码映射 | 不导入 `model`，只使用契约类型 |
| service | 业务用例，出入参用普通值 / DTO | 只在 `toDTO` 中接触 `model` |
| repository | 定义 `OrderRepositoryInterface` 与 `ErrNotFound` | 拥有 `model` 和 GORM |
| model | GORM 实体，映射 `orders` 表 | 不感知上层 |

## 路由

模块名 `order` 会作为挂载前缀；`main.go` 里的
`gofy.WithAPIPrefix("/api/v1")` 再套一层，因此完整路径是：

| 方法 | 路径 | OperationID | 说明 | 成功响应 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/order/orders/{id}` | `GetOrder` | 查询订单详情 | `200` + `GetOrderResponse` |
| `GET` | `/api/v1/order/orders` | `ListOrders` | 按用户分页查询 | `200` + `paging.ListData[OrderDTO]` |
| `POST` | `/api/v1/order/orders` | `CreateOrder` | 创建订单 | `200` + `CreateOrderResponse` |
| `DELETE` | `/api/v1/order/orders/{id}` | `CancelOrder` | 取消订单 | `204`，无响应体 |
| `GET` | `/api/v1/order/config` | `GetConfig` | 查询订单模块配置 | `200` + `GetConfigResponse` |

路由描述符定义在 `gofy-interface-service` 的
`appinterface/demoservice/orderroute`，本模块通过
`gofyadapter.BindGroup` 按分组绑定，不在业务代码里重复写路径和参数校验。

只使用模块（不带 `/api/v1`）时路径是 `/order/orders...`，测试里就是这种形态。

### 请求示例

创建订单，`user_id` 和 `items` 为必填，`amount` 单位为分：

```bash
curl -X POST http://127.0.0.1:8080/api/v1/order/orders \
  -H 'Content-Type: application/json' \
  -d '{
    "user_id": 100,
    "amount": 500,
    "items": [{"sku_id": 90001, "num": 1}]
  }'
```

分页查询，`user_id` 必填，`page` 默认 1，`page_size` 默认 20、最大 100：

```bash
curl 'http://127.0.0.1:8080/api/v1/order/orders?user_id=100&page=1&page_size=20'
```

> 契约还声明了 `status`、`created_from`、`created_to` 三个查询参数，会参与
> 参数校验，但当前 demo 的 `List` 只按 `user_id` 过滤，这三个条件尚未生效。

## 配置

配置位于 `config.yaml` 顶层的 `jwt` 段，结构体是 `middleware.JWTConfig`
（`app/middleware`）：

```yaml
# 契约在分组上显式声明了 "auth" 中间件，其内部用框架 JWT 校验 Bearer Token；
# secret 为空时放行。生产环境从环境变量注入密钥。
jwt:
  secret: <从 JWT_SECRET 注入，不要提交>
  algorithms: [HS256]
  issuer: demo-service
  audience: demoservice
  require_expiry: true
```

| YAML 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `jwt.secret` | string | 空 | HMAC 密钥；非空时契约中声明了 `"auth"` 的路由校验 Bearer Token，为空时放行 |
| `jwt.algorithms` | []string | `[HS256]` | 允许的 `alg`，支持 HS256/HS384/HS512 |
| `jwt.issuer` | string | 空 | 非空时要求 token 的 `iss` 与之一致 |
| `jwt.audience` | string | 空 | 非空时要求 token 的 `aud` 包含该值 |
| `jwt.require_expiry` | bool | false | 为 true 时拒绝没有 `exp` 的 token |

`middleware.JWTConfig` 由 `app/config` 从 `config.yaml` 的 `jwt` 段填充，再由
`options.go` 的 `middlewareRegistrar(cfg)` 闭包在 App 启动时注册 `auth` 别名；
手工构造不带 `JWT.Secret` 的配置时，`auth` 是放行中间件。

## 装配

`main.go` 通过工厂注册模块，启动和热更新共用同一份工厂列表：

```go
gofy.WithMiddlewareRegistrar(middlewareRegistrar(cfg)),
gofy.WithModuleFactories(order.NewModuleFactory())
```

`NewModuleFactory` 在 `Runtime` 就绪后执行：

1. 检查 `rt.DB`，为空直接返回 `order module requires database`；
2. 用 `repository.NewFromHandle(rt.Logger)` 创建 GORM 仓库，查询时从 `db.Db` 取连接；
3. 用 `service.NewOrderService(service.Deps{...})` 创建服务；
4. 调用 `NewModule(svc, rt.Logger)` 包装出模块。

工厂执行时 `app/io` 还没 `Start`，`db.Db` 仍是 nil，所以 `NewFromHandle`
把句柄解析推迟到每次查询；`Start` 安装新连接后仓库自动使用新句柄。脚本模式不经过
模块生命周期，直接用 `repository.New(rt.DB, rt.Logger)` 传显式连接。

`Register` 只创建 controller（传入 logger），遍历 `orderroute.Orders` 与
`orderroute.Config` 调用 `gofyadapter.BindGroup`，不再创建或注入中间件注册表。

契约里的中间件只声明别名，认证也不例外：`Orders` 分组声明了
`group.Use("audit", "auth")`，`Config` 分组声明了 `group.Use("auth")`，
`CreateOrder` 另外声明了 `spec.Middleware = []string{"order-audit"}`。审计名字
映射到 `app/middleware.Audit(logger, scope)`（`scope` 区分 `group`/`route`），
`auth` 映射到 `app/middleware.Auth`（内部调用框架
`support/middleware.JWTAuth`）。`spec.Auth = AuthRequired` 只用于 OpenAPI 的
security 标记，adapter 不会因此自动附加认证中间件。审计中间件在 handler 之后
记录方法、路由模板、状态码和 `req_id`，与框架的 `AccessLog` 互补。执行顺序：
`分组 audit → 分组 auth → BindOptions.Global → 路由 order-audit → Routes[OperationID] → RateLimit`。

别名与实例的对应关系集中在 `app/middleware.Register`：该包导出
`NameAuth = "auth"`、`NameAudit = "audit"`、`NameOrderAudit = "order-audit"`，
`Register(reg, Deps{Logger, JWT, Responder})` 把应用层别名注册进 App 在本次装配
（启动 / `Reload`）创建的共享注册表。注册表随 Router 下发给模块，`BindGroup`
直接按名字解析；新增别名只改 `app/middleware`，不用改模块。名字查不到时启动
直接失败，不会静默跳过；重复注册同一别名也会报错，避免两个中间件抢同一个名字。
热更新会执行新的 registrar 并得到新注册表，不会与旧实例冲突
（`TestModuleRegisterBuildsFreshRegistry`、`TestRegisterUsesTheGivenRegistry`）。

认证同样是显式声明：契约在分组或路由上写 `"auth"`，再由注册表解析，没有默认
别名或框架兜底；`AuthRequired` 只影响 OpenAPI 文档。需要覆盖某个别名时（例如
测试桩），用 `BindOptions.Middleware` 传显式映射，它的优先级最高。

测试或自定义场景可以跳过工厂，自己构造 service 后调用 `NewModule`，并用
`gofy.WithMiddlewareRegistrar` 安装与 main 相同的 registrar：

```go
module := order.NewModule(myService, zap.NewNop())
app := gofytest.New(t,
    gofy.WithMiddlewareRegistrar(testRegistrar(zap.NewNop(), middleware.JWTConfig{})),
    gofy.WithModules(module),
)
```

## 服务依赖

`OrderService` 的依赖集中在 `service.Deps`，构造函数只收一个结构体：

```go
svc := service.NewOrderService(service.Deps{
    Orders: repo,      // repository.OrderRepositoryInterface
    Logger: rt.Logger, // nil 时退化为 zap.NewNop()
})
```

要组合多个 repository（例如订单还需要读用户、产品）时，只在 `Deps` 里加字段，
构造函数签名不变，调用方按字段名注入，不必记参数顺序（示意）：

```go
type Deps struct {
    Orders   repository.OrderRepositoryInterface
    Users    user.Repository    // 其他模块导出的持久化接口
    Products product.Repository
    Logger   *zap.Logger
}
```

约定：

- 字段按聚合命名（`Orders` 而不是 `Repo`），service 内部对应 `s.orders`、`s.users`，
  读代码时能直接看出数据来自哪个聚合；
- 跨模块依赖由 `module.go` 装配后注入：对方模块导出接口，service 不 import 它的
  GORM 实现，也不在用例里临时 `new`；
- 必填依赖（如 `Orders`）由装配方保证非 nil，`Logger` 允许为 nil；
- 只有“同一聚合、同一事务”的多个接口才考虑 embed 成一个聚合接口，否则会把不相关
  的持久化绑死在同一个实现和 mock 上；
- 测试用 fake 实现填 `Deps`，不需要真实数据库（见 `module_test.go`）。

## 日志

三个包都用构造函数注入 `*zap.Logger`，由工厂统一传 `rt.Logger`，不使用包级
全局 logger，也不在函数里临时 `zap.New`。构造函数在 logger 为 `nil` 时退化
为 `zap.NewNop()`，测试因此可以完全不关心日志。每个构造函数还会附加
`component` 字段（`order.controller` / `order.service` / `order.repository`），
便于按层过滤。

```go
repo := repository.NewFromHandle(rt.Logger) // HTTP 模块：查询时取 db.Db
svc := service.NewOrderService(service.Deps{Orders: repo, Logger: rt.Logger})
h := controller.NewOrderController(svc, rt.Logger)
```

分层记录约定是“一个事件只在一个层记录一次”：

| 层 | 级别 | 记录内容 |
| --- | --- | --- |
| controller | `Error` | 只记录最终映射成 500 的意外错误，带 `zap.Error(err)` 和业务键 |
| service | `Info` / `Debug` | `Info`：创建、取消等状态变更；`Debug`：查询、预期内的 not found |
| repository | `Debug` | 数据访问的参数和影响行数；错误只 `fmt.Errorf` 包装后返回，不在这里打 |

具体做法：

- 成功请求不在 controller 里重复记录，访问日志已经有 method/path/status/
  `req_id`；业务日志用 `order_id`、`user_id` 等字段与之关联；
- 错误只在“决定 HTTP 响应”的 controller 里打一次 `Error`，service 和
  repository 只返回带上下文的错误，避免 `error.log` 出现同一条错误的多个副本；
- `ErrOrderNotFound` 属于预期业务结果，service 用 `Debug`，controller 直接
  映射为 404，不进 `error.log`；
- 用 `zap.Uint64`、`zap.Int64`、`zap.Error` 等强类型字段，不要用 `fmt.Sprintf`
  拼消息；字段名保持 snake_case；
- 不记录 token、密码和完整请求体；需要排查时用业务主键回查。

一条创建订单的成功日志和一条失败日志大致如下：

```text
{"level":"info","ts":"...","caller":"service/order_service.go:...","msg":"order created","component":"order.service","order_id":42,"user_id":100,"amount":500}
{"level":"error","ts":"...","caller":"controller/order_controller.go:...","msg":"create order failed","component":"order.controller","user_id":100,"amount":500,"error":"create order: ..."}
```

按执行模式和日期，这两类日志分别落在
`storage/log/http/<YYYYMMDD>/` 或 `storage/log/script/<YYYYMMDD>/` 下。
HTTP 模式是 `info-h<H>.log` / `error.log` / `system_error.log`；脚本模式在
日期目录下再分 `info/` 和 `error/`，并以脚本名命名（`info/fix-order-h<H>.log`、
`error/fix-order.log`、`error/fix-order-system_error.log`），一个脚本对应
一组日志文件，panic 才进 `system_error.log`。

Nacos 热更新时，`main.go` 会重新执行 registrar 与工厂并替换模块；端口不变，
新配置里的 `jwt` 段会生效（registrar 重建 `auth` 实例）。已有请求不受影响，
新请求使用新模块。

## 鉴权

契约在分组/路由上显式声明 `"auth"`，对应实例由应用层 `app/middleware.Auth`
构造的 JWT 中间件（内部调用 `support/middleware.JWTAuth` + `support/jwtauth`）
校验 `Authorization: Bearer <token>`。`Auth` 把 `config.yaml` 的 `jwt` 段映射成
`jwtauth.Config`，以别名 `auth` 注册进 App 共享注册表，adapter 按契约声明的名字
把它装到路由上；`AuthRequired` 只是 OpenAPI 标记。校验失败时返回与框架一致的
响应包：

```json
{"code": 40100, "msg": "unauthorized", "data": null, "req_id": "..."}
```

HTTP 状态码为 `401`。`jwt.secret` 为空时 `Auth` 返回一个直接放行的
中间件，因此示例默认不需要凭证；填入密钥后只接受同一密钥签发、且满足
`algorithms` / `issuer` / `audience` / `require_expiry` 约束的 token。

## 响应与错误码

所有非 `204` 响应都由框架统一包装为：

```json
{"code": 0, "msg": "success", "data": {...}, "req_id": "..."}
```

`req_id` 会回写到 `X-Trace-ID`；请求带 `X-Trace-ID` 时原样透传。

| code | HTTP | 场景 |
| --- | --- | --- |
| `40001` | 400 | 参数校验失败，`details` 按字段给出原因 |
| `40100` | 401 | JWT 鉴权失败（仅在配置了 `jwt.secret` 时） |
| `40401` | 404 | 订单不存在 |
| `50000` | 500 | 内部错误；具体原因只写日志，不返回给客户端 |

映射方式：

- controller 把 `service.ErrOrderNotFound` 转成
  `gofyadapter.Error(demoservice.ErrOrderNotFound)`；
- 其他错误用 `apperr.Internal(...).WithCause(err)`，响应只保留通用文案；
- panic 由框架恢复为 `50000`，堆栈和内部信息不会出现在响应里。

> 契约里 `CancelOrder` 还声明了 `409 / ErrCodeOrderClosed`，当前 demo 的实现
> 只是把状态更新为 `cancelled`，重复取消仍返回 `204`，不会触发 409。

## 订单状态

状态枚举来自 `appinterface/demoservice/orderenum`：

| 值 | 含义 |
| --- | --- |
| `pending` | 待支付，创建订单时的初始状态 |
| `paid` | 已支付 |
| `cancelled` | 已取消 |

订单 ID、用户 ID 在契约中都是 `types.Uint64String`，JSON 里以字符串传输，
避免 JavaScript 大整数精度丢失；写响应时由 `toDTO` 转换。

## 数据表

模型 `model.Order` 对应 `orders` 表，DDL 在
[`migrations/0001_orders.sql`](../../../migrations/0001_orders.sql)：

```sql
CREATE TABLE IF NOT EXISTS orders (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    amount BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    KEY idx_orders_user_id (user_id),
    KEY idx_orders_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
```

项目不使用 `AutoMigrate`，首次运行前需要手动执行迁移文件。`ListByUser`
按 `id DESC` 排序，并先 `COUNT` 再分页，返回当前用户的总数。
`UpdateStatus` 在 `RowsAffected == 0` 时返回 `ErrNotFound`，因此取消不存在的
订单会得到 `40401`。

## 测试

```bash
cd examples/demo-service
go test ./app/module/order/...
```

测试不需要数据库：`module_test.go` 用 `fakeRepository` 实现
`OrderRepositoryInterface`，再通过 `gofytest.New` 启动一个只挂载 order 模块的
测试应用。覆盖内容包括：

- 成功响应的 envelope、`req_id` 及 `X-Trace-ID` 透传；
- 参数校验（非法 id、缺失必填字段、`page_size` 超限）；
- 订单不存在返回 `40401`；
- panic 返回 `50000` 且不泄露内部信息；
- 配置 `jwt.secret` 后的 `40100`、合法 token 的正常路径与错误签名
  （`TestCancelWithJWT`）；
- 创建订单的初始状态为 `pending`，列表分页结果正确；
- 契约中间件名被解析：分组 `audit` 与 `CreateOrder` 的路由级 `order-audit`
  都落审计日志，且路由级先于分组级完成（`TestContractMiddlewareNamesAreResolved`）；
- 同一个模块装配两次不会撞别名冲突，每次装配 App 都新建注册表
  （`TestModuleRegisterBuildsFreshRegistry`、`TestRegisterUsesTheGivenRegistry`）。

新增路由时，先在 `gofy-interface-service` 里补契约描述符，再在
`gofyadapter.BindGroup` 的绑定范围内实现 controller 方法即可；路径、校验和
错误声明都不需要在业务模块里重复。契约新增中间件别名时，在 `app/middleware`
里导出对应的 `NameXxx` 常量，并在 `Register` 的 `entries` 里加一行；业务模块
的 `Register` 不用改。
