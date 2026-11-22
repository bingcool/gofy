package queues_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/queues"
)

// TestDelayPopClampsBatchSize pins the upper bound on one Pop. The Lua script
// deletes the batch with unpack(members) and Redis' Lua interpreter refuses more
// than about 8000 values, so an unbounded caller-supplied limit used to turn the
// whole poll into a script error. 1000 is the cap; the rest stays queued.
// TestDelayPopClampsBatchSize 固定单次 Pop 的上限。Lua 脚本用 unpack(members) 删除
// 整批，而 Redis 的 Lua 解释器最多只能 unpack 约 8000 个值，调用方传入无上限的 limit
// 会让整次轮询变成脚本错误。上限是 1000，其余消息留在队列里。
func TestDelayPopClampsBatchSize(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	ctx := context.Background()

	const total = 1100
	for i := 0; i < total; i++ {
		if err := q.Add(ctx, map[string]any{"n": i}, 0); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	got, err := q.Pop(ctx, 1_000_000)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(got) != 1000 {
		t.Fatalf("Pop(1000000) returned %d messages, want the 1000 cap", len(got))
	}

	rest, err := q.Pop(ctx, 0)
	if err != nil {
		t.Fatalf("Pop remainder: %v", err)
	}
	if len(rest) != 10 {
		t.Fatalf("Pop(0) returned %d messages, want the default of 10", len(rest))
	}
}

// TestDelayConsumeReportsWriteBackFailureAsNackError pins the error channel for a
// failed write-back. The delayed consumer used to call OnError only, so a Redis
// failure while retrying a failed message never reached OnNackError and the nack
// metric stayed silent; the immediate queue already reported it there.
// TestDelayConsumeReportsWriteBackFailureAsNackError 固定回写失败的错误通道。延迟消费者
// 原先只调用 OnError，消息重试时遇到 Redis 故障永远到不了 OnNackError，nack 指标因此
// 静默；即时队列早已走那条通道。
func TestDelayConsumeReportsWriteBackFailureAsNackError(t *testing.T) {
	q, c := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	if err := q.Add(context.Background(), map[string]any{"n": 1}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nack := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = q.Consume(runCtx, func(context.Context, *queues.Message) error {
			// Break Redis before the write-back so Retry fails.
			// 在回写之前弄坏 Redis，让 Retry 失败。
			c.mr.SetError("redis down")
			return errors.New("handler failed")
		}, queues.ConsumeOptions{
			OnNackError: func(err error) {
				select {
				case nack <- err:
				default:
				}
			},
		})
	}()

	select {
	case err := <-nack:
		if err == nil {
			t.Fatal("OnNackError received a nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write-back failure was not reported through OnNackError")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Consume did not return after cancellation")
	}
}
