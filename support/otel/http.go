package otel

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPTransport wraps base so outbound requests continue the current trace and
// produce a client span. A nil base uses http.DefaultTransport. When tracing is
// disabled, base is returned unchanged.
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
