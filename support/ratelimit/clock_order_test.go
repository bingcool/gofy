package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestClockReadIsInsideLock pins the invariant that fixes timestamp scrambling:
// the clock is read while the store lock is held. With the read outside the
// lock, A could read t1, B read t2 and append first, leaving hits as [t2, t1] —
// which breaks head-only pruning, sweep's newest-hit check and ResetAt math.
// TryLock from inside the callback succeeds only if the caller did not hold the
// lock, so this reconstructs the exact precondition for the race.
// TestClockReadIsInsideLock 钉住修复时间戳乱序的不变量：取时钟时持有 store 锁。
// 若在锁外取，A 读到 t1、B 读到 t2 却先追加，hits 变成 [t2, t1]，只从头部清理、
// sweep 看最新命中、ResetAt 计算都会出错。回调里 TryLock 成功只可能是调用方没持锁，
// 因此这个测试重建了竞态的前提条件。
func TestClockReadIsInsideLock(t *testing.T) {
	store := NewMemoryStore()
	var lockWasFree bool
	store.SetClock(func() time.Time {
		// TryLock succeeds only when the mutex was not held by the caller —
		// i.e. the clock is being read outside the critical section.
		// TryLock 成功只可能是调用方没持锁，也就是时钟在临界区外被读取。
		lockWasFree = store.mu.TryLock()
		if lockWasFree {
			store.mu.Unlock()
		}
		return time.Unix(1_700_000_000, 0)
	})

	if _, err := store.Allow(context.Background(), "k", 1, time.Minute); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if lockWasFree {
		t.Fatal("clock was read without holding the store lock; concurrent callers can append timestamps out of order")
	}
}

// TestConcurrentHitsStayOrdered is the behavioural counterpart: with a clock
// that hands out strictly increasing times, concurrent callers must end up with
// an ordered hit log regardless of goroutine scheduling.
// TestConcurrentHitsStayOrdered 是行为层面的对照：时钟严格递增时，无论 goroutine
// 如何调度，并发调用后的命中日志都必须有序。
func TestConcurrentHitsStayOrdered(t *testing.T) {
	store := NewMemoryStore()
	base := time.Unix(1_700_000_000, 0)
	var seq atomic.Int64
	store.SetClock(func() time.Time {
		return base.Add(time.Duration(seq.Add(1)) * time.Second)
	})

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = store.Allow(context.Background(), "k", goroutines, time.Hour)
		}()
	}
	wg.Wait()

	store.mu.Lock()
	hits := append([]time.Time(nil), store.keys["k"].hits...)
	store.mu.Unlock()

	if len(hits) != goroutines {
		t.Fatalf("hits = %d, want %d", len(hits), goroutines)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].After(hits[i]) {
			t.Fatalf("hits out of order at %d: %v", i, hits)
		}
	}
}
