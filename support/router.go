package support

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// Router is a thin wrapper around a gin.RouterGroup. It keeps the router
// prefix, the per-application responder and the shared validator.
// A Router is only used while modules register routes; it is not safe for
// concurrent use.
// Router 是 gin.RouterGroup 的薄封装，保留路由前缀、所属应用的 responder 与共享
// validator。它只在模块注册路由期间使用，不可并发访问；请求期请改用 gin.Context。
type Router struct {
	group  *gin.RouterGroup
	engine *engine
	prefix string
	// bindPrefix is the group prefix relative to the module root Router; the
	// adapter trims contract paths with it. Routers created for modules start
	// here, and each business Group call accumulates another level.
	// bindPrefix 是相对「模块根 Router」的分组前缀，适配器据此裁剪契约路径；
	// 框架为模块创建的 Router 从这里开始，业务再 Group 时逐层累加。
	bindPrefix string
}

// MiddlewareResolver resolves a middleware instance by its logical name in the
// contract. The App installs one registry for the whole process at startup
// (support/middleware.Registry implements this interface); modules only bind
// routes and no longer create or inject the registry themselves.
// MiddlewareResolver 按契约里的逻辑名查找中间件实例。App 启动时为整个进程
// 安装一份注册表（support/middleware.Registry 实现该接口），模块只负责绑定
// 路由，不再自己创建或注入注册表。
type MiddlewareResolver interface {
	Lookup(name string) (MiddlewareFunc, bool)
}

type engine struct {
	responder  Responder
	validator  *validator.Validate
	middleware MiddlewareResolver
}

// RouterConfig configures the root router of an application.
// RouterConfig 配置应用根路由；Responder 为 nil 时使用默认 responder，
// Validator 为 nil 时使用 NewValidator()。
type RouterConfig struct {
	Prefix     string
	Responder  Responder
	Validator  *validator.Validate
	Middleware MiddlewareResolver
}

// NewRouter creates the root router on top of e. It is called by the gofy
// root package while building the HTTP layer.
// NewRouter 在 e 之上创建根路由，由 gofy 根包构建 HTTP 层时调用；Prefix 会先经过
// NormalizePrefix，之后再挂载各模块的路由。
func NewRouter(e *gin.Engine, cfg RouterConfig) *Router {
	if cfg.Responder == nil {
		cfg.Responder = NewResponder(ResponderConfig{})
	}
	if cfg.Validator == nil {
		cfg.Validator = NewValidator()
	}
	eng := &engine{responder: cfg.Responder, validator: cfg.Validator, middleware: cfg.Middleware}
	prefix := NormalizePrefix(cfg.Prefix)
	return &Router{
		group:  e.Group(prefix),
		engine: eng,
		prefix: prefix,
	}
}

// Group creates a sub-router. The prefix is normalized to start with "/" and
// to have no trailing "/". An empty prefix keeps the parent path.
// Group 创建子路由。前缀会被规范化为以 "/" 开头、无结尾 "/"，空前缀保持父路径；
// 子路由共享同一 responder 与 validator，中间件按层级累积而非覆盖。
func (r *Router) Group(prefix string, mw ...MiddlewareFunc) *Router {
	return r.subRouter(prefix, true, mw...)
}

// ModuleGroup creates the Router handed to a module during startup. Unlike
// Group it does not accumulate the binding prefix, so adapters can bind full
// contract paths without stripping the module name. It is used by the gofy
// root package; business code should call Group.
// ModuleGroup 创建启动期交给模块的 Router。与 Group 不同，它不累计绑定前缀，
// 适配器可直接绑定完整契约路径而不会被裁掉模块名。它由 gofy 根包使用，
// 业务代码应调用 Group。
func (r *Router) ModuleGroup(prefix string, mw ...MiddlewareFunc) *Router {
	return r.subRouter(prefix, false, mw...)
}

// subRouter is the internal implementation of Group; when bind is false (the
// framework creating a module Router) it does not accumulate the binding
// prefix, so a module binding full contract paths directly is not trimmed.
// subRouter 是 Group 的内部实现；bind 为 false 时（框架创建模块 Router）不累计
// 绑定前缀，保证模块内直接绑定完整契约路径不会被误裁剪。
func (r *Router) subRouter(prefix string, bind bool, mw ...MiddlewareFunc) *Router {
	prefix = NormalizePrefix(prefix)
	group := r.group.Group(prefix)
	if len(mw) > 0 {
		group.Use(mw...)
	}
	bindPrefix := r.bindPrefix
	if bind {
		bindPrefix = joinPrefix(r.bindPrefix, prefix)
	}
	return &Router{
		group:      group,
		engine:     r.engine,
		prefix:     joinPrefix(r.prefix, prefix),
		bindPrefix: bindPrefix,
	}
}

// GroupPrefix returns the group prefix of this Router since the module root,
// for example "/orders"; the module root Router and an ordinary root Router
// both return an empty string.
// GroupPrefix 返回当前 Router 自模块根以来的分组前缀（如 "/orders"）；
// 模块根 Router 与普通根 Router 返回空字符串。
func (r *Router) GroupPrefix() string {
	if r == nil {
		return ""
	}
	return r.bindPrefix
}

// Use appends middleware to this router.
// Use 为本路由追加中间件；只影响之后注册的路由，已注册的路由不受影响。
func (r *Router) Use(mw ...MiddlewareFunc) {
	r.group.Use(mw...)
}

// GET registers a GET route.
// GET 注册 GET 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) GET(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("GET", path, h, mw...)
}

// POST registers a POST route.
// POST 注册 POST 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) POST(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("POST", path, h, mw...)
}

// PUT registers a PUT route.
// PUT 注册 PUT 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) PUT(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("PUT", path, h, mw...)
}

// PATCH registers a PATCH route.
// PATCH 注册 PATCH 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) PATCH(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("PATCH", path, h, mw...)
}

// DELETE registers a DELETE route.
// DELETE 注册 DELETE 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) DELETE(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("DELETE", path, h, mw...)
}

// HEAD registers a HEAD route.
// HEAD 注册 HEAD 路由；路径相对当前分组前缀，mw 先于处理器执行。
func (r *Router) HEAD(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("HEAD", path, h, mw...)
}

// OPTIONS registers an OPTIONS route.
// OPTIONS 注册 OPTIONS 路由；CORS 预检若已由中间件处理，无需再单独注册。
func (r *Router) OPTIONS(path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	r.Handle("OPTIONS", path, h, mw...)
}

// Handle registers a route for an arbitrary HTTP method.
// Handle 为任意 HTTP 方法注册路由；mw 按传入顺序先于处理器执行。
func (r *Router) Handle(method, path string, h gin.HandlerFunc, mw ...MiddlewareFunc) {
	handlers := make([]gin.HandlerFunc, 0, len(mw)+1)
	handlers = append(handlers, mw...)
	handlers = append(handlers, h)
	r.group.Handle(method, path, handlers...)
}

// Static serves files from root under relativePath.
// Static 从 root 目录提供 relativePath 下的静态文件；root 是服务端本地路径，
// 不要传入用户可控的值。
func (r *Router) Static(relativePath, root string) {
	r.group.Static(relativePath, root)
}

// Prefix returns the full prefix of this router.
// Prefix 返回本路由的完整前缀，已包含父级前缀。
func (r *Router) Prefix() string { return r.prefix }

// Gin exposes the underlying router group. Its API is not covered by the
// gofy compatibility promise.
// Gin 暴露底层 router group；其 API 不在 gofy 兼容性承诺范围内，升级 gin 时
// 可能变化，业务代码应优先使用本类型的方法。
func (r *Router) Gin() *gin.RouterGroup { return r.group }

// Responder returns the responder configured for this application so adapters
// can render results with the same envelope.
// Responder 返回本应用配置的 responder，供适配器用同一 envelope 渲染结果；
// 返回值为接口，调用方不应假设具体实现类型。
func (r *Router) Responder() Responder { return r.engine.responder }

// Middleware looks up an application-level middleware by its logical name in
// the contract. The registry is installed by the App at startup and shared by
// grouped Routers; without a registry or for an unknown name it returns
// (nil, false).
// Middleware 按契约里的逻辑名查找应用级中间件。注册表由 App 在启动时统一
// 安装，分组 Router 共享同一份；未安装注册表或名字不存在时返回 (nil, false)。
func (r *Router) Middleware(name string) (MiddlewareFunc, bool) {
	if r == nil || r.engine == nil || r.engine.middleware == nil {
		return nil, false
	}
	return r.engine.middleware.Lookup(name)
}

// Validator returns the validator shared by this application.
// Validator 返回本应用共享的 validator；自定义校验规则应在启动期注册，
// 运行期并发注册并不安全。
func (r *Router) Validator() *validator.Validate { return r.engine.validator }

// BindAndValidate applies defaults, binds path/query/body values and validates
// req. It is exported for typed adapters that invoke handlers outside the
// generic helpers.
// BindAndValidate 先写入默认值，再绑定 path/query/body 并校验 req；供在通用
// helper 之外调用处理器的有类型适配器使用，错误映射与通用入口完全一致。
func (r *Router) BindAndValidate(c *gin.Context, req any) error {
	return r.engine.bindAndValidate(c, req)
}

// NormalizePrefix makes prefix start with "/" and removes a trailing "/".
// An empty prefix stays empty.
// NormalizePrefix 让前缀以 "/" 开头并去掉结尾 "/"，空前缀仍为空；传入 "/" 同样
// 规范化为空，表示挂在根路径。
func NormalizePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func joinPrefix(base, sub string) string {
	if sub == "" {
		return base
	}
	if base == "" {
		return sub
	}
	return base + sub
}
