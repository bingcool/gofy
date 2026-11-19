package uuidint

import (
	"context"
	"errors"
	"testing"
	"time"
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
	p := newIDPool(4, time.Hour)
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
	p := newIDPool(4, time.Hour)
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
