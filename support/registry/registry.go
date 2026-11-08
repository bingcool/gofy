// Package registry 提供显式的「名字 → 实例」注册表，供应用在启动装配时登记
// 跨模块共享的实例（repository、client、service 等），再按名字取用。
//
// 注册表是普通对象而不是包级全局：每次装配（启动 / 热更新）新建一份，随依赖
// 显式传递。名字由使用方以常量定义，注册表本身不解释名字格式；空名字、nil 实例
// 和重复注册都会直接报错，避免两个实现悄悄抢同一个名字。
//
// 所有方法都可并发调用。Lookup 返回 any，业务层应使用 Get[T] 做类型化取用，
// 不要在调用处手写类型断言。
package registry

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Registry 保存 name → instance 的一一映射。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]any
}

// NewRegistry 创建一份空注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]any)}
}

// Register 把实例绑定到名字上。名字去掉首尾空白后不能为空，实例不能为 nil
// （包括 nil 指针、nil map/slice 等），同一个名字重复注册返回错误而不覆盖。
func (r *Registry) Register(name string, instance any) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("registry: name must not be empty")
	}
	if isNil(instance) {
		return fmt.Errorf("registry: %q instance must not be nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("registry: %q is already registered", name)
	}
	r.entries[name] = instance
	return nil
}

// MustRegister 是 Register 的 panic 版本，用于启动期装配：名字写错或重复时
// 立刻失败，而不是等到请求期才发现依赖缺失。
func (r *Registry) MustRegister(name string, instance any) {
	if err := r.Register(name, instance); err != nil {
		panic(err)
	}
}

// Lookup 按名字取实例。名字不存在、实例为 nil，或注册表本身为 nil 时返回
// (nil, false)；调用方应优先使用 Get[T] 获取带类型检查的返回值。
func (r *Registry) Lookup(name string) (any, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	instance, ok := r.entries[strings.TrimSpace(name)]
	if !ok || isNil(instance) {
		return nil, false
	}
	return instance, true
}

// Names 返回已注册的名字，按字典序排序，供启动日志和排查「名字没注册」的问题。
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

// Get 按名字取实例并断言为 T。未注册、实例为 nil 或类型不符时返回错误，
// 错误信息包含名字、期望类型和实际类型。
func Get[T any](r *Registry, name string) (T, error) {
	var zero T
	instance, ok := r.Lookup(name)
	if !ok {
		return zero, fmt.Errorf("registry: %q is not registered", strings.TrimSpace(name))
	}
	typed, ok := instance.(T)
	if !ok {
		return zero, fmt.Errorf("registry: %q: want %T, got %T", strings.TrimSpace(name), zero, instance)
	}
	return typed, nil
}

// isNil 把「any 里包着 nil 指针 / nil interface / nil map 等」也视为 nil，
// 避免 Register 成功但取用方拿到一个必然 panic 的零值。
func isNil(instance any) bool {
	if instance == nil {
		return true
	}
	value := reflect.ValueOf(instance)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
