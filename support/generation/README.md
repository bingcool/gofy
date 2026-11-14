# support/generation

把「当前一代」就绪句柄做成可原子替换的集合，并允许入口把其中一代固定到
`context.Context`。用来解决热更新（`Reload`）与并发请求的关系：旧一代正在
关闭，新请求仍在进入；请求一旦进入，就必须从头到尾使用同一代连接。

包只持有指针、不做业务解释，也不是全局注册表：应用在装配根新建 `Set`，由模块
`Start` 发布新一代，HTTP 中间件、脚本包装器、守护进程在入口把当前代固定进
`ctx`，共享代码只调用 `WithContext(ctx)`。

## 快速开始

```go
import (
    "context"

    "github.com/bingcool/gofy/support/generation"
)

// 装配根创建一次；整个进程生命周期复用（不要每次请求新建）。
var current = generation.NewSet[Snapshot]("app/appio")

// 模块 Start：本代句柄全部就绪后一次发布。
current.Publish(snapshot)

// 模块 Stop：只撤回仍指向自己的那一代，Reload 已发布的新一代不受影响。
current.CompareAndSwap(snapshot, nil)

// HTTP 入口：进入请求时固定当前代。
engine.Use(current.Middleware())

// 脚本 / 守护进程入口：直接固定。
ctx := current.WithSnapshot(ctx, snapshot)

// 共享代码（service、repository、后台任务）：只读固定代。
snapshot := current.WithContext(ctx)
```

## API

| 方法 | 说明 |
| --- | --- |
| `NewSet[T](name string) *Set[T]` | 创建空集合；`name` 出现在 panic 信息里 |
| `Name() string` | 集合名 |
| `Publish(v *T)` | 发布当前代；`nil` 表示撤回 |
| `Current() *T` | 当前代；未发布时为 `nil` |
| `CompareAndSwap(old, next *T) bool` | 仅当当前代仍是 `old` 时替换，模块停止时用 |
| `WithSnapshot(ctx, v) context.Context` | 把 `v` 固定到 `ctx`；`nil` ctx 用 `context.Background()` |
| `WithContext(ctx) *T` | ctx 固定优先，否则回退 `Current()`；两者都没有就 panic |
| `Middleware() gin.HandlerFunc` | 请求入口固定当前代；未发布时原样放行 |

## 约定

- **入口发布，共享代码只读**：HTTP 中间件、脚本包装器、模块 `Start` 负责
  `Publish` / `WithSnapshot`；`service`、`repository`、后台任务只调用
  `WithContext(ctx)`，不要自己发布，也不要退回包级句柄。
- **固定值优先**：在 N 代进入的请求，即使 Reload 发布了 N+1，也继续使用 N 直到
  请求结束。
- **缺失即 panic**：两个来源都没有，说明入口漏了发布。`WithContext` 直接 panic
  并带上集合名，避免 nil 句柄在离现场很远的地方解引用。
- **`CompareAndSwap` 而不是 `Publish(nil)`**：Reload 先启动新模块再停旧模块，
  旧模块只能撤回自己那一代，否则会抹掉新一代。
- **一个进程一个 `Set`**：`Set` 内含原子指针，首次使用后不可复制；不同 `Set`
  的固定值互不可见，因此一个服务可以有多个相互隔离的代（例如 IO 与别的资源）。
- **不替代 `support/registry`**：注册表解决「按名字取装配期实例」，`Set` 解决
  「按代取运行时句柄」，两者职责不同。

## demo 用法

`examples/demo-service/app/appio` 是完整示例：`Snapshot` 汇总一代的 db、redis、
amqp、kafka、rocketmq、uuidint 句柄，`currentSnapshot` 就是
`generation.NewSet[Snapshot]("app/appio")`；`Middleware`、`WithContext`、
`WithSnapshot`、`Current`、`Publish` 都是薄封装，`Module.Stop` 用
`CompareAndSwap` 撤回本代。脚本侧由 `script/register.go` 的 `withIO` 在
`Run` 前后执行同一套 `Start` / `Stop`。
