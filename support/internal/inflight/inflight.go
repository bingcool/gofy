// Package inflight defines the marker shared by every consumer transport for a
// delivery that another instance is still processing. It lives under
// support/internal because callers should use the public marker they already
// have — consumer.ErrInFlight for the shared pipeline or queues.InFlightError
// for Redis queues — while the transports need one spelling of the same rule.
//
// The error is deliberately not owned by support/consumer: support/amqp,
// support/kafka and support/rocketmq must be able to recognize it without
// importing the consumer package, which imports them.
// 包 inflight 定义所有消费者传输共用的标记：这次投递仍由另一实例处理中。它放在
// support/internal 下，因为调用方应使用已有的公开标记——共用链路用
// consumer.ErrInFlight，Redis 队列用 queues.InFlightError——而各传输只需对同一条
// 规则有统一写法。
//
// 错误刻意不归 support/consumer 所有：support/amqp、support/kafka 与
// support/rocketmq 必须能在不导入 consumer 包的情况下识别它，而 consumer 包本身
// 会导入这些传输。
package inflight

import (
	"errors"
	"time"
)

// DefaultDelay is how long a transport defers an in-flight duplicate when its
// own option is unset. It matches consumer.DefaultProcessingLease: the other
// instance's lease must expire before this copy can be handled again.
// DefaultDelay 是传输未配置自身选项时延后在途重复投递的时长。它与
// consumer.DefaultProcessingLease 一致：另一个实例的租约到期后，这份副本才能再次
// 被处理。
const DefaultDelay = 5 * time.Minute

// Error marks a handler error that asks for a later redelivery without
// consuming the normal retry budget. Implementations expose InFlight so the
// marker is structural: the consumer package does not need to import this
// package to satisfy it.
// Error 标记一种 handler 错误：要求稍后重投且不消耗正常重试预算。实现方通过
// InFlight 方法做结构匹配：consumer 包无需导入本包也能满足它。
type Error interface {
	error
	InFlight()
}

// Is reports whether err, including any wrapped error, is an in-flight marker.
// Is 判断 err（包括被包装的链）是否带有在途标记。
func Is(err error) bool {
	var marker Error
	return errors.As(err, &marker)
}

// Delay resolves a configured delay, falling back to DefaultDelay when it is
// zero or negative.
// Delay 解析配置的延迟；零或负值回退到 DefaultDelay。
func Delay(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultDelay
	}
	return configured
}
