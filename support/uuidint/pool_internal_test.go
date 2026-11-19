package uuidint

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// TestStopPoolContextGivesUpAtTheDeadline pins the reason StopPoolContext
// exists: a ticker that does not exit must not hold the caller past its budget.
// The buffer stays open on purpose — closing it would panic the abandoned
// goroutine's next send — so the test also pins that a later call can still
// finish the job once the ticker is gone.
// TestStopPoolContextGivesUpAtTheDeadline 钉住 StopPoolContext 存在的理由：停不下来
// 的 ticker 不能把调用方拖过预算。缓冲故意保持打开——此时关闭会让被放弃的 goroutine
// 下一次发送 panic——所以这里也钉住后者：ticker 消失后再调一次仍能完成收尾。
func TestStopPoolContextGivesUpAtTheDeadline(t *testing.T) {
	p := newIDPool(4, time.Hour, time.Second)
	p.started = true
	cancelled := false
	p.cancel = func() { cancelled = true }
	// done is never closed: this is a ticker stuck inside refill.
	gen := &Generator{pool: p}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := gen.StopPoolContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopPoolContext = %v, want DeadlineExceeded", err)
	}
	if !cancelled {
		t.Fatal("the ticker was never cancelled")
	}
	select {
	case <-p.ids:
		t.Fatal("the buffer was closed while the ticker could still send into it")
	default:
	}

	// The ticker finally exits; a second call completes the release.
	close(p.done)
	if err := gen.StopPoolContext(context.Background()); err != nil {
		t.Fatalf("second StopPoolContext: %v", err)
	}
	if _, ok := <-p.ids; ok {
		t.Fatal("the buffer was not closed after the ticker exited")
	}
}

// TestStopPoolContextWithoutStartIsNoop: an unstarted pool has an open done
// channel, so waiting on it would block forever. Stopping it must be a no-op
// even when the caller's ctx is already cancelled.
// TestStopPoolContextWithoutStartIsNoop：未启动的池的 done 是打开的，等它就是永远
// 等。因此即使调用方的 ctx 已经取消，停止它也必须是空操作。
func TestStopPoolContextWithoutStartIsNoop(t *testing.T) {
	p := newIDPool(4, time.Hour, time.Second)
	gen := &Generator{pool: p}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gen.StopPoolContext(ctx); err != nil {
		t.Fatalf("StopPoolContext without StartPool = %v, want nil", err)
	}
	if p.stopped {
		t.Fatal("a pool that never started was marked stopped")
	}
}

// TestRefillDoesNothingAfterStop is the audit's "can the background task still
// write after a stop?" answered directly: once stopped, a refill must return
// before it can allocate a batch or push anything into the buffer. The Redis
// counter proves it — no key is written at all — which is stronger than
// observing that the buffer happens to be empty.
// TestRefillDoesNothingAfterStop 直接回答审计的问题"停止后后台任务还能写吗"：池一旦
// 停止，refill 必须在申请批次、写缓冲之前就返回。Redis 计数器是证据——连 key 都没有
// 写——这比"缓冲恰好是空的"更强。
func TestRefillDoesNothingAfterStop(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	gen := &Generator{
		key:        "stopped-key",
		ttl:        15 * time.Second,
		retryTimes: 1,
		primary:    source{client: client},
		pool:       newIDPool(4, time.Hour, time.Second),
	}
	gen.pool.stopped = true

	gen.refill(context.Background())

	if _, err := mr.Get("stopped-key"); err == nil {
		t.Fatal("refill allocated a batch after the pool was stopped")
	}
	if len(gen.pool.ids) != 0 {
		t.Fatalf("refill buffered %d ids after the pool was stopped", len(gen.pool.ids))
	}
}

// TestGenerateDoesNotStartAnAttemptWithADoneContext: after a cancel there is
// nobody waiting for the result, so a second round trip must not be started —
// that is what keeps a shutdown to one command's time instead of the whole retry
// sequence. No key is written, so the cancellation is observed before Redis is
// touched.
// TestGenerateDoesNotStartAnAttemptWithADoneContext：取消之后已经没人在等结果，
// 就不该再发起第二个来回——这正是让关闭只花一个命令的时间、而不是整个重试序列的原因。
// 没有写入 key，说明取消在访问 Redis 之前就被看到。
func TestGenerateDoesNotStartAnAttemptWithADoneContext(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	gen := &Generator{
		key:        "cancelled-key",
		ttl:        15 * time.Second,
		retryTimes: 3,
		retryDelay: time.Millisecond,
		primary:    source{client: client},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := gen.generate(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("generate = %v, want context.Canceled", err)
	}
	if _, err := mr.Get("cancelled-key"); err == nil {
		t.Fatal("generate ran an attempt with a cancelled context")
	}
}
