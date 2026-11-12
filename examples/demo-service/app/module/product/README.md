# app/module/product

演示模块：产品能力有两条出口。

- **HTTP**：绑定 `gofy-interface-service` 的 `productroute.Products` 分组，暴露
  只读接口 `GET /api/v1/product/products/{id}` 与
  `GET /api/v1/product/products`（分页）。controller 只做契约 ↔ service 映射。
- **innerapi**：向其它模块发布 `ProductAPI`。订单 service 下单时经它读取产品、
  扣减库存；跨模块只依赖这个纯契约包，不接触本模块的 controller / service /
  repository / model。

分层取舍的权威说明见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md)。

## 目录结构

```
product/
├── module.go                  # 模块根：NewModule(service, logger, bindOptions)；
│                              #   Prefix() 返回 ""，Register 绑定 Products 分组
├── module_test.go             # SQLite + gofytest：详情、404 映射、分页
├── controller/
│   └── product_controller.go  # 实现 demoservice.ProductsService，映射契约 DTO
├── service/
│   ├── product_service.go     # 实现 innerapi.ProductAPI：读取 + 分页 + 扣库存
│   ├── deps.go                # Dependencies（本模块仓库 + Logger）
│   └── product_service_test.go
├── innerapi/
│   └── innerapi.go            # ProductAPI 接口 + Product 只读视图 + 哨兵错误
├── repository/
│   └── product_repository.go  # GORM 仓库，导出具体类型，返回 model
└── model/
    └── product.go             # 持久化模型（products 表）
```

## 分层约定

| 层 | 职责 | 依赖限制 |
| --- | --- | --- |
| module | 模块清单成员：`Name` / `Prefix` / `Register` | 只 import 本模块 controller/service、`support` 与契约路由包 |
| controller | 契约 ↔ service 映射、错误码转换 | 不 import `model`/`repository`/`innerapi`/其它模块 |
| service | 业务用例：读取、分页、扣库存、0 行时区分不存在 / 下架 / 库存不足 | 实现本模块 `innerapi`；不 import HTTP 契约、其它模块 |
| innerapi | 模块对外纯契约：接口 + 只读视图 + 哨兵错误 | 只允许标准库；不 import `service`/`repository`/`model` |
| repository | 持久化，返回 `model` | 不 import `service`/`innerapi` |
| model | 表结构 | 只允许 `gorm` / 标准库 |

## 路由与契约

契约分组 `productroute.Products` 自带完整部署前缀 `/api/v1/product`，组内路径
只写 `/products`、`/products/{id}`。模块根因此实现 `Prefix() string { return "" }`，
把分组直接挂在根 Router 上；`Register` 调用一次
`gofyadapter.BindGroup(r, productroute.Products, handler, bindOptions)`，adapter
按 `OperationID` 反射找到 controller 的 `GetProduct` / `ListProducts`。

- 分组中间件只声明别名 `audit`、`auth`，由 App 启动时注册进共享中间件注册表；
- 产品是只读接口，**不声明** `Spec.RateLimit`，`BindOptions` 只原样转交；
- 产品不存在时 service 返回 `innerapi.ErrNotFound`，controller 用
  `errors.Is` 判断后返回契约错误 `demoservice.ErrProductNotFound`（60001/404）。

`list` 接口包含已下架产品，按 `id DESC` 分页；是否可售由 DTO 的 `on_sale`
表达，调用方自行过滤。

## 装配方式

service 由装配根 `app/container` 构造（`ProductService()` 惰性槽 + 本次服务的
仓库与 `logger`），订单模块通过 `innerapi.ProductAPI` 拿到同一实例。模块工厂
`ProductModuleFactory` 检查 `rt.DB`、`Init` logger，再把 `c.BindOptions()` 一并
传给 `product.NewModule`。模块根只负责路由装配，service 装配仍集中在容器。
