// Package ratelimit provides a small sliding-window limiter that the HTTP
// middleware can use. The Store interface lets applications swap the in-memory
// implementation for a shared backend (for example Redis) without touching the
// middleware.
// 包 ratelimit 提供供 HTTP 中间件使用的滑动窗口限流器。Store 接口让应用把内存
// 实现换成共享后端（例如 Redis），而无需改动中间件。
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrInvalidLimit means limit is not positive.
	// ErrInvalidLimit 表示 limit 不是正数。
	ErrInvalidLimit = errors.New("ratelimit: limit must be positive")
	// ErrInvalidWindow means window is not positive.
	// ErrInvalidWindow 表示 window 不是正数。
	ErrInvalidWindow = errors.New("ratelimit: window must be positive")
)

// Result is the outcome of one Allow call.
// Result 是一次 Allow 调用的结果。
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	ResetAt    time.Time
}

// Store counts requests per key. Implementations must be safe for concurrent
// use. Key lifetime is bounded by window, and the window belongs to the key: one
// store may serve rules with different windows, so an implementation must never
// apply the window of one call to another key's counters.
// Store 按 key 统计请求数。实现必须可并发使用，key 的生命周期由 window 决定。窗口
// 属于 key 而不属于调用：同一个 Store 可以服务 window 不同的多条规则，实现不能把一次
// 调用的 window 套到别的 key 的计数上。
type Store interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// MemoryStore keeps a sliding-window log per key in process memory.
// A key is limited to `limit` timestamps, so memory per key stays small even
// under a flood; idle keys are removed by sweep.
//
// One store may serve several rules with different windows. Each key therefore
// remembers the window it belongs to, and the sweep judges a key against that
// window — never against the window of whichever rule happened to call first.
// Using the caller's window would delete counters another rule still needs, and
// an under-counted key silently admits requests past its limit.
// MemoryStore 在进程内存中为每个 key 维护一份滑动窗口日志。单个 key 最多保留
// limit 个时间戳，即使遭遇洪峰，每个 key 的内存占用也很小；闲置 key 由清理
// 逻辑回收。
//
// 一个 Store 可以服务多条不同 window 的规则。因此每个 key 记住自己所属的窗口，
// 清理时按该 key 自己的窗口判断，而不是用"恰好先调用的那条规则"的窗口——后者的
// 后果是删掉别的规则仍在窗口内的计数，少算的 key 会悄悄放行超出限额的请求。
type MemoryStore struct {
	mu       sync.Mutex
	keys     map[string]*bucket
	lastGC   time.Time
	clock    func() time.Time
	gcPeriod time.Duration
}

// bucket is one key's hits together with the window they were counted in.
// 一个 key 的命中记录，以及这些命中所属的窗口。
type bucket struct {
	hits   []time.Time
	window time.Duration
}

// NewMemoryStore returns an in-process store. Its clock can be replaced in
// tests through SetClock.
// NewMemoryStore 返回进程内 Store；测试可通过 SetClock 替换它的时钟。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		keys:     make(map[string]*bucket),
		clock:    time.Now,
		gcPeriod: time.Minute,
	}
}

// SetClock replaces the time source. It must be called before the store is
// used concurrently.
// SetClock 替换时间源；必须在 Store 被并发使用之前调用。
func (s *MemoryStore) SetClock(clock func() time.Time) {
	if clock == nil {
		clock = time.Now
	}
	s.mu.Lock()
	s.clock = clock
	s.mu.Unlock()
}

// Allow implements Store.
// Allow 实现 Store 接口。
func (s *MemoryStore) Allow(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 {
		return Result{}, ErrInvalidLimit
	}
	if window <= 0 {
		return Result{}, ErrInvalidWindow
	}
	now := s.clock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.keys == nil {
		s.keys = make(map[string]*bucket)
	}
	s.sweepLocked(now)

	b := s.keys[key]
	if b == nil {
		b = &bucket{window: window}
		s.keys[key] = b
	} else if b.window != window {
		// The key now belongs to a rule with a different window — a config change
		// moved it. The newest window is authoritative: hits that fall outside it
		// stop counting from here on.
		// 这个 key 现在归另一条 window 不同的规则（配置变更迁移了它）。以最新的窗口
		// 为准：落在它之外的命中从此刻起不再计数。
		b.window = window
	}

	cutoff := now.Add(-window)
	hits := b.hits
	idx := 0
	for idx < len(hits) && !hits[idx].After(cutoff) {
		idx++
	}
	if idx > 0 {
		hits = append(hits[:0], hits[idx:]...)
	}

	res := Result{Limit: limit}
	if len(hits) >= limit {
		// RetryAfter is when the oldest hit leaves the window.
		// RetryAfter 是最早一次命中滑出窗口的时间。
		reset := hits[0].Add(window)
		res.Remaining = 0
		res.RetryAfter = reset.Sub(now)
		if res.RetryAfter < 0 {
			res.RetryAfter = 0
		}
		res.ResetAt = reset
		b.hits = hits
		return res, nil
	}

	hits = append(hits, now)
	b.hits = hits
	res.Allowed = true
	res.Remaining = limit - len(hits)
	res.ResetAt = hits[0].Add(window)
	return res, nil
}

// sweepLocked drops keys that can no longer contribute: no hits at all, or a
// newest hit that has left that key's own window. Running it at most once per
// gcPeriod keeps the hot path cheap.
//
// The window comes from each bucket, not from the caller. A shared store serving
// rules with different windows would otherwise prune live counters of the longer
// rule every time a shorter one made a call.
// sweepLocked 丢弃不再可能贡献计数的 key：没有任何命中，或最新命中已经离开该 key
// 自己的窗口。每个 gcPeriod 最多执行一次，保证热路径开销足够低。
//
// 窗口取自每个 bucket 而不是调用方。否则共享同一个 Store、窗口长短不一的规则会在短
// 窗口规则每次调用时被剪掉仍然有效的计数。
func (s *MemoryStore) sweepLocked(now time.Time) {
	if !s.lastGC.IsZero() && now.Sub(s.lastGC) < s.gcPeriod {
		return
	}
	s.lastGC = now
	for key, b := range s.keys {
		if len(b.hits) == 0 || !b.hits[len(b.hits)-1].After(now.Add(-b.window)) {
			delete(s.keys, key)
		}
	}
}

// Len returns the number of tracked keys. It is intended for diagnostics and
// tests.
// Len 返回当前跟踪的 key 数量，用于诊断与测试。
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}
