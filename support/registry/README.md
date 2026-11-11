# support/registry

显式的**名字 → 实例**注册表，供应用在启动装配时登记跨模块共享的实例
（repository、client、service 等），再按名字取用。它解决的是「service 如何
即取即用地拿到其他模块的依赖」，同时避免包级单例：注册表是普通对象，
每次装配（启动 / 热更新）新建一份，随依赖显式传递。

包只依赖标准库，不持有全局状态，所有方法都可并发调用。

## 快速开始

```go
import (
    "fmt"

    "github.com/bingcool/gofy/support/registry"
    productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
)

// 组合根（reload.factories）每次装配新建一份共享注册表，传给各模块工厂。
reg := registry.NewRegistry()

// 属主模块工厂登记自己实现 innerapi 接口的 service；名字常量归 innerapi 包。
if err := reg.Register(productapi.Name, productSvc); err != nil {
    return err
}

// 消费方模块工厂在构造期按接口取用，显式注入 service 构造函数。
products, err := registry.Get[productapi.ProductAPI](reg, productapi.Name)
if err != nil {
    return fmt.Errorf("order module: %w", err)
}
svc := service.NewOrderService(service.Dependencies{
    Orders:   orders,
    Products: products,
    Logger:   logger,
})
```

`Get[T]` 的 `T` 是属主模块发布的 **innerapi 接口**（demo 里是
`productapi.ProductAPI`），注册进去的是实现该接口的 service。名字常量归 innerapi
包（`productapi.Name = "product.innerapi"`）。组合根建共享注册表，属主模块工厂
自登记、消费方工厂构造期取用；登记或取用失败即启动失败，不会拖到请求期才暴露。
service 不把注册表缓存到包级变量，也不在请求期按需取用——跨模块依赖在装配期就
固定下来，依赖可见、测试可换实例。

## API

| 方法 | 说明 |
| --- | --- |
| `NewRegistry() *Registry` | 创建空注册表 |
| `Register(name string, instance any) error` | 注册实例；空名字、nil、重复名字报错 |
| `MustRegister(name string, instance any)` | `Register` 的 panic 版本，用于启动期装配 |
| `Lookup(name string) (any, bool)` | 按名字取原始实例；未注册返回 `(nil, false)` |
| `Names() []string` | 返回排序后的名字副本，供启动日志和诊断 |
| `Get[T](r *Registry, name string) (T, error)` | 类型化取用；未注册或类型不符时报错 |

## 约定

- **名字是契约**：使用方用常量定义，demo 里归 innerapi 包，格式 `<module>.innerapi`，
  例如 `product.innerapi`；注册表本身不校验格式。
- **显式注册**：没有默认实现、没有兜底。缺依赖就是错误，启动期失败优于请求期失败。
- **不覆盖**：同一名字重复注册返回错误，避免两个实现抢同一个名字。
- **按装配隔离**：每次启动 / `Reload` 新建注册表，旧实例不会跨代复用。
- **不放基础设施全局**：logger、db 句柄等如无跨模块取用需求，不要塞进注册表。
- **不要手写断言**：调用处统一使用 `Get[T]`，错误信息会带上名字和类型。

## 与 support/middleware.Registry 的区别

| | `support/registry` | `support/middleware.Registry` |
| --- | --- | --- |
| 存放内容 | 任意跨模块实例 | 只存 `support.Middleware` |
| 类型 | `any` + 泛型 `Get[T]` | 强类型 |
| 使用方 | 属主模块工厂登记、消费方构造期取用 | Router / adapter 解析契约中间件名 |

两者都遵循「显式注册、按名解析、启动失败」，但职责不同，不合并。
