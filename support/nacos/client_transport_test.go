package nacos

import (
	"net/http"
	"testing"
)

// replacedTransport stands in for an APM SDK or a test that swapped the global
// transport for something that is not an *http.Transport.
// replacedTransport 代表 APM SDK 或测试把全局 transport 换成了非 *http.Transport
// 的实现。
type replacedTransport struct{}

func (replacedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

// TestNewHTTPClientSurvivesAReplacedDefaultTransport: the old code asserted the
// global transport to *http.Transport without a check, so construction panicked
// before any error could be reported. The replacement must still produce a
// transport that can time out — a zero-value Transport cannot.
// TestNewHTTPClientSurvivesAReplacedDefaultTransport：旧实现不带检查地把全局
// transport 断言成 *http.Transport，构造阶段就 panic，来不及报任何错误。替换实现
// 下仍必须得到能超时的 transport——零值 Transport 做不到。
func TestNewHTTPClientSurvivesAReplacedDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = replacedTransport{}
	t.Cleanup(func() { http.DefaultTransport = original })

	client := newHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("fallback transport has no DialContext: dialing would never time out")
	}
	if transport.Proxy != nil {
		t.Fatal("proxy must stay disabled: Nacos is reached by intranet address")
	}
}
