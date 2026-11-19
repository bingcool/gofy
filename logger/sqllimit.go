package logger

import (
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// sampler decides whether an informational SQL entry is written. It exists
// because "log every statement" is only affordable on a quiet service; the
// ratio applies to Info rows only, since slow and failed statements are already
// capped by their own limiters.
// sampler 决定一条 Info 级 SQL 条目是否写出。因为"每条都记"只有在低流量服务上才划算；
// 采样只作用于 Info 行——慢 SQL 与失败语句各自有限流。
type sampler struct {
	rate float64
}

// allow reports whether this entry should be written. A nil sampler means "no
// sampling configured", which writes everything.
// allow 报告本条是否应写出。nil 表示未配置采样，全部写出。
func (s *sampler) allow() bool {
	if s == nil || s.rate >= 1 {
		return true
	}
	if s.rate <= 0 {
		return false
	}
	return rand.Float64() < s.rate //nolint:gosec // sampling does not need a cryptographic source
}

// limiter caps how many entries may be written per second. When a window is
// exhausted it counts the drops, and the caller writes one summary line when
// the next window opens — a storm stays visible without filling the disk.
//
// A window counter is used instead of a token bucket on purpose: the contract
// is "at most N per second", and a token bucket would additionally allow a
// burst of N.
// limiter 限制每秒最多写多少条。窗口用尽后只累计丢弃数，调用方在下个窗口开始时打一条
// 汇总——风暴因此可见，又不会写满磁盘。
//
// 刻意用窗口计数器而不是令牌桶：契约是"每秒最多 N 条"，令牌桶会额外允许一次 N 条的突发。
type limiter struct {
	mu     sync.Mutex
	limit  int
	window int64
	count  int
	drops  int
}

// allow reports whether this entry may be written, plus how many were dropped
// in the window that just closed. The drop count is non-zero only on the first
// call of a new window, which is exactly when the caller writes the summary.
// allow 报告本条可否写出，并返回刚刚结束的窗口里丢弃了多少条。丢弃数只在新窗口的
// 第一次调用时非零，那正是调用方写汇总行的时机。
func (l *limiter) allow() (ok bool, dropped int) {
	if l == nil || l.limit <= 0 {
		return true, 0
	}
	now := time.Now().Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now != l.window {
		dropped = l.drops
		l.window, l.count, l.drops = now, 0, 0
	}
	if l.count < l.limit {
		l.count++
		return true, dropped
	}
	l.drops++
	return false, dropped
}

// sanitizeControl replaces control characters that would either break a
// single-line entry or forge a second one. JSON encoders escape them, console
// encoders do not, so the input is normalized instead of relying on the encoder.
// sanitizeControl 替换控制字符：它们会破坏单行条目，或者伪造出第二条。JSON 编码器
// 会转义，console 编码器不会，因此这里直接归一化输入，而不是依赖编码器。
func sanitizeControl(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
}

// truncateLimit clamps an over-long statement. maxLength < 0 means "never
// truncate"; a zero value has already been turned into the default during
// normalization, so it never reaches here.
// truncateLimit 截断过长的语句。maxLength < 0 表示不截断；0 已在归一化阶段换成默认值，
// 因此不会走到这里。
func truncateLimit(s string, maxLength int) (string, bool) {
	if maxLength < 0 || len(s) <= maxLength {
		return s, false
	}
	// Do not split a UTF-8 sequence: walk back to a rune boundary.
	// 不要切断 UTF-8 序列：回退到 rune 边界。
	cut := maxLength
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…", true
}
