package worker

import (
	"context"
	"errors"
	"time"

	"github.com/bingcool/gofy/logger"
	"go.uber.org/zap"
)

// Ticker runs fn once per interval until ctx is cancelled, then returns nil. It
// is the resident shape used by polling workers: a failed call is logged and the
// next tick still happens, because a loop that dies on one error leaves the
// process alive with the work silently stopped. A panic is recovered the same
// way, so one bad tick cannot take the worker down with it.
//
// The interval is a millisecond count, not a time.Duration: callers configure
// polling rhythms as "every 5000 ms", and a bare number cannot be misread as
// seconds. Zero is rejected before the first tick.
//
// The callback receives the worker context, which is also where a caller finds
// DrainContext: work that is already on the wire should use it, so a shutdown
// does not cut a query in half.
// Ticker 每经过 intervalMS 毫秒调用一次 fn，直到 ctx 取消后返回 nil。它是轮询型 worker
// 的常驻形态：单次失败只记日志，下一个 tick 照常执行——出错就退出的循环会让进程还活着、
// 工作却悄悄停了。panic 同样被恢复，一次坏 tick 带不走整个 worker。
//
// 间隔是毫秒数而不是 time.Duration：轮询节奏通常按"每 5000 毫秒"配，裸数字不会被人读成
// 秒。0 在第一个 tick 之前就被拒绝。
//
// 回调拿到的是 worker context，DrainContext 也从这里取：已经在链路上的活应该用它，
// 停机才不会把查询切成两半。
func Ticker(ctx context.Context, intervalMS uint32, fn func(context.Context) error) error {
	if intervalMS == 0 {
		return errors.New("worker: ticker interval must be positive (milliseconds)")
	}
	if fn == nil {
		return errors.New("worker: ticker callback is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(time.Duration(intervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			runTick(ctx, fn)
		}
	}
}

// runTick isolates one call: the callback's error is reported at warn level and
// a panic at error level, and neither escapes to the loop.
// runTick 隔离单次调用：回调的错误按 warn 记录、panic 按 error 记录，两者都不会逃到
// 循环外。
func runTick(ctx context.Context, fn func(context.Context) error) {
	lg := tickerLogger(ctx)
	metrics := metricsFrom(ctx)
	start := time.Now()
	failed := false
	defer func() {
		if r := recover(); r != nil {
			failed = true
			lg.Error("worker tick panicked", tickerFields(ctx, zap.Any("panic", r), zap.Stack("stack"))...)
		}
		// Record after the panic is recovered: one bad tick must still produce
		// its duration and error count instead of skipping both.
		// 在 panic 恢复之后再记录：一次坏 tick 仍要留下耗时与错误计数。
		if metrics != nil {
			metrics.recordTick(ctx, time.Since(start), failed)
		}
	}()
	if err := fn(ctx); err != nil {
		failed = true
		lg.Warn("worker tick failed", tickerFields(ctx, zap.Error(err))...)
	}
}

// tickerFields tags a tick log with the worker identity, so a shared log file
// still says which instance failed.
// tickerFields 给 tick 日志带上 worker 身份，共用日志文件时也能看出是哪个实例失败。
func tickerFields(ctx context.Context, fields ...zap.Field) []zap.Field {
	if id, ok := IdentityFrom(ctx); ok {
		return append([]zap.Field{zap.String("worker", id.String())}, fields...)
	}
	return fields
}

func tickerLogger(ctx context.Context) *zap.Logger {
	if rt, ok := RuntimeFrom(ctx); ok && rt.Logger != nil {
		return rt.Logger
	}
	return logger.Nop()
}
