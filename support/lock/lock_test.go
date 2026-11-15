package lock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/lock"
	gofyredis "github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
)

// testClient connects to miniredis and is closed at the end of the test.
// testClient 连接 miniredis，并在测试结束时关闭。
func testClient(t *testing.T, mr *miniredis.Miniredis) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         mr.Addr(),
		MaxRetries:   -1,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// deadClient points at an unreachable address to exercise the unavailable path.
// deadClient 指向不可达地址，用来验证不可用路径。
func deadClient(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         "127.0.0.1:1",
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestLockWritesAndReleasesTheKey(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)), lock.WithPrefix("demo:lock"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	mu, err := l.Lock(ctx, "order:1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if mu.Key() != "order:1" || mu.RedisKey() != "demo:lock:order:1" {
		t.Fatalf("keys = %q / %q", mu.Key(), mu.RedisKey())
	}
	if mu.Token() == "" {
		t.Fatal("lock has no token")
	}
	stored, err := mr.Get("demo:lock:order:1")
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if stored != mu.Token() {
		t.Fatalf("stored value = %q, want the mutex token %q", stored, mu.Token())
	}
	if ttl := mr.TTL("demo:lock:order:1"); ttl <= 0 {
		t.Fatalf("key has no TTL: %s", ttl)
	}

	if err := mu.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if mr.Exists("demo:lock:order:1") {
		t.Fatal("Unlock left the key behind")
	}
	// Releasing twice must be visible: the second call owns nothing.
	if err := mu.Unlock(ctx); !errors.Is(err, lock.ErrNotHeld) {
		t.Fatalf("second Unlock = %v, want ErrNotHeld", err)
	}
}

func TestLockReportsWhenTheKeyIsHeld(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)
	first, err := lock.New(lock.WithClient(client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := lock.New(lock.WithClient(client), lock.WithRetry(2, time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	held, err := first.Lock(ctx, "settle")
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer func() { _ = held.Unlock(ctx) }()

	if _, err := second.Lock(ctx, "settle"); !errors.Is(err, lock.ErrAlreadyHeld) {
		t.Fatalf("second Lock = %v, want ErrAlreadyHeld", err)
	}
	// TryLock is a single attempt: it must answer immediately even though the
	// locker was configured to retry.
	start := time.Now()
	if _, err := second.TryLock(ctx, "settle"); !errors.Is(err, lock.ErrAlreadyHeld) {
		t.Fatalf("TryLock = %v, want ErrAlreadyHeld", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("TryLock took %s; it must not retry", elapsed)
	}
}

func TestLockExpiryFreesTheKeyAndUnlockStopsOwningIt(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	mu, err := l.Lock(ctx, "once", lock.WithLockExpiry(time.Second))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	mr.FastForward(2 * time.Second)
	if mr.Exists("once") {
		t.Fatal("miniredis kept the key past its TTL")
	}
	// The lock is gone, so this mutex no longer owns anything. Reporting that is
	// what stops a caller from believing it is still exclusive.
	if err := mu.Unlock(ctx); !errors.Is(err, lock.ErrNotHeld) {
		t.Fatalf("Unlock after expiry = %v, want ErrNotHeld", err)
	}
}

func TestExtendKeepsTheLockAlive(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	mu, err := l.Lock(ctx, "long-task", lock.WithLockExpiry(time.Second))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	mr.FastForward(800 * time.Millisecond)
	if err := mu.Extend(ctx); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	// Past the original TTL, still inside the extended one.
	mr.FastForward(800 * time.Millisecond)
	if !mr.Exists("long-task") {
		t.Fatal("Extend did not reset the TTL")
	}
	if err := mu.Unlock(ctx); err != nil {
		t.Fatalf("Unlock after Extend: %v", err)
	}
}

func TestExtendedLockCannotBeTakenOver(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)
	owner, err := lock.New(lock.WithClient(client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	other, err := lock.New(lock.WithClient(client), lock.WithRetry(1, time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	mu, err := owner.Lock(ctx, "handover", lock.WithLockExpiry(time.Second))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	mr.FastForward(500 * time.Millisecond)
	if err := mu.Extend(ctx); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if _, err := other.TryLock(ctx, "handover"); !errors.Is(err, lock.ErrAlreadyHeld) {
		t.Fatalf("TryLock during the extended window = %v, want ErrAlreadyHeld", err)
	}
}

func TestCanceledContextIsNotReportedAsBusy(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)
	holder, err := lock.New(lock.WithClient(client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	waiter, err := lock.New(lock.WithClient(client), lock.WithRetry(5, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	held, err := holder.Lock(context.Background(), "cancel-me")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = held.Unlock(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// redsync folds a canceled context into ErrFailed; the caller must still be
	// able to tell "shutting down" from "lock is busy".
	_, err = waiter.Lock(ctx, "cancel-me")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock with canceled ctx = %v, want context.Canceled", err)
	}
	if errors.Is(err, lock.ErrAlreadyHeld) {
		t.Fatalf("Lock with canceled ctx also matched ErrAlreadyHeld: %v", err)
	}
}

func TestRedisFailureIsNotReportedAsBusy(t *testing.T) {
	var reported []error
	l, err := lock.New(
		lock.WithClient(deadClient(t)),
		lock.WithRetry(1, time.Millisecond),
		lock.WithErrorHandler(func(err error) { reported = append(reported, err) }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = l.Lock(context.Background(), "offline")
	if !errors.Is(err, lock.ErrUnavailable) {
		t.Fatalf("Lock against a dead redis = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, lock.ErrAlreadyHeld) {
		t.Fatalf("a dead redis was reported as a busy lock: %v", err)
	}
	if len(reported) == 0 {
		t.Fatal("WithErrorHandler did not receive the failure")
	}
}

func TestEmptyKeyIsRejected(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := l.Lock(context.Background(), "   "); !errors.Is(err, lock.ErrKeyRequired) {
		t.Fatalf("Lock with an empty key = %v, want ErrKeyRequired", err)
	}
}

func TestNilContextIsAccepted(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A nil ctx must behave like Background, the way the rest of the framework
	// treats it, instead of panicking inside redsync.
	// nil ctx 必须与 Background 等价（框架其余部分也这样处理），而不是在 redsync
	// 里 panic。
	mu, err := l.Lock(nil, "nil-ctx")
	if err != nil {
		t.Fatalf("Lock(nil ctx): %v", err)
	}
	if err := mu.Unlock(nil); err != nil {
		t.Fatalf("Unlock(nil ctx): %v", err)
	}
}

func TestTokenIsCallerSupplied(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := lock.New(lock.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mu, err := l.Lock(context.Background(), "traced", lock.WithLockToken("req-42"))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if mu.Token() != "req-42" {
		t.Fatalf("token = %q, want req-42", mu.Token())
	}
	if stored, err := mr.Get("traced"); err != nil || stored != "req-42" {
		t.Fatalf("stored = %q, %v", stored, err)
	}
}

func TestClientAndInstanceAreMutuallyExclusive(t *testing.T) {
	mr := miniredis.RunT(t)
	_, err := lock.New(lock.WithClient(testClient(t, mr)), lock.WithInstance("default"))
	if err == nil {
		t.Fatal("New accepted both WithClient and WithInstance")
	}
	if _, err := lock.New(lock.WithClient(nil)); !errors.Is(err, lock.ErrClientRequired) {
		t.Fatalf("WithClient(nil) = %v, want ErrClientRequired", err)
	}
}

func TestInstanceAliasResolvesThroughSupportRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	// The registry is process-wide; this is the only test in the package that
	// uses an alias, so installing and closing one generation here cannot leak
	// into another test.
	// 注册表是进程级的；本包只有这个测试用别名，因此这里安装并关闭一代不会影响其它测试。
	reg, err := gofyredis.Configure(map[string]gofyredis.Config{
		"locktest": {Addr: mr.Addr()},
	})
	if err != nil {
		t.Fatalf("configure redis: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	l, err := lock.New(lock.WithInstance("locktest"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mu, err := l.Lock(context.Background(), "via-registry")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if !mr.Exists("via-registry") {
		t.Fatal("the alias path did not reach the configured redis")
	}
	if err := mu.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
