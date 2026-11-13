package otel

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// GinMiddleware returns the server span middleware. It is nil when tracing is
// disabled, so callers can skip installing it. filter receives each request
// and returns true when the request should be traced; nil traces everything.
//
// The middleware extracts the W3C traceparent header from the request, so a
// caller's trace continues into this service.
// GinMiddleware 返回 server Span 中间件。追踪关闭时返回 nil，调用方可直接跳过
// 安装。filter 对每个请求求值，返回 true 表示需要追踪；nil 表示全部追踪。
// 中间件从请求中提取 W3C traceparent 头，调用方的 Trace 因此能延续到本服务。
func (p *Provider) GinMiddleware(filter func(*http.Request) bool) gin.HandlerFunc {
	if !p.Enabled() {
		return nil
	}
	opts := []otelgin.Option{
		otelgin.WithTracerProvider(p.tp),
		otelgin.WithPropagators(p.propagator),
	}
	if filter != nil {
		opts = append(opts, otelgin.WithFilter(otelgin.Filter(filter)))
	}
	return otelgin.Middleware(p.serviceName, opts...)
}

// SkipPaths returns a Gin filter that traces every request except the given
// URL paths. It keeps probes and metrics endpoints out of the trace stream.
// SkipPaths 返回一个 Gin filter：除给定 URL 路径外都追踪。它让探活与指标接口
// 不进入 Trace 流，避免噪声。
func SkipPaths(paths ...string) func(*http.Request) bool {
	skip := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		skip[path] = struct{}{}
	}
	return func(r *http.Request) bool {
		if r == nil || r.URL == nil {
			return false
		}
		_, found := skip[r.URL.Path]
		return !found
	}
}
