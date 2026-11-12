# 跨模块 Service / Repository 组合调用设计方案

- 状态：已确认（2026-11-08）；P1–P5 已落地（2026-11-09）；P6–P8 已落地（2026-11-11）。
  P5 删除了 `app/repository` 组合层：仓库集合与默认实例归 service 包，注册表由模块
  工厂每次装配新建。P6 去掉 repository 接口，调用方直接依赖具体类型。P7 把仓库改成
  零值可用的无状态类型，P8 只给跨模块取用的仓库登记名字（本模块仓库直接注入）。
- 日期：2026-11-08（2026-11-09 修订 P5，2026-11-11 修订 P6 / P7 / P8）
- 范围：`gofy/support/registry`（通用注册表）、`gofy/support/db`（事务上下文）、
  `examples/demo-service`（模块工厂建注册表、产品持久化、模式 B 示例）
- 关联：[library-design.md](./library-design.md)、[gofy-interface-service-design.md](./gofy-interface-service-design.md)

> **本文的跨模块耦合方式已被 [module-layering-design.md](./module-layering-design.md) 取代（2026-11-11）。**
> 原方案让 `service → service` / `service → repository` 直接调用（决策 D2、§4 方案选型、
> §5.1 目录、§5.3–§5.6 的模式 A/B、§6 示例、§8 import 规则）。新方案改为
> **`service → 对方 innerapi`**：每个模块新增 `innerapi` 纯契约包（接口 + 视图 + 哨兵错误），
> 由应用层 `app/container` 在构造期把实现按接口注入消费方，不再 import 其他模块的
> repository / model / service。demo 不使用 `support/registry`（单跨模块实现不需要名字索引）；
> 本文 §5.2 的通用注册表契约仍在，作为需要按名字索引时的**可选**库能力保留。
>
> **仍然有效、本文继续作为权威的部分**：§5.2 通用注册表契约（可选工具）、§5.7 事务上下文传播、
> §5.8 生命周期与热更新，以及 §5.10「用纯 Go SQLite 跑真实 SQL、异常路径在驱动层注入」的测试取向。
> 下文被取代的小节保留原文供追溯，阅读时以 module-layering-design.md 为准。

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
- 现有 repository 不持连接，每次查询用 `support/db.WithContext` 回落到 `appdb.Db`
  （[order_repository.go](../examples/demo-service/app/module/order/repository/order_repository.go)）。
- 虽然 `app/io` 还维护 `db.order`、`db.product` 等命名实例，按本方案 D1，
  order / product 等业务模块统一使用框架托管的 `db.default`，命名实例只服务于
  确实需要独立存储的场景。

### 2.2 现有规则

`library-design.md` §3.3 明确：`Runtime` **不是依赖注入容器**，业务 service /
repository 不塞进 `Runtime`；业务依赖通过工厂闭包显式构建。§9 的约定是“跨模块依赖
由 `module.go` 装配后注入，service 不 import 其他模块的 GORM 实现”。

本方案保留“`Runtime` 不放业务对象”和“显式注册”两条底线，但放宽“只能由 `module.go`
逐个手动注入”的限制，引入按装配周期创建的 repository 注册表（由模块工厂持有，
不设集中组合层，见 P5）。

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
- 测试不依赖外部数据库：仓库只有一种实现，直接用纯 Go SQLite 跑真实 SQL（§5.10）。
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
  > **已被取代**：跨模块只走对方 `innerapi` 接口，不再 service→service / service→repository 直调，
  > 见 [module-layering-design.md](./module-layering-design.md) §3 / §6。D1 单库仍然有效。

## 4. 方案选型

| 方案 | 取用方式 | 优点 | 问题 | 结论 |
| --- | --- | --- | --- | --- |
| A. 构造参数注入（模式 A） | `NewOrderService(orders, products, ...)` | 依赖显式、测试最直接 | 每加一个跨模块依赖都要改工厂和构造函数；模块间 wiring 扩散 | 保留为默认写法，但需要注册表支撑 |
| B. 包级单例 / 全局访问器 | `productrepo.Default()` | 最简单、即取即用 | 隐式全局状态、无法按装配替换、Reload 不安全、测试互相污染 | 否决 |
| C. 应用级 Repository Registry | 注册一次，按名字/类型取 | 显式注册、启动期校验、装配周期隔离、易测试 | 名字是跨模块契约，需治理；集中组合层容易变成上帝包（P5 已移除） | **推荐** |
| D. Service Locator（service 持 Registry） | 方法内随时 `Get` | 构造函数稳定、加依赖不改签名 | 依赖不可见，可能变成隐式全局 | 作为 C 的可选使用模式，限制使用 |
| E. wire / fx | 生成或运行时注入 | 减少手写 wiring | 引入代码生成/反射魔法，与“显式装配”取向冲突 | 否决 |

**结论：方案 C 为基础，服务层提供两种取用模式（§5.5）：默认装配期取用（A），
demo 中允许按需取用（D），但必须启动期校验。**

注册表分两层：

- **通用注册表**放在 `gofy/support/registry`：只负责“名字 → 实例”的注册、查找、
  类型化取用和并发安全，不包含任何业务语义，供所有项目复用；
- **业务注册表**由消费方模块工厂在每次装配时 `registry.NewRegistry()` 新建，只登记
  它跨模块按名字读取的仓库；本模块自用的仓库直接传给 service，不占名字。被跨模块
  读取的名字常量归仓库的属主模块（`product/repository.NameProducts`），
  取用直接写 `registry.Get[T](reg, name)`，不再需要 accessor 层。

P1–P4 曾在应用里放一个 `app/repository` 组合层（Provider / Assemble / names /
accessors）。P5 把它整体删除：Provider 的懒构建只为“多个工厂共享一份注册表”服务，
而 demo 只有订单服务跨模块读产品仓库，消费方工厂自己建注册表更短、更明确，也少一个
需要跟着模块清单同步维护的集中包。

这样既保留“显式注册、按名解析、启动失败”的原则，也避免每个项目重复实现一份
注册表；框架仍然不参与业务 repository 的创建和路由绑定。

## 5. 推荐设计

### 5.1 目录与职责

```text
gofy/
└── support/registry/                   # 新增：通用 name -> instance 注册表（框架层）

examples/demo-service/
├── app/
│   ├── io/db/                          #   已有：命名连接句柄
│   └── module/
│       ├── order/
│       │   ├── module.go               #   每次装配新建注册表，登记仓库并注入 service
│       │   ├── repository/             #   Orders 具体仓库（不声明接口）
│       │   └── service/
│       │       ├── a_init_repository.go #  RepositoryDeps + RepoDeps（直接注入，不登记）
│       │       └── order_service.go     #  订单用例（模式 B：持注册表按需取产品仓库）
│       └── product/
│           ├── module.go               #   新增：产品模块工厂/路由
│           ├── model/                  #   新增：产品持久化模型
│           ├── repository/             #   新增：NameProducts、具体仓库、只读视图
│           └── service/                #   产品用例：读取与扣库存
└── migrations/
    ├── 0001_orders.sql                 #   已有
    └── 0002_products.sql               #   新增：产品表 + 演示数据
```

依赖方向：

```text
app/module/*/module.go ──► gofy/support/registry（新建 + 登记）
app/module/*/service   ──► gofy/support/registry（模式 B 取用）
app/module/*/service   ──► 其他模块的 repository（具体类型 + 名字常量 + 只读视图）或 service
```

- 没有集中组合层：一个包最多 import 一个“其他模块”的 repository；需要更多跨模块
  仓库时，把读取点拆到各自的 service，每个 service 的工厂登记它用到的名字；
- 名字常量归仓库属主模块（订单自己的名字在 `order/service`，产品的在
  `product/repository`），登记由消费方模块工厂负责；
- 模块的 repository 包不 import 任何 service / module 包；
- controller 不 import 任何模块的 repository / 其他模块 service；
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

### 5.3 装配注册表与时序

> **已被取代**：跨模块依赖不再用注册表，改为 innerapi 契约——应用层 `app/container`
> 的 `OrderService()` 访问器按需解析产品 service 并按 `ProductAPI` 接口注入，模块根
> 只保留 `NewModule` / `Register` 绑定路由。见
> [module-layering-design.md](./module-layering-design.md) §6.4–§6.6。下文保留原文供追溯。

每个装配周期（启动一次、每次 `Reload` 一次）由模块工厂新建一份注册表，只登记
它跨模块按名字读取的仓库；本模块仓库直接传给 service：

```go
// app/module/order/module.go
func NewModuleFactory() gofy.ModuleFactory {
    return func(rt *gofy.Runtime) (support.Module, error) {
        if rt.DB == nil {
            return nil, errors.New("order module requires database")
        }
        reg := registry.NewRegistry()
        // 跨模块仓库：名字和实现归产品模块，订单模块作为消费方负责登记。
        if err := reg.Register(productrepo.NameProducts, &productrepo.ProductRepository{}); err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        svc := service.NewOrderService(service.RepoDeps.Orders, reg, rt.Logger)
        return NewModule(svc, rt.Logger), nil
    }
}
```

`reload.factories(cfg)` 不再构造任何 Provider，只返回工厂列表：

```go
factories := []gofy.ModuleFactory{
    appio.NewModuleFactory(cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt),
    order.NewModuleFactory(),
}
```

时序（启动与 `Reload` 相同）：

```text
reloader.factories(cfg)                                # 只组装工厂列表，无共享状态

App.buildHTTPWith
  ├── 构建 Logger / DB / Runtime / middleware registry
  ├── 依次执行 module factories
  │     ├── appio.NewModuleFactory(...)                # 安装 app/io 句柄的模块
  │     └── order.NewModuleFactory()                   # 新建 Registry，
  │                                                    # 登记 product.products
  └── 依次 Register 所有模块（路由/中间件名解析）
```

要点：

- 注册表由**消费方工厂**创建，工厂之间没有共享可变状态，也不依赖执行顺序；
  两个模块都需要读同一个仓库时，各自登记自己那一份（实例无状态，见下）；
- repository 不持有连接：构造不需要连接，每次查询从 `appdb.*` 解析句柄，
  因此 `app/io.Start` 之前构建 repository 是安全的；
- `Reload` 会重新执行 `factories(cfg)`，工厂再建一份新注册表；旧模块继续持有旧注册表
  直到交换完成，新旧实例不共享可变状态；
- service 不得把注册表或取出的仓库缓存到包级变量。唯一的包级默认值是
  `service.RepoDeps`（§5.5）：它只放零值构造的无状态仓库，不持连接、
  不持注册表，跨装配复用是安全的。

### 5.4 类型化取用与命名

> **已被取代**：跨模块不再按名字取用，改为 innerapi 契约；实现由应用层
> `app/container` 在构造期按接口注入，不依赖名字常量。见
> [module-layering-design.md](./module-layering-design.md) §6。

名字常量只给“会被其他模块读取”的仓库，格式统一为 `<module>.<aggregate>`：

- 被其他模块读取的仓库名字放在属主模块的 repository 包里
  （`product/repository.NameProducts`），由消费方模块工厂登记、消费方 service 取用；
- 本模块自用的仓库（如订单仓库）不声明名字、不登记：模块工厂直接把实例传给
  service，注册表里不会出现没有消费者的条目。新增跨模块读取点时再按上面的规则
  补名字。

```go
// app/module/product/repository/product_repository.go
const NameProducts = "product.products"

// 取用：消费方 service 直接用通用注册表的类型化 Get，不再包一层 accessor。
products, err := registry.Get[*productrepo.ProductRepository](s.repos, productrepo.NameProducts)
```

`registry.Get[T]` 的 `T` 就是注册进去的具体类型：repository 不声明接口（§5.6），
注册和取用都写 `*productrepo.ProductRepository`。它已包含“未注册 / nil / 类型不符”
的错误信息；调用方永远不写 `Lookup("...")` 或 `any` 断言。名字重命名属于破坏性变更，
属主模块和所有消费方要一起改。

### 5.5 两种取用模式

> **已被取代**：模式 A/B 与包级 `RepoDeps` / `a_init_repository.go` 均已删除。
> 跨模块依赖由 `app/container.Build` 构造产品 service 并按 `ProductAPI` 接口注入
> `OrderService`；`service.Dependencies` 声明本模块仓库、跨模块接口与 `Logger`。见
> [module-layering-design.md](./module-layering-design.md) §5–§6。

**模式 A：装配期取用（默认，推荐）**

模块工厂把 service 包声明的默认仓库直接传给构造函数（完整代码见 §5.3），
不经过注册表。仓库集合与默认实例放在 `a_init_repository.go`，文件里没有注册逻辑：

```go
// app/module/order/service/a_init_repository.go
package service

// RepositoryDeps 的字段就是本模块声明的全部仓库；新增仓库在这里加字段并由
// 模块工厂传给 service。跨模块依赖不放在这里，由 service 持注册表按需取用（模式 B）。
type RepositoryDeps struct {
    // Orders 是订单持久化仓库，必填。
    Orders *repository.OrderRepository
}

// RepoDeps 是模块自带的默认集合。仓库无状态：不持连接和日志，连接推迟到每次查询
// 才解析，因此包初始化早于 app/io.Start 也安全，Reload 换连接后自动生效。
var RepoDeps = RepositoryDeps{Orders: &repository.OrderRepository{}}
```

放在 service 包而不是 `module.go`，是因为 service 已经 import 本模块 repository，
不会形成 `service → order → service` 的环；模块工厂只负责把 `service.RepoDeps.Orders`
传进构造函数，不关心仓库怎么构造。

无状态仓库的取舍（已确认）：

- 仓库不持有连接和日志，每次查询从 `app/io` 句柄解析 `db.Db`（ctx 事务优先），
  因此装配早于 `app/io.Start`、`Reload` 换连接都自动生效；
- 日志统一在 service/module 层用 `rt.Logger` 记录，仓库只包装并返回错误，
  避免同一失败被记录两次；
- `RepoDeps` 只放无状态仓库（不持连接、不持注册表），跨装配共享安全；它是唯一被允许的
  包级仓库变量；
- 测试不改 `RepoDeps`，而是直接给 `NewOrderService` 传自己构造的仓库实例，保持用例
  互不污染。

优点：依赖可见、测试可换实例。新增本模块仓库时在 `RepositoryDeps` 加字段，并在
模块工厂的构造函数调用里传入；仓库不被跨模块读取，就不需要名字和注册表。

**模式 B：按需取用（可选，demo 采用）**

service 持通用注册表，在方法内取用：

```go
type OrderService struct {
    repos  *registry.Registry // 按需取用 product.products（模式 B）
    orders *orderrepo.OrderRepository
    logger *zap.Logger
}

// validateItems 里的跨模块读取点：
products, err := registry.Get[*productrepo.ProductRepository](s.repos, productrepo.NameProducts)
```

demo-service 的订单服务用模式 B 读取产品仓库，用于验证“即取即用”的体验：

- `order.NewModuleFactory()` 在装配期登记 `productrepo.NameProducts`，登记失败直接
  启动报错，然后把注册表交给 service；
- service 在 `CreateOrder`/查询组装时按需取产品仓库，跨模块读取点增加时不用改
  构造函数签名；
- 产品仓库缺失或类型不符时，`reg.Register` 在启动期就失败；service 内的 `Get`
  只是把同一份契约再确认一次，不会拖到请求期才暴露装配错误。

适用场景：依赖是可选的、按条件才会用到，或跨模块读取点很多、构造参数爆炸。

强制约束：

- 消费方模块工厂必须登记它 service 会取用的每个名字，把缺失依赖提前到启动期：
  ```go
  if err := reg.Register(productrepo.NameProducts, &productrepo.ProductRepository{}); err != nil {
      return nil, fmt.Errorf("order module: %w", err)
  }
  ```
- 模式 B 只允许出现在 service 层，禁止 controller / middleware；
- 使用模式 B 的 service 必须在字段/构造函数注释里列出它取用的名字，便于审查；
- service 不得把注册表或取出的仓库缓存到包级变量，也不得跨请求保存。

**选择建议**：业务上必需的依赖优先用模式 A；跨模块读取点多、依赖按分支使用的
场景可以用模式 B，但每个取用名字都要在工厂校验，避免整个应用退化成 service locator。

### 5.6 跨模块调用规则

> **部分被取代**：下表的 `service → service` / `service → repository` 方向已改为
> `service → 对方 innerapi`；`ProductView` 只读视图迁到 `product/innerapi.Product`。
> 见 [module-layering-design.md](./module-layering-design.md) §3.2 / §6。
> **仍然有效**：repository 不声明接口（只用具体类型）、controller 不碰其他模块、
> repository 不 import service。

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
repository。两种方向都显式依赖导出的构造函数、具体类型和只读视图，不通过全局变量
或反射取用。

导入规则：

- 允许 `service` import 其他模块的 `repository`（具体类型 + 只读视图）或 `service`
  （导出构造函数）；
- 禁止 repository import 任何 service；
- 需要事件或回调解耦的双向依赖，通过领域事件 / 接口下沉解决，不能靠全局注册表绕环。

**repository 不声明接口（P6）**：`order/repository` 和 `product/repository` 都只导出
具体类型（`*OrderRepository`、`*ProductRepository`），不再定义
`OrderRepositoryInterface` / `ProductRepositoryInterface`，也不写编译期断言。理由是
每个仓库只有一种 GORM 实现，接口除了让测试塞 fake 之外没有替换价值，反而多一份要
跟着方法签名同步维护的清单。跨模块契约由**导出的方法 + 只读视图**（`ProductView`）
承担，持久化 `model` 仍留在属主模块内。代价是测试不能替换实现，改用纯 Go SQLite
跑真实 SQL（§5.10）；异常路径在驱动层注入。service 若确实出现双向依赖需要解环，
仍可以在消费方声明最小接口，但那是 service 之间的事，不回到 repository 层。

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

// WithContext 返回本次调用应使用的连接：ctx 中有事务时返回事务，否则返回 fallback。
func WithContext(ctx context.Context, fallback *gorm.DB) *gorm.DB
```

repository 的连接解析统一改为：

```go
func (r *ProductRepository) conn(ctx context.Context) *gorm.DB {
    fallback := r.db
    if fallback == nil {
        fallback = appdb.Db // 单库：所有业务 repository 共用 db.default
    }
    return gofydb.WithContext(ctx, fallback)
}
```

约定：

- 所有 repository 查询使用 `conn(ctx)`，不再直接读包级句柄；
- 跨模块只读调用可以不显式开事务；需要一致性的读写在同一个 `Transaction` 内；
- 事务由调用方 service 开启，跨模块调用的 repository/service 只消费 ctx，不自行提交；
- 嵌套 `Transaction` 直接复用外层事务，不创建 savepoint：内层返回错误会冒泡到外层，
  内层成功也不会提前提交；
- `WithContext` 优先返回 ctx 中的事务，没有事务时才用 repository 的显式连接或
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

- Registry 的生命周期 = 一次装配，由模块工厂新建，`Reload` 后重建；
- 每次 `Reload` 重新执行工厂并替换 `app/io` 句柄，无状态 repository 下一次查询
  自然读到新连接；旧注册表随旧模块一起淘汰；
- 旧模块在新模块启动完成前继续服务，期间两代 repository 并存是预期行为；
- 除 `service.RepoDeps` 这份无状态默认集合外，任何 repository 都不允许存到包级变量、
  `Runtime` 或全局 map；注册表本身永远不是包级变量；
- 后台 worker / 脚本不经过模块装配时，直接构造无状态 repository；脚本没有
  `app/io` 生命周期，`script/fix_order.go` 把 `rt.DB` 临时发布到 `appdb.Db`
  并在调用后恢复，不从请求上下文的注册表拿。

### 5.9 错误与诊断

- 注册失败（空名、nil、重复）→ 工厂返回错误 → 启动或热更新失败，保留旧实例；
- 取用失败（名字缺失、类型不符）→ 错误包含名字、期望类型和实际类型；
- 启动日志可输出 `reg.Names()`，按模块分组，便于确认实际注册了哪些仓库；
- 没有兜底实现：注册表里缺名字就是错误。`service.RepoDeps` 只是包级默认实例，由工厂
  直接传给构造函数，不参与注册表取用，也不是取用失败时的回退。

### 5.10 测试

repository 是具体类型，测试不再塞 fake，而是用纯 Go SQLite（`support/sqlite`，
cgo-free）建内存库跑真实 SQL，不连外部 MySQL：

- HTTP 用例的 `newTestDB` 建一个同时含订单表和产品表的 `:memory:` 库，
  `AutoMigrate` 后塞固定数据：订单 1（用户 100、已支付）、产品 90001 上架 /
  90002 下架；`newTestService` 把内存库发布成 `appdb.Db`（`t.Cleanup` 恢复），
  注册表里填的是零值构造的真实产品仓库：
  ```go
  appdb.Db = conn
  reg := registry.NewRegistry()
  reg.MustRegister(productrepo.NameProducts, &productrepo.ProductRepository{})
  svc := service.NewOrderService(&repository.OrderRepository{}, reg, zap.NewNop())
  ```
- “产品不存在 / 已下架”等分支由种子数据驱动（下单 SKU 404 / 90002），
  断言仍然是 `40000` 且不落单；
- panic 用例改在驱动层注入：`panicPool` 包装 `gorm.ConnPool`，遇到查询参数 `9999`
  就 panic，验证 Recovery 返回 `50000` 且不泄漏内部信息。注意 GORM 执行 SQL 用的是
  `db.Statement.ConnPool`（`Open` 时从 `Config` 复制一份），只替换 `Config.ConnPool`
  不生效，两处都要赋值；关闭连接要在包装前取出 `conn.DB()`，因为包装后的池不再是
  `*sql.DB`，`sqlite.Close` 取不到底层连接；
- 本模块仓库要换实例时另建 `service.RepositoryDeps{Orders: &repository.OrderRepository{}}`
  并直接传给 `NewOrderService`；不改包级 `service.RepoDeps`，也不需要注册表；
- 模块工厂无参数，测试直接连续调用两次验证“每次装配新建注册表、不撞名字”，
  并验证 `rt.DB == nil` 时启动失败；
- `support/registry` 自身单测覆盖：空名字、nil、重复注册、并发注册与取用、
  `Get[T]` 类型不符、`Names` 排序和副本语义；
- 跨模块事务用例放在 `order/service/transaction_test.go`（service 是组合其他模块
  repository 的地方），同样用 SQLite 内存库验证：
  - 事务上下文被两个模块的 repository 共同识别，扣库存 + 建订单一起提交或一起回滚；
  - 同一事务内能读到刚写入的订单；
  - 库存不足时 `DecreaseStock` 返回 0 行且库存不变，非正数 `num` 直接报错。

## 6. 示例：订单服务读取产品名称

> **已被取代**：订单服务不再持注册表按需取产品 repository，而是在构造期接收
> `app/container` 注入的 `ProductAPI` 接口实现；产品视图为
> `product/innerapi.Product`。
> 最新示例见 [module-layering-design.md](./module-layering-design.md) §7–§9 与
> [library-design.md](./library-design.md) §9。下文保留原文供追溯。

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
// 只导出具体类型，不声明接口；跨模块契约就是这几个导出方法 + ProductView。
func (r *ProductRepository) FindByID(ctx context.Context, id uint64) (*ProductView, error)
func (r *ProductRepository) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]ProductView, error)
func (r *ProductRepository) DecreaseStock(ctx context.Context, id uint64, num int) (int64, error)

// ProductView 是跨模块只读视图，订单服务只依赖它，不 import product/model。
type ProductView struct {
    ID     uint64
    Name   string
    Price  int64
    Stock  int
    Status int8
}

// 无构造参数：仓库不持连接和日志，查询时解析 appdb.Db（单库）。
type ProductRepository struct{}
```

`ListByIDs` 返回 `map[uint64]ProductView`，订单服务据此批量补全 SKU 名称；产品不
存在或已下架时由订单 service 决定报错还是跳过，repository 不做业务判断。

### 6.2 订单 service（模式 B）

```go
type OrderService struct {
    // repos 用于按需取用 product.products（模式 B）；实例由模块工厂在装配期登记。
    repos  *registry.Registry
    orders *orderrepo.OrderRepository
    logger *zap.Logger
}

func NewOrderService(
    orders *orderrepo.OrderRepository,
    repos *registry.Registry,
    logger *zap.Logger,
) *OrderService
```

创建订单或拼装响应时批量查产品名称：

```go
// productNames 按 SKU 批量取名称，订单服务只依赖产品 repository 的只读视图。
func (s *OrderService) productNames(ctx context.Context, skuIDs []uint64) (map[uint64]string, error) {
    products, err := registry.Get[*productrepo.ProductRepository](s.repos, productrepo.NameProducts)
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

说明：订单服务只依赖产品 repository 的导出方法和 `ProductView`，不 import 产品
`model`；跨模块调用点集中在 service 层。

### 6.3 模块工厂

完整工厂见 §5.3。示例里只有一处登记：

```go
// app/module/order/module.go（工厂内部）
// 本模块仓库由 service 包的 RepositoryDeps 直接持有，随构造函数注入，不登记。
// 跨模块仓库：名字和实现归产品模块，订单模块作为消费方负责登记。
if err := reg.Register(productrepo.NameProducts, &productrepo.ProductRepository{}); err != nil {
    return nil, fmt.Errorf("order module: %w", err)
}
svc := service.NewOrderService(service.RepoDeps.Orders, reg, rt.Logger)
```

只有被跨模块读取的仓库需要名字：产品仓库登记为 `product.products`（属主 = 产品
repository 包，登记方 = 订单工厂），订单仓库没有名字、不进入注册表，由
`service.RepoDeps.Orders` 直接注入。注册表随工厂每次执行重建，`reload.go` 只写
`order.NewModuleFactory()`。

## 7. 迁移计划

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| P0（本文） | 方案评审，确认取用模式、跨模块写规则、事务范围、注册表落点 | 文档确认 |
| P1（已落地，组合层在 P5 移除） | 新增 `gofy/support/registry`（通用注册表 + 单测 + README + CHANGELOG）；新增 `app/repository`（Provider / assemble / names / accessors）；把订单 repository 迁入注册表；`reload.go` 每个装配周期新建 Provider | `support/registry` 单测通过；订单功能回归通过；缺少注册时启动失败 |
| P2 | 新增 `migrations/0002_products.sql`（产品表 + 演示数据）、产品 `model` / `repository`（基于 `db.default`）；订单 service 用模式 B 读取产品名称并校验下单 SKU；补注册表/仓库测试 | 下单校验产品存在且上架；订单 DTO/响应带产品名称；单测不连外部库 |
| P3（已落地 2026-11-09） | 补 `support/db` 事务上下文；repository 改用 `conn(ctx)`；覆盖跨模块事务用例 | 扣库存 + 建订单可回滚 |
| P4（已落地 2026-11-09） | 增加 import guard（controller 禁止取 repository、repository 禁止 import service、跨模块聚合上限）；更新 README / library-design / CHANGELOG | `make check` 与三仓库回归通过 |
| P5（已落地 2026-11-09） | 删除 `app/repository`（Provider / assemble / names / accessors 及其测试）；`RepositoryDeps` + `RepoDeps` + `init()` + `InitRepository(reg)` 落到 `order/service/a_init_repository.go`；`NameProducts` 落到 `product/repository`；注册表改由 `order.NewModuleFactory()` 每次装配新建；跨模块事务用例迁到 `order/service/transaction_test.go`；import guard 去掉组合层豁免 | 连续装配两次不撞名字；`rt.DB == nil` 启动失败；`make check` 与 demo 回归通过 |
| P6（已落地 2026-11-11） | 删除 `OrderRepositoryInterface` / `ProductRepositoryInterface` 及编译期断言；`RepositoryDeps.Orders`、`NewOrderService`、`registry.Get[T]` 全部改用具体类型；订单模块测试去掉 fake，改用 SQLite 真实仓库 + 驱动层 `panicPool`；同步 README / 设计文档 / CHANGELOG | `go vet` 与订单模块测试通过；HTTP 行为、错误码、契约不变 |
| P7（已落地 2026-11-11） | repository 去掉 `New` / `NewFromHandle` 与 `db` / `logger` 字段，改为零值可用的无状态类型，`conn(ctx)` 固定回落 `appdb.Db`；`RepoDeps` 改为 `var` 零值构造并删除 `init()`；`script/fix_order.go` 临时发布 `rt.DB`；测试把内存库发布成 `appdb.Db`；同步 README / 设计文档 / CHANGELOG | `go vet` 与 demo 回归通过；事务、热更新与 HTTP 行为不变 |
| P8（已落地 2026-11-11） | 删除 `NameOrders` 及其登记链路（`RepositoryDeps.Register`、`InitRepository`、`order.orders` 注册项）；`RepositoryDeps` / `RepoDeps` 只作为本模块仓库声明，由工厂直接传给 service；注册表只保留真正被跨模块取用的 `product.products`；同步 README / 设计文档 / CHANGELOG | 订单仓库不再登记名字；产品取用、事务与 HTTP 行为不变；demo 与根仓库 `make check` 通过 |

每阶段的回滚点：P1 只新增包并迁移订单，不改变产品模块；P2 的产品仓库先从只读
查询开始，产品表用 `IF NOT EXISTS` + `INSERT IGNORE`，可重复执行；P3 之前跨模块写
操作可以直接调 repository（同库），但还不保证多步原子性，P3 完成后统一走 ctx 事务；
P5 只动装配代码和文档，不动表结构、契约和 HTTP 行为，回滚即恢复 `app/repository`
并把工厂签名改回接收 Provider；P6 同样不动表结构和 HTTP 行为，回滚即重新声明两个
repository 接口并把字段/泛型参数改回接口类型；P7 的仓库无状态化可单独回滚，恢复
`New(db, logger)` 和字段即可；P8 只删注册链路，回滚即恢复 `NameOrders`、
`RepositoryDeps.Register` 和 `InitRepository`，不涉及表结构、契约和 HTTP 行为。

## 8. 约束与自动校验

> **已被取代**：import guard 改为按 5 层（controller / service / innerapi / repository / model）
> 校验，跨模块只允许 import 对方 `innerapi`，不再有「一个包最多 import 一个其他模块 repository」
> 的上限规则。最新规则见 [module-layering-design.md](./module-layering-design.md) §10 与
> `examples/demo-service/shell/check-imports.sh`。下文保留原文供追溯。

已在 `examples/demo-service/shell/check-imports.sh` 落地 import guard（沿用根仓库
`shell/check-imports.sh` 的写法），由 demo 与根仓库的 `make guards` / `make check`
调用。脚本只检查非测试文件的 import（`go list` 的 `.Imports` 不含 `_test.go`），
测试仍可同时引用多个模块的 `model` 和 `repository` 来建 SQLite 库；包依赖成环由
`go build` 直接报错兜底。

1. `app/module/*/controller` 不得依赖任何 `model`、任何模块的 `repository`，以及其他
   模块的 `service`；只允许 import 本模块的 `service`；
2. `app/module/*/repository` 不得依赖任何 `service` 包；
3. 同一个包最多 import 一个“其他模块”的 `repository`（本模块的 repository 不计入）；
   没有集中组合层可以豁免，需要聚合更多模块的数据时把读取点拆到各自的 service；
4. `app/module/*/service` 可以依赖其他模块 `repository` / `service`，但依赖图必须无环，
   由 `go build` 的包依赖检查兜底。

根仓库的 `make guards` 另有“公开包不得写 `func init()`”一条，只约束 gofy 框架包；
demo 的 `app/**` 业务包不在范围内。demo 目前也没有 `init()`：默认仓库集合是 `var RepoDeps`
零值构造（P7），登记链路已在 P8 删除。

代码评审清单：

- 新增本模块 repository 是否加进 service 的 `RepositoryDeps`，并由模块工厂传给
  service（本模块仓库不登记名字）；
- 新增 repository 是否只导出具体类型（不声明接口），跨模块返回只读视图而非 `model`；
- 需要被跨模块读取的 repository 是否在属主模块定义名字常量，消费方工厂是否把它
  登记进本次装配的注册表；
- service 的跨模块依赖是否显式声明（模式 A）或走模式 B 且在工厂登记；
- 跨模块写是否优先走 service；直接调 repository 时是否在同一 ctx 事务内且不绕过业务不变量；
- 事务内的跨模块调用是否传入同一 ctx；
- 除无状态的 `service.RepoDeps` 外，是否把 repository 或注册表缓存到包级变量或跨装配结构。

## 9. 对现有文档的影响

- `library-design.md` §3.3「`Runtime` 不是 DI 容器」保持有效，本方案不向 `Runtime`
  塞业务对象；
- `library-design.md` 新增 `support/registry` 章节，说明通用注册表与
  `support/middleware.Registry` 的分工；
- `library-design.md` §9 的模块装配示例改为“工厂新建注册表 + 直接注入本模块仓库 +
  只登记跨模块仓库”，不再出现 `app/repository.Provider` 和 `service.InitRepository`；
- `examples/demo-service/app/module/order/README.md` 的“跨模块依赖由 module.go 装配
  后注入”要补充两种取用模式，并把订单服务改为模式 B 示例；产品表初始化步骤写在
  该 README 的“数据表”一节；P5 后还要写明 `service/a_init_repository.go` 的职责，
  P7/P8 后更新为“`RepositoryDeps` + `RepoDeps` 只声明本模块仓库，不登记名字”；
- CHANGELOG 在 P1/P2 落地时新增 `support/registry` 和产品仓库条目；P5 把原
  `app/repository` 条目改写为“组合层已删除，注册表由模块工厂新建”；
- P3 在 `support/db` README 与 `library-design.md` §3.7 补充事务上下文 API 和约定；
- P4 的 import guard 路径与规则记录在本文 §8，并在 demo README 与 CHANGELOG 中说明；
  P5 去掉组合层豁免后同步更新这两处描述；
- P6 把 `library-design.md` §9 的 service / repository 代码片段、`support/registry`
  README 的 `Get[T]` 示例、demo 订单 README 的分层表和测试说明、CHANGELOG 全部改成
  具体类型，并写明测试改用 SQLite；
- P7/P8 同步 `library-design.md`、demo 订单 README、`support/registry` README 与
  CHANGELOG：仓库不再接连接和日志、`RepoDeps` 是零值 `var`、注册表只登记跨模块仓库，
  全文不再出现 `NameOrders` / `InitRepository`。

## 10. 风险与取舍

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 注册表膨胀 | 所有 repository 一个 map，最终变成“上帝注册表” | 名字按模块前缀分组；规模过大时按域拆分多个 Registry |
| 模式 B 隐式依赖 | service 内部取用，依赖不可见 | 只作为例外；消费方工厂装配期登记；字段/构造函数注释列出名字 |
| 跨模块写绕过不变量 | 直接调对方 repository 修改数据 | 简单写允许；带业务不变量时走 service；写调用必须在同一个 ctx 事务内 |
| 事务改造面大 | 所有 repository 都要改用 `conn(ctx)` | P3 统一改造并加测试；未改造前跨模块多步写不保证原子性 |
| 模型泄漏 | 跨模块返回 `model` 导致耦合 | 只返回 repository 包的只读视图/DTO |
| 热更新竞争 | 旧模块仍在使用旧 repository | 工厂每次装配新建注册表；除无状态 `RepoDeps` 外不在包级缓存；新旧交换沿用现有机制 |
| 仓库不记日志 | 仓库拿不到 `rt.Logger`，也不应重复记录错误 | 仓库只包装并返回错误；service/module 用 `rt.Logger` 记录一次（P7 已确认） |
| 重复登记 | 多个消费方工厂登记同一个名字到各自注册表 | 注册表按工厂隔离，互不影响；实例无状态，重复构造只多一次轻量分配 |
| 仓库无法替换实现 | repository 是具体类型，测试不能塞 fake | 测试用纯 Go SQLite 跑真实 SQL；异常路径在驱动层（`ConnPool`）注入；真出现第二实现时再按需下沉接口 |

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
   业务名字与装配留在应用（P5 后归各模块，见 11.2）。

### 11.2 已确认（2026-11-09，P5）

6. **删除组合层**：`app/repository`（Provider / Assemble / names / accessors）整体
   删除。注册表由消费方模块工厂在每次装配时 `registry.NewRegistry()` 新建，
   `reload.factories(cfg)` 只返回工厂列表，不再持有跨工厂共享状态。
7. **仓库集合归 service 包**：`RepositoryDeps`、包级默认 `RepoDeps` 放在
   `order/service/a_init_repository.go`（P5 时的 `Register`、`InitRepository(reg)`
   已在 P8 删除）。连接推迟到查询时解析（P5 用 `init()` + `NewFromHandle`，P7 起
   改为零值构造并删除 `init()`）。P4 的 `RepositoryRegistrar` 接口随之删除。
8. **产品仓库由订单工厂登记**：`NameProducts` 常量归 `product/repository`（属主），
   订单模块是唯一消费方，由 `order.NewModuleFactory()` 构造
   `&productrepo.ProductRepository{}` 并登记；取用直接写
   `registry.Get[*productrepo.ProductRepository](reg, productrepo.NameProducts)`。
9. **测试随层迁移**：跨模块事务用例从 `app/repository/transaction_test.go` 迁到
   `order/service/transaction_test.go`；工厂测试改为“连续装配两次不撞名字 +
   缺数据库启动失败”，替换实例通过另建 `RepositoryDeps` 注入，不改包级 `RepoDeps`。

### 11.3 已确认（2026-11-11，P6）

10. **repository 不声明接口**：删除 `OrderRepositoryInterface` 与
    `ProductRepositoryInterface`（含 `var _ … = (*…)(nil)` 断言）。每个仓库只有一种
    GORM 实现，接口没有替换价值，只多一份要跟方法签名同步的清单；`RepositoryDeps`、
    service 字段与构造函数、`registry.Get[T]` 一律用 `*OrderRepository` /
    `*ProductRepository`。跨模块契约由导出方法 + `ProductView` 承担。
    代价与对策：测试不能塞 fake，改用纯 Go SQLite 内存库跑真实 SQL，panic 等异常
    路径在 `gorm.ConnPool` 层注入（§5.10）。

### 11.4 已确认（2026-11-11，P7）

11. **repository 不注入连接和日志**：删除 `New(db, logger)`、`NewFromHandle(logger)`、
    `newRepository` 以及 struct 的 `db` / `logger` 字段，`OrderRepository` /
    `ProductRepository` 变成零值可用的空结构体；`conn(ctx)` 固定为
    `gofydb.WithContext(ctx, appdb.Db)`。理由：仓库只有一种连接来源（`app/io` 句柄）
    和一种日志来源（service 的 `rt.Logger`），构造函数只是把包级依赖再传一遍。
    删除后 `RepoDeps`、工厂、测试都直接用 `&repository.OrderRepository{}`；脚本模式没有
    `app/io` 生命周期，`script/fix_order.go` 调用前把 `rt.DB` 发布到 `appdb.Db`
    并在结束后恢复。

### 11.5 已确认（2026-11-11，P8）

12. **本模块仓库不登记名字**：删除 `NameOrders`、`RepositoryDeps.Register`、
    `InitRepository` 以及 `order.orders` 注册项。`RepositoryDeps` / `RepoDeps` 只回答
    “本模块有哪些仓库、默认实例是什么”，实例由模块工厂直接传给 `NewOrderService`；
    注册表只保留真正被跨模块按名取用的 `product.products`。理由：订单仓库没有任何
    跨模块消费者，登记名字只多一条无人读取的链路和一次重复注册的失败面。以后某个
    本模块仓库需要被别的模块读取时，再在属主包加名字常量并由消费方工厂登记。
