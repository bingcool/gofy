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
)

const nameProductRepository = "product.products"

reg := registry.NewRegistry()
if err := reg.Register(nameProductRepository, productRepository); err != nil {
    return err
}

// 装配期取用（推荐）：一次取出，显式传给 service 构造函数
products, err := registry.Get[*productrepo.ProductRepository](reg, nameProductRepository)
if err != nil {
    return fmt.Errorf("order module: %w", err)
}
```

service 持有注册表时按需取用（即取即用）：

```go
type OrderService struct {
    repos *registry.Registry // 取用 product.products
}

func (s *OrderService) productNames(ctx context.Context, ids []uint64) (map[uint64]string, error) {
    products, err := registry.Get[*productrepo.ProductRepository](s.repos, nameProductRepository)
    if err != nil {
        return nil, err
    }
    return products.Names(ctx, ids)
}
```

`Get[T]` 的 `T` 就是注册进去的具体类型（demo 里 repository 不声明接口，直接注册
`*ProductRepository`）。消费方模块工厂负责登记它 service 会取用的每个名字，登记
失败即启动失败，不会拖到请求期才暴露。

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

- **名字是契约**：使用方用常量定义，应用层建议 `<module>.<aggregate>`，
  例如 `product.products`；注册表本身不校验格式。
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
| 使用方 | 模块工厂登记、service 取用 | Router / adapter 解析契约中间件名 |

两者都遵循「显式注册、按名解析、启动失败」，但职责不同，不合并。
