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
func (p *Provider) InstrumentRedis(rdb goredis.UniversalClient) error {
	if !p.Enabled() {
		return nil
	}
	return InstrumentRedisWith(rdb, p.TracerProvider())
}

// InstrumentRedisWith is the tracer-provider form of InstrumentRedis, for
// callers that hold a Runtime instead of a Provider.
func InstrumentRedisWith(rdb goredis.UniversalClient, tp trace.TracerProvider) error {
	if rdb == nil || tp == nil {
		return nil
	}
	return redisotel.InstrumentTracing(rdb,
		redisotel.WithTracerProvider(tp),
		redisotel.WithDBStatement(false),
	)
}
