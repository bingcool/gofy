package middleware

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bingcool/gofy/support"
)

// Registry holds the one-to-one mapping from middleware logical name to
// middleware instance for project contract routes: the contract
// only names a middleware (group.Use("audit") or Route.Use("cors")), App
// registers built-in and application-level instances during startup assembly,
// and the adapter resolves instances by name while binding routes.
//
// The registry is an explicit instance rather than package-level global state:
// App creates one with NewRegistry for each assembly (startup / Reload),
// registers the built-in middleware first, then runs the application's
// MiddlewareRegistrar, and finally hands it to modules through support.Router.
// Hot reload from Nacos and tests therefore each own one and cannot interfere.
// Neither the contract layer nor the adapter knows concrete middleware, they
// only depend on Lookup.
//
// An alias can never be overwritten: registering a duplicate returns an error,
// so two middleware cannot silently fight over the same name. All methods are
// safe for concurrent use.
// Registry 保存「中间件逻辑名 → 中间件实例」的一一映射，供项目契约（appinterface）
// 的契约路由使用：契约里只写名字（group.Use("audit") 或 Route.Use("cors")），
// App 在启动装配时把内建与应用层实例注册进来，adapter 绑定路由时按名字查出实例。
//
// 注册表是显式实例而非包级全局：App 为每次装配（启动 / Reload）用 NewRegistry
// 创建一份，先把内建中间件注册进去，再执行应用注册的 MiddlewareRegistrar；
// 随后随 support.Router 下发给模块。因此 Nacos 热更新和测试各自持有一份，
// 互不影响。契约层与 adapter 都不认识具体中间件，只依赖 Lookup。
//
// 别名一经注册不可覆盖，重复注册返回错误，避免两个中间件悄悄抢同一个名字。
// 所有方法都可并发调用。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]support.MiddlewareFunc
}

// NewRegistry creates an empty middleware registry.
// NewRegistry 创建一份空的中间件注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]support.MiddlewareFunc)}
}

// Register binds an instance to an alias. The alias must not be blank once
// trimmed and the instance must not be nil; registering an existing alias
// returns an error instead of overwriting it.
// Register 把实例绑定到别名上。别名去掉首尾空白后不能为空，实例不能为 nil，
// 同一个别名重复注册会返回错误而不是覆盖。
func (r *Registry) Register(name string, mw support.MiddlewareFunc) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("middleware alias must not be empty")
	}
	if mw == nil {
		return fmt.Errorf("middleware %q must not be nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("middleware alias %q is already registered", name)
	}
	r.entries[name] = mw
	return nil
}

// MustRegister is the panicking form of Register, meant for startup assembly: a
// misspelt alias fails immediately instead of being noticed only when a request
// arrives and the middleware is missing from the chain.
// MustRegister 是 Register 的 panic 版本，用于启动期装配：别名写错时立刻失败，
// 而不是等到请求进来才发现某个中间件没有生效。
func (r *Registry) MustRegister(name string, mw support.MiddlewareFunc) {
	if err := r.Register(name, mw); err != nil {
		panic(err)
	}
}

// Lookup resolves a middleware by alias. An unknown name, a nil instance or a nil
// registry all return (nil, false); the caller decides whether that is an error
// or a skip.
// Lookup 按别名查中间件。名字不存在、实例为 nil，或注册表本身为 nil 时返回
// (nil, false)，由调用方决定是报错还是跳过。
func (r *Registry) Lookup(name string) (support.MiddlewareFunc, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	mw, ok := r.entries[strings.TrimSpace(name)]
	if !ok || mw == nil {
		return nil, false
	}
	return mw, true
}

// Names returns the registered aliases in lexicographic order, for startup logs
// and for diagnosing "the name was never registered". The returned slice is a
// copy, so callers cannot mutate the registry through it.
// Names 返回已注册的别名，按字典序排序，用于启动日志和排查「名字没注册」的问题。
// 返回的是副本，调用方修改不会影响注册表。
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
