# 模块分层与跨模块 InnerAPI 调用设计

- 状态：已确认（2026-11-11）；P1–P7 已落地（2026-11-11）。装配方式于 2026-11-11
  收敛到 `app/container`：每代新建容器，构造期显式注入仓库与 service，跨模块只传
  innerapi 接口；不再有包级服务单例、module getter 或工厂顺序约束。每个模块固定
  `controller / service / innerapi / repository / model`，跨模块只走对方 innerapi。
- 范围：`examples/demo-service/app/module/{order,product}` 的分层（新增 `innerapi`）、
  装配方式、跨模块调用；`examples/demo-service/shell/check-imports.sh` 规则；
  `gofy/support/registry` 仍作为可选的「名字 → 实例」工具存在，demo 不再使用
- 关联：[repository-composition-design.md](./repository-composition-design.md)（本文取代其
  §5.3–§5.6 的取用模式与 §8 的 import 规则）、[library-design.md](./library-design.md) §9
- 一句话目标：每个模块固定 `controller / service / innerapi / repository / model`，
  模块内调用链 `controller → service → repository → model` 单向向下；**模块之间一律
  通过对方的 `innerapi` 调用，不再直接依赖对方 service**（`order.service →
  product.innerapi`）。repository 与 model 永远不出模块。

## 1. 改造前问题（带证据，2026-11-11 之前）

四包目录本身没问题，问题出在「装配错位」「层间类型泄漏」和「跨模块没有稳定契约」：

1. **跨模块依赖落在 repository 层，绕过了对方业务层。**
   [order_service.go:99](../examples/demo-service/app/module/order/service/order_service.go#L99)
   直接 `registry.Get[*productrepo.ProductRepository]` 取产品仓库做校验。订单因此
   依赖产品的持久化实现（GORM、`ProductView` 字段、`StatusOnSale` 常量），产品改表
   就会打断订单。

2. **即便改成 service→service，消费方仍依赖对方的具体实现包。**
   上一版方案让 `order.service` 直接 import `product/service` 并持有
   `*productservice.ProductService`。消费方与提供方的 service 实现强耦合：产品 service
   的构造函数、内部方法、所在包路径任何变化都会波及订单。模块间缺一层**稳定的发布契约**。

3. **消费方替属主装配别人的仓库。**
   [module.go:42](../examples/demo-service/app/module/order/module.go#L42) 由订单工厂
   `new` 产品仓库并登记 `product.products`。产品模块自己没有装配入口，属主与装配方倒置。

4. **service / repository 被 HTTP 契约绑架。**
   [order_service.go:13-15](../examples/demo-service/app/module/order/service/order_service.go#L13)
   import `orderdto` / `orderenum` / `types` 并直接返回 `orderdto.OrderDTO`；
   [order_repository.go:11](../examples/demo-service/app/module/order/repository/order_repository.go#L11)
   也 import `orderenum`。业务层与持久层依赖 HTTP 契约，换协议就要改 service。

5. **注册表是字符串查找 + 隐式依赖 + 各自为政。**
   每个工厂 `registry.NewRegistry()` 各建一份，消费方按名取用，依赖不可见、谁忘了
   登记就请求期才报错。

6. **包级 `RepoDeps` 是全局可变状态，且 product 模块不完整。**
   [a_init_repository.go](../examples/demo-service/app/module/order/service/a_init_repository.go)
   把默认仓库集合放到包级变量；产品模块没有 `module.go`、controller 是未接线的内存
   假数据、service 无人构造，跨模块调用无从装配。

## 2. 目标与非目标

目标：

- 模块内固定分层，调用链单向向下，层间类型边界可被 import guard 强制；
- **新增 `innerapi` 作为模块对外的发布契约**：跨模块只依赖对方 `innerapi`，不依赖
  对方 service / repository / model；
- 依赖显式、编译期类型安全、装配期失败即报错；装配归属主模块；
- service 可脱离 HTTP 契约被脚本 / 其他模块复用；
- 事务、热更新、cgo-free 测试等既有能力不退化。

非目标：

- 不改 `gofy` 框架的 `ModuleFactory` / `Runtime` / `support/registry` API；
- 不引入跨库事务（仍单库 `db.default`）；
- 本轮只做 innerapi 装配，不改 product 的 HTTP 面。后续迭代已按同一契约模式补齐
  产品只读路由（`productroute.Products`，前缀 `/api/v1/product`），controller 因此
  从内存示例改为契约适配器；见 §4 目录树与 §11 变更记录。
- 不重新引入集中式 `app/repository` 组合层。

## 3. 分层契约

### 3.1 五包职责

| 包 | 职责 | 输入类型 | 输出类型 | 禁止 |
| --- | --- | --- | --- | --- |
| `controller` | 契约 ↔ service 映射、错误→HTTP 码、记录意外错误 | 契约 request | 契约 response/DTO | import model / repository / innerapi / 其他模块任何包 |
| `service` | 业务规则、用例编排、事务边界、日志；实现本模块 innerapi | 本模块 model + plain 入参 + 其他模块 innerapi | 本模块 innerapi 视图 | import HTTP 契约；import 其他模块 service / repository / model |
| `innerapi` | **模块对外发布的内部 API 契约**：接口 + 只读视图 + 哨兵错误 | — | — | import 任何模块的 service / repository / model / controller、HTTP 契约、app/io |
| `repository` | 单聚合数据访问（GORM） | 本模块 model | 本模块 model | import service / innerapi / 契约 / 其他模块任何包 |
| `model` | 表结构定义 | — | — | 任何业务依赖（仅 `time` / gorm tag） |

`innerapi` 是**纯契约包**：只有接口、plain 视图和哨兵错误，不 import 本模块
service/repository/model，也不 import HTTP 契约。消费方 import 它时不会牵连提供方
的实现细节。本模块 service 实现 innerapi 接口，由装配根（`app/container`）以接口
注入消费方（service → innerapi 单向依赖）。

### 3.2 import 矩阵（√ 允许，× 禁止）

| 调用方 \ 被调方 | 本模块 model | 本模块 repository | 本模块 service | 本模块 innerapi | 其他模块 innerapi | 其他模块 service/repository/model | HTTP 契约 | app/io 句柄 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| controller | × | × | √ | × | × | × | √ | × |
| service | √ | √ | — | √（实现） | √（调用） | × | × | √（开事务） |
| innerapi | × | × | × | — | × | × | × | × |
| repository | √ | — | × | × | × | × | × | √（解析连接） |
| model | — | × | × | × | × | × | × | × |

关键变化：**跨模块只允许 `service → 其他模块 innerapi`**；service 不再 import 其他
模块 service/repository/model，也不再 import HTTP 契约。依赖图无环由 `go build` 兜底
（innerapi 是纯契约、无回边，天然不会成环）。

### 3.3 类型边界

- `model` 是 GORM 持久化模型，**只在 repository ↔ service 之间流动，永不跨模块**；
- repository 统一返回 `model`（产品现在的 `ProductView` 从 repository 迁出）；
- **跨模块视图与错误定义在 `innerapi`**（plain struct，无 gorm tag、无契约类型），
  既是 service 的出参，也是模块间契约；
- 领域状态常量归 service（如 `service.StatusPending`），repository 方法接收状态字符串
  参数，不再 import `orderenum`；契约 `orderenum` 只在 controller 出现，由 controller
  做 `service.Status* ↔ orderenum.OrderStatus` 映射，并用测试保证两侧一致（§11.4）。

### 3.4 为什么 innerapi 用接口，而 repository 不用

repository 只有一种 GORM 实现、没有替换价值，接口是纯开销，故用具体类型 + 真实
SQLite 测试（沿用既有决策）。innerapi 不同：它是**模块间的发布契约/防腐层**，接口的
价值在于让消费方依赖稳定契约而非提供方实现、隔离提供方内部重构、并允许测试替换。
这是接口真正赚取收益的地方，因此 innerapi 用接口，service 提供实现。

## 4. 目标目录

```text
examples/demo-service/app/
├── container/                      # 装配根（不在 module 目录内）
│   ├── container.go                # Container：NewContainer / Init / logger
│   ├── lazy_value.go               # lazyValue[T]：惰性构造槽 + 成环检测
│   ├── module_product.go           # ProductService 访问器
│   ├── module_order.go             # OrderConfigService / OrderService 访问器
│   ├── module_factories.go         # ProductModuleFactory / OrderModuleFactory
│   ├── repository_declare.go       # RepositoryDeclare{OrdersRepo, ProductsRepo}
│   └── service_declare.go          # ServiceDeclare{ProductService, OrderConfigService, OrderService}
└── module/
    ├── order/
    │   ├── module.go               # 模块根：NewModule + Register（service 由容器注入）
    │   ├── controller/
    │   │   └── order_controller.go # 契约映射 + 错误→HTTP 码（唯一 import HTTP 契约的层）
    │   ├── service/
    │   │   ├── order_service.go    # 用例编排、事务、日志；跨模块调 product.innerapi
    │   │   ├── order_view.go       # 领域状态常量 + 订单域哨兵错误 + CreateItem 入参
    │   │   └── dependencies.go     # Dependencies（仓库 + 产品接口 + Logger）
    │   ├── innerapi/
    │   │   └── innerapi.go         # 订单对外契约：OrderAPI 接口 + Order 视图 + 错误
    │   ├── repository/
    │   │   └── order_repository.go # GORM，返回 model；状态由参数传入
    │   └── model/
    │       └── order.go            # 表结构
    └── product/
        ├── module.go               # 模块根：NewModule/Register；Prefix() 返回 ""
        ├── controller/
        │   └── product_controller.go # 实现 demoservice.ProductsService，契约 ↔ service 映射
        ├── service/
        │   ├── product_service.go  # 实现 innerapi.ProductAPI；读取 + 分页 + 扣库存；0 行分类
        │   └── dependencies.go      # Dependencies（本模块仓库 + Logger）
        ├── innerapi/
        │   └── innerapi.go         # ProductAPI 接口 + Product 视图 + 错误
        ├── repository/
        │   └── product_repository.go # GORM，返回 model（ProductView 迁出）
        └── model/
            └── product.go
```

`a_init_repository.go` / 包级 `RepoDeps` 删除，由 `service/dependencies.go` 的 `Dependencies`
结构取代。仓库与 service 的实例化集中在 `app/container`，模块根只保留
`NewModule` / `Register`。每个模块都拥有自己的 `innerapi`（即使本轮只有 product
被跨模块调用，order 也预留契约，保持对称）。product 同时通过 controller 暴露
只读 HTTP 路由，跨模块调用仍只走 innerapi，两条出口互不依赖。

## 5. 调用链详解

### 5.1 模块内：controller → service → repository → model

controller 是唯一 import HTTP 契约的层，只做三件事：契约入参 → service 入参、service
view → 契约 DTO、service 错误 → HTTP 码。

```go
// controller/order_controller.go
func (c *OrderController) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
    items := make([]service.CreateItem, 0, len(req.Items))
    for _, it := range req.Items {
        items = append(items, service.CreateItem{SkuID: it.SkuID.Uint64(), Num: it.Num})
    }
    o, err := c.orderService.Create(ctx, req.UserID.Uint64(), req.Amount, items)
    if err != nil {
        switch {
        case errors.Is(err, service.ErrProductNotFound),
            errors.Is(err, service.ErrProductUnavailable),
            errors.Is(err, service.ErrInsufficientStock):
            return nil, apperr.BadRequest(err.Error())
        default:
            c.logger.Error("create order failed", zap.Error(err))
            return nil, apperr.Internal("create order failed").WithCause(err)
        }
    }
    return &orderresponse.CreateOrderResponse{Order: toOrderDTO(o)}, nil
}
```

service 编排用例、开事务、记日志；repository 只做数据访问、返回 model；service 把
model 转成 innerapi 视图上抛。`conn(ctx)` 维持 `gofydb.WithContext(ctx, appdb.Db)`，
repository 无状态、零值可用。

**模块内 service → service（同包协作）**：同一个 `service` 包里可以有多个 service
结构体。若一个用例需要组合同包另一个 service，这是**模块内组合**，由装配根
`app/container` 统一实例化：访问器先解析被依赖者，再经 `Dependencies` 把具体类型
注入，消费方方法里直接调用（如 `s.config.GetConfig(ctx)`）并传同一个 ctx。
**不走 innerapi**——innerapi 是跨模块的缝。当前订单模块没有这种组合：
`OrderConfigService` 独立服务 `Config` 分组，`OrderService` 不持有它。

Go 的 import 方向是 `container → order(module) → service`，`service` 包不能反向
import 模块根或容器（会成环），因此 service 之间的组合只能在装配期由容器完成；
同包内若确需直接构造，也只能用 `service` 包自己的构造函数。取舍与 repository
相同：只有一种实现，不声明接口；若出现双向依赖，应抽出共享逻辑或改单向，而不是
靠包级单例硬解。只有当**别的模块**也要调这个能力时，才把它加进本模块 innerapi，
并在容器装配新消费方时以接口注入（如订单配置当前无跨模块消费方，就不发布到
innerapi；`OrderConfigController` 直接依赖 `OrderConfigService` 取领域视图再映射
契约）。

### 5.2 模块间：service → 对方 innerapi

`order.service` 持有 `product.innerapi.ProductAPI`（接口），调用就是普通方法调用；
不 import `product/service`、`product/repository`、`product/model`。提供方内部如何
实现（service → repository → model）对消费方完全透明。

## 6. InnerAPI 设计

### 6.1 纯契约包

```go
// product/innerapi/innerapi.go
// Package innerapi 是产品模块对其它模块发布的内部 API 契约：接口、只读视图、
// 哨兵错误，不 import 本模块 service/repository/model，也不 import HTTP 契约。
package innerapi

import (
    "context"
    "errors"
)

// Product 是跨模块只读视图：plain struct，不含 model / gorm / 契约类型。
type Product struct {
    ID     uint64
    Name   string
    Price  int64
    Stock  int
    OnSale bool
}

var (
    ErrNotFound          = errors.New("product not found")
    ErrUnavailable       = errors.New("product unavailable")
    ErrInsufficientStock = errors.New("product stock insufficient")
)

// ProductAPI 是产品模块对外发布的契约。消费方只依赖这个接口。
type ProductAPI interface {
    Get(ctx context.Context, id uint64) (*Product, error)
    ListByIDs(ctx context.Context, ids []uint64) (map[uint64]Product, error)
    DecreaseStock(ctx context.Context, id uint64, num int) error
}
```

`innerapi` 只保存接口、视图和哨兵错误；实现由装配根 `app/container` 构造后以接口
注入消费方。发布时机归容器，消费方不感知实现类型，也没有包级可变状态。

### 6.2 service 实现契约

本模块 service 实现 innerapi 接口，出参即 innerapi 视图；业务规则仍留在 service，
innerapi 只是发布面，不重复实现逻辑。

```go
// product/service/product_service.go
import productapi "github.com/yourorg/demo-service/app/module/product/innerapi"

// 编译期保证 service 满足发布契约。
var _ productapi.ProductAPI = (*ProductService)(nil)

type Dependencies struct {
    Products *repository.ProductRepository // 本模块仓库，必填
    Logger   *zap.Logger                   // 允许 nil
}

// 返回前不发布任何全局状态：实例由 app/container 持有并注入消费方。
func NewProductService(deps Dependencies) *ProductService

func (s *ProductService) Get(ctx context.Context, id uint64) (*productapi.Product, error)
func (s *ProductService) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]productapi.Product, error)
func (s *ProductService) DecreaseStock(ctx context.Context, id uint64, num int) error
```

`DecreaseStock` 把仓库的「0 行」分类成 innerapi 业务错误（§8.2）。

### 6.3 取用机制对比

| 方案 | 消费方依赖 | 类型安全 | 装配失败时机 | 结论 |
| --- | --- | --- | --- | --- |
| 取对方 repository（更早） | 持久化实现 | 运行期断言 | 请求期/装配期 | 废弃：绕过业务层 |
| 取对方 service（上一版） | 具体 service 包 | 编译期 | 装配期 | 废弃：耦合实现包 |
| 名字注册表 + `Get[T]` | 纯契约接口 | `Get[T]` 编译期 | 装配期 | 可选：名字是字符串，样板偏多 |
| **app/container 构造期注入（当前）** | **纯契约接口** | **编译期接口** | **装配期** | **采用：无包级状态、顺序无关** |

当前做法：`app/container` 每代新建容器，每个 service 一个惰性构造槽，首次取用时
按依赖链构建，产品实现以 `ProductAPI` 接口直接传给订单 service。运行期调用仍是普通
方法调用，无字符串、无每请求查找；也不需要名字常量、注册表、单例槽或工厂排序。该
容器是 demo 业务代码，不是框架内建的 DI；框架本身仍只提供 `Runtime` 与工厂/钩子。

### 6.4 装配根每代新建容器

```go
// app/container/container.go
type Container struct {
    repos    RepositoryDeclare
    services ServiceDeclare

    loggerMu  sync.Mutex
    zapLogger *zap.Logger
}

func (c *Container) Init(logger *zap.Logger) { /* 记录本次服务的 logger，首次生效 */ }
```

```go
// app/container/module_product.go / module_order.go
func (c *Container) ProductService() *productservice.ProductService {
    return c.services.ProductService.Get(func() *productservice.ProductService {
        return productservice.NewProductService(productservice.Dependencies{
            Products: c.repos.ProductsRepo, Logger: c.logger(),
        })
    })
}

func (c *Container) OrderService() *orderservice.OrderService {
    return c.services.OrderService.Get(func() *orderservice.OrderService {
        return orderservice.NewOrderService(orderservice.Dependencies{
            OrderRepository: c.repos.OrdersRepo,
            ProductAPI:      c.ProductService(), // 按需构建产品子树
            Logger:          c.logger(),
        })
    })
}
```

`app/container/lazy_value.go` 的 `lazyValue[T]` 负责“首次构建、之后复用”：每个槽
自己持有一个 `sync.Once` 和一个 `building` 标记；构建期间再次取用同一个槽说明
装配图成环，直接 panic 并带上服务名，而不是让 `sync.Once` 静默死锁。装配只在启动
期串行执行，不支持并发的首次取用，因此成环检测不需要 goroutine 标识。

```go
// reload.go
func (r *reloader) factories(cfg *config.Config) []gofy.ModuleFactory {
    ctr := container.NewContainer()
    factories := []gofy.ModuleFactory{
        appio.NewModuleFactory(cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt),
    }
    factories = append(factories, ctr.ModuleFactories()...)
    if r.watch { /* 追加 watcher 模块，不变 */ }
    return factories
}
```

启动和每次 `Reload` 各执行一次 `factories(cfg)`，也就是各新建一个容器。service 在
模块工厂首次取用时按依赖链构建，产品实现直接作为 `Dependencies.ProductAPI` 传入；
模块工厂的排列顺序不承载依赖顺序，未被挂载的能力也不会因为别的模块启动而被构建。
旧模块继续服务在途请求，代际之间没有共享的包级状态。仓库声明也归容器：

```go
// app/container/repository_declare.go
type RepositoryDeclare struct {
    OrdersRepo   *orderrepo.OrderRepository
    ProductsRepo *productrepo.ProductRepository
}
```

### 6.5 消费方通过 `Dependencies` 接收接口

```go
// order/service/dependencies.go
type Dependencies struct {
    OrderRepository *repository.OrderRepository // 本模块仓库
    ProductAPI      productapi.ProductAPI       // 跨模块接口，由容器注入本次服务的产品 service
    Logger          *zap.Logger                 // 允许 nil
}

func NewOrderService(deps Dependencies) *OrderService {
    return &OrderService{
        orderRepository: deps.OrderRepository,
        productAPI:      deps.ProductAPI,
        logger:          ...,
    }
}
```

`OrderService` 只 import `product/innerapi` 的接口与视图，不 import 产品的
service/repository/model；运行期调用 `s.productAPI.ListByIDs/DecreaseStock` 是普通
接口方法调用。

### 6.6 模块根只绑定路由

```go
// order/module.go
type Module struct {
    orderService       *service.OrderService
    orderConfigService *service.OrderConfigService
    logger             *zap.Logger
}

func NewModule(orderService *service.OrderService, orderConfigService *service.OrderConfigService, logger *zap.Logger) *Module { ... }
func (m *Module) Name() string { return "order" }
func (m *Module) Register(r *support.Router) error { /* 每个分组 BindGroup 到自己的 handler */ }
```

`app/container.ModuleFactories()` 返回的工厂检查 `rt.DB`、调用 `Init(rt.Logger)`
记录日志器，再用 `order.NewModule(ctr.OrderService(), ctr.OrderConfigService(), rt.Logger)`
包装；订单 service 的取用会先解析产品与配置 service。`BindGroup` 把一个实现绑定到
分组的全部路由（按 `OperationID` 反射找方法），所以 `Orders` 与 `Config` 各用自己
的 handler：`OrderController` 实现 `demoservice.OrdersService`，`OrderConfigController`
只实现 `demoservice.ConfigService`。模块根不再声明仓库、不提供
service getter、不创建模块工厂。order 自己的 `innerapi`（OrderAPI）本轮暂无消费方，
保留纯契约供后续模块使用：将来由容器把订单 service 以接口注入新的消费方，不需要
恢复单例槽。Go 的 import 方向是 `container → order(module) → service`，service 包
不可能反向 import 模块根或容器，依赖只能在构造期注入。`order/service` 只 import
`product/innerapi`。

## 7. 事务与一致性

- **事务由发起用例的 service 开启**：`order.service.Create` 用
  `gofydb.Transaction(ctx, appdb.Db, fn)` 开事务；
- 跨模块调用传 **同一 ctx**：`fn` 内调 `s.productAPI.DecreaseStock(ctx, ...)`（innerapi）
  与 `s.orderRepository.Create(ctx, ...)`，两个模块的 repository 都从 ctx 取到同一事务；
- 被调用的 product service **不另开事务**：`support/db.Transaction` 嵌套复用外层；
- 跨模块写优先走对方 innerapi（带业务不变量）；任何情况下都不跨模块直接调 repository
  或 service。

```go
// order/service/order_service.go（Create 节选）
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64, items []CreateItem) (*orderapi.Order, error) {
    views, err := s.validateItems(ctx, items) // 调 s.productAPI.ListByIDs（innerapi）
    if err != nil {
        return nil, err
    }
    var created *model.Order
    err = gofydb.Transaction(ctx, appdb.Db, func(ctx context.Context) error {
        for _, it := range items {
            if err := s.productAPI.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
                return err // product service 已分类成 innerapi 业务错误
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
    if err != nil {
        return nil, err
    }
    s.logger.Info("order created", zap.Uint64("order_id", created.ID))
    return toOrderView(created, items, views), nil
}
```

扣库存 + 建订单同提交同回滚；产品扣库存仍是单条原子条件更新。innerapi 只是契约面，
不改变事务传播路径。

## 8. 错误与视图契约

### 8.1 错误翻译边界

- product innerapi 暴露哨兵错误 `ErrNotFound / ErrUnavailable / ErrInsufficientStock`；
- order service 捕获后翻译成 **订单域** 哨兵错误（`service.ErrProductNotFound /
  ErrProductUnavailable / ErrInsufficientStock`）再上抛；
- controller 只认 order service 的哨兵错误并映射 HTTP 码（400/404/500）。

controller 不 import 产品模块；产品错误语义的变化被 order service 吸收。

```go
// order/service/order_service.go（validateItems 节选）
view, ok := views[it.SkuID]
if !ok {
    return fmt.Errorf("%w: sku %d", ErrProductNotFound, it.SkuID)
}
if !view.OnSale {
    return fmt.Errorf("%w: sku %d", ErrProductUnavailable, it.SkuID)
}
```

### 8.2 扣库存 0 行分类

repository 条件更新返回 0 行可能是「不存在 / 已下架 / 库存不足」。product service
在 **失败路径** 补一次 `FindByID` 区分原因（正常路径仍单条 SQL）：

```go
func (s *ProductService) DecreaseStock(ctx context.Context, id uint64, num int) error {
    if num <= 0 {
        return fmt.Errorf("decrease product %d stock: num must be positive", id)
    }
    rows, err := s.products.DecreaseStock(ctx, id, num)
    if err != nil {
        return err
    }
    if rows > 0 {
        s.logger.Info("product stock decreased", zap.Uint64("product_id", id), zap.Int("num", num))
        return nil
    }
    p, err := s.products.FindByID(ctx, id)
    switch {
    case errors.Is(err, repository.ErrNotFound):
        return fmt.Errorf("%w: sku %d", productapi.ErrNotFound, id)
    case err != nil:
        return err
    case !p.OnSale:
        return fmt.Errorf("%w: sku %d", productapi.ErrUnavailable, id)
    default:
        return fmt.Errorf("%w: sku %d", productapi.ErrInsufficientStock, id)
    }
}
```

分类读取仅用于报错，不影响原子性。

### 8.3 不泄漏的内容

跨模块只暴露：innerapi 接口 + plain 视图 + 哨兵错误。不暴露 model、repository、
service 实现、GORM 类型、HTTP 契约 DTO。

## 9. 端到端示例：下单整合产品数据

```text
HTTP POST /api/v1/order/orders
  └─ controller.CreateOrder
       ├─ 契约 req → []service.CreateItem
       └─ order.service.Create(ctx, userID, amount, items)
            ├─ validateItems: s.productAPI.ListByIDs(ctx, ids)      ← product.innerapi 接口
            │    └─ [product 模块内] service → repository → []model.Product → map[uint64]innerapi.Product
            ├─ gofydb.Transaction(ctx, appdb.Db, fn)
            │    ├─ s.productAPI.DecreaseStock(ctx, sku, num)       ← product.innerapi，同一 ctx 事务
            │    │    └─ [product 模块内] service → repository（条件更新）
            │    └─ order.repository.Create(ctx, *model.Order)
            └─ model.Order → order.innerapi.Order（view）
       ├─ innerapi.Order → orderdto.OrderDTO（状态/ID 映射）
       └─ 错误 → HTTP 码
```

订单 service 全程只依赖 `product.innerapi` 的接口与视图，不接触产品的 service 实现、
表结构、GORM 或 HTTP 契约。

## 10. import guard 规则（替换现有 §8）

`examples/demo-service/shell/check-imports.sh` 改为按层校验（仍只查非测试文件），
取用规则由 `examples/demo-service/shell/check-io-access.sh` 单独校验：

1. `controller`：只允许 import 本模块 `service` 与 HTTP 契约包
   （`github.com/bingcool/gofy-interface-service/...`）；禁止任何 `model` / `repository` / `innerapi` /
   其他模块包；
2. `service`：禁止 import HTTP 契约包；禁止 import 其他模块 `service` / `repository` /
   `model`；允许本模块 `innerapi`（实现）与其他模块 `innerapi`（调用）；
3. `innerapi`：纯契约，禁止 import 任何 `app/module/*/...`（含本模块 service/repository/
   model）、HTTP 契约、`app/io`；只允许标准库（必要时 `support` 的纯类型）；
4. `repository`：禁止 import 任何 `service` / `innerapi` / 契约 / 其他模块包；只允许
   本模块 `model` + `app/io` + `support`；
5. `model`：禁止任何 `app/module` / 契约 / `app/io` 依赖；
6. 删除「一个包最多 import 一个其他模块 repository」旧规则，由规则 2 取代；
7. 包依赖成环仍由 `go build` 兜底（innerapi 无回边，天然无环）；
8. `service` 与 `repository` 只允许经 `appio.WithContext(ctx)` 取本代句柄：
   禁止调用 `appdb.Default` / `appredis.Cache` 等包级访问器，也禁止自行
   `Publish`。HTTP、脚本、守护进程共用这两层代码，入口负责发布一代，这两层只
   负责消费，否则同一份代码会出现第二种取法。发布与固定机制来自
   `support/generation`，`app/io` 只是薄封装。

规则按 `app/module/<name>/<layer>/...` 匹配：模块根（`app/module/<name>`）只做路由
绑定，`app/container` 只做装配，两者都不在分层检查范围内。容器是唯一允许同时
import 多个模块的包，跨模块依赖只在它里面解析。

根仓库 `make guards` 的「公开包不得写 `func init()`」只约束 gofy 框架包，demo
`app/**` 不在范围内；本方案 demo 侧也不使用 `init()`。

## 11. 测试策略

### 11.1 分层单测

- repository：纯 Go 内存 SQLite（`support/sqlite`，cgo-free）跑真实 SQL，发布
  `appdb.Db`，`t.Cleanup` 恢复；
- service：构造 `Dependencies` 注入真实仓库与产品服务；测试用
  `container.NewContainer()` + `Init`（同库 SQLite）按需装配 service，或直接
  构造真实 product service 后作为 `ProductAPI` 传入，不塞 fake、不连外部 MySQL；
- controller：用真实 service（SQLite）验证契约映射与错误码。

### 11.2 innerapi 契约测试

- 编译期断言 `var _ innerapi.ProductAPI = (*ProductService)(nil)` 保证实现满足契约；
- 因 innerapi 是接口，需要隔离消费方的测试可用最小桩实现该接口（这是接口在跨模块
  边界的正当用途，与 repository 不声明接口不冲突）。

### 11.3 跨模块事务

`order/service/transaction_test.go` 建同时含订单表与产品表的内存库，验证：扣库存 +
建订单同提交同回滚；同一事务内读到刚写入的订单；库存不足/下架时 0 行且库存不变。

### 11.4 装配与状态映射

- 装配测试：每个装配代用独立容器，得到不同 service 实例；访问器幂等；产品访问器
  只构建产品子树，订单访问器按链解析产品与配置；缺数据库时
  模块工厂启动失败；订单工厂先于产品工厂执行也能工作；
  `lazyValue` 的成环检测由单测覆盖（`app/container/*_test.go`）；
- 状态一致性：controller 测试遍历 `orderenum.AllOrderStatus`，断言每个契约状态都有
  对应 `service.Status*` 且往返映射一致，防漂移。

## 12. 迁移计划

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0（本文） | 评审分层、innerapi 契约、取用机制、事务/错误边界、guard 规则 | 文档确认 |
| P1 | product：新增 `innerapi`（ProductAPI + Product 视图 + 错误）；`ProductView` 从 repository 迁出，repository 返回 model；service 实现 ProductAPI；新增模块根；`Dependencies` 取代构造参数 | product 单测通过；契约断言通过 |
| P2 | order：新增 `innerapi`（OrderAPI + Order 视图）、`order_view.go`（状态常量 + 订单域错误 + CreateItem）、`dependencies.go`；service 去掉 HTTP 契约 import，返回 innerapi 视图；repository 去掉 `orderenum`，状态改参数 | order 单测通过；service 不再 import 契约 |
| P3 | controller：契约 ↔ view 映射 + 状态映射 + 错误码；补状态一致性测试 | HTTP 行为/错误码/契约不变；一致性测试通过 |
| P4 | 跨模块切换：order service 改依赖 `product.innerapi.ProductAPI`（ListByIDs/DecreaseStock），删除对 product repository/service 的 import；产品实现由装配根以接口注入 | 跨模块事务用例通过 |
| P5 | 删除 `a_init_repository.go` / `RepoDeps`；更新 import guard 为 §10 规则 | `make check` 与三仓库回归通过 |
| P6 | 同步 README / library-design / repository-composition-design / CHANGELOG | 文档与代码一致 |
| P7（2026-11-11） | 装配收敛：新增 `app/container`（`RepositoryDeclare` + 惰性访问器 + 模块工厂）；删除 innerapi 单例槽、module getter 与模块级工厂；`reload` 每代新建容器 | `make check` 与三仓库回归通过；容器测试覆盖幂等、按需构建、代际隔离、缺库失败、工厂顺序无关与成环检测 |
| P8（2026-11-12） | product 补只读 HTTP 路由：controller 改为 `ProductsService` 适配器，service/repository 增加分页；模块根实现 `Prefix() == ""` 并绑定 `productroute.Products`。配套：gofy 允许多个模块同时挂根前缀，docgen 支持同一服务的多个部署前缀 | 产品 HTTP 用例（详情 / 404 / 分页）通过；三仓库回归通过 |

P0 已确认，P1–P8 已全部落地：根仓库 `make check` 与 demo / interface-service
三仓库回归通过；订单侧 HTTP 行为、错误码与契约不变，产品侧新增
`/api/v1/product/products` 两条只读路由。

回滚点：P1–P3 不改 HTTP 行为与表结构，可独立回滚；P4 回滚即恢复 order 对 product
repository 的取用；P7 回滚即恢复模块级工厂（但不恢复包级单例）。全程不动
`migrations/`、HTTP 契约与框架 API。

## 13. 风险与取舍

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 多一层 innerapi | 每个模块多一个契约包 | 纯契约、无实现，维护成本低；换来模块间稳定边界与可替换性 |
| 接口与项目「少用接口」倾向 | repository 不用接口，innerapi 用 | 场景不同：innerapi 是发布契约/防腐层，接口是解耦缝（§3.4） |
| 装配根成为单点 | 所有仓库/service 构造集中到 `app/container` | 访问器按模块拆文件；新增依赖只改对应模块文件与 `RepositoryDeclare`；模块工厂顺序不承载依赖 |
| 惰性构建隐藏依赖关系 | 服务在首次取用时才构造，依赖顺序不再集中可见 | 访问器内直接写 `c.XxxService()`，依赖图即代码；`lazyValue` 检测成环并 panic 出服务名；容器测试断言子树范围 |
| 并发首次取用 | `building` 标记按“串行装配”假设设计 | 取用只发生在模块工厂装配期；服务一旦构建完成，运行期只读字段 |
| 状态常量双份 | 领域常量与契约枚举各一份 | controller 映射 + 一致性测试（§11.4） |
| 0 行分类多一次查询 | 失败路径补 FindByID | 仅失败路径，正常路径单条 SQL；不影响原子性 |
| 产品路由与 innerapi 双出口 | 同一 service 同时服务 HTTP 与跨模块调用 | controller 只做契约映射；跨模块仍只依赖 innerapi，两条出口互不 import |

## 14. 决策记录

1. **模块内四包 + innerapi，调用链单向向下**：`controller → service → repository →
   model`，层间类型边界由 import guard 强制。
2. **跨模块一律走 innerapi**：`order.service → product.innerapi`；不再直接依赖对方
   service / repository / model。innerapi 是纯契约包（接口 + 视图 + 错误），不含
   任何发布/查找机制。
3. **innerapi 用接口、service 提供实现**：接口是模块间发布契约/防腐层（与 repository
   用具体类型的决策不冲突）；业务规则留在 service，innerapi 不重复实现。
4. **装配根每代新建容器，构造期显式注入**：`app/container` 每个 service 一个
   惰性构造槽，首次取用时按依赖链构建，跨模块实现以 innerapi 接口传给消费方；
   `reload` 每代新建容器，代际隔离、测试互不影响。没有包级单例、module getter
   或工厂顺序约束；装配图成环时 panic 报出服务名，不会静默死锁。
5. **service 脱离 HTTP 契约**：service 返回 innerapi 视图、领域状态常量归 service；
   契约 DTO/枚举只在 controller，映射 + 一致性测试保证不漂移。
6. **repository 统一返回 model**：产品 `ProductView` 迁到 innerapi；repository 不再
   import 契约枚举，状态由参数传入。
7. **`Dependencies` 取代包级 `RepoDeps`**：显式声明本模块仓库、跨模块接口（以及
   可能存在的同包协作 service）与 Logger，由 `app/container` 的访问器构造注入；
   删除全局可变默认集合和运行期查找。
8. **仓库与 service 声明集中在 `app/container`**：`RepositoryDeclare` 声明装配
   用到的全部仓库，`ServiceDeclare` 声明全部 service 构造槽；访问器按依赖链惰性
   构造 service 并拆到各模块文件；模块根只保留 `NewModule` / `Register`。Go 的 import 方向是
   `container → module → service`，service 不能反向 import 容器或模块根，所以组合
   只能在装配期完成。
9. **product 与 order 一样挂根前缀**（2026-11-12）：两条契约分组各自携带完整部署
   前缀（`/api/v1/order`、`/api/v1/product`），模块根 `Prefix()` 都返回空串。
   gofy 因此对空前缀跳过挂载点去重，真实重复路由交给 gin 在注册阶段报错；
   docgen 用 `demoservice.product` 这类更长 key 为产品路由选择第二个前缀。
10. **事务由发起用例的 service 开启**：跨模块传同一 ctx，被调用方不另开事务。
11. **错误逐层翻译**：product innerapi 哨兵 → order 域哨兵 → controller HTTP 码；
    controller 不 import 其他模块。
12. **模块内 service → service 用具体类型组合**：同包 service 若需要协作，由容器
    先构造、经 `Dependencies` 注入具体类型，不走 innerapi（那是跨模块的缝）；只有
    别的模块也要调时，才加进 innerapi 并由容器以接口注入新消费方。当前
    `OrderService` 与 `OrderConfigService` 互不依赖，配置由 `OrderConfigController`
    直接消费。

## 15. 对现有文档的影响

- [repository-composition-design.md](./repository-composition-design.md)：§5.3–§5.6
  的「模式 A/B 取 repository」与 §8 import 规则由本文取代；保留其事务上下文（§5.7）、
  生命周期（§5.8），`support/registry` 降级为可选工具；新增指向本文的说明；
- [library-design.md](./library-design.md) §9：模块装配示例改为「`app/container`
  每代构造仓库与 service，跨模块实现以 innerapi 接口注入」，service/repository
  片段去掉契约 import；
- `examples/demo-service/app/module/order/README.md`：分层表、装配步骤、跨模块示例
  改为 container 装配；新增 product 模块装配与 innerapi 说明；
- `CHANGELOG.md`：记录分层重构、InnerAPI 跨模块调用、container 装配与 guard 规则
  变更。
