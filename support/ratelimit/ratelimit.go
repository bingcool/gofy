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
// use. Key lifetime is bounded by window.
// Store 按 key 统计请求数。实现必须可并发使用，key 的生命周期由 window 决定。
type Store interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// MemoryStore keeps a sliding-window log per key in process memory.
// A key is limited to `limit` timestamps, so memory per key stays small even
// under a flood; idle keys are removed by sweep.
// MemoryStore 在进程内存中为每个 key 维护一份滑动窗口日志。单个 key 最多保留
// limit 个时间戳，即使遭遇洪峰，每个 key 的内存占用也很小；闲置 key 由清理
// 逻辑回收。
type MemoryStore struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	lastGC   time.Time
	clock    func() time.Time
	gcPeriod time.Duration
}

// NewMemoryStore returns an in-process store. Its clock can be replaced in
// tests through SetClock.
// NewMemoryStore 返回进程内 Store；测试可通过 SetClock 替换它的时钟。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		hits:     make(map[string][]time.Time),
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
	cutoff := now.Add(-window)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.hits == nil {
		s.hits = make(map[string][]time.Time)
	}
	s.sweepLocked(now, window)

	hits := s.hits[key]
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
		s.hits[key] = hits
		return res, nil
	}

	hits = append(hits, now)
	s.hits[key] = hits
	res.Allowed = true
	res.Remaining = limit - len(hits)
	res.ResetAt = hits[0].Add(window)
	return res, nil
}

// sweepLocked drops keys whose newest hit is outside the window. Running it at
// most once per gcPeriod keeps the hot path cheap.
// sweepLocked 丢弃最新命中已落在窗口外的 key。每个 gcPeriod 最多执行一次，
// 保证热路径开销足够低。
func (s *MemoryStore) sweepLocked(now time.Time, window time.Duration) {
	period := s.gcPeriod
	if period < window {
		period = window
	}
	if !s.lastGC.IsZero() && now.Sub(s.lastGC) < period {
		return
	}
	s.lastGC = now
	cutoff := now.Add(-window)
	for key, hits := range s.hits {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(s.hits, key)
		}
	}
}

// Len returns the number of tracked keys. It is intended for diagnostics and
// tests.
// Len 返回当前跟踪的 key 数量，用于诊断与测试。
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hits)
}
