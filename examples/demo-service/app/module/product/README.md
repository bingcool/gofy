# app/module/product

演示模块：本轮**不挂 HTTP 路由**，产品能力只通过 `innerapi` 发布给其它模块。
消费方是订单 service —— 下单时经 `innerapi.ProductAPI` 读取产品、扣减库存；
HTTP 契约只出现在 order 模块的 controller 里。分层取舍的权威说明见
[`docs/module-layering-design.md`](../../../../docs/module-layering-design.md)。

## 目录结构

```
product/
├── module.go                  # 模块根：NewModule 保存 service/logger；Register 不挂路由
├── module_test.go             # 模块身份 + 「不注册任何路由」的约定测试
├── controller/
│   └── product_controller.go  # 暂保留内存示例，接契约时改为依赖 service
├── service/
│   ├── product_service.go     # 实现 innerapi.ProductAPI：读取 + 扣库存 + 0 行分类
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
| module | 模块清单成员：`Name` / `Prefix` / 空 `Register` | 只 import 本模块 `service` 与 `support`；不碰 `app/io`、契约 |
| controller | 内存样例，尚未接入路由 | 不 import `model`/`repository`；接契约后只依赖本模块 `service` |
| service | 业务用例：读取、扣库存、0 行时区分不存在 / 下架 / 库存不足 | 实现本模块 `innerapi`；不 import HTTP 契约、其它模块 |
| innerapi | 模块对外纯契约：接口 + 只读视图 + 哨兵错误 | 只允许标准库；不 import `service`/`repository`/`model` |
| repository | 持久化，返回 `model` | 不 import `service`/`innerapi` |
| model | 表结构 | 只允许 `gorm` / 标准库 |

## 装配方式

service 由装配根 `app/container` 构造（`ProductService()` 惰性槽 + 本次服务的仓库与
`logger`），订单模块通过 `innerapi.ProductAPI` 拿到同一实例。模块根不参与
service 装配，也不发布路由；`Register` 为空实现，只保证每个模块走同一套
工厂 → `Register` → `Start` 生命周期。

路由后续按契约另议：新增 `productservice` 契约组后，controller 改为适配契约
并在 `Register` 里调用 `gofyadapter.BindGroup`，同时实现 `Prefix()` 返回空串，
让契约自带的完整部署前缀直接挂在根 Router 上。当前没有路由，因此**不实现**
`Prefixer`：order 已经占用空前缀，重复声明会被 gofy 的前缀去重检查拒绝。
