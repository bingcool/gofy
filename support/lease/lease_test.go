package lease_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/lease"
)

// fakeLocker hands out one lease at a time, the way a real store does.
// fakeLocker 一次只发放一个租约，与真实存储一致。
type fakeLocker struct {
	mu       sync.Mutex
	held     bool
	acquires int
	renew    func(ctx context.Context, l *fakeLease) error
	released int
}

func (f *fakeLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (lease.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	if f.held {
		return nil, lease.ErrHeld
	}
	f.held = true
	return &fakeLease{locker: f, key: key, token: int64(f.acquires)}, nil
}

type fakeLease struct {
	locker *fakeLocker
	key    string
	token  int64
}

func (l *fakeLease) Key() string  { return l.key }
func (l *fakeLease) Token() int64 { return l.token }

func (l *fakeLease) Renew(ctx context.Context) error {
	l.locker.mu.Lock()
	renew := l.locker.renew
	l.locker.mu.Unlock()
	if renew == nil {
		return nil
	}
	return renew(ctx, l)
}

func (l *fakeLease) Release(ctx context.Context) error {
	l.locker.mu.Lock()
	defer l.locker.mu.Unlock()
	l.locker.held = false
	l.locker.released++
	return nil
}

func TestRunRejectsBadInput(t *testing.T) {
	l := &fakeLocker{}
	fn := func(context.Context, int64) error { return nil }

	if err := lease.Run(context.Background(), nil, "k", lease.RunConfig{}, fn); !errors.Is(err, lease.ErrNilLocker) {
		t.Fatalf("nil locker: got %v, want ErrNilLocker", err)
	}
	if err := lease.Run(context.Background(), l, "k", lease.RunConfig{}, nil); !errors.Is(err, lease.ErrNilFunc) {
		t.Fatalf("nil func: got %v, want ErrNilFunc", err)
	}
	if err := lease.Run(context.Background(), l, "", lease.RunConfig{}, fn); !errors.Is(err, lease.ErrInvalidKey) {
		t.Fatalf("empty key: got %v, want ErrInvalidKey", err)
	}
	if err := lease.Run(context.Background(), l, "a{b", lease.RunConfig{}, fn); !errors.Is(err, lease.ErrInvalidKey) {
		t.Fatalf("braced key: got %v, want ErrInvalidKey", err)
	}
	if err := lease.Run(context.Background(), l, "k", lease.RunConfig{TTL: time.Millisecond}, fn); !errors.Is(err, lease.ErrInvalidTTL) {
		t.Fatalf("short ttl: got %v, want ErrInvalidTTL", err)
	}
}

// TestRunWaitsWhileHeld covers the standby path: another instance owns the
// lease, so Run must keep polling instead of returning or calling fn.
// TestRunWaitsWhileHeld 覆盖待命路径：租约被别处持有，Run 必须持续轮询而不是返回
// 或调用 fn。
func TestRunWaitsWhileHeld(t *testing.T) {
	l := &fakeLocker{held: true}
	called := 0
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := lease.Run(ctx, l, "settle", lease.RunConfig{TTL: 300 * time.Millisecond}, func(context.Context, int64) error {
		called++
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 0 {
		t.Fatalf("fn called %d times while the lease was held", called)
	}
}

// TestRunReleasesOnShutdown is the property the whole primitive exists for: a
// clean shutdown must hand the lease back instead of leaving the next instance
// waiting out the ttl.
// TestRunReleasesOnShutdown 是这个原语存在的理由：正常停机必须把租约交还，而不是
// 让下一个实例白等一个租期。
func TestRunReleasesOnShutdown(t *testing.T) {
	l := &fakeLocker{}
	ctx, cancel := context.WithCancel(context.Background())

	err := lease.Run(ctx, l, "settle", lease.RunConfig{TTL: 300 * time.Millisecond}, func(ctx context.Context, token int64) error {
		if token != 1 {
			t.Errorf("token = %d, want 1", token)
		}
		cancel()
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if l.released != 1 {
		t.Fatalf("released %d times, want 1", l.released)
	}
}

// TestRunCallsFnOnInterval checks the periodic shape: the lease stays held
// between calls so no other instance can insert an extra run.
// TestRunCallsFnOnInterval 检查周期形态：租约在两次调用之间不释放，其他实例因此
// 无法插进一次额外的执行。
func TestRunCallsFnOnInterval(t *testing.T) {
	l := &fakeLocker{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	err := lease.Run(ctx, l, "settle", lease.RunConfig{
		TTL:      300 * time.Millisecond,
		Interval: 10 * time.Millisecond,
	}, func(context.Context, int64) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls < 3 {
		t.Fatalf("fn called %d times, want at least 3", calls)
	}
	if l.acquires != 1 {
		t.Fatalf("acquired %d times, want 1", l.acquires)
	}
}

// TestRunStopsOnLeaseLoss covers the fencing half: when renewal fails the guard
// cancels fn so it stops before its token becomes stale, and Run reports the
// loss instead of a clean shutdown.
// TestRunStopsOnLeaseLoss 覆盖 fencing 的另一半：续约失败时 Run 取消 fn，让它在
// token 过期之前停下，并把结果报告为租约丢失而不是正常停机。
func TestRunStopsOnLeaseLoss(t *testing.T) {
	l := &fakeLocker{renew: func(context.Context, *fakeLease) error { return lease.ErrLeaseLost }}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var fnErr error
	err := lease.Run(ctx, l, "settle", lease.RunConfig{TTL: 300 * time.Millisecond}, func(ctx context.Context, _ int64) error {
		<-ctx.Done()
		fnErr = ctx.Err()
		return ctx.Err()
	})
	if !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("Run: got %v, want ErrLeaseLost", err)
	}
	if fnErr == nil {
		t.Fatal("fn context was not cancelled when the lease was lost")
	}
	if l.released != 1 {
		t.Fatalf("released %d times, want 1", l.released)
	}
}

// TestRunReturnsFnError makes sure a real failure is not swallowed by the
// shutdown path.
// TestRunReturnsFnError 确保真正的失败不会被停机路径吞掉。
func TestRunReturnsFnError(t *testing.T) {
	l := &fakeLocker{}
	boom := errors.New("boom")
	err := lease.Run(context.Background(), l, "settle", lease.RunConfig{TTL: 300 * time.Millisecond}, func(context.Context, int64) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Run: got %v, want boom", err)
	}
	if l.released != 1 {
		t.Fatalf("released %d times, want 1", l.released)
	}
}
