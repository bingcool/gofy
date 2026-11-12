# app/module/order

演示模块：用 `gofy` 的模块机制挂载一组订单 HTTP 路由，同时展示
controller / service / innerapi / repository / model 的五包分层边界。HTTP 契约来自
`gofy/gofy-interface-service`，只在 controller 出现；模块间调用走对方 `innerapi`
契约。分层取舍的权威说明见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md)。

## 目录结构

```
order/
├── module.go                  # 模块根：NewModule 包装订单/配置 service + Register 绑定路由
├── module_test.go             # 基于 gofytest 的接口测试 + 装配测试
├── controller/
│   ├── order_controller.go    # 适配 demoservice.OrdersService 契约，视图↔DTO 映射 + 错误码
│   ├── order_config_controller.go  # 适配 demoservice.ConfigService 契约（OrderConfigController）
│   └── order_status_test.go   # service.Status* ↔ orderenum 状态一致性测试
├── service/
│   ├── order_service.go       # 业务用例，实现 innerapi.OrderAPI，返回领域视图
│   ├── order_config_service.go # OrderConfigService + Config 视图
│   ├── order_view.go          # 领域状态常量 + 订单域哨兵错误 + CreateItem 别名
│   ├── dependencies.go        # Dependencies（仓库 + 产品接口 + Logger）
│   ├── order_config_service_test.go # 配置样例测试
│   └── transaction_test.go    # 跨模块事务用例（内存 SQLite）
├── innerapi/
│   └── innerapi.go            # 订单对外纯契约：OrderAPI 接口 + Order 视图 + CreateItem + 错误
├── repository/
│   └── order_repository.go    # GORM 仓库，导出具体类型（不声明接口），返回 model
└── model/
    └── order.go               # 持久化模型（orders 表）
```

分层约定（import 矩阵见设计文档 §3.2，由 `shell/check-imports.sh` 强制）：

| 层 | 职责 | 依赖限制 |
| --- | --- | --- |
| controller | 实现契约接口，视图↔DTO 映射、状态映射、错误码映射 | 只 import 本模块 `service` 与 HTTP 契约；不碰 `model`/`repository`/`innerapi`/其他模块/`app/io` |
| service | 业务用例、事务、日志；实现本模块 `innerapi`，跨模块调对方 `innerapi` | 出入参用领域视图与普通值；不 import HTTP 契约；跨模块只 import 对方 `innerapi` |
| innerapi | 模块对外发布的纯契约：接口 + 只读视图 + 入参 + 哨兵错误 | 只允许标准库（必要时 `support` 纯类型）；不 import 任何 `app/module`、契约、`app/io` |
| repository | 导出 `OrderRepository` 具体类型与 `ErrNotFound`，返回 `model` | 只 import 本模块 `model` + `app/io` + `support`；状态由参数传入，不 import 契约枚举 |
| model | GORM 实体，映射 `orders` 表 | 不依赖任何 `app/module`、契约、`app/io` |

`module.go` 只保留 `NewModule` / `Register`；仓库与 service 的装配集中在
`app/container`，模块根不再声明仓库或 service 构造入口。

## 路由

完整前缀由契约路由分组提供：`orderroute.RoutePrefix = "/api/v1/order"`，
组内 `Spec.Path` 只写相对路径，模块的 `Prefix()` 返回 `""`，`options.go`
不再设置 `gofy.WithAPIPrefix`。完整路由 = 分组前缀 + `Spec.Path`：

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

`BindGroup` 把一个实现按 `OperationID` 反射绑定到分组的全部路由，因此
`Register` 给 `Orders` 和 `Config` 各传自己的处理器：`OrderController` 实现
`demoservice.OrdersService` 的四个方法，`OrderConfigController` 只实现
`demoservice.ConfigService.GetConfig`。两个分组共享 `RoutePrefix`，但处理器
与依赖互不复用。

测试与线上使用同一套契约分组，因此路径同样是 `/api/v1/order/...`；模块名
`order` 不再参与路由前缀。

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

## 限流

限流完全由契约驱动：契约在四条订单路由上都声明了 `Spec.RateLimit`，其中的
`Limit` / `WindowSeconds` 就是实际生效的阈值，`Key` 决定计数主体，
`RedisAlias` 决定计数存放在哪个 Redis 命名实例（当前都是 `ratelimit`，也是
缺省值，对应 `config.yaml` 的 `redis.ratelimit`）。没有声明 `Spec.RateLimit`
的路由（如 `GetConfig`）不挂限流。

装配根的 `BindOptions()` 与模块无关、所有模块共用，只把中间件包交给
`gofyadapter.BindOptions.RateLimit`；模块根只负责透传：

```go
// app/container/container.go
func (c *Container) BindOptions() gofyadapter.BindOptions {
	return gofyadapter.BindOptions{RateLimit: appmiddleware.RateLimiter{}.Callback()}
}
```

`app/middleware.RateLimiter` 按 `RateKey` 选择计数主体，统一使用框架的
`middleware.RateLimit` + `ratelimit.Store`：

| `RateKey` | 计数主体 | 桶键 |
| --- | --- | --- |
| `user` | `frameworkcontext.UserID`，取不到退回客户端 IP | `user\|<uid>\|<method>\|<route>` |
| `ip`（缺省或未知 Key 同此） | 客户端 IP | `ip\|<ip>\|<method>\|<route>` |
| `api_key` | `X-API-Key` 请求头，缺失退回客户端 IP | `api_key\|<key>\|<method>\|<route>` |
| `route` | 整条路由，不区分调用方 | `route\|\|<method>\|<route>` |

订单契约声明的数值（当前全部是 `RateKeyUser`）：

| 路由 | 数值 |
| --- | --- |
| `GetOrder` | 每用户 120 次/分钟 |
| `ListOrders` | 每用户 100 次/分钟 |
| `CreateOrder` / `CancelOrder` | 每用户 60 次/分钟 |

- 限流中间件装在单条路由中间件链的**最前面**：超额请求在进入其余路由中间件和
  handler 之前就被拒绝。分组中间件（`audit`、`auth`）由 `router.Group` 安装，
  仍然先执行，所以用户级限流能读到已认证身份。
- 计数存储是契约里 `RedisAlias` 指定的 Redis 命名实例（缺省 `ratelimit`），
  中间件在首次请求时用 `redisstore.New(redis.NewIO(ctx, alias))` 构建并缓存：
  路由绑定早于 `app/io` 模块打开 Redis，`Reload` 后新代中间件也会绑定到新代
  连接。解析失败不缓存，下一次请求重试；实例未启用或 Redis 故障时请求返回
  503，不会静默放行。
- 契约里的 `Limit` / `WindowSeconds` 非法时中间件返回 503，不会静默放行。
- 超限返回 `429 / 42900`，响应带 `X-RateLimit-Limit`、`X-RateLimit-Remaining`、
  `X-RateLimit-Reset` 和 `Retry-After`。

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

组合根是 `app/container`。`reload.go` 的 `factories(cfg)` 在启动和每次热更新时
各新建一个容器，把仓库与 service 的构造收敛到一处：

```go
ctr := container.NewContainer()
factories := []gofy.ModuleFactory{
    appio.NewModuleFactory(cfg.DB, cfg.Redis, ...),
}
factories = append(factories, ctr.ModuleFactories()...)
```

`container.NewContainer()` 零值构造本次服务的全部仓库（`RepositoryDeclare`），service
则在模块工厂首次取用时按依赖链惰性构造；每个服务一个构造槽，只构建自己需要的
子树：

1. `ProductService`（使用本模块仓库 `ProductsRepo`，只被产品访问器或订单访问器触发）；
2. `OrderConfigService`（无外部依赖，由模块根直接注入 `OrderConfigController`）；
3. `OrderService`：首次取用时解析
   `Dependencies{OrderRepository, ProductAPI, Logger}`，其中 `ProductAPI` 是本次服务
   的容器按需构造的产品 service，以 `productapi.ProductAPI` 接口传入。

`ModuleFactories()` 返回 `ProductModuleFactory()` 与 `OrderModuleFactory()`；
每个工厂检查 `rt.DB`，调用 `c.Init(rt.Logger)` 记录本次服务的 logger，再取本模块
service 交给模块根绑定路由。**模块工厂的排列顺序不承载依赖顺序**：订单工厂
单独执行时也会先构造产品 service；未被挂载的能力不会因为别的模块启动而被构建。
启动和每次 `Reload` 各用一个新容器，旧的模块实例继续服务在途请求，代际之间
没有共享的包级状态。

`app/container` 同时集中声明仓库：

```go
type RepositoryDeclare struct {
    OrdersRepo   *orderrepo.OrderRepository
    ProductsRepo *productrepo.ProductRepository
}
```

仓库无状态、按查询解析连接，零值构造后在本次服务内复用；构造槽只生效一次
（每个服务一个 `sync.Once`，重复取用返回同一实例）。构造期间再次取用同一个槽
会被判定为依赖成环，直接 panic 并带上服务名，不会让 `sync.Once` 静默死锁；装配
只在启动期串行执行，不支持并发的首次取用。工厂执行时 `app/io` 还没 `Start`，
`db.Db` 仍是 nil，仓库在每次查询时解析句柄；`Start` 安装新连接后自动使用新句柄。
脚本模式不经过模块生命周期，`script/fix_order.go` 调用前把 `rt.DB` 临时发布到
`appdb.Db`。

service 包不能反向 import 模块根或容器（import 方向是
`container → module → service`），所以依赖只在构造期由容器显式传入。没有包级
`RepoDeps`、没有 module getter、也没有 innerapi 单例槽：`innerapi` 只保留纯
契约，跨模块耦合点仍是 `productapi.ProductAPI` 接口。制造新的服务时，在
`container/module_product.go` 或 `container/module_order.go` 里加一个访问器即可，
装配面不会随着模块数量线性膨胀成一个巨大的 `Build`。

访问器文件按模块命名：`module_<module>.go`（当前 `module_product.go`、
`module_order.go`），与 `module_factories.go`、模块根的 `module.go` 保持同一条
命名线索。后续新增模块沿用这个规则，不要用 `product.go` / `order.go` 这类裸模块名，
以免和模块内同名文件（如 `model/product.go`、`model/order.go`）混淆。

`Register` 只创建 controller（传入 logger），遍历 `orderroute.Orders` 与
`orderroute.Config` 调用 `gofyadapter.BindGroup`，不再创建或注入中间件注册表。
绑定选项来自装配根：`container.BindOptions()` 生成按契约声明工作的限流回调（所有
模块共用同一份），`order.NewModule` 只把它原样交给两次 `BindGroup`。

契约里的中间件只声明别名，认证也不例外：`Orders` 分组声明了
`group.Use("audit", "auth")`，`Config` 分组声明了 `group.Use("auth")`，
`CreateOrder` 另外声明了 `spec.Middleware = []string{"order-audit"}`。审计名字
映射到 `app/middleware.Audit(logger, scope)`（`scope` 区分 `group`/`route`），
`auth` 映射到 `app/middleware.Auth`（内部调用框架
`support/middleware.JWTAuth`）。`spec.Auth = AuthRequired` 只用于 OpenAPI 的
security 标记，adapter 不会因此自动附加认证中间件。审计中间件在 handler 之后
记录方法、路由模板、状态码和 `req_id`，与框架的 `AccessLog` 互补。执行顺序：
`分组 audit → 分组 auth → RateLimit（契约声明时）→ BindOptions.Global → 路由 order-audit → Routes[OperationID]`。

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
module := order.NewModule(myService, myConfigService, zap.NewNop(), gofyadapter.BindOptions{})
app := gofytest.New(t,
    gofy.WithMiddlewareRegistrar(testRegistrar(zap.NewNop(), middleware.JWTConfig{})),
    gofy.WithModules(module),
)
```

## 服务依赖

`service/dependencies.go` 用一个 `Dependencies` 结构显式声明订单 service 的全部输入，
构造函数 `NewOrderService(deps Dependencies)` 接收它，实例由 `app/container` 的
`OrderService()` 访问器按需构造：

```go
// app/module/order/service/dependencies.go
type Dependencies struct {
    OrderRepository *repository.OrderRepository // 本模块仓库，来自 RepositoryDeclare
    ProductAPI      productapi.ProductAPI       // 跨模块产品契约，由容器注入本次服务的产品 service
    Logger          *zap.Logger                 // 允许 nil，退化为 zap.NewNop()
}
```

- **本模块仓库（OrderRepository）**：`*repository.OrderRepository` 具体类型，由
  `container.RepositoryDeclare` 零值构造后传入。repository 不声明接口——只有一种
  GORM 实现，接口除了让测试塞 fake 之外没有替换价值，测试改用纯 Go SQLite 跑
  真实 SQL；
- **跨模块依赖（ProductAPI）**：通过 `Dependencies.ProductAPI` 以
  `productapi.ProductAPI` **接口**注入。容器把本次服务刚构造的 `ProductService`
  直接传进来，service 不 import 产品的 service/repository/model，也不在构造期做
  运行期查找。`Create` 里调 `s.productAPI.ListByIDs` /
  `s.productAPI.DecreaseStock`。
- **配置 service（不经过 `OrderService`）**：`OrderConfigService` 是 service 包内
  的独立类型，拥有订单模块配置读取用例（当前是固定样例），由容器构造后交给模块根
  注入 `OrderConfigController`；`OrderService` 不持有它。消费方直接依赖具体类型，
  **不走 innerapi**——那是跨模块的缝。配置没有跨模块消费方，因此不发布到
  innerapi；若将来别的模块要读订单配置，才把能力加进
  `order/innerapi.OrderAPI`。

service 内部：

```go
type OrderService struct {
    orderRepository *repository.OrderRepository
    productAPI      productapi.ProductAPI
    logger          *zap.Logger
}
```

`Create` 先按 SKU 批量查产品契约（`ListByIDs`）确认全部存在且上架，再在同一
事务里对每个 SKU 调 `DecreaseStock` 扣库存、最后落单；产品不存在 / 已下架 /
库存不足都返回 400（`40000`），不会创建订单。产品 innerapi 的哨兵错误由
`translateProductErr` 翻译成订单域错误（见「响应与错误码」）。

约定：

- 字段按角色命名：`OrderRepository` / `ProductAPI`，service 内部对应
  `s.orderRepository` / `s.productAPI`；
- repository 不声明接口，service 直接依赖 `*repository.OrderRepository`；
- 跨模块只依赖对方 `innerapi` 的接口与只读视图（`productapi.Product`），不
  import 对方的 service/repository/model；持久化模型仍留在属主模块内；
- `innerapi` 是纯契约包：只含接口、只读视图、入参和哨兵错误，只允许标准库
  （必要时 `support` 纯类型），不 import 任何 `app/module`、HTTP 契约或
  `app/io`。它是模块间唯一合法的耦合点；
- 依赖只在构造期由 `app/container` 注入：service 不持有包级状态，也不在运行期
  查找其他 service；代际切换由新建容器完成；
- 必填依赖（`Orders`、`Products`）由容器保证非 nil；`Logger` 允许为 nil；
- 测试用纯 Go 内存 SQLite 跑真实仓库（零值构造，内存库发布成 `appdb.Db`），
  通过 `container.NewContainer()` + `Init` 按需装配 service，不连外部 MySQL
  （见 `module_test.go` 与 `app/container/*_test.go`）。

## 产品模块装配与 innerapi

产品模块（`app/module/product`）是订单的跨模块依赖方，结构与订单对称，但本轮
不挂 HTTP 路由：

```
product/
├── module.go                  # 模块根：NewModule/Register（无路由）
├── controller/                # 预留的内存示例（仅标准库 + zap），本轮不绑定路由
├── service/
│   ├── product_service.go     # 实现 innerapi.ProductAPI，返回领域视图
│   └── deps.go                # Dependencies{Products, Logger}
├── innerapi/
│   └── innerapi.go            # ProductAPI 接口 + Product 视图 + 错误
├── repository/
│   └── product_repository.go  # GORM 仓库，DecreaseStock 条件更新返回行数
└── model/
    └── product.go             # 持久化模型（products 表）
```

`app/container` 的 `ProductService()` 访问器用本次服务的 `RepositoryDeclare.ProductsRepo`
按需构造 `ProductService`；订单访问器再把它作为 `Dependencies.Products` 传入。
`svc` 编译期满足 `var _ productapi.ProductAPI = (*ProductService)(nil)`。
`Register` 返回 nil（无路由）——产品模块的价值是提供 innerapi 实现，装配和
跨模块注入都由容器完成，没有包级发布或名字登记。

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
    return gofydb.WithContext(ctx, appdb.Db)
}
```

订单 service 的 `Create` 用 `gofydb.Transaction(ctx, appdb.Db, fn)` 开启事务，
事务会放进传给 `fn` 的 context；扣库存与建订单都在 `fn` 里用同一个 ctx，产品
仓库从 ctx 取到同一个事务：

```go
err := gofydb.Transaction(ctx, appdb.Db, func(ctx context.Context) error {
    for _, it := range items {
        if err := s.productAPI.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
            return translateProductErr(it.SkuID, err)
        }
    }
    o, err := s.orderRepository.Create(ctx, &model.Order{
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
- 没有事务时 `WithContext` 回退到 `appdb.Db`；ctx 中的事务永远优先；
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
ctr := container.NewContainer()
ctr.Init(rt.Logger)
h := controller.NewOrderController(ctr.OrderService(), rt.Logger)
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
| `42900` | 429 | 触发路由限流（只作用于契约声明了 `RateLimit` 的路由） |
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
恢复）。`newTestServices` 通过 `container.NewContainer()` + `Init` 按需构造
产品、订单与配置 service，再用 `gofytest.New` 启动一个只挂载 order 模块的测试应用。
panic 用例由包装后的 `gorm.ConnPool`（`panicPool`）在驱动层对 `id = 9999`
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
- 配置样例由 `OrderConfigService` 拥有（`service/order_config_service_test.go`），
  `OrderConfigController` 直接读取它并只做视图↔DTO 映射
  （`controller/order_config_controller_test.go`）；
- 跨模块事务：SQLite 上提交、回滚与库存不足分支
  （`service/transaction_test.go`，覆盖“扣库存 + 建订单”一起回滚）；
- 契约中间件名被解析：分组 `audit` 与 `CreateOrder` 的路由级 `order-audit`
  都落审计日志，且路由级先于分组级完成（`TestContractMiddlewareNamesAreResolved`）；
- 同一个模块装配两次不会撞别名冲突，每次装配 App 都新建中间件注册表
  （`TestModuleRegisterBuildsFreshRegistry`、`app/middleware` 的
  `TestRegisterUsesTheGivenRegistry`）；
- 限流由契约驱动：`ListOrders` 声明 100 次/分钟，第 101 次请求返回 `42900`，
  `GetOrder` 有独立额度；`app/middleware` 逐个覆盖 `user`/`ip`/`api_key`/`route`
  四种计数主体（`TestRateLimitFollowsContract`、`TestRateLimiter*`、
  `TestBindOptionsRateLimitFollowsContract`）；
- 容器装配：访问器按需构建产品 / 订单配置 / 订单 service，重复取用不替换实例；
  产品访问器只构建产品子树；每个装配代用独立容器得到不同实例；缺数据库时模块
  工厂启动失败；订单工厂先于产品工厂执行也能工作；构造期间重入同一槽会 panic
  报出成环（`app/container/*_test.go`）。

`make guards`（根仓库 `make check` 也会调用）运行
`examples/demo-service/shell/check-imports.sh`，按层强制 import 边界（规则见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md) §10）：

| 层 | 允许的跨包 import | 禁止 |
| --- | --- | --- |
| controller | 本模块 `service`、HTTP 契约 | 任何 `model`/`repository`/`innerapi`、其他模块、`app/io` |
| service | 本模块各层、`app/io`、其他模块 `innerapi` | HTTP 契约、其他模块 service/repository/model |
| innerapi | 标准库、`support` 纯类型 | 任何 `app/module`、契约、`app/io`、框架、第三方 |
| repository | 本模块 `model`、`app/io`、`support` | service/innerapi、契约、其他模块 |
| model | 标准库、`support`、第三方（如 gorm） | 任何 `app/module`、契约、`app/io` |

模块根包（`module.go`）只做路由绑定，不受上表约束；装配根 `app/container`
位于 `app/module` 之外，同样不在 guard 检查范围内，是唯一同时 import 多个模块
的地方。测试文件不参与检查
（`go list` 的 `.Imports` 不含 `_test.go`），可以同时引用多个模块的 model 和
repository 来建 SQLite 库。包依赖成环由 `go build` 兜底（innerapi 无回边，天然
无环），guard 不重复检查。

新增路由时，先在 `gofy-interface-service` 里补契约描述符，再在
`gofyadapter.BindGroup` 的绑定范围内实现 controller 方法即可；路径、校验和
错误声明都不需要在业务模块里重复。契约新增中间件别名时，在 `app/middleware`
里导出对应的 `NameXxx` 常量，并在 `Register` 的 `entries` 里加一行；业务模块
的 `Register` 不用改。新增跨模块依赖时，在对方 `innerapi` 补接口与视图，由
`app/container` 构造实现并通过 `Dependencies` 注入本模块 service——不要
import 对方的 service/repository/model。
