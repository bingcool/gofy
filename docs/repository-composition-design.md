# 跨模块 Service / Repository 组合调用设计方案

- 状态：已确认（2026-11-08）；P1–P4 已落地（2026-11-09）
- 日期：2026-11-08
- 范围：`gofy/support/registry`（通用注册表）、`gofy/support/db`（事务上下文）、
  `examples/demo-service`（组合层、产品持久化、模式 B 示例）
- 关联：[library-design.md](./library-design.md)、[gofy-interface-service-design.md](./gofy-interface-service-design.md)

## 1. 背景

当前 demo-service 的组装方式是：

1. `reload.factories(cfg)` 返回一组 `gofy.ModuleFactory`；
2. 每个模块的 `module.go` 在工厂里自行 `new` 本模块的 repository 和 service；
3. `order` 模块目前只有 `Orders` 一个 repository；`product` 模块只有内存 controller，
   没有 repository / service。

用户目标：

> 不同模块的 service、repository 包应该可以直接调用。例如订单服务需要产品名称，
> 可以在订单的 service 层直接调用产品模块 repository 的查询。组合调用更自由，
> 关键是解决 service 如何方便地取用 repository，做到即取即用。

这带来两个问题：

- **取用机制**：service 从哪里拿到别的模块 repository，既方便又不退化成全局单例？
- **边界与正确性**：跨模块直接查库后，事务、写操作、依赖方向和测试如何约束？

本文给出推荐方案与迁移路径，先定设计，不写实现代码。

## 2. 现状与约束

### 2.1 现有装配链路

- 模块工厂按 `factories` 顺序执行，**全部工厂执行完后**才统一 `Register` 路由
  （[start.go:381](../start.go#L381)、[start.go:427](../start.go#L427)）。
- 热更新通过 `Reload` 重新执行同一组工厂，模块实例整体替换；旧模块在新模块启动后
  才停止（[reload.go:118](../reload.go#L118)）。
- `app/io` 模块在 `Start` 阶段才安装 `appdb.Db`、`appdb.OrderDb`、`appdb.ProductDb`
  等句柄，因此模块工厂执行时这些包级句柄仍是 `nil`
  （[io_runtime.go](../examples/demo-service/app/io/io_runtime.go)、
  [db.go](../examples/demo-service/app/io/db/db.go)）。
- 现有 repository 用 `NewFromHandle(logger)` 把连接解析推迟到每次查询
  （[order_repository.go](../examples/demo-service/app/module/order/repository/order_repository.go)）。
- 虽然 `app/io` 还维护 `db.order`、`db.product` 等命名实例，按本方案 D1，
  order / product 等业务模块统一使用框架托管的 `db.default`，命名实例只服务于
  确实需要独立存储的场景。

### 2.2 现有规则

`library-design.md` §3.3 明确：`Runtime` **不是依赖注入容器**，业务 service /
repository 不塞进 `Runtime`；业务依赖通过工厂闭包显式构建。§9 的约定是“跨模块依赖
由 `module.go` 装配后注入，service 不 import 其他模块的 GORM 实现”。

本方案保留“`Runtime` 不放业务对象”和“显式注册”两条底线，但放宽“只能由 `module.go`
逐个手动注入”的限制，引入应用级 repository 注册表。

### 2.3 痛点

- 订单服务要读产品名称时，必须在 `order/module.go` 里构造产品 repository，或让
  `order` 模块 import `product` 模块的构造代码；模块之间开始互相渗透。
- 每加一个跨模块读取，就要改调用方模块的工厂和 service 构造函数，取用成本高。
- `product` 模块没有 repository / service，示例无法展示组合调用。

## 3. 目标与非目标

### 3.1 目标

- 任意模块的 service 可以**直接组合**其他模块的 repository 和 service：
  数据操作走 repository，业务规则走 service（决策 D2）。
- repository 的取用是**一行、类型安全、启动期可校验**的，不需要全局单例。
- 注册表和实例按装配周期隔离，`Reload` 后旧实例不会被复用。
- 测试可以用 fake repository，不依赖真实数据库。
- 不新增框架级 DI 容器，不修改 `Runtime` 的定位。

### 3.2 非目标

- 不引入 wire / fx 等代码生成或反射型 DI 框架。
- 不允许 controller、HTTP 中间件直接取 repository。
- 不引入跨库分布式事务；order、product 等模块共用同一个数据库连接，事务在单库内解决。
- 不允许 repository 反向依赖 service（避免包循环）。

### 3.3 已确认的架构决策（2026-11-08）

- **D1 单库**：order、product 等模块属于同一个服务，只是代码边界不同，共用框架托管的
  同一个数据库连接（`db.default` / `Runtime.DB`）。模块拆分不引入跨库事务。
- **D2 跨模块调用方向**：`service → service` 与 `service → repository` 都是正式模式。
  简单数据读取/写入可以直接调对方 repository；涉及业务不变量（库存扣减、状态机）
  或多步骤写操作时优先走对方 service，但不再禁止 repository 直调。

## 4. 方案选型

| 方案 | 取用方式 | 优点 | 问题 | 结论 |
| --- | --- | --- | --- | --- |
| A. 构造参数注入（模式 A） | `NewOrderService(orders, products, ...)` | 依赖显式、测试最直接 | 每加一个跨模块依赖都要改工厂和构造函数；模块间 wiring 扩散 | 保留为默认写法，但需要注册表支撑 |
| B. 包级单例 / 全局访问器 | `productrepo.Default()` | 最简单、即取即用 | 隐式全局状态、无法按装配替换、Reload 不安全、测试互相污染 | 否决 |
| C. 应用级 Repository Registry | 注册一次，按名字/类型取 | 显式注册、启动期校验、装配周期隔离、易测试 | 需要一个组合层包；名字是跨模块契约，需治理 | **推荐** |
| D. Service Locator（service 持 Registry） | 方法内随时 `Get` | 构造函数稳定、加依赖不改签名 | 依赖不可见，可能变成隐式全局 | 作为 C 的可选使用模式，限制使用 |
| E. wire / fx | 生成或运行时注入 | 减少手写 wiring | 引入代码生成/反射魔法，与“显式装配”取向冲突 | 否决 |

**结论：方案 C 为基础，服务层提供两种取用模式（§5.5）：默认装配期取用（A），
demo 中允许按需取用（D），但必须启动期校验。**

注册表分两层：

- **通用注册表**放在 `gofy/support/registry`：只负责“名字 → 实例”的注册、查找、
  类型化取用和并发安全，不包含任何业务语义，供所有项目复用；
- **组合层**留在应用（`app/repository`）：定义业务仓库名字常量、`Provider`、装配
  函数和类型化 accessor，把应用的 repository 注册进通用注册表。

这样既保留“显式注册、按名解析、启动失败”的原则，也避免每个项目重复实现一份
注册表；框架仍然不参与业务 repository 的创建和路由绑定。

## 5. 推荐设计

### 5.1 目录与职责

```text
gofy/
└── support/registry/                   # 新增：通用 name -> instance 注册表（框架层）

examples/demo-service/
├── app/
│   ├── repository/                     # 新增：组合层
│   │   ├── provider.go                 #   每个装配周期一个 Provider，懒构建
│   │   ├── assemble.go                 #   构造并注册所有模块 repository
│   │   ├── names.go                    #   业务仓库名字常量
│   │   └── accessors.go                #   每个 repository 的类型化取用函数
│   ├── io/db/                          #   已有：命名连接句柄
│   └── module/
│       ├── order/
│       │   ├── module.go               #   从 Provider 取注册表并注入 service
│       │   ├── repository/             #   Orders 契约与实现
│       │   └── service/                #   订单用例（模式 B：持注册表按需取产品仓库）
│       └── product/
│           ├── module.go               #   新增：产品模块工厂/路由
│           ├── model/                  #   新增：产品持久化模型
│           ├── repository/             #   新增：Products 契约、只读视图与实现
│           └── service/                #   新增：产品用例（写操作时）
└── migrations/
    ├── 0001_orders.sql                 #   已有
    └── 0002_products.sql               #   新增：产品表 + 演示数据
```

依赖方向：

```text
app/module/*/module.go ──► app/repository ──► gofy/support/registry
app/module/*/service   ──► gofy/support/registry（模式 B）
app/module/*/service   ──► 其他模块的 repository（接口）或 service（接口）
```

- `app/repository` 是**唯一**允许跨模块聚合多个 repository 的包；其他包最多再
  import 一个其他模块的 repository，更多依赖通过 accessor 取用；
- 模块的 repository 包不 import 任何 service / module 包；
- controller 不 import `app/repository`、其他模块 repository/service；
- 出现双向 service 依赖时，先拆出接口或领域事件，不允许用注册表硬解包循环。

### 5.2 Registry 契约（`gofy/support/registry`）

框架包只做通用的“名字 → 实例”容器，不认识 repository、service 或任何业务类型：

```go
// support/registry
package registry

// Registry 保存 name -> instance 的一一映射，所有方法并发安全。
type Registry struct {
    mu      sync.RWMutex
    entries map[string]any
}

// NewRegistry 创建一个空注册表。
func NewRegistry() *Registry

// Register 注册一个实例。空名字、nil 实例、重复名字都返回错误。
func (r *Registry) Register(name string, instance any) error

// MustRegister 是 Register 的 panic 版本，用于启动期装配。
func (r *Registry) MustRegister(name string, instance any)

// Lookup 按名字取出实例；未注册或实例为 nil 时返回 (nil, false)。
func (r *Registry) Lookup(name string) (any, bool)

// Names 返回排序后的名字副本，供启动日志和诊断使用。
func (r *Registry) Names() []string

// Get 是按名字取用并做类型断言的泛型辅助函数。
// 未注册、实例为 nil 或类型不符时返回包含名字和期望类型的错误。
func Get[T any](r *Registry, name string) (T, error)
```

约定：

- 名字去掉首尾空白后不能为空；不接受 nil（`any` 包装的 nil 指针/接口也视为 nil）；
- 重复注册直接报错，不覆盖，避免两个实现抢同一个名字；
- 名字由使用方定义为常量（应用里是 `<module>.<aggregate>`，例如 `product.products`），
  注册表本身不校验格式；
- 注册表是显式实例而非包级全局：每个装配周期新建一份，`Reload` 后重建；
- 不在注册表里放 logger、db 句柄等基础设施对象，除非确实需要跨模块共享；
- 与 `support/middleware.Registry` 的区别：中间件注册表是强类型、专用于
  `support.Middleware`，并在 Router 内解析；通用注册表存放任意跨模块实例，
  供业务层按名字取用。两者不合并，避免框架核心被业务扩展拖宽。

### 5.3 Provider 与装配时序

每个装配周期（启动一次、每次 `Reload` 一次）创建一份 `Provider`，首次使用时用
当时的 `Runtime` 构建所有 repository：

```go
// app/repository/provider.go
package apprepository

type Provider struct {
    once sync.Once
    reg  *registry.Registry
    err  error
}

// NewProvider 创建空 Provider；每次 factories() 调用新建一份。
func NewProvider() *Provider

// Repositories 在首次调用时构造并缓存本次装配的注册表。
// 构造失败的错误会被缓存，后续调用返回同一个错误。
func (p *Provider) Repositories(rt *gofy.Runtime) (*registry.Registry, error) {
    p.once.Do(func() {
        p.reg, p.err = Assemble(rt)
    })
    return p.reg, p.err
}
```

`Assemble`（在 `assemble.go`）集中构造各模块 repository：

```go
func Assemble(rt *gofy.Runtime) (*registry.Registry, error) {
    reg := registry.NewRegistry()
    entries := []struct {
        name string
        repo any
    }{
        // 每个模块在这里登记自己的 repository；新增仓库只改这一处。
        // repository 必须延迟解析连接（NewFromHandle），不能捕获 nil 句柄。
        {NameOrderOrders, orderrepo.NewFromHandle(rt.Logger)},
        {NameProductProducts, productrepo.NewFromHandle(rt.Logger)},
    }
    for _, e := range entries {
        if err := reg.Register(e.name, e.repo); err != nil {
            return nil, fmt.Errorf("app/repository: %s: %w", e.name, err)
        }
    }
    return reg, nil
}
```

时序（启动与 `Reload` 相同）：

```text
reloader.factories(cfg)
  └── provider := apprepository.NewProvider()          # 每个装配周期一份

App.buildHTTPWith
  ├── 构建 Logger / DB / Runtime / middleware registry
  ├── 依次执行 module factories
  │     ├── appio.NewModuleFactory(...)               # 不依赖 provider
  │     ├── order.NewModuleFactory(provider)          # 首次调用构建 Registry
  │     └── product.NewModuleFactory(provider)
  └── 依次 Register 所有模块（路由/中间件名解析）
```

要点：

- 工厂按顺序执行，但 Provider 是**共享且懒构建**的，谁先调用都一样，不依赖工厂顺序；
- repository 构造时不连库：`NewFromHandle` 每次查询再从 `appdb.*` 解析句柄，
  因此 `app/io.Start` 之前构建 repository 是安全的；
- `Reload` 会重新执行 `factories(cfg)`，得到新的 Provider 和 Registry；旧模块继续持有
  旧注册表直到交换完成，新旧实例不共享可变状态；
- 模块不得把 repository 存到包级变量或跨装配缓存。

### 5.4 类型化取用与命名

名字常量放在 `app/repository/names.go`，类型化取用函数放在 `app/repository/accessors.go`：

```go
const (
    NameOrderOrders     = "order.orders"
    NameProductProducts = "product.products"
)

// OrderOrders 取订单 repository；名字缺失或类型不符时返回错误。
func OrderOrders(reg *registry.Registry) (orderrepo.OrderRepositoryInterface, error) {
    return registry.Get[orderrepo.OrderRepositoryInterface](reg, NameOrderOrders)
}

// ProductProducts 取产品 repository。
func ProductProducts(reg *registry.Registry) (productrepo.ProductRepositoryInterface, error) {
    return registry.Get[productrepo.ProductRepositoryInterface](reg, NameProductProducts)
}
```

`registry.Get[T]` 已包含“未注册 / nil / 类型不符”的错误信息；应用层只负责把业务
名字和接口类型固定下来，调用方永远不写 `Lookup("...")` 或 `any` 断言。

### 5.5 两种取用模式

**模式 A：装配期取用（默认，推荐）**

模块工厂一次取出所有依赖，显式传给 service 构造函数：

```go
func NewModuleFactory(repos *apprepository.Provider) gofy.ModuleFactory {
    return func(rt *gofy.Runtime) (support.Module, error) {
        reg, err := repos.Repositories(rt)
        if err != nil {
            return nil, err
        }
        orders, err := apprepository.OrderOrders(reg)
        if err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        products, err := apprepository.ProductProducts(reg)
        if err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        svc := service.NewOrderService(orders, products, rt.Logger)
        return NewModule(svc, rt.Logger), nil
    }
}
```

优点：依赖可见、测试直接传 fake、启动期报错。新增跨模块依赖时需要改一行工厂和
service 构造函数，这是可接受的显式成本。

**模式 B：按需取用（可选，demo 采用）**

service 持通用注册表，在方法内取用：

```go
type OrderService struct {
    repos  *registry.Registry // 取用 product.products，见构造函数注释
    orders orderrepo.OrderRepositoryInterface
    logger *zap.Logger
}

func (s *OrderService) productRepo() (productrepo.ProductRepositoryInterface, error) {
    return apprepository.ProductProducts(s.repos)
}
```

demo-service 的订单服务用模式 B 读取产品仓库，用于验证“即取即用”的体验：

- `order.NewModuleFactory(provider)` 只在启动期校验 `product.products` 是否存在，
  然后把注册表交给 service；
- service 在 `CreateOrder`/查询组装时按需取产品仓库，跨模块读取点增加时不用改
  构造函数签名；
- 产品仓库缺失或缺类型时，工厂直接启动失败，不会拖到请求期才暴露。

适用场景：依赖是可选的、按条件才会用到，或跨模块读取点很多、构造参数爆炸。

强制约束：

- 模块工厂仍要做一次取用校验，把缺失依赖提前到启动期：
  ```go
  if _, err := apprepository.ProductProducts(reg); err != nil {
      return nil, fmt.Errorf("order module: %w", err)
  }
  ```
- 模式 B 只允许出现在 service 层，禁止 controller / middleware；
- 使用模式 B 的 service 必须在构造函数注释里列出它取用的名字，便于审查；
- service 不得把注册表或取出的仓库缓存到包级变量，也不得跨请求保存。

**选择建议**：业务上必需的依赖优先用模式 A；跨模块读取点多、依赖按分支使用的
场景可以用模式 B，但每个取用名字都要在工厂校验，避免整个应用退化成 service locator。

### 5.6 跨模块调用规则

按决策 D2，`service → service` 与 `service → repository` 都是一等模式：

| 场景 | 允许的调用 | 规则 |
| --- | --- | --- |
| 读其他模块数据 | order service → product repository 或 product service | 两种都可以；返回视图/DTO，不把对方的 `model` 暴露给 controller |
| 简单写其他模块数据 | order service → product repository | 与读取同库；必须在调用方事务内，且不绕过业务不变量 |
| 带业务规则的写 | order service → product service | 库存扣减、状态机、发事件等多步骤逻辑走属主模块用例 |
| 跨模块事务 | 同一 context 事务内调用对方 repository/service | 所有模块共用 `db.default`，双方 repository 必须支持事务上下文（§5.7） |
| controller → 其他模块 repository/service | 禁止 | HTTP 层只依赖本模块 service |
| repository → 其他模块 service/repository | 禁止 | repository 只做本聚合的数据访问 |

选择原则：**数据操作直接走 repository，业务操作走 service**。判断标准是“这段逻辑
是否包含属主模块的业务不变量”；包含就升级为 service 调用，不包含就可以直接查/写
repository。两种方向都显式依赖接口，不通过全局变量或反射取用。

导入规则：

- 允许 `service` import 其他模块的 `repository`（接口）或 `service`（导出构造函数）；
- 禁止 repository import 任何 service；
- 需要事件或回调解耦的双向依赖，通过领域事件 / 接口下沉解决，不能靠全局注册表绕环。

跨模块读取的返回类型：产品模块在 repository 包定义只读视图，例如：

```go
// app/module/product/repository/product_view.go
type ProductView struct {
    ID    uint64
    Name  string
    Price int64
    Stock int
}
```

订单服务只依赖 `ProductView`，不 import `product/model`，避免持久化模型跨模块扩散。

### 5.7 事务传播

决策 D1 已消除“跨库事务”问题：order、product 等模块共用 `db.default` 这一个
`*gorm.DB`。剩下要保证的是**同一次请求里的多次 repository 调用使用同一个事务**。
共用连接池并不自动等于共用事务，因此仍需把事务放进 context：

`support/db` 已提供上下文事务辅助：

```go
// support/db
// Transaction 在 db 上开启事务，把事务放入 ctx 后执行 fn；嵌套调用复用已有事务。
func Transaction(ctx context.Context, db *gorm.DB, fn func(ctx context.Context) error) error

// FromContext 优先返回 ctx 中的事务，否则返回 fallback。
func FromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB
```

repository 的连接解析统一改为：

```go
func (r *ProductRepository) conn(ctx context.Context) *gorm.DB {
    fallback := r.db
    if fallback == nil {
        fallback = appdb.Db // 单库：所有业务 repository 共用 db.default
    }
    return gofydb.FromContext(ctx, fallback)
}
```

约定：

- 所有 repository 查询使用 `conn(ctx)`，不再直接读包级句柄；
- 跨模块只读调用可以不显式开事务；需要一致性的读写在同一个 `Transaction` 内；
- 事务由调用方 service 开启，跨模块调用的 repository/service 只消费 ctx，不自行提交；
- 嵌套 `Transaction` 直接复用外层事务，不创建 savepoint：内层返回错误会冒泡到外层，
  内层成功也不会提前提交；
- `FromContext` 优先返回 ctx 中的事务，没有事务时才用 repository 的显式连接或
  `appdb.Db`；显式连接不会覆盖事务；
- 单库事务是本文范围；如果某个 repository 确实使用独立命名实例（`db.order` /
  `db.product`），该调用不参与 `db.default` 的事务，需要在设计上避开或拆成补偿流程；
- 不在 ctx 里放 repository/service，ctx 只承载请求范围数据（trace、request id、tx）。

产品仓库的写操作同样走 `conn(ctx)`，例如扣库存把可用条件写进 SQL，返回受影响
行数，由调用方把 0 行映射为业务错误：

```go
func (r *ProductRepository) DecreaseStock(ctx context.Context, id uint64, num int) (int64, error) {
    res := r.conn(ctx).WithContext(ctx).
        Model(&model.Product{}).
        Where("id = ? AND status = ? AND stock >= ?", id, StatusOnSale, num).
        Update("stock", gorm.Expr("stock - ?", num))
    return res.RowsAffected, res.Error
}
```

### 5.8 生命周期与热更新

- Provider / Registry 的生命周期 = 一次装配，`Reload` 后重建；
- 每次 `Reload` 生成的 `Runtime`（新日志、新连接句柄）由 `Repositories(rt)` 进入
  新的 repository，旧实例随旧注册表一起淘汰；
- 旧模块在新模块启动完成前继续服务，期间两代 repository 并存是预期行为；
- 任何 repository 都不允许注册到包级变量、`Runtime` 或全局 map；
- 后台 worker / 脚本不经过模块装配时，直接显式构造 repository（脚本用
  `repository.New(rt.DB, rt.Logger)`），不从请求上下文的注册表拿。

### 5.9 错误与诊断

- 注册失败（空名、nil、重复）→ 工厂返回错误 → 启动或热更新失败，保留旧实例；
- 取用失败（名字缺失、类型不符）→ 错误包含名字、期望类型和实际类型；
- 启动日志可输出 `reg.Names()`，按模块分组，便于确认实际注册了哪些仓库；
- 没有默认 repository、没有兜底实现；缺依赖就是错误。

### 5.10 测试

- 单测直接构造注册表并填 fake：
  ```go
  reg := registry.NewRegistry()
  _ = reg.Register(apprepository.NameProductProducts, fakeProducts)
  products, _ := apprepository.ProductProducts(reg)
  ```
- 模块工厂接受 `Provider` 接口而非具体类型，测试可注入返回 fake 注册表的 provider；
- 订单模块测试用 fake 产品仓库验证“产品不存在/已下架”等分支，不连数据库；
- `support/registry` 自身单测覆盖：空名字、nil、重复注册、并发注册与取用、
  `Get[T]` 类型不符、`Names` 排序和副本语义；
- 新增测试点：
  - 名字缺失时启动失败；
  - `Reload` 后新注册表不复用旧实例；
  - 事务上下文被两个模块的 repository 共同识别。

## 6. 示例：订单服务读取产品名称

### 6.1 产品表与 repository

产品模块本轮落到真实的 `db.default` 仓库。迁移文件 `migrations/0002_products.sql`
沿用 `0001_orders.sql` 的手工执行方式（demo 不使用 `AutoMigrate`）：

```sql
CREATE TABLE IF NOT EXISTS products (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name       VARCHAR(128)    NOT NULL,
    price      BIGINT          NOT NULL,
    stock      INT             NOT NULL DEFAULT 0,
    status     TINYINT         NOT NULL DEFAULT 1, -- 1 上架，0 下架
    created_at DATETIME(3)     NOT NULL,
    updated_at DATETIME(3)     NOT NULL,
    PRIMARY KEY (id),
    KEY idx_products_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT IGNORE INTO products (id, name, price, stock, status, created_at, updated_at)
VALUES
    (1, '演示商品 A', 9900, 100, 1, NOW(3), NOW(3)),
    (2, '演示商品 B', 19900, 50, 1, NOW(3), NOW(3)),
    (3, '演示商品 C', 29900, 20, 1, NOW(3), NOW(3));
```

```go
// app/module/product/repository/product_repository.go
type ProductRepositoryInterface interface {
    FindByID(ctx context.Context, id uint64) (*ProductView, error)
    ListByIDs(ctx context.Context, ids []uint64) (map[uint64]ProductView, error)
}

// ProductView 是跨模块只读视图，订单服务只依赖它，不 import product/model。
type ProductView struct {
    ID     uint64
    Name   string
    Price  int64
    Stock  int
    Status int8
}

type ProductRepository struct {
    logger *zap.Logger
}

func NewFromHandle(logger *zap.Logger) *ProductRepository // 查询时解析 appdb.Db（单库）
func New(db *gorm.DB, logger *zap.Logger) *ProductRepository
```

`ListByIDs` 返回 `map[uint64]ProductView`，订单服务据此批量补全 SKU 名称；产品不
存在或已下架时由订单 service 决定报错还是跳过，repository 不做业务判断。

### 6.2 订单 service（模式 B）

```go
type OrderService struct {
    // repos 用于按需取用 product.products（模式 B），名字在 module 工厂启动期校验。
    repos  *registry.Registry
    orders orderrepo.OrderRepositoryInterface
    logger *zap.Logger
}

func NewOrderService(
    orders orderrepo.OrderRepositoryInterface,
    repos *registry.Registry,
    logger *zap.Logger,
) *OrderService
```

创建订单或拼装响应时批量查产品名称：

```go
// productNames 按 SKU 批量取名称，订单服务只依赖产品 repository 的只读视图。
func (s *OrderService) productNames(ctx context.Context, skuIDs []uint64) (map[uint64]string, error) {
    products, err := apprepository.ProductProducts(s.repos)
    if err != nil {
        return nil, err
    }
    views, err := products.ListByIDs(ctx, skuIDs)
    if err != nil {
        return nil, err
    }
    names := make(map[uint64]string, len(views))
    for id, v := range views {
        names[id] = v.Name
    }
    return names, nil
}
```

`CreateOrder` 校验请求里的 `items`：每个 `sku_id` 必须能在产品仓库查到且上架，
再创建订单。若订单响应要返回 `OrderItemDTO`，由 service 在组装 DTO 时填入 `SkuName`
（[dto.go](../../gofy-interface-service/appinterface/demoservice/orderdto/dto.go)）；
订单持久化模型本身不存产品名称，避免跨模块数据冗余。

说明：订单服务只依赖产品 repository 的接口和 `ProductView`，不 import 产品
`model`、不 import 产品 GORM 实现；跨模块调用点集中在 service 层。

### 6.3 模块工厂

```go
// app/module/order/module.go
func NewModuleFactory(repos *apprepository.Provider) gofy.ModuleFactory {
    return func(rt *gofy.Runtime) (support.Module, error) {
        reg, err := repos.Repositories(rt)
        if err != nil {
            return nil, err
        }
        orders, err := apprepository.OrderOrders(reg)
        if err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        // 模式 B：启动期校验产品仓库存在且类型正确。
        if _, err := apprepository.ProductProducts(reg); err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        svc := service.NewOrderService(orders, reg, rt.Logger)
        return NewModule(svc, rt.Logger), nil
    }
}
```

产品模块的工厂同样从 Provider 取 `product.products`，交给产品 service/controller；
两个模块共用同一份注册表，但各自只取自己需要的实例。

## 7. 迁移计划

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0（本文） | 方案评审，确认取用模式、跨模块写规则、事务范围、注册表落点 | 文档确认 |
| P1 | 新增 `gofy/support/registry`（通用注册表 + 单测 + README + CHANGELOG）；新增 `app/repository`（Provider / assemble / names / accessors）；把订单 repository 迁入注册表；`reload.go` 每个装配周期新建 Provider | `support/registry` 单测通过；订单功能回归通过；缺少注册时启动失败 |
| P2 | 新增 `migrations/0002_products.sql`（产品表 + 演示数据）、产品 `model` / `repository`（基于 `db.default`）；订单 service 用模式 B 读取产品名称并校验下单 SKU；补 fake 注册表/仓库测试 | 下单校验产品存在且上架；订单 DTO/响应带产品名称；单测不连库 |
| P3（已落地 2026-11-09） | 补 `support/db` 事务上下文；repository 改用 `conn(ctx)`；覆盖跨模块事务用例 | 扣库存 + 建订单可回滚 |
| P4（已落地 2026-11-09） | 增加 import guard（controller 禁止取 repository、repository 禁止 import service、只有 `app/repository` 可跨模块聚合）；更新 README / library-design / CHANGELOG | `make check` 与三仓库回归通过 |

每阶段的回滚点：P1 只新增包并迁移订单，不改变产品模块；P2 的产品仓库先从只读
查询开始，产品表用 `IF NOT EXISTS` + `INSERT IGNORE`，可重复执行；P3 之前跨模块写
操作可以直接调 repository（同库），但还不保证多步原子性，P3 完成后统一走 ctx 事务。

## 8. 约束与自动校验

已在 `examples/demo-service/scripts/check-imports.sh` 落地 import guard（沿用根仓库
`scripts/check-imports.sh` 的写法），由 demo 与根仓库的 `make guards` / `make check`
调用。脚本只检查非测试文件的 import（`go list` 的 `.Imports` 不含 `_test.go`），
测试仍可同时引用多个模块的 fake；包依赖成环由 `go build` 直接报错兜底。

1. `app/module/*/controller` 不得依赖 `app/repository`、任何 `model`，以及其他模块的
   `repository` / `service`；只允许 import 本模块的 `service`；
2. `app/module/*/repository` 不得依赖任何 `service` 包；
3. 除 `app/repository` 外，同一个包最多 import 一个“其他模块”的 `repository`
   （本模块的 repository 不计入）；需要聚合更多模块的数据时，通过
   `app/repository` 的 accessor 取用；
4. `app/module/*/service` 可以依赖其他模块 `repository` / `service`，但依赖图必须无环，
   由 `go build` 的包依赖检查兜底。

代码评审清单：

- 新增 repository 是否在 `assemble.go` 注册、在 `accessors.go` 提供类型化取用；
- service 的跨模块依赖是否显式声明（模式 A）或走模式 B 且在工厂校验；
- 跨模块写是否优先走 service；直接调 repository 时是否在同一 ctx 事务内且不绕过业务不变量；
- 事务内的跨模块调用是否传入同一 ctx；
- 是否把 repository 缓存到包级变量或跨装配结构。

## 9. 对现有文档的影响

- `library-design.md` §3.3「`Runtime` 不是 DI 容器」保持有效，本方案不向 `Runtime`
  塞业务对象；
- `library-design.md` 新增 `support/registry` 章节，说明通用注册表与
  `support/middleware.Registry` 的分工；
- `library-design.md` §9 的模块装配示例需要从“module.go 自己 new repository”改为
  “从 `app/repository.Provider` 取用注册表”；
- `examples/demo-service/app/module/order/README.md` 的“跨模块依赖由 module.go 装配
  后注入”要补充两种取用模式，并把订单服务改为模式 B 示例；产品表初始化步骤写在
  该 README 的“数据表”一节；
- CHANGELOG 在 P1/P2 落地时新增 `support/registry`、`app/repository` 和产品仓库条目；
- P3 在 `support/db` README 与 `library-design.md` §3.7 补充事务上下文 API 和约定；
- P4 的 import guard 路径与规则记录在本文 §8，并在 demo README 与 CHANGELOG 中说明。

## 10. 风险与取舍

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 注册表膨胀 | 所有 repository 一个 map，最终变成“上帝注册表” | 名字按模块前缀分组；规模过大时按域拆分多个 Registry |
| 模式 B 隐式依赖 | service 内部取用，依赖不可见 | 只作为例外；工厂启动期校验；构造函数注释列出名字 |
| 跨模块写绕过不变量 | 直接调对方 repository 修改数据 | 简单写允许；带业务不变量时走 service；写调用必须在同一个 ctx 事务内 |
| 事务改造面大 | 所有 repository 都要改用 `conn(ctx)` | P3 统一改造并加测试；未改造前跨模块多步写不保证原子性 |
| 模型泄漏 | 跨模块返回 `model` 导致耦合 | 只返回 repository 包的只读视图/DTO |
| 热更新竞争 | 旧模块仍在使用旧 repository | 每装配周期独立 Provider；不在包级缓存；新旧交换沿用现有机制 |

## 11. 决策记录

### 11.1 已确认（2026-11-08）

1. **单库**：order、product 等模块属于同一个服务，共用同一个数据库连接；模块拆分
   不引入跨库事务，事务在 `db.default` 上解决。
2. **跨模块方向**：`service → service` 与 `service → repository` 都允许。数据操作
   直接走 repository，包含业务不变量的操作走 service。
3. **产品模块落真实库**：产品模块本轮实现 `db.default` 上的真实 repository，并新增
   `migrations/0002_products.sql` 创建产品表（含演示数据），不是只读假数据仓库。
4. **模式 B 进入 demo**：订单服务持有注册表，按需取用 `product.products`，用于验证
   即取即用的体验；模块工厂仍做启动期校验。
5. **注册表抽到框架**：通用注册表放在 `gofy/support/registry` 供其他项目复用；
   `app/repository` 只保留业务名字、Provider、装配和 accessor。
