package otel

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPTransport wraps base so outbound requests continue the current trace and
// produce a client span. A nil base uses http.DefaultTransport. When tracing is
// disabled, base is returned unchanged.
// HTTPTransport 包装 base，使出站请求延续当前 Trace 并产生 client Span。
// base 为 nil 时使用 http.DefaultTransport；追踪关闭时原样返回 base。
func (p *Provider) HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if !p.Enabled() {
		return base
	}
	return otelhttp.NewTransport(base,
		otelhttp.WithTracerProvider(p.tp),
		otelhttp.WithPropagators(p.propagator),
	)
}

// HTTPClient returns a client with an instrumented transport. base fields such
// as Timeout and CheckRedirect are preserved. A nil base starts from the
// standard library defaults.
// HTTPClient 返回带埋点 transport 的客户端。base 的 Timeout、CheckRedirect 等
// 字段会被保留；base 为 nil 时从标准库默认值开始。
func (p *Provider) HTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	if !p.Enabled() {
		return base
	}
	out := *base
	out.Transport = p.HTTPTransport(out.Transport)
	return &out
}
