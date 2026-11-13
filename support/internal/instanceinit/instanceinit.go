// Package instanceinit coordinates lazy, per-name initialization for the named
// instance registries in support/db, support/pg, support/sqlite, support/redis
// and the message-queue packages.
//
// The registry owns configuration and lifecycle; this helper owns the map of
// already-opened values plus the in-flight attempts. Different names open in
// parallel, concurrent callers of one name share a single attempt, and a failed
// attempt is never cached, so the next call retries.
// 包 instanceinit 为 support/db、support/pg、support/sqlite、support/redis 以及
// 各消息队列包的命名实例注册表协调按名字的惰性初始化。
//
// 注册表持有配置与生命周期，本辅助类型持有已打开的值与进行中的尝试：不同名字
// 并行打开，同一名字的并发调用共享同一次尝试，失败的尝试不会被缓存，因此下一次
// 调用会重新尝试。
package instanceinit

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Get once Close has started. Registries translate it
// to their own ErrNotConfigured.
// ErrClosed 在 Close 开始后由 Get 返回，注册表会把它转换成自己的
// ErrNotConfigured。
var ErrClosed = errors.New("instanceinit: closed")

// errPanicked is stored for waiters when an open call panics, so they do not
// receive a zero value. The panic itself still reaches the original caller.
// errPanicked 在 open 发生 panic 时交给等待方，避免它们拿到零值；panic 本身仍
// 继续抛给原始调用方。
var errPanicked = errors.New("instanceinit: open panicked")

// Slots holds the values opened by name and the attempts in flight.
// Slots 保存按名字打开的值与进行中的尝试。
type Slots[T any] struct {
	mu      sync.Mutex
	values  map[string]T
	opening map[string]*attempt[T]
	closeFn func(name string, value T) error
	closed  bool
}

// attempt is one in-flight open. done is closed after value and err are set;
// waiters read them without the mutex because the channel close provides the
// happens-before edge.
// attempt 是一次进行中的打开操作。value 与 err 写入后才关闭 done；等待方不加锁
// 读取它们，因为 channel 关闭提供了 happens-before 语义。
type attempt[T any] struct {
	done  chan struct{}
	value T
	err   error
}

// New builds empty slots. closeFn releases one value and is called both by
// Close and when an open completes after Close started.
// New 构建空的 Slots。closeFn 负责释放单个值，Close 以及 Close 开始之后才完成的
// open 都会调用它。
func New[T any](closeFn func(name string, value T) error) *Slots[T] {
	if closeFn == nil {
		closeFn = func(string, T) error { return nil }
	}
	return &Slots[T]{
		values:  make(map[string]T),
		opening: make(map[string]*attempt[T]),
		closeFn: closeFn,
	}
}

// Get returns the value registered under name, opening it through open on first
// use. Concurrent calls for the same name wait for the first attempt; different
// names run independently. A failed attempt is dropped, so a later call retries.
// Get 返回 name 对应的值，首次使用时通过 open 打开。同一名字的并发调用会等待
// 第一次尝试；不同名字互不阻塞。失败的尝试会被丢弃，后续调用会重新尝试。
func (s *Slots[T]) Get(ctx context.Context, name string, open func(context.Context) (T, error)) (T, error) {
	var zero T
	if s == nil {
		return zero, ErrClosed
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return zero, ErrClosed
	}
	if value, ok := s.values[name]; ok {
		s.mu.Unlock()
		return value, nil
	}
	if call, ok := s.opening[name]; ok {
		s.mu.Unlock()
		<-call.done
		return call.value, call.err
	}
	call := &attempt[T]{done: make(chan struct{})}
	if s.opening == nil {
		s.opening = make(map[string]*attempt[T])
	}
	s.opening[name] = call
	s.mu.Unlock()

	return s.open(ctx, name, call, open)
}

// open runs one attempt outside the lock and publishes or discards its result.
// open 在锁外执行一次尝试，并发布或丢弃它的结果。
func (s *Slots[T]) open(ctx context.Context, name string, call *attempt[T], open func(context.Context) (T, error)) (value T, err error) {
	completed := false
	defer func() {
		s.mu.Lock()
		delete(s.opening, name)
		closed := s.closed
		switch {
		case !completed:
			call.err = errPanicked
		case err != nil:
			call.err = err
		case closed:
			call.err = ErrClosed
		default:
			s.values[name] = value
			call.value = value
		}
		s.mu.Unlock()

		// Close already snapshotted the values it owns, so an open that finishes
		// after Close must release its value; otherwise the pool leaks. The
		// opener gets the same ErrClosed its waiters see instead of a value from
		// a generation that is shutting down.
		// Close 已经为它负责的值做了快照，因此在 Close 之后才完成的 open 必须自行
		// 释放值，否则连接池会泄漏。发起方拿到的错误与等待方一致，都是 ErrClosed，
		// 而不是一个正在关闭的代际里的值。
		if completed && err == nil && closed {
			call.err = errors.Join(ErrClosed, s.closeFn(name, value))
			var zero T
			value = zero
			err = call.err
		}
		close(call.done)
	}()

	value, err = open(ctx)
	completed = true
	return value, err
}

// Publish runs fn while the slots are still open, so Install cannot make a
// registry current after its Close has started.
// Publish 在 Slots 仍处于打开状态时执行 fn，避免 Install 在注册表已开始 Close
// 之后还把它设为当前实例。
func (s *Slots[T]) Publish(fn func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		fn()
	}
}

// Close stops new opens and releases every value. An open that is already
// running releases its own value when it finishes.
// Close 停止新的打开操作并释放所有值。已经在执行中的 open 会在结束时自行释放
// 它的值。
func (s *Slots[T]) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	values := s.values
	s.values = nil
	s.mu.Unlock()

	var errs []error
	for name, value := range values {
		if err := s.closeFn(name, value); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
