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

func TestMemoryStoreSweepHonoursEachKeysWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := ratelimit.NewMemoryStore()
	store.SetClock(func() time.Time { return now })
	ctx := context.Background()

	// One rule allows one request per hour.
	// 一条规则：每小时只允许一次。
	if res, err := store.Allow(ctx, "hourly", 1, time.Hour); err != nil || !res.Allowed {
		t.Fatalf("first hourly allow = %+v, %v", res, err)
	}

	// A second rule on the same store uses a one-second window, and 61 seconds
	// pass, which triggers the periodic sweep.
	// 同一个 Store 上的另一条规则用 1 秒窗口；时间推进 61 秒，触发周期性清理。
	now = now.Add(61 * time.Second)
	if res, err := store.Allow(ctx, "per-second", 5, time.Second); err != nil || !res.Allowed {
		t.Fatalf("per-second allow = %+v, %v", res, err)
	}

	// The hourly hit is 61 seconds old — still inside its own hour, so it must
	// still count. Sweeping with the second rule's window would have deleted it
	// and let this request through.
	// 小时窗口里的那次命中才过去 61 秒，仍在自己的一小时内，必须继续计数。若用秒窗口
	// 清理，它会被删掉，这个请求就会被放行。
	res, err := store.Allow(ctx, "hourly", 1, time.Hour)
	if err != nil {
		t.Fatalf("second hourly allow: %v", err)
	}
	if res.Allowed {
		t.Fatalf("hourly key was reset by a short-window sweep: %+v", res)
	}
	if store.Len() != 2 {
		t.Fatalf("keys = %d, want 2 (both rules still tracked)", store.Len())
	}
}

func TestMemoryStoreSweepDropsKeysPastTheirOwnWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := ratelimit.NewMemoryStore()
	store.SetClock(func() time.Time { return now })
	ctx := context.Background()

	if _, err := store.Allow(ctx, "short", 5, time.Second); err != nil {
		t.Fatalf("Allow short: %v", err)
	}
	if _, err := store.Allow(ctx, "long", 5, time.Hour); err != nil {
		t.Fatalf("Allow long: %v", err)
	}

	// Two minutes later the second-window key is dead weight, the hourly one is
	// not: the sweep has to tell them apart.
	// 两分钟后，秒窗口的 key 已经没用了，小时窗口的还有效：清理必须能区分两者。
	now = now.Add(2 * time.Minute)
	if _, err := store.Allow(ctx, "trigger", 5, time.Second); err != nil {
		t.Fatalf("Allow trigger: %v", err)
	}
	if store.Len() != 2 {
		t.Fatalf("keys = %d, want 2 (short key dropped, long key kept)", store.Len())
	}
}

func TestMemoryStoreRebindsKeyOnWindowChange(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := ratelimit.NewMemoryStore()
	store.SetClock(func() time.Time { return now })
	ctx := context.Background()

	// The same key is first counted in a wide window, then in a narrow one: a
	// config change moved the route to a shorter window. Hits outside the new
	// window stop counting immediately.
	// 同一个 key 先在宽窗口里计数，随后改到窄窗口（配置变更把路由换成了更短的窗口）。
	// 落在新窗口外的命中立刻失效。
	if res, err := store.Allow(ctx, "route", 2, time.Hour); err != nil || !res.Allowed {
		t.Fatalf("first allow = %+v, %v", res, err)
	}
	now = now.Add(10 * time.Second)
	if res, err := store.Allow(ctx, "route", 2, time.Hour); err != nil || !res.Allowed {
		t.Fatalf("second allow = %+v, %v", res, err)
	}
	now = now.Add(10 * time.Second)
	res, err := store.Allow(ctx, "route", 1, time.Second)
	if err != nil {
		t.Fatalf("narrow allow: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("narrow window must not inherit hour-old hits: %+v", res)
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
