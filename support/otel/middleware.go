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
