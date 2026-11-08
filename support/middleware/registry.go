package middleware

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bingcool/gofy/support"
)

// Registry 保存「中间件逻辑名 → 中间件实例」的一一映射，供 gofy-interface-service
// 的契约路由使用：契约里只写名字（group.Use("audit") 或 Route.Use("cors")），
// 服务在装配时把实例注册进来，adapter 绑定路由时按名字查出实例。
//
// 注册表是显式实例而非包级全局：装配方用 NewRegistry 创建并注入
// BindOptions.Registry，因此 Nacos 热更新和测试各自持有一份，互不影响。
// 契约层与 adapter 都不认识具体中间件，只依赖 Lookup。
//
// 别名一经注册不可覆盖，重复注册返回错误，避免两个中间件悄悄抢同一个名字。
// 所有方法都可并发调用。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]support.Middleware
}

// NewRegistry 创建一份空的中间件注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]support.Middleware)}
}

// Register 把实例绑定到别名上。别名去掉首尾空白后不能为空，实例不能为 nil，
// 同一个别名重复注册会返回错误而不是覆盖。
func (r *Registry) Register(name string, mw support.Middleware) error {
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

// MustRegister 是 Register 的 panic 版本，用于启动期装配：别名写错时立刻失败，
// 而不是等到请求进来才发现某个中间件没有生效。
func (r *Registry) MustRegister(name string, mw support.Middleware) {
	if err := r.Register(name, mw); err != nil {
		panic(err)
	}
}

// Lookup 按别名查中间件。名字不存在、实例为 nil，或注册表本身为 nil 时返回
// (nil, false)，由调用方决定是报错还是跳过。
func (r *Registry) Lookup(name string) (support.Middleware, bool) {
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
