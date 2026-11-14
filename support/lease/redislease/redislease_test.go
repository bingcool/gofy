package redislease_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bingcool/gofy/support/lease"
	"github.com/bingcool/gofy/support/lease/redislease"
)

const ttl = 300 * time.Millisecond

func newLocker(t *testing.T, mr *miniredis.Miniredis, prefix string) *redislease.Locker {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	l, err := redislease.New(client, redislease.Config{Prefix: prefix})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func acquire(t *testing.T, l *redislease.Locker, key string) lease.Lease {
	t.Helper()
	held, err := l.Acquire(context.Background(), key, ttl)
	if err != nil {
		t.Fatalf("Acquire %q: %v", key, err)
	}
	return held
}

func TestAcquireIsExclusive(t *testing.T) {
	mr := miniredis.RunT(t)
	first := newLocker(t, mr, "")
	second := newLocker(t, mr, "")

	held := acquire(t, first, "settle:reconcile")
	if held.Token() != 1 {
		t.Fatalf("token = %d, want 1", held.Token())
	}
	if _, err := second.Acquire(context.Background(), "settle:reconcile", ttl); !errors.Is(err, lease.ErrHeld) {
		t.Fatalf("second acquire: got %v, want ErrHeld", err)
	}
}

// TestReleaseLetsAnotherTakeoverLargeToken checks the fence counter survives a
// release: the next holder must get a strictly larger token, otherwise a
// delayed write from the first holder would still pass a `fence < token` test.
// TestReleaseLetsAnotherTakeoverLargeToken 检查 fence 计数器能跨释放存活：下一个
// 持有者必须拿到严格更大的 token，否则第一个持有者的迟到写入仍能通过
// `fence < token` 的比较。
func TestReleaseLetsAnotherTakeoverLargeToken(t *testing.T) {
	mr := miniredis.RunT(t)
	first := newLocker(t, mr, "")
	second := newLocker(t, mr, "")

	held := acquire(t, first, "settle:reconcile")
	if err := held.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	next := acquire(t, second, "settle:reconcile")
	if next.Token() <= held.Token() {
		t.Fatalf("token %d did not advance past %d", next.Token(), held.Token())
	}
}

func TestRenewExtendsTheLease(t *testing.T) {
	mr := miniredis.RunT(t)
	first := newLocker(t, mr, "")
	second := newLocker(t, mr, "")

	held := acquire(t, first, "settle:reconcile")

	// Two thirds of the term pass, then a renewal buys a fresh term.
	// 走过三分之二租期后续约，等于重新买一整段。
	mr.FastForward(2 * ttl / 3)
	if err := held.Renew(context.Background()); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	mr.FastForward(2 * ttl / 3)
	if _, err := second.Acquire(context.Background(), "settle:reconcile", ttl); !errors.Is(err, lease.ErrHeld) {
		t.Fatalf("acquire after renew: got %v, want ErrHeld", err)
	}
}

// TestExpiredHolderCannotRenewOrRelease is the pause scenario: the holder stalls
// past the ttl, another instance takes over, and the stalled holder's calls
// must fail instead of extending or deleting the new lease.
// TestExpiredHolderCannotRenewOrRelease 是"暂停"场景：持有者停顿超过 ttl，另一个
// 实例接管后，停顿者的调用必须失败，而不是续上或删掉新租约。
func TestExpiredHolderCannotRenewOrRelease(t *testing.T) {
	mr := miniredis.RunT(t)
	stalled := newLocker(t, mr, "")
	replacement := newLocker(t, mr, "")

	old := acquire(t, stalled, "settle:reconcile")
	mr.FastForward(ttl + time.Millisecond)

	next := acquire(t, replacement, "settle:reconcile")
	if next.Token() <= old.Token() {
		t.Fatalf("replacement token %d did not advance past %d", next.Token(), old.Token())
	}

	if err := old.Renew(context.Background()); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("stalled renew: got %v, want ErrLeaseLost", err)
	}
	if err := old.Release(context.Background()); err != nil {
		t.Fatalf("stalled release: %v", err)
	}
	if err := next.Renew(context.Background()); err != nil {
		t.Fatalf("replacement lost its lease: %v", err)
	}
}

// guardedRow models a business row protected by a fencing column. The write is
// the same shape the design recommends: UPDATE ... WHERE fence < :token.
// guardedRow 模拟用 fencing 列保护的业务行，写入形态与设计推荐的一致：
// UPDATE ... WHERE fence < :token。
type guardedRow struct {
	mu    sync.Mutex
	fence int64
}

func (r *guardedRow) write(token int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if token <= r.fence {
		return false
	}
	r.fence = token
	return true
}

// TestFenceRejectsTheStaleWriter ties the two halves together: the paused
// holder still has a token, and the resource refuses it once a newer token has
// written.
// TestFenceRejectsTheStaleWriter 把两半串起来：暂停的持有者手里仍有 token，但一旦
// 更新的 token 写过资源，它的写入就会被拒绝。
func TestFenceRejectsTheStaleWriter(t *testing.T) {
	mr := miniredis.RunT(t)
	stalled := newLocker(t, mr, "")
	replacement := newLocker(t, mr, "")
	row := &guardedRow{}

	old := acquire(t, stalled, "settle:reconcile")
	if !row.write(old.Token()) {
		t.Fatal("first write was rejected")
	}

	mr.FastForward(ttl + time.Millisecond)
	next := acquire(t, replacement, "settle:reconcile")
	if !row.write(next.Token()) {
		t.Fatal("replacement write was rejected")
	}
	if row.write(old.Token()) {
		t.Fatalf("stale token %d overwrote fence %d", old.Token(), row.fence)
	}
}

func TestPrefixSeparatesKeySpaces(t *testing.T) {
	mr := miniredis.RunT(t)
	staging := newLocker(t, mr, "staging:")
	production := newLocker(t, mr, "prod:")

	acquire(t, staging, "settle:reconcile")
	acquire(t, production, "settle:reconcile")
}

func TestAcquireValidatesItsInput(t *testing.T) {
	mr := miniredis.RunT(t)
	l := newLocker(t, mr, "")

	if _, err := l.Acquire(context.Background(), "", ttl); !errors.Is(err, lease.ErrInvalidKey) {
		t.Fatalf("empty key: got %v, want ErrInvalidKey", err)
	}
	if _, err := l.Acquire(context.Background(), "k", time.Millisecond); !errors.Is(err, lease.ErrInvalidTTL) {
		t.Fatalf("short ttl: got %v, want ErrInvalidTTL", err)
	}
	if _, err := redislease.New(nil, redislease.Config{}); err == nil {
		t.Fatal("New(nil) returned no error")
	}
	if _, err := redislease.New(goredis.NewClient(&goredis.Options{}), redislease.Config{Prefix: "a{b:"}); err == nil {
		t.Fatal("New accepted a braced prefix")
	}
}

// TestRunUnderRedisLease wires lease.Run to the Redis implementation end to end:
// the guarded function runs, then a second instance takes over once the first
// shuts down and releases.
// TestRunUnderRedisLease 把 lease.Run 与 Redis 实现端到端接起来：受保护的操作执行
// 后，第一个实例停机释放，第二个实例随即接管。
func TestRunUnderRedisLease(t *testing.T) {
	mr := miniredis.RunT(t)
	first := newLocker(t, mr, "")
	second := newLocker(t, mr, "")

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan int64, 1)
	done := make(chan error, 1)
	go func() {
		done <- lease.Run(ctx, first, "settle:reconcile", lease.RunConfig{TTL: ttl}, func(ctx context.Context, token int64) error {
			ran <- token
			<-ctx.Done()
			return nil
		})
	}()

	token := <-ran
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	held := acquire(t, second, "settle:reconcile")
	if held.Token() <= token {
		t.Fatalf("takeover token %d did not advance past %d", held.Token(), token)
	}
}
