package worker

import (
	"math"
	"testing"
	"time"
)

// TestBackoffSaturatesInsteadOfOverflowing pins the cap arithmetic. MaxBackoff
// is a time.Duration, so "effectively no cap" means a value near MaxInt64; the
// old loop doubled until it passed the cap, which wrapped the value negative
// and turned the restart delay into an immediate retry. Saturation must stop at
// the cap instead.
// TestBackoffSaturatesInsteadOfOverflowing 固定上限运算。MaxBackoff 是 time.Duration，
// 因此"实际不设上限"意味着一个接近 MaxInt64 的值；旧循环一直加倍到超过上限，会把值溢出成
// 负数，让重启等待变成立即重试。饱和必须在到达上限时停住。
func TestBackoffSaturatesInsteadOfOverflowing(t *testing.T) {
	unbounded := time.Duration(math.MaxInt64)
	p := Policy{Backoff: time.Second, MaxBackoff: unbounded}

	if got := p.backoff(1); got != time.Second {
		t.Fatalf("backoff(1) = %v, want the base delay", got)
	}
	if got := p.backoff(2); got != 2*time.Second {
		t.Fatalf("backoff(2) = %v, want 2s", got)
	}
	for _, attempt := range []int{64, 200, 1 << 20} {
		if got := p.backoff(attempt); got != unbounded {
			t.Fatalf("backoff(%d) with no cap = %v, want %v", attempt, got, unbounded)
		}
	}
}

// TestBackoffStillGrowsAndCaps keeps the ordinary shape: doubling below the cap
// and a hard stop at it.
// TestBackoffStillGrowsAndCaps 保留普通形状：上限之下加倍，到达上限即停。
func TestBackoffStillGrowsAndCaps(t *testing.T) {
	p := Policy{Backoff: 100 * time.Millisecond, MaxBackoff: 400 * time.Millisecond}
	cases := map[int]time.Duration{
		1:  100 * time.Millisecond,
		2:  200 * time.Millisecond,
		3:  400 * time.Millisecond,
		4:  400 * time.Millisecond,
		99: 400 * time.Millisecond,
	}
	for attempt, want := range cases {
		if got := p.backoff(attempt); got != want {
			t.Fatalf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// TestBackoffJitterStaysWithinTwentyPercent pins the jitter window on a delay
// that is already at the cap: the spread is computed by dividing before
// multiplying, so a saturated delay cannot overflow into a negative wait.
// TestBackoffJitterStaysWithinTwentyPercent 固定已达上限的延迟的抖动窗口：先除后乘计算
// 幅度，饱和后的延迟因此不会溢出成负等待。
func TestBackoffJitterStaysWithinTwentyPercent(t *testing.T) {
	delay := 10 * time.Second
	p := Policy{Backoff: delay, MaxBackoff: delay, Jitter: true}

	for i := 0; i < 200; i++ {
		got := p.backoff(1)
		if got <= 0 {
			t.Fatalf("jittered backoff = %v, want a positive wait", got)
		}
		if low := delay - delay/5; got < low || got > delay+delay/5 {
			t.Fatalf("jittered backoff = %v, want within [%v, %v]", got, low, delay+delay/5)
		}
	}
}
