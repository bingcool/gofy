// Package generation holds one atomically swapped "current generation" of
// ready-to-use values and lets an entry point pin a generation to a context.
//
// Shared code (services, repositories, background workers) must not read
// package-level handles: during a hot reload the previous generation is being
// closed while new work keeps arriving. The convention is that every entry
// point — HTTP middleware, script wrapper, daemon module Start — publishes one
// generation and pins it into the context; consumers only call WithContext(ctx)
// and keep using that generation even if a reload publishes a successor in the
// middle of a request.
//
// 包 generation 持有可原子替换的「当前一代」就绪值，并允许入口把某一代固定到
// context 上。
//
// 共享代码（service、repository、后台任务）不要读取包级句柄：热更新时上一代
// 正在关闭，而新请求仍在进入。约定是每个入口——HTTP 中间件、脚本包装器、
// 守护进程模块的 Start——发布一代并固定进 context；消费方只调用
// WithContext(ctx)，即使进程级当前代在请求中途被替换，也继续使用固定的一代。
package generation

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// Set keeps the currently published generation of T. Publish and Current are
// lock-free; the zero value is not usable, build one with NewSet.
//
// A Set must not be copied after first use: it wraps an atomic pointer.
//
// Set 保存 T 的当前一代。Publish 与 Current 无锁；零值不可用，请用 NewSet
// 构造。Set 首次使用后不可复制：内部封装了原子指针。
type Set[T any] struct {
	name  string
	value atomic.Pointer[T]
}

// NewSet returns a set with no published generation. name identifies the set in
// panic messages and is trimmed; an empty name falls back to "generation".
//
// NewSet 创建一份尚未发布任何一代的集合。name 用于在 panic 信息里标识集合，
// 会去掉首尾空白；传空时回退为 "generation"。
func NewSet[T any](name string) *Set[T] {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "generation"
	}
	return &Set[T]{name: name}
}

// Name returns the name reported in panic messages.
//
// Name 返回 panic 信息中使用的集合名。
func (s *Set[T]) Name() string { return s.name }

// Publish installs v as the current generation; a nil v withdraws the
// publication, which is how a stopped generation or a finished script hands
// the process back to "nothing published".
//
// Publish 把 v 安装为当前一代；v 为 nil 表示撤回发布，用于停止的一代或
// 已结束的脚本把进程交还成「尚未发布」状态。
func (s *Set[T]) Publish(v *T) { s.value.Store(v) }

// Current returns the published generation, or nil when nothing is published.
//
// Current 返回已发布的一代；尚未发布时为 nil。
func (s *Set[T]) Current() *T { return s.value.Load() }

// CompareAndSwap replaces the current generation with next only when the
// current one is still old, and reports whether the swap happened. A module
// stops with it so an old generation cannot withdraw the successor a reload
// has already published.
//
// CompareAndSwap 只在当前一代仍是 old 时把它替换为 next，并返回是否替换成功。
// 模块停止时用它撤回自己的一代，旧代因此不会抹掉 Reload 已经发布的新一代。
func (s *Set[T]) CompareAndSwap(old, next *T) bool {
	return s.value.CompareAndSwap(old, next)
}

// WithSnapshot pins v to ctx so downstream code keeps using this generation
// regardless of later Publish calls. A nil ctx falls back to
// context.Background.
//
// WithSnapshot 把 v 固定到 ctx，下游代码因此只使用这一代，不受之后的 Publish
// 影响。ctx 为 nil 时回退到 context.Background。
func (s *Set[T]) WithSnapshot(ctx context.Context, v *T) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, s, v)
}

// WithContext returns the generation pinned in ctx, falling back to Current().
// Pinned values always win: a request that entered while generation N was
// current keeps using N even after a reload publishes N+1.
//
// If neither exists, the entry point failed to publish a generation, so this
// panics instead of handing a nil handle to shared code that would dereference
// it far from the cause. The message carries the set name.
//
// WithContext 返回 ctx 中固定的一代，没有固定值时回退到 Current()。固定值优先：
// 在 N 代进入的请求即使 Reload 发布了 N+1，也继续使用 N。
//
// 两者都没有，说明入口没有发布本代，因此直接 panic，而不是把 nil 句柄交给
// 下游解引用；panic 信息带上集合名。
func (s *Set[T]) WithContext(ctx context.Context) *T {
	if ctx != nil {
		if v, ok := ctx.Value(s).(*T); ok && v != nil {
			return v
		}
	}
	if v := s.Current(); v != nil {
		return v
	}
	panic(fmt.Sprintf("%s: no generation published; publish one at the entry (request middleware, script wrapper, or module Start) before using shared code", s.name))
}

// Pinned returns the generation this context already carries, without falling
// back to Current(). Entries use it to avoid replacing a pin that a framework
// binder has already installed: overwriting it would hand the request a
// successor generation while the handler still belongs to the previous one.
//
// Pinned 返回 ctx 已经固定的一代，不回退到 Current()。入口用它避免覆盖框架
// binder 已装好的固定值：覆盖会让请求拿到后继代，而它的 handler 仍属于上一代。
func (s *Set[T]) Pinned(ctx context.Context) (*T, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Value(s).(*T)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// Middleware pins the current generation at request entry, unless the context
// already carries one. A framework request binder pins the generation that owns
// the handler; overwriting that pin here would pair handler N with generation
// N+1. When nothing is published the middleware passes the request through
// untouched, so WithContext can report the missing generation at first use.
//
// Middleware 在请求入口固定当前一代；ctx 已经固定过则不覆盖。框架的请求 binder
// 固定的是 handler 所属的那一代，这里覆盖会让 handler N 配上 N+1。尚未发布时
// 中间件原样放行，由 WithContext 在首次取用时报告缺失。
func (s *Set[T]) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := s.Pinned(c.Request.Context()); ok {
			c.Next()
			return
		}
		if v := s.Current(); v != nil {
			c.Request = c.Request.WithContext(s.WithSnapshot(c.Request.Context(), v))
		}
		c.Next()
	}
}
