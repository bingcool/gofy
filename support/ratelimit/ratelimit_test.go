package ratelimit_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/ratelimit"
)

func TestMemoryStoreSlidingWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := ratelimit.NewMemoryStore()
	store.SetClock(func() time.Time { return now })
	ctx := context.Background()

	first, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !first.Allowed || first.Remaining != 1 || first.Limit != 2 {
		t.Fatalf("first = %+v", first)
	}

	now = now.Add(10 * time.Second)
	second, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !second.Allowed || second.Remaining != 0 {
		t.Fatalf("second = %+v", second)
	}

	now = now.Add(10 * time.Second)
	third, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if third.Allowed {
		t.Fatalf("third = %+v, want rejection", third)
	}
	// The oldest hit was at t0, so the window opens 50s from now.
	// 最早一次命中发生在 t0，因此窗口从现在起还要 50 秒才重新打开。
	if third.RetryAfter != 40*time.Second {
		t.Fatalf("retry after = %v, want 40s", third.RetryAfter)
	}
	if !third.ResetAt.Equal(now.Add(40 * time.Second)) {
		t.Fatalf("reset at = %v", third.ResetAt)
	}

	// Once the first hit expires only one hit remains, so one request fits.
	// 第一次命中滑出窗口后只剩一次命中，因此还能再放行一个请求。
	now = now.Add(40 * time.Second)
	fourth, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !fourth.Allowed || fourth.Remaining != 0 {
		t.Fatalf("fourth = %+v, want allowed with no remaining", fourth)
	}
}

func TestMemoryStoreSeparatesKeys(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	ctx := context.Background()
	if res, _ := store.Allow(ctx, "a", 1, time.Minute); !res.Allowed {
		t.Fatal("first key was rejected")
	}
	if res, _ := store.Allow(ctx, "a", 1, time.Minute); res.Allowed {
		t.Fatal("first key was allowed twice")
	}
	if res, _ := store.Allow(ctx, "b", 1, time.Minute); !res.Allowed {
		t.Fatal("second key shared the first bucket")
	}
}

func TestMemoryStoreInvalidArguments(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.Allow(ctx, "k", 0, time.Minute); !errors.Is(err, ratelimit.ErrInvalidLimit) {
		t.Fatalf("limit error = %v", err)
	}
	if _, err := store.Allow(ctx, "k", 1, 0); !errors.Is(err, ratelimit.ErrInvalidWindow) {
		t.Fatalf("window error = %v", err)
	}
}

func TestMemoryStorePrunesIdleKeys(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := ratelimit.NewMemoryStore()
	store.SetClock(func() time.Time { return now })

	if _, err := store.Allow(context.Background(), "idle", 5, time.Second); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("keys = %d, want 1", store.Len())
	}

	// Past the sweep interval (one minute) the idle key is dropped.
	// 超过清理周期（一分钟）后，闲置的 key 会被回收。
	now = now.Add(61 * time.Second)
	if _, err := store.Allow(context.Background(), "active", 5, time.Second); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("keys = %d, want 1 after sweep", store.Len())
	}
}

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	ctx := context.Background()
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := store.Allow(ctx, "shared", 10, time.Minute)
			if err != nil {
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed = %d, want 10", got)
	}
}
