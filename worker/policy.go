package worker

import (
	"math/rand/v2"
	"time"
)

// FailureMode decides what the runner does when Worker.Run returns an error.
// FailureMode 决定 Worker.Run 返回错误时 Runner 的行为。
type FailureMode int

const (
	// RestartOnError restarts the instance with exponential backoff. It is the
	// zero value because a transient broker or network failure should not take
	// the whole group down.
	// RestartOnError 按指数退避重启实例。它作为零值：broker 或网络的临时故障
	// 不应该让整个分组退出。
	RestartOnError FailureMode = iota
	// ExitOnError stops the whole group and returns the error, for failures a
	// restart cannot fix such as a bad configuration or a missing subscription.
	// ExitOnError 停止整个分组并返回错误，用于重启也修不好的失败，例如配置错误
	// 或订阅不存在。
	ExitOnError
)

// Policy controls restarts of one worker instance.
// Policy 控制单个 worker 实例的重启行为。
type Policy struct {
	// Mode selects restart or exit.
	// Mode 选择重启还是退出。
	Mode FailureMode
	// Backoff is the first restart delay; zero means 1s.
	// Backoff 是首次重启延迟，0 表示 1s。
	Backoff time.Duration
	// MaxBackoff caps the exponential growth; zero means 30s.
	// MaxBackoff 是指数增长的上限，0 表示 30s。
	MaxBackoff time.Duration
	// MaxRestarts is how many restarts one instance may attempt; 0 means
	// unlimited.
	// MaxRestarts 是单个实例允许重启的次数，0 表示不限。
	MaxRestarts int
	// Jitter spreads restarts by ±20% so a fleet of pods does not reconnect at
	// the same instant after a broker restart.
	// Jitter 给重启加 ±20% 抖动，避免 broker 重启后整批 Pod 同时重连。
	Jitter bool
}

// DefaultPolicy is the framework default: keep restarting with capped
// exponential backoff and jitter, never give up on its own.
// DefaultPolicy 是框架默认值：带上限的指数退避加抖动持续重启，不主动放弃。
func DefaultPolicy() Policy {
	return Policy{
		Mode:       RestartOnError,
		Backoff:    time.Second,
		MaxBackoff: 30 * time.Second,
		Jitter:     true,
	}
}

func (p Policy) withDefaults() Policy {
	if p.Backoff <= 0 {
		p.Backoff = time.Second
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 30 * time.Second
	}
	if p.MaxBackoff < p.Backoff {
		p.MaxBackoff = p.Backoff
	}
	if p.MaxRestarts < 0 {
		p.MaxRestarts = 0
	}
	return p
}

// backoff returns the delay before the given restart attempt, counting from 1.
// backoff 返回第 n 次重启（从 1 开始）前的等待时间。
func (p Policy) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.Backoff
	for i := 1; i < attempt; i++ {
		if d >= p.MaxBackoff {
			break
		}
		// Saturate before doubling: MaxBackoff is a Duration, so a caller that
		// sets it near MaxInt64 ("no cap, effectively") would otherwise let d*=2
		// wrap negative and turn the delay into a busy loop.
		// 加倍之前先饱和：MaxBackoff 是 Duration，把它设到接近 MaxInt64（相当于"没有上限"）
		// 的调用方，否则会让 d*=2 溢出成负数，把等待变成忙循环。
		if d > p.MaxBackoff/2 {
			d = p.MaxBackoff
			break
		}
		d *= 2
	}
	if d > p.MaxBackoff {
		d = p.MaxBackoff
	}
	if p.Jitter {
		// ±20% keeps the average delay unchanged while breaking lockstep.
		// Divided before multiplying so a saturated delay cannot overflow.
		// ±20% 保持平均延迟不变，同时打散同步重连。先除后乘，饱和后的延迟因此不会溢出。
		spread := int64(d/5) * 2
		if spread > 0 {
			d += time.Duration(rand.Int64N(spread+1)) - time.Duration(spread/2)
		}
	}
	return d
}
