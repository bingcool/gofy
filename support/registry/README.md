# support/registry

显式的**名字 → 实例**注册表，供应用在启动装配时登记跨模块共享的实例
（client、配置好的第三方客户端、多个同类型实现等），再按名字取用。注册表是
普通对象而非全局：每次装配（启动 / 热更新）新建一份，随依赖显式传递。

它是一个**可选工具**：需要按名字索引、或存在多份同类型实例时才划算。只有一个
跨模块实现时，在应用层装配根（demo 的 `app/container`）构造实例、按对方
`innerapi` 的接口注入消费方更简单：

```go
// app/container/container.go（每次装配执行一次）
products := productservice.NewProductService(...)
orders := orderservice.NewOrderService(orderservice.Dependencies{
    ProductAPI: products, // 以 productapi.ProductAPI 接口注入
    ...
})
```

不需要名字，也不需要把注册表传进模块工厂。包只依赖标准库，不持有全局状态，
所有方法都可并发调用。

## 快速开始

```go
import (
    "fmt"

    "github.com/bingcool/gofy/support/registry"
)

// 组合根每次装配新建一份注册表，随依赖显式传递。
reg := registry.NewRegistry()

if err := reg.Register("sms.primary", primaryClient); err != nil {
    return err
}
reg.MustRegister("sms.backup", backupClient) // 启动期装配，失败 panic

// 消费方按名字做类型化取用。
client, err := registry.Get[*sms.Client](reg, "sms.primary")
if err != nil {
    return fmt.Errorf("sms: %w", err)
}
```

名字常量建议由提供方包导出（例如 `const PrimaryName = "sms.primary"`），消费方
引用常量而不是手写字符串。登记或取用失败即启动失败，不会拖到请求期才暴露。

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

- **名字是契约**：使用方用常量定义，提供方包导出；注册表本身不校验格式。
- **显式注册**：没有默认实现、没有兜底。缺依赖就是错误，启动期失败优于请求期失败。
- **不覆盖**：同一名字重复注册返回错误，避免两个实现抢同一个名字。
- **按装配隔离**：每次启动 / `Reload` 新建注册表，旧实例不会跨代复用。
- **不放基础设施全局**：logger、db 句柄等如无跨模块取用需求，不要塞进注册表。
- **不要手写断言**：调用处统一使用 `Get[T]`，错误信息会带上名字和类型。
- **一个实现别上注册表**：单实现的跨模块依赖在装配根按 `innerapi` 接口注入，
  少一层名字与传递参数。

## 与 support/middleware.Registry 的区别

| | `support/registry` | `support/middleware.Registry` |
| --- | --- | --- |
| 存放内容 | 任意跨模块实例 | 只存 `support.Middleware` |
| 类型 | `any` + 泛型 `Get[T]` | 强类型 |
| 使用方 | 应用装配期登记，按名取用 | Router / adapter 解析契约中间件名 |

两者都遵循「显式注册、按名解析、启动失败」，但职责不同，不合并。
