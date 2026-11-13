package otel

import (
	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"
)

// InstrumentRedis adds client spans to every command issued through rdb. It is
// a no-op when tracing is disabled or rdb is nil.
//
// Command arguments are not recorded (redisotel.WithDBStatement(false)): cache
// keys often carry user or tenant identifiers and values may be secrets.
// InstrumentRedis 为经由 rdb 发出的每条命令加上 client Span。追踪关闭或 rdb
// 为 nil 时为空操作。
// 命令参数不会被记录（redisotel.WithDBStatement(false)）：缓存键常带有用户或
// 租户标识，值也可能是敏感数据。
func (p *Provider) InstrumentRedis(rdb goredis.UniversalClient) error {
	if !p.Enabled() {
		return nil
	}
	return InstrumentRedisWith(rdb, p.TracerProvider())
}

// InstrumentRedisWith is the tracer-provider form of InstrumentRedis, for
// callers that hold a Runtime instead of a Provider.
// InstrumentRedisWith 是 InstrumentRedis 的 tracer-provider 形式，适用于持有
// Runtime 而非 Provider 的调用方。
func InstrumentRedisWith(rdb goredis.UniversalClient, tp trace.TracerProvider) error {
	if rdb == nil || tp == nil {
		return nil
	}
	return redisotel.InstrumentTracing(rdb,
		redisotel.WithTracerProvider(tp),
		redisotel.WithDBStatement(false),
	)
}
