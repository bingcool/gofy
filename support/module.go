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
// Stopper 在 HTTP 服务停止接收新请求后调用；实现应在 ctx 截止前完成释放，
// 该阶段不应再依赖已经关闭的入站流量。
type Stopper interface {
	Stop(ctx context.Context) error
}
