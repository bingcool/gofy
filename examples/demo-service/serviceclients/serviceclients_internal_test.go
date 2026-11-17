package serviceclients

import (
	"context"
	"errors"
	"testing"

	gclient "github.com/bingcool/gofy-interface-api/client"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
)

// fakeCall records how often it was called and returns a fixed outcome.
// fakeCall 记录被调用的次数，并返回固定结果。
type fakeCall struct {
	calls int
	res   *orderresponse.GetConfigResponse
	err   error
}

func (f *fakeCall) GetConfig(context.Context, *orderrequest.GetConfigRequest, ...gclient.CallOption) (*orderresponse.GetConfigResponse, error) {
	f.calls++
	return f.res, f.err
}

func newDegradable(primary, fallback DemoService) *degradable {
	return &degradable{primary: primary, fallback: fallback, owner: &Clients{}}
}

// The scenario the fallback exists for: the resolved instance refuses the
// connection, so the deployment address answers instead.
// 兜底存在的场景：解析到的实例拒绝连接，改由部署地址答复。
func TestDegradableFallsBackOnTransportError(t *testing.T) {
	primary := &fakeCall{err: errors.New("dial tcp 10.0.0.9:8080: connect: connection refused")}
	want := &orderresponse.GetConfigResponse{}
	fallback := &fakeCall{res: want}

	got, err := newDegradable(primary, fallback).GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got != want {
		t.Fatalf("response = %p, want the fallback's %p", got, want)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls)
	}
}

// A response that arrived — even a 500 — proves the target is up. Repeating the
// request against another environment would turn one answered call into two,
// and hide the server's own error behind a "success".
// 响应已经到达，哪怕是 500，也说明目标活着。换个环境重发会把一次已答复的调用变成两次，
// 并把服务端自己的错误伪装成"成功"。
func TestDegradableKeepsAnsweredError(t *testing.T) {
	apiErr := &gclient.APIError{Status: 500, Code: 50001, Message: "boom"}
	primary := &fakeCall{err: apiErr}
	fallback := &fakeCall{res: &orderresponse.GetConfigResponse{}}

	_, err := newDegradable(primary, fallback).GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	var got *gclient.APIError
	if !errors.As(err, &got) || got.Code != 50001 {
		t.Fatalf("err = %v, want the server's APIError", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls)
	}
}

// A caller that gave up must not trigger a second request: its deadline is the
// caller's decision, not evidence that the address is wrong.
// 调用方已经放弃时不能发起第二次请求：它的 deadline 是调用方的决定，不是"地址不对"
// 的证据。
func TestDegradableSkipsCanceledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &fakeCall{err: context.Canceled}
	fallback := &fakeCall{res: &orderresponse.GetConfigResponse{}}

	_, err := newDegradable(primary, fallback).GetConfig(ctx, &orderrequest.GetConfigRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls)
	}
}

// When the fallback fails too, both causes have to survive: the first explains
// why the discovered address was not used, the second why the retry failed.
// 兜底也失败时两个原因都要留下：前一个说明为什么没用解析到的地址，后一个说明重试为什么
// 失败。
func TestDegradableJoinsBothErrors(t *testing.T) {
	primaryErr := errors.New("nacos resolver: no available instances")
	fallbackErr := errors.New("dial tcp: i/o timeout")
	primary := &fakeCall{err: primaryErr}
	fallback := &fakeCall{err: fallbackErr}

	_, err := newDegradable(primary, fallback).GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if !errors.Is(err, primaryErr) || !errors.Is(err, fallbackErr) {
		t.Fatalf("err = %v, want both causes", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls)
	}
}

// The client's own deadline expiring is exactly the blackholed-pod case: no
// response arrived, so a second address is worth trying. Only the caller's
// context ends the argument.
// 客户端自己的 deadline 到期正是"Pod 被黑洞"的情形：没有响应到达，因此值得换地址重试。
// 只有调用方的 context 才有否决权。
func TestDegradableFallsBackOnOwnDeadline(t *testing.T) {
	primary := &fakeCall{err: context.DeadlineExceeded}
	want := &orderresponse.GetConfigResponse{}
	fallback := &fakeCall{res: want}

	got, err := newDegradable(primary, fallback).GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got != want {
		t.Fatalf("response = %p, want the fallback's %p", got, want)
	}
}
