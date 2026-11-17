package serviceclients

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	nacosadapter "github.com/bingcool/gofy-adapter/client/nacos"
	gclient "github.com/bingcool/gofy-interface-api/client"
)

// TestShouldDegradeWhitelist is the P3-01 acceptance test. The old rule was a
// blacklist — anything that was not an API/HTTP/Protocol error degraded — so a
// request that never left the process got a pointless second attempt and a
// two-layer joined error. The rule is now a whitelist: transport failures and
// failed lookups only.
// TestShouldDegradeWhitelist 是 P3-01 的验收测试。旧规则是排除法——凡不是
// API/HTTP/Protocol 错误都降级——于是根本没发出进程的请求会多一次无意义调用，错误也
// 变成两层 Join。现在改成白名单：只认传输失败与寻址失败。
func TestShouldDegradeWhitelist(t *testing.T) {
	refused := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connect: connection refused"),
	}
	dns := &net.DNSError{Err: "no such host", Name: "demo-service"}
	timeout := &net.DNSError{Err: "i/o timeout", Name: "demo-service", IsTimeout: true}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", refused, true},
		{"dns failure", dns, true},
		{"transport timeout", timeout, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{
			"no instances",
			fmt.Errorf("client: resolve %q: %w", "demo-service", nacosadapter.ErrNoInstances),
			true,
		},
		{
			"stale expired",
			fmt.Errorf("client: resolve %q: %w", "demo-service", nacosadapter.ErrStaleExpired),
			true,
		},
		{"api error", &gclient.APIError{Status: 500, Code: 1, Message: "boom"}, false},
		{"http error", &gclient.HTTPError{Status: 502, BodyPrefix: "bad gateway"}, false},
		{"protocol error", &gclient.ProtocolError{Message: "invalid envelope"}, false},
		// A request that never left the process: encoding, validation or
		// credentials. Another address cannot fix it.
		// 根本没发出进程的请求：编码、校验或凭据。换个地址解决不了。
		{"request encoding", errors.New("client: marshal request body: unsupported type"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDegrade(context.Background(), tc.err); got != tc.want {
				t.Fatalf("shouldDegrade(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestShouldDegradeIgnoresCancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Even a transport failure must not trigger a second attempt once the
	// caller is gone: nobody is waiting for the answer.
	// 调用方已经取消时，即便是传输失败也不该再试：没人等这个答案了。
	if shouldDegrade(ctx, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}) {
		t.Fatal("a cancelled caller degraded")
	}
	if shouldDegrade(context.Background(), nil) {
		t.Fatal("a nil error degraded")
	}
}
