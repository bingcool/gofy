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
gofy.WithModuleFactories(order.NewModuleFactory(apprepository.NewProvider()))
```

`NewModuleFactory` 在 `Runtime` 就绪后执行：

1. 检查 `rt.DB`，为空直接返回 `order module requires database`；
2. 向装配级 `app/repository.Provider` 取本次装配的注册表；
3. 用 `apprepository.OrderOrders(reg)` 取出订单仓库（模式 A，装配期取用）；
4. 用 `apprepository.ProductProducts(reg)` 只做启动期校验，产品仓库由 service
   在用例中按需取用（模式 B）；
5. 用 `service.NewOrderService(repos.Orders, reg, rt.Logger)` 创建服务；
6. 调用 `NewModule(svc, rt.Logger)` 包装出模块。

注册表里的 repository 都用 `NewFromHandle` 构造：工厂执行时 `app/io` 还没
`Start`，`db.Db` 仍是 nil，所以句柄解析推迟到每次查询；`Start` 安装新连接后
仓库自动使用新句柄，`Reload` 也会得到新的 Provider / 注册表。脚本模式不经过
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

本模块用两种取用模式组合其他模块的 repository：

- **模式 A（订单仓库）**：模块工厂在装配期取出，显式传给构造函数。`module.go`
  保留 `OrderRepositoryDeps` 汇总本模块的持久化契约，新增依赖时只加字段；
- **模式 B（产品仓库）**：service 持有装配级注册表，在 `Create` 里按需取出
  `product.products`。模块工厂仍会调用一次 `ProductProducts`，把缺注册或类型
  不符提前到启动期失败。

```go
type OrderRepositoryDeps struct {
    Orders repository.OrderRepositoryInterface
}

reg, _ := repos.Repositories(rt)                 // repos 是 app/repository.Provider
deps := OrderRepositoryDeps{}
deps.Orders, _ = apprepository.OrderOrders(reg)  // 模式 A
_, _ = apprepository.ProductProducts(reg)        // 模式 B 的启动期校验
svc := service.NewOrderService(deps.Orders, reg, rt.Logger)
```

service 内部：

```go
type OrderService struct {
    orders repository.OrderRepositoryInterface
    repos  *registry.Registry // 取用 product.products
    logger *zap.Logger
}
```

`Create` 按 SKU 批量查产品仓库，确认全部存在且上架后才落单；产品不存在或已下架
返回 400（`40000`），不会创建订单。这样同一个 service 既能读其他模块数据，
又不需要在构造函数上堆积每个读取点。

约定：

- 字段按聚合命名（`Orders` 而不是 `Repo`），service 内部对应 `s.orders`；
- 跨模块只依赖对方 repository 的接口和只读视图（`productrepo.ProductView`），
  不 import 对方的 `model` 或 GORM 实现；
- 注册表按装配周期创建，service 不得缓存到包级变量或跨请求保存；
- 必填依赖由装配方保证非 nil，`Logger` 单独传入并允许为 nil；
- 测试直接填 fake 注册表与 fake repository，不连数据库（见 `module_test.go`）；
- 如果后续某个跨模块依赖是业务必需的，也可以改回模式 A：在模块工厂取出后作为
  构造参数传入，两种模式可以混用。

## 事务

单库（`db.default`）下，repository 的连接解析统一为 `conn(ctx)`：

```go
func (r *OrderRepository) conn(ctx context.Context) *gorm.DB {
    fallback := r.db
    if fallback == nil {
        fallback = appdb.Db
    }
    return gofydb.FromContext(ctx, fallback)
}
```

调用方 service 用 `gofydb.Transaction(ctx, rt.DB, fn)` 开启事务，事务会放进传给
`fn` 的 context；订单、产品等模块的 repository 从 ctx 取到同一个事务：

```go
err := gofydb.Transaction(ctx, db, func(ctx context.Context) error {
    if _, err := products.DecreaseStock(ctx, skuID, num); err != nil {
        return err
    }
    _, err := orders.Create(ctx, order)
    return err
})
```

- 嵌套 `Transaction` 复用外层事务，不创建 savepoint；内层错误冒泡到外层，由最外层
  统一回滚，内层成功也不会提前提交；
- 没有事务时 `FromContext` 回退到 repository 的显式连接或 `appdb.Db`；显式连接
  不会覆盖事务；
- 跨模块只读调用可以不显式开事务；需要一致性的多步读写必须传入同一个 ctx；
- 当前 demo 的 `Create` 只写一条订单记录，尚未在 service 里开启事务；事务能力由
  [`app/repository/transaction_test.go`](../../repository/transaction_test.go)
  的跨模块用例覆盖：同一事务内扣库存 + 建订单，返回错误后两者一起回滚。

## 日志

三个包都用构造函数注入 `*zap.Logger`，由工厂统一传 `rt.Logger`，不使用包级
全局 logger，也不在函数里临时 `zap.New`。构造函数在 logger 为 `nil` 时退化
为 `zap.NewNop()`，测试因此可以完全不关心日志。每个构造函数还会附加
`component` 字段（`order.controller` / `order.service` / `order.repository`），
便于按层过滤。

```go
reg, _ := apprepository.NewProvider().Repositories(rt)
repo, _ := apprepository.OrderOrders(reg)
svc := service.NewOrderService(repo, reg, rt.Logger)
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
{"level":"info","ts":"...","caller":"service/order_service.go:...","msg":"order created","component":"order.service","order_id":42,"user_id":100,"amount":500,"products":["Gofy Demo SKU"]}
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
| `40000` | 400 | 下单失败：SKU 不存在或未上架 |
| `40001` | 400 | 参数校验失败，`details` 按字段给出原因 |
| `40100` | 401 | JWT 鉴权失败（仅在配置了 `jwt.secret` 时） |
| `40401` | 404 | 订单不存在 |
| `50000` | 500 | 内部错误；具体原因只写日志，不返回给客户端 |

映射方式：

- controller 把 `service.ErrOrderNotFound` 转成
  `gofyadapter.Error(demoservice.ErrOrderNotFound)`；
- `service.ErrProductNotFound` / `ErrProductUnavailable` 是客户端错误，映射为
  `apperr.BadRequest(err.Error())`，响应里带具体 SKU；
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

产品表由产品模块维护，DDL 在
[`migrations/0002_products.sql`](../../../migrations/0002_products.sql)；订单
服务只通过 `product.products` 仓库读取 `ProductView`，不直接查产品表。创建订单
前会校验请求里的每个 SKU 存在且 `status = 1`（上架），否则返回 `40000`。产品
仓库同时提供 `DecreaseStock`：上架与库存条件写在 SQL 里，返回受影响行数，0 行
由调用方映射为业务错误，避免先查后改的竞争。

## 测试

```bash
cd examples/demo-service
go test ./app/module/order/... ./app/repository/...
```

测试不需要外部数据库：`module_test.go` 用 `fakeRepository` 实现
`OrderRepositoryInterface`，并用 `fakeProductRepository` 填进
`registry.NewRegistry()`，再通过 `gofytest.New` 启动一个只挂载 order 模块的
测试应用；跨模块事务用例改用纯 Go 的内存 SQLite，同样不依赖外部 MySQL。覆盖
内容包括：

- 成功响应的 envelope、`req_id` 及 `X-Trace-ID` 透传；
- 参数校验（非法 id、缺失必填字段、`page_size` 超限）；
- 订单不存在返回 `40401`；
- panic 返回 `50000` 且不泄露内部信息；
- 配置 `jwt.secret` 后的 `40100`、合法 token 的正常路径与错误签名
  （`TestCancelWithJWT`）；
- 创建订单的初始状态为 `pending`，列表分页结果正确；
- 下单时 SKU 不存在或已下架返回 `40000`，不会落单
  （`TestCreateOrderRejectsUnknownOrOffSaleProduct`）；
- 跨模块事务：SQLite 上提交、回滚与库存不足分支
  （`app/repository/transaction_test.go`，覆盖“扣库存 + 建订单”一起回滚）；
- 契约中间件名被解析：分组 `audit` 与 `CreateOrder` 的路由级 `order-audit`
  都落审计日志，且路由级先于分组级完成（`TestContractMiddlewareNamesAreResolved`）；
- 同一个模块装配两次不会撞别名冲突，每次装配 App 都新建注册表
  （`TestModuleRegisterBuildsFreshRegistry`、`TestRegisterUsesTheGivenRegistry`）。

`make guards`（根仓库 `make check` 也会调用）运行
`examples/demo-service/scripts/check-imports.sh`，强制分层边界：controller 不得
import `app/repository`、任何 `model` 或其他模块的 repository/service；repository
不得 import service；除 `app/repository` 外，一个包最多 import 一个其他模块的
repository。测试文件不参与检查，可以同时引用多个 fake。

新增路由时，先在 `gofy-interface-service` 里补契约描述符，再在
`gofyadapter.BindGroup` 的绑定范围内实现 controller 方法即可；路径、校验和
错误声明都不需要在业务模块里重复。契约新增中间件别名时，在 `app/middleware`
里导出对应的 `NameXxx` 常量，并在 `Register` 的 `entries` 里加一行；业务模块
的 `Register` 不用改。
