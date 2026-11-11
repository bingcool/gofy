# app/module/order

演示模块：用 `gofy` 的模块机制挂载一组订单 HTTP 路由，同时展示
controller / service / innerapi / repository / model 的五包分层边界。HTTP 契约来自
`gofy/gofy-interface-service`，只在 controller 出现；模块间调用走对方 `innerapi`
契约。分层取舍的权威说明见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md)。

## 目录结构

```
order/
├── module.go                  # 装配：从共享注册表取 product.innerapi，登记 order.innerapi，绑定路由
├── module_test.go             # 基于 gofytest 的接口测试 + 装配测试
├── controller/
│   ├── order_controller.go    # 适配 demoservice.Service 契约，视图↔DTO 映射 + 错误码
│   ├── order_config_controller.go  # GetConfig 契约实现
│   └── order_status_test.go   # service.Status* ↔ orderenum 状态一致性测试
├── service/
│   ├── order_service.go       # 业务用例，实现 innerapi.OrderAPI，返回领域视图
│   ├── order_config_service.go # OrderConfigService（同包协作 service）+ Config 视图
│   ├── order_view.go          # 领域状态常量 + 订单域哨兵错误 + CreateItem 别名
│   ├── deps.go                # Dependencies（本模块仓库 + 跨模块 innerapi + 同包 Config + Logger）
│   ├── order_config_service_test.go # 配置样例 + OrderService 委托测试
│   └── transaction_test.go    # 跨模块事务用例（内存 SQLite）
├── innerapi/
│   └── innerapi.go            # 订单对外契约：OrderAPI 接口 + Order 视图 + CreateItem + 错误 + Name
├── repository/
│   └── order_repository.go    # GORM 仓库，导出具体类型（不声明接口），返回 model
└── model/
    └── order.go               # 持久化模型（orders 表）
```

分层约定（import 矩阵见设计文档 §3.2，由 `scripts/check-imports.sh` 强制）：

| 层 | 职责 | 依赖限制 |
| --- | --- | --- |
| controller | 实现契约接口，视图↔DTO 映射、状态映射、错误码映射 | 只 import 本模块 `service` 与 HTTP 契约；不碰 `model`/`repository`/`innerapi`/其他模块/`app/io` |
| service | 业务用例、事务、日志；实现本模块 `innerapi`，跨模块调对方 `innerapi` | 出入参用领域视图与普通值；不 import HTTP 契约；跨模块只 import 对方 `innerapi` |
| innerapi | 模块对外发布的纯契约：接口 + 只读视图 + 入参 + 哨兵错误 + 注册名 | 只允许标准库（必要时 `support` 纯类型）；不 import 任何 `app/module`、契约、`app/io` |
| repository | 导出 `OrderRepository` 具体类型与 `ErrNotFound`，返回 `model` | 只 import 本模块 `model` + `app/io` + `support`；状态由参数传入，不 import 契约枚举 |
| model | GORM 实体，映射 `orders` 表 | 不依赖任何 `app/module`、契约、`app/io` |

`module.go` 是装配/接线层，合法 import 本模块各层、其他模块 `innerapi` 与契约路由，
不受上面的分层限制。

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

组合根是 `reload.go` 的 `factories(cfg)`：启动和每次热更新都执行同一份工厂
列表，列表里先新建一份**共享注册表**再传给所有模块工厂：

```go
reg := registry.NewRegistry()
factories := []gofy.ModuleFactory{
    appio.NewModuleFactory(cfg.DB, cfg.Redis, ...),
    product.NewModuleFactory(reg),   // 属主：自登记 product.innerapi
    order.NewModuleFactory(reg),     // 消费方：构造期取 product.innerapi
}
```

工厂顺序即依赖顺序，被依赖方在前：`product` 必须先于 `order` 执行，否则订单
工厂构造期 `registry.Get` 取不到产品契约、启动即报错。注册表按装配周期创建，
新旧两代互不影响；`appio` 工厂负责在 `Start` 时安装 `db.Db` 等句柄。

`order.NewModuleFactory(reg)` 在 `Runtime` 就绪后执行：

1. 检查 `rt.DB`，为空直接返回 `order module requires database`；
2. 从共享注册表按接口取产品契约：
   `registry.Get[productapi.ProductAPI](reg, productapi.Name)`，取不到即报错；
3. 先构造同包协作 service `cfgSvc := service.NewOrderConfigService(rt.Logger)`，再用
   `service.NewOrderService(service.Dependencies{Orders: &repository.OrderRepository{},
   Products: products, Config: cfgSvc, Logger: rt.Logger})` 创建服务——本模块仓库零值
   构造直接注入，跨模块依赖只持有产品 `innerapi` 接口，同包配置 service 用具体类型注入；
4. 把订单自己的 innerapi 登记进同一注册表：`reg.Register(orderapi.Name, svc)`
   （本轮无消费方，保持与产品模块对称）；
5. 调用 `NewModule(svc, rt.Logger)` 包装出模块。

没有集中组合层（原 `app/repository` 已删除），也没有包级 `RepoDeps`：本模块
仓库在工厂里零值构造后经 `Dependencies` 注入，跨模块能力通过对方 `innerapi`
契约取用，名字常量归各自模块的 `innerapi` 包所有。

注册表里的 repository 都是零值可用的无状态类型：工厂执行时 `app/io` 还没
`Start`，`db.Db` 仍是 nil，仓库在每次查询时解析句柄；`Start` 安装新连接后
自动使用新句柄，`Reload` 重新执行工厂得到新注册表。脚本模式不经过模块
生命周期，`script/fix_order.go` 调用前把 `rt.DB` 临时发布到 `appdb.Db`。

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

`service/deps.go` 用一个 `Dependencies` 结构显式声明订单 service 的全部依赖，
构造函数 `NewOrderService(deps Dependencies)` 接收它：

```go
// app/module/order/service/deps.go
type Dependencies struct {
    Orders   *repository.OrderRepository // 本模块仓库，零值构造直接注入
    Products productapi.ProductAPI       // 跨模块产品契约（接口，非实现）
    Config   *OrderConfigService         // 同包协作 service（模块内组合，具体类型）
    Logger   *zap.Logger                 // 允许 nil，退化为 zap.NewNop()
}
```

- **本模块仓库（Orders）**：`*repository.OrderRepository` 具体类型，工厂里
  `&repository.OrderRepository{}` 零值构造后传入。repository 不声明接口——只有
  一种 GORM 实现，接口除了让测试塞 fake 之外没有替换价值，测试改用纯 Go SQLite
  跑真实 SQL；
- **跨模块依赖（Products）**：只持有产品模块发布的 `productapi.ProductAPI`
  **接口**，不 import 产品的 service/repository/model。实例由产品工厂在装配期
  自登记进共享注册表，订单工厂构造期用 `registry.Get[productapi.ProductAPI]`
  取出后注入。service 内部对应 `s.products`，在 `Create` 里调
  `s.products.ListByIDs` / `s.products.DecreaseStock`。
- **同包协作 service（Config）**：`*OrderConfigService` 具体类型，拥有订单模块
  配置读取用例（当前是固定样例）。它和 `OrderService` 在同一个 `service` 包，
  属于**模块内组合**：工厂先构造它再经 `Dependencies` 注入，`OrderService.GetConfig`
  直接委托 `s.config.GetConfig(ctx)`。**不走 innerapi、也不走注册表**——那是跨模块
  的缝；同包结构体本就能直接互相引用，用具体类型即可，与 repository 不声明接口的
  取舍一致。配置没有跨模块消费方，因此不发布到 innerapi；若将来别的模块要读订单
  配置，才把能力加进 `order/innerapi.OrderAPI`（或单独的 `OrderConfigAPI` + 自己的
  `Name`）登记出去。

service 内部：

```go
type OrderService struct {
    orders   *repository.OrderRepository
    products productapi.ProductAPI
    config   *OrderConfigService
    logger   *zap.Logger
}
```

`Create` 先按 SKU 批量查产品契约（`ListByIDs`）确认全部存在且上架，再在同一
事务里对每个 SKU 调 `DecreaseStock` 扣库存、最后落单；产品不存在 / 已下架 /
库存不足都返回 400（`40000`），不会创建订单。产品 innerapi 的哨兵错误由
`translateProductErr` 翻译成订单域错误（见「响应与错误码」）。

约定：

- 字段按聚合命名（`Orders` 而不是 `Repo`），service 内部对应 `s.orders`；
- repository 不声明接口，service 直接依赖 `*repository.OrderRepository`；
- 跨模块只依赖对方 `innerapi` 的接口与只读视图（`productapi.Product`），不
  import 对方的 service/repository/model；持久化模型仍留在属主模块内；
- `innerapi` 是纯契约包：只含接口、只读视图、入参、哨兵错误与 `Name` 常量，
  只允许标准库（必要时 `support` 纯类型），不 import 任何 `app/module`、HTTP
  契约或 `app/io`。它是模块间唯一合法的耦合点；
- 共享注册表按装配周期由组合根新建后传入工厂，service 不得把注册表缓存到包级
  变量或跨请求保存；本模块没有包级默认依赖（`RepoDeps` 已删除），测试自行构造
  `Dependencies` 注入；
- 必填依赖（`Orders` / `Products`）由装配方保证非 nil，`Logger` 允许为 nil；
- 测试用纯 Go 内存 SQLite 跑真实仓库（零值构造，内存库发布成 `appdb.Db`），
  `Products` 注入真实产品服务，不连外部 MySQL（见 `module_test.go`）。

## 产品模块装配与 innerapi

产品模块（`app/module/product`）是订单的跨模块依赖方，结构与订单对称，但本轮
不挂 HTTP 路由：

```
product/
├── module.go                  # 装配 service，自登记 product.innerapi；Register 为空
├── controller/                # 预留的内存示例（仅标准库 + zap），本轮不绑定路由
├── service/
│   ├── product_service.go     # 实现 innerapi.ProductAPI，返回领域视图
│   └── deps.go                # Dependencies{Products, Logger}
├── innerapi/
│   └── innerapi.go            # ProductAPI 接口 + Product 视图 + 错误 + Name
├── repository/
│   └── product_repository.go  # GORM 仓库，DecreaseStock 条件更新返回行数
└── model/
    └── product.go             # 持久化模型（products 表）
```

`product.NewModuleFactory(reg)` 检查 `rt.DB`，用
`service.NewProductService(service.Dependencies{Products: &repository.ProductRepository{},
Logger: rt.Logger})` 构造服务，再 `reg.Register(innerapi.Name, svc)` 自登记。
`svc` 编译期满足 `var _ productapi.ProductAPI = (*ProductService)(nil)`。
`Register` 返回 nil（无路由）——模块存在的意义就是装配 service 并发布 innerapi。

`innerapi.ProductAPI` 发布三个能力：

| 方法 | 用途 | 错误 |
| --- | --- | --- |
| `Get(ctx, id)` | 单个产品视图 | `ErrNotFound` |
| `ListByIDs(ctx, ids)` | 批量产品视图，按 id 索引，缺失 id 不出现 | — |
| `DecreaseStock(ctx, id, num)` | 条件扣库存 | `ErrNotFound` / `ErrUnavailable` / `ErrInsufficientStock` |

`DecreaseStock` 正常路径只跑一条带「上架 + 库存充足」条件的更新 SQL，返回受
影响行数；0 行时补一次 `FindByID` 把「不存在 / 已下架 / 库存不足」区分成对应
哨兵错误。分类读取只用于报错，不影响扣减的原子性，避免先查后改的竞争。
`Product` 视图的 `OnSale bool` 由 service 从持久化状态码换算，消费方不感知产品
的状态码定义。

## 事务

单库（`db.default`）下，repository 的连接解析统一为 `conn(ctx)`：

```go
func (r *OrderRepository) conn(ctx context.Context) *gorm.DB {
    return gofydb.FromContext(ctx, appdb.Db)
}
```

订单 service 的 `Create` 用 `gofydb.Transaction(ctx, appdb.Db, fn)` 开启事务，
事务会放进传给 `fn` 的 context；扣库存与建订单都在 `fn` 里用同一个 ctx，产品
仓库从 ctx 取到同一个事务：

```go
err := gofydb.Transaction(ctx, appdb.Db, func(ctx context.Context) error {
    for _, it := range items {
        if err := s.products.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
            return translateProductErr(it.SkuID, err)
        }
    }
    o, err := s.orders.Create(ctx, &model.Order{
        UserID: userID, Amount: amount, Status: StatusPending,
    })
    if err != nil {
        return err
    }
    created = o
    return nil
})
```

- 嵌套 `Transaction` 复用外层事务，不创建 savepoint；内层错误冒泡到外层，由最外层
  统一回滚，内层成功也不会提前提交；
- 没有事务时 `FromContext` 回退到 `appdb.Db`；ctx 中的事务永远优先；
- 跨模块只读调用（如 `validateItems` 的 `ListByIDs`）可以不显式开事务；需要一致性
  的多步读写（扣库存 + 建订单）必须传入同一个 ctx；
- `Create` 里任一 SKU 扣库存失败或建订单失败，整个事务回滚，已扣的库存一并恢复；
  跨模块事务由 [`service/transaction_test.go`](service/transaction_test.go)
  在内存 SQLite 上覆盖提交、回滚与库存不足分支。

## 日志

controller 和 service 用构造函数注入 `*zap.Logger`，由工厂统一传 `rt.Logger`，
不使用包级全局 logger，也不在函数里临时 `zap.New`。构造函数在 logger 为 `nil`
时退化为 `zap.NewNop()`，测试因此可以完全不关心日志。每个构造函数还会附加
`component` 字段（`order.controller` / `order.service` / `order.config.service`），
便于按层过滤。

repository 不持有 logger：它只包装并返回错误，日志由调用它的 service/module
用 `rt.Logger` 记录一次，避免同一失败重复落日志。

```go
svc := service.NewOrderService(service.Dependencies{
    Orders:   &repository.OrderRepository{},
    Products: products, // 从共享注册表取出的 productapi.ProductAPI
    Config:   service.NewOrderConfigService(rt.Logger),
    Logger:   rt.Logger,
})
h := controller.NewOrderController(svc, rt.Logger)
```

分层记录约定是“一个事件只在一个层记录一次”：

| 层 | 级别 | 记录内容 |
| --- | --- | --- |
| controller | `Error` | 只记录最终映射成 500 的意外错误，带 `zap.Error(err)` 和业务键 |
| service | `Info` / `Debug` | `Info`：创建、取消等状态变更；`Debug`：查询、预期内的 not found |
| repository | — | 不记录日志；错误只用 `fmt.Errorf` 包装后返回，由 service/controller 决定如何记录 |

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
| `40000` | 400 | 下单失败：SKU 不存在 / 已下架 / 库存不足 |
| `40001` | 400 | 参数校验失败，`details` 按字段给出原因 |
| `40100` | 401 | JWT 鉴权失败（仅在配置了 `jwt.secret` 时） |
| `40401` | 404 | 订单不存在 |
| `50000` | 500 | 内部错误；具体原因只写日志，不返回给客户端 |

错误经过两段翻译，controller 只认订单域哨兵错误：

1. **service 内部**：`validateItems` 把产品缺失 / 下架直接返回订单域
   `ErrProductNotFound` / `ErrProductUnavailable`；事务里 `DecreaseStock` 返回的
   产品 innerapi 错误（`productapi.ErrNotFound` / `ErrUnavailable` /
   `ErrInsufficientStock`）由 `translateProductErr` 翻译成对应的订单域错误
   （`ErrProductNotFound` / `ErrProductUnavailable` / `ErrInsufficientStock`），
   都用 `fmt.Errorf("%w: sku %d", ...)` 保留 SKU 上下文；
2. **controller**：
   - `service.ErrOrderNotFound` → `gofyadapter.Error(demoservice.ErrOrderNotFound)`（40401）；
   - `service.ErrProductNotFound` / `ErrProductUnavailable` / `ErrInsufficientStock`
     是客户端错误 → `apperr.BadRequest(err.Error())`（40000），响应里带具体 SKU；
   - 其他错误 → `apperr.Internal(...).WithCause(err)`（50000），响应只保留通用文案；
   - panic 由框架恢复为 `50000`，堆栈和内部信息不会出现在响应里。

产品错误语义的变化被 service 的 `translateProductErr` 吸收，controller 不需要
import 产品 innerapi，也不感知产品的状态码定义。

> 契约里 `CancelOrder` 还声明了 `409 / ErrCodeOrderClosed`，当前 demo 的实现
> 只是把状态更新为 `cancelled`，重复取消仍返回 `204`，不会触发 409。

## 订单状态

service 与 repository 用领域状态常量（`service/order_view.go`）表达订单状态，
不 import HTTP 契约枚举：

| 常量 | 值 | 含义 |
| --- | --- | --- |
| `service.StatusPending` | `pending` | 待支付，创建订单时的初始状态 |
| `service.StatusPaid` | `paid` | 已支付 |
| `service.StatusCancelled` | `cancelled` | 已取消 |

controller 的 `toOrderDTO` 把领域状态字符串转成契约枚举
`orderenum.OrderStatus(status)`，把 `uint64` 转成 `types.Uint64String`。订单 ID、
用户 ID 在契约中都是 `types.Uint64String`，JSON 里以字符串传输，避免 JavaScript
大整数精度丢失。两侧状态取值的一致性由 `controller/order_status_test.go` 的
`TestStatusMappingConsistency` 保证：它断言领域常量个数与 `orderenum.AllOrderStatus`
相等、每个状态的 wire 值一致、且经 `orderenum.OrderStatus(domain)` 往返不变。
契约新增状态而 service 漏加常量（或反之）都会让测试失败，防止两侧漂移。

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
服务只通过 `product.innerapi` 契约读取 `Product` 视图、扣减库存，不直接查产品表，
也不 import 产品的 repository/model。创建订单前 `validateItems` 校验请求里的每个
SKU 存在且上架（`Product.OnSale`），否则返回 `40000`；事务里再对每个 SKU 调
`DecreaseStock`。产品仓库的 `DecreaseStock` 把上架与库存条件写在 SQL 里，返回受
影响行数，0 行由产品 service 补查分类成 `ErrNotFound` / `ErrUnavailable` /
`ErrInsufficientStock`，避免先查后改的竞争。

## 测试

```bash
cd examples/demo-service
go test ./app/module/order/...
```

测试不需要外部数据库：repository 不声明接口，`module_test.go` 用纯 Go 的内存
SQLite（`support/sqlite`，`DSN: ":memory:"`）建订单表和产品表并塞入固定数据
（订单 1、产品 90001 上架 / 90002 下架），把内存库发布成 `appdb.Db`（测试结束
恢复）。`newTestService` 用零值构造的真实订单仓库 + 真实产品 service
（`productservice.NewProductService`，它实现 `innerapi.ProductAPI`）装配
`service.Dependencies`，再通过 `gofytest.New` 启动一个只挂载 order 模块的测试
应用。panic 用例由包装后的 `gorm.ConnPool`（`panicPool`）在驱动层对 `id = 9999`
的查询抛出，验证 Recovery 不泄漏内部信息；`panicPool` 还补了 `BeginTx` 委托给
底层连接池——内嵌的窄接口 `gorm.ConnPool` 不含 `BeginTx`，不补会让走事务的
`Create` 报 `ErrInvalidTransaction`（事务内查询直接用 `*sql.Tx`，panic 注入只
作用于非事务查询）。覆盖内容包括：

- 成功响应的 envelope、`req_id` 及 `X-Trace-ID` 透传；
- 参数校验（非法 id、缺失必填字段、`page_size` 超限）；
- 订单不存在返回 `40401`；
- panic 返回 `50000` 且不泄露内部信息；
- 配置 `jwt.secret` 后的 `40100`、合法 token 的正常路径与错误签名
  （`TestCancelWithJWT`）；
- 创建订单的初始状态为 `pending`，列表分页结果正确；
- 下单时 SKU 不存在或已下架返回 `40000`，不会落单
  （`TestCreateOrderRejectsUnknownOrOffSaleProduct`）；
- 领域状态常量与契约枚举一一对应、往返一致
  （`controller/order_status_test.go` 的 `TestStatusMappingConsistency`）；
- 配置样例由 `OrderConfigService` 拥有、`OrderService.GetConfig` 委托注入的协作
  service（`service/order_config_service_test.go`），controller 只映射视图
  （`controller/order_config_controller_test.go`）；
- 跨模块事务：SQLite 上提交、回滚与库存不足分支
  （`service/transaction_test.go`，覆盖“扣库存 + 建订单”一起回滚）；
- 契约中间件名被解析：分组 `audit` 与 `CreateOrder` 的路由级 `order-audit`
  都落审计日志，且路由级先于分组级完成（`TestContractMiddlewareNamesAreResolved`）；
- 同一个模块装配两次不会撞别名冲突，每次装配 App 都新建中间件注册表
  （`TestModuleRegisterBuildsFreshRegistry`、`app/middleware` 的
  `TestRegisterUsesTheGivenRegistry`）；
- 组合根每次装配新建共享注册表，产品工厂先登记 `product.innerapi`、订单工厂
  构造期取用：连续装配两次不撞名字，缺产品登记或缺数据库时启动失败
  （`TestModuleFactoryBuildsFreshRegistryPerAssembly`）。

`make guards`（根仓库 `make check` 也会调用）运行
`examples/demo-service/scripts/check-imports.sh`，按层强制 import 边界（规则见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md) §10）：

| 层 | 允许的跨包 import | 禁止 |
| --- | --- | --- |
| controller | 本模块 `service`、HTTP 契约 | 任何 `model`/`repository`/`innerapi`、其他模块、`app/io` |
| service | 本模块各层、`app/io`、其他模块 `innerapi` | HTTP 契约、其他模块 service/repository/model |
| innerapi | 标准库、`support` 纯类型 | 任何 `app/module`、契约、`app/io`、框架、第三方 |
| repository | 本模块 `model`、`app/io`、`support` | service/innerapi、契约、其他模块 |
| model | 标准库、`support`、第三方（如 gorm） | 任何 `app/module`、契约、`app/io` |

模块根包（`module.go`）是装配/接线层，不受上表约束。测试文件不参与检查
（`go list` 的 `.Imports` 不含 `_test.go`），可以同时引用多个模块的 model 和
repository 来建 SQLite 库。包依赖成环由 `go build` 兜底（innerapi 无回边，天然
无环），guard 不重复检查。

新增路由时，先在 `gofy-interface-service` 里补契约描述符，再在
`gofyadapter.BindGroup` 的绑定范围内实现 controller 方法即可；路径、校验和
错误声明都不需要在业务模块里重复。契约新增中间件别名时，在 `app/middleware`
里导出对应的 `NameXxx` 常量，并在 `Register` 的 `entries` 里加一行；业务模块
的 `Register` 不用改。新增跨模块依赖时，在对方 `innerapi` 补接口与视图，由对方
工厂自登记，本模块工厂构造期 `registry.Get` 取用——不要 import 对方的
service/repository/model。
