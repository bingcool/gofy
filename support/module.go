// Package support contains the HTTP-facing building blocks: modules,
// routers, generic handlers, binding, validation and response rendering.
// 包 support 提供 HTTP 层的基础构件：模块、路由、有类型处理器、绑定、校验
// 与响应渲染；这些构件只在路由注册阶段使用，不承担连接与后台任务的生命周期。
package support

import (
	"context"

	"github.com/bingcool/gofy/core"
	"github.com/gin-gonic/gin"
)

// Middleware is a gin middleware function.
// Middleware 是 gin 中间件函数；按注册顺序执行，调用 c.Next() 之前的代码先于
// 处理器运行。
type Middleware = gin.HandlerFunc

// Module is how a business package attaches itself to the framework.
// Module 是业务包接入框架的方式；实现应保持无状态，装配所需的依赖在
// ModuleFactory 阶段注入。
type Module interface {
	// Name is the module name. It must be unique and match
	// ^[a-z][a-z0-9-]{0,62}$.
	// Name 返回模块名；必须全局唯一并匹配 ^[a-z][a-z0-9-]{0,62}$，否则启动失败。
	Name() string
	// Register registers routes. It must not start goroutines or open
	// connections; use Start for that.
	// Register 只做路由注册，不得启动 goroutine 或打开连接；需要运行期资源的
	// 逻辑放到 Start，否则 Reload 时无法安全回收。
	Register(r *Router) error
}

// ModuleFactory builds a Module during startup, when the Runtime is ready.
// ModuleFactory 在启动阶段、Runtime 就绪后构造 Module；返回错误会中止启动。
type ModuleFactory func(rt *core.Runtime) (Module, error)

// Prefixer lets a module override its mount prefix.
// The default prefix is "/" + Module.Name().
// Prefixer 允许模块自定义挂载前缀，默认是 "/" + Module.Name()；返回值会经过
// NormalizePrefix，空值表示挂载在根前缀。
type Prefixer interface {
	Prefix() string
}

// Starter is called after the HTTP listener is ready.
// Starter 在 HTTP 监听就绪后调用；返回错误会中止启动，实现必须响应 ctx 取消。
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is called after the HTTP server stops accepting new requests.
// The ctx is a budget, not an enforced limit: the framework calls Stop
// synchronously and cannot take the work back, so a module that ignores ctx
// holds every later Stop, the database close and the pid file removal behind
// it. Return promptly, or honor the deadline inside Stop.
// Stopper 在 HTTP 服务停止接收新请求后调用；实现应在 ctx 截止前完成释放，
// 该阶段不应再依赖已经关闭的入站流量。ctx 是"预算"而不是强制限制：框架同步调用
// Stop，无法把已经交出去的工作收回来，因此忽略 ctx 的模块会把它之后的所有 Stop、
// 数据库关闭与 pid 文件删除一起拖住。请及时返回，或在 Stop 内部自己遵守截止时间。
type Stopper interface {
	Stop(ctx context.Context) error
}

// RequestBinderFunc pins generation-scoped state into a request context. It is
// called once per request, after the generation lease is acquired and before
// the handler runs.
// RequestBinderFunc 把本代状态固定进请求上下文；每个请求调用一次，时机是拿到
// generation lease 之后、进入 handler 之前。
type RequestBinderFunc func(ctx context.Context) context.Context

// RequestBinder is implemented by a module that owns state which must become
// visible at exactly the same moment as the handler of its generation — the
// demo's IO snapshot is the reference case.
//
// The framework composes the binders of every started module into the
// generation it publishes, so the ordering is: module Start (build the state) →
// generation publish (state and handler become visible together) → request
// entry (binder pins the state). A reload that fails before publishing leaves
// the serving generation's binder and state untouched, and an in-flight request
// keeps the state it was pinned to even after a successor is published.
//
// Implementations must read only immutable state and must not open connections:
// the binder runs on the request path. A nil return means "nothing to pin".
// RequestBinder 由持有「必须与所属代 handler 同时可见」的状态的模块实现，demo 的
// IO 快照就是典型例子。
//
// 框架把已启动模块的 binder 组合进它发布的那一代，顺序因此是：模块 Start
// （构建状态）→ 发布 generation（状态与 handler 一起可见）→ 请求进入（binder
// 固定状态）。发布前失败的 Reload 不会碰到正在服务那一代的 binder 与状态；在途
// 请求即使后继代已发布，也继续使用自己被固定的那一代。
//
// 实现只能读取不可变状态，且不得打开连接：binder 运行在请求路径上。返回 nil
// 表示没有需要固定的东西。
type RequestBinder interface {
	RequestBinder() RequestBinderFunc
}
