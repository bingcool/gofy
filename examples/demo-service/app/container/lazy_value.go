package container

import (
	"fmt"
	"sync"
)

// lazyValue 是一个可检测依赖环的惰性构造槽：首次 Get 执行 build，之后返回同一
// 实例。build 期间再次 Get 同一个槽说明装配图成环，直接 panic 并带上服务名，
// 避免 sync.Once 静默死锁。装配在启动期串行执行，不支持并发的首次取用。
type lazyValue[T any] struct {
	name string

	mu       sync.Mutex
	building bool
	once     sync.Once
	value    T
}

func newLazyValue[T any](name string) lazyValue[T] {
	return lazyValue[T]{name: name}
}

// Get 返回槽内实例，首次调用时执行 build。
func (l *lazyValue[T]) Get(build func() T) T {
	l.mu.Lock()
	building := l.building
	l.mu.Unlock()
	if building {
		panic(fmt.Sprintf("container: circular dependency detected while building %q", l.name))
	}

	l.once.Do(func() {
		l.mu.Lock()
		l.building = true
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			l.building = false
			l.mu.Unlock()
		}()
		l.value = build()
	})
	return l.value
}
