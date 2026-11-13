package client_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

func newClient(t *testing.T, url string, cfg client.Config) *client.Client {
	t.Helper()
	cfg.BaseURL = url
	c, err := client.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCallSuccessAndRequestID(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("X-Trace-ID", r.Header.Get("X-Trace-ID"))
		_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{"order":{"id":"42"}},"req_id":"r1"}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{
		TokenSource: func(context.Context) (string, error) { return "token", nil },
	})
	res, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 42})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order == nil || res.Order.ID != 42 {
		t.Fatalf("unexpected response: %+v", res)
	}
	if gotPath != "/orders/42" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	_ = gotQuery
}

func TestCallAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":40401,"msg":"order not found","req_id":"r2","details":{"id":"missing"}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T", err)
	}
	if apiErr.Code != int(demoservice.ErrCodeOrderNotFound) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
	if client.CodeOf(err) != int(demoservice.ErrCodeOrderNotFound) {
		t.Fatalf("CodeOf = %d", client.CodeOf(err))
	}
	if client.RequestIDOf(err) != "r2" {
		t.Fatalf("RequestIDOf = %q", client.RequestIDOf(err))
	}
	if client.DetailsOf(err)["id"] != "missing" {
		t.Fatalf("details = %v", client.DetailsOf(err))
	}
	if !isOrderNotFound(err) {
		t.Fatal("order helper did not match")
	}
}

func TestCallNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/404") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":40401,"msg":"order not found"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Retry: 1})
	if err := cancel(c, 9); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if err := cancel(c, 404); err == nil {
		t.Fatal("expected an error for 404")
	}
}

func cancel(c *client.Client, id types.Uint64String) error {
	_, err := client.Call(context.Background(), c, orderroute.CancelOrder, &orderrequest.CancelOrderRequest{ID: id})
	return err
}

func TestGetRetriesServiceUnavailable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"total":0,"list":[]}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Retry: 1})
	res, err := client.Call(context.Background(), c, orderroute.ListOrders, &orderrequest.ListOrdersRequest{PageRequest: paging.PageRequest{Page: 1, PageSize: 20}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("total = %d", res.Total)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestPostDoesNotRetryWithoutIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "down")
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Retry: 2})
	_, err := client.Call(context.Background(), c, orderroute.CreateOrder, &orderrequest.CreateOrderRequest{UserID: 1, Amount: 100})
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestPostRetriesWithIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Retry: 1})
	res, err := client.Call(context.Background(), c, orderroute.CreateOrder,
		&orderrequest.CreateOrderRequest{UserID: 1, Amount: 100},
		client.WithHeader("Idempotency-Key", "abc"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order == nil || calls.Load() != 2 {
		t.Fatalf("res=%+v calls=%d", res, calls.Load())
	}
}

func TestProtocolErrorOnInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not-json")
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1})
	var protocolErr *client.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error type = %T: %v", err, err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 64))
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{MaxResponseBytes: 16})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Timeout: 50 * time.Millisecond})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestWithTimeoutOverridesClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{Timeout: time.Second})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1},
		client.WithTimeout(20*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestWithHeadersAndConnectTimeout(t *testing.T) {
	var gotTenant string
	var gotTrace []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Tenant")
		gotTrace = r.Header.Values("X-Trace")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	// A client without a Transport falls back to http.DefaultTransport, which
	// WithConnectTimeout clones.
	// 未设置 Transport 的客户端会回落到 http.DefaultTransport，
	// WithConnectTimeout 克隆它来改连接超时，而非修改全局默认值。
	c := newClient(t, server.URL, client.Config{HTTPClient: &http.Client{}})
	_, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1},
		client.WithHeaders(http.Header{
			"x-tenant": {"tenant-a"},
			"X-Trace":  {"first", "second"},
		}),
		client.WithConnectTimeout(time.Second),
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotTenant != "tenant-a" {
		t.Fatalf("X-Tenant = %q", gotTenant)
	}
	if len(gotTrace) != 2 || gotTrace[0] != "first" || gotTrace[1] != "second" {
		t.Fatalf("X-Trace = %v", gotTrace)
	}
}

type stubTransport struct{ calls atomic.Int32 }

func (s *stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"ok","data":{"order":{"id":"5"}}}`)),
		Request:    r,
	}, nil
}

func TestWithHTTPClientOverridesConfiguredClient(t *testing.T) {
	stub := &stubTransport{}
	c, err := client.New(client.Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := client.Call(context.Background(), c, orderroute.GetOrder,
		&orderrequest.GetOrderRequest{ID: 5},
		client.WithHTTPClient(&http.Client{Transport: stub}))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order == nil || res.Order.ID != 5 {
		t.Fatalf("response = %+v", res)
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("stub calls = %d, want 1", stub.calls.Load())
	}
}

func TestWithConnectTimeoutRequiresHTTPTransport(t *testing.T) {
	c, err := client.New(client.Config{
		BaseURL:    "http://127.0.0.1:1",
		HTTPClient: &http.Client{Transport: &stubTransport{}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Call(context.Background(), c, orderroute.GetOrder,
		&orderrequest.GetOrderRequest{ID: 1},
		client.WithConnectTimeout(time.Second))
	if err == nil || !strings.Contains(err.Error(), "requires an *http.Transport") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"7"}}}`)
	}))
	defer server.Close()

	c, err := client.New(client.Config{
		Service:  "order",
		Resolver: client.StaticResolver{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 7})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order.ID != 7 {
		t.Fatalf("id = %d", res.Order.ID)
	}
}

type failoverResolver struct {
	dead    string
	live    string
	resolve atomic.Int32
}

func (r *failoverResolver) Resolve(context.Context, string) (string, error) {
	if r.resolve.Add(1) == 1 {
		return r.dead, nil
	}
	return r.live, nil
}

// sequenceResolver returns bases in order and counts calls, so tests can prove
// the client resolves once per attempt instead of pinning the first address.
// sequenceResolver 按顺序轮换返回地址并统计调用次数，
// 用于证明客户端每次尝试都重新选址，而不是固定第一个地址。
type sequenceResolver struct {
	bases   []string
	resolve atomic.Int32
}

func (r *sequenceResolver) Resolve(context.Context, string) (string, error) {
	n := int(r.resolve.Add(1))
	if len(r.bases) == 0 {
		return "", errors.New("no instances")
	}
	return r.bases[(n-1)%len(r.bases)], nil
}

// literalResolver returns a fixed string without validation, so tests can feed
// unusable addresses to the client.
// literalResolver 原样返回固定字符串且不做校验，
// 测试可借此把非法地址喂给客户端，覆盖错误处理路径。
type literalResolver string

func (r literalResolver) Resolve(context.Context, string) (string, error) {
	return string(r), nil
}

func okHandler(hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"9"}}}`)
	}
}

func TestEveryCallResolvesAgain(t *testing.T) {
	var hitsA, hitsB atomic.Int32
	a := httptest.NewServer(okHandler(&hitsA))
	defer a.Close()
	b := httptest.NewServer(okHandler(&hitsB))
	defer b.Close()

	resolver := &sequenceResolver{bases: []string{a.URL, b.URL}}
	c, err := client.New(client.Config{Service: "order", Resolver: resolver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 9}); err != nil {
			t.Fatalf("Call %d: %v", i, err)
		}
	}
	if got := resolver.resolve.Load(); got != 2 {
		t.Fatalf("resolve calls = %d, want one per call", got)
	}
	if hitsA.Load() != 1 || hitsB.Load() != 1 {
		t.Fatalf("hits = (%d, %d), want each instance used once", hitsA.Load(), hitsB.Load())
	}
}

func TestFixedBaseURLSkipsResolver(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(okHandler(&hits))
	defer server.Close()

	resolver := &sequenceResolver{}
	c, err := client.New(client.Config{
		BaseURL:  server.URL,
		Service:  "order",
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 9}); err != nil {
			t.Fatalf("Call %d: %v", i, err)
		}
	}
	if got := resolver.resolve.Load(); got != 0 {
		t.Fatalf("fixed BaseURL consulted the resolver %d times", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestServiceUnavailableRetryReResolves(t *testing.T) {
	var unavailableHits, liveHits atomic.Int32
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unavailableHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	live := httptest.NewServer(okHandler(&liveHits))
	defer live.Close()

	resolver := &sequenceResolver{bases: []string{unavailable.URL, live.URL}}
	c, err := client.New(client.Config{Service: "order", Resolver: resolver, Retry: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 9})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order.ID != 9 {
		t.Fatalf("id = %d", res.Order.ID)
	}
	if unavailableHits.Load() != 1 || liveHits.Load() != 1 {
		t.Fatalf("hits = (%d, %d), want one attempt per instance", unavailableHits.Load(), liveHits.Load())
	}
	if got := resolver.resolve.Load(); got != 2 {
		t.Fatalf("resolve calls = %d, want 2", got)
	}
}

func TestResolverRejectsUnusableAddresses(t *testing.T) {
	for name, base := range map[string]string{
		"empty":              "",
		"unsupported scheme": "ftp://order.example.com",
		"missing host":       "http://",
		"unparseable":        "://order",
	} {
		t.Run(name, func(t *testing.T) {
			c, err := client.New(client.Config{Service: "order", Resolver: literalResolver(base)})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 9})
			if err == nil {
				t.Fatal("Call accepted an unusable resolved address")
			}
			if !strings.Contains(err.Error(), "resolver returned") {
				t.Fatalf("error = %v, want a resolver address error", err)
			}
		})
	}
}

func TestRetryReResolvesAfterTransportError(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"9"}}}`)
	}))
	defer live.Close()

	resolver := &failoverResolver{dead: deadURL, live: live.URL}
	c, err := client.New(client.Config{Service: "order", Resolver: resolver, Retry: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := client.Call(context.Background(), c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 9})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Order.ID != 9 {
		t.Fatalf("id = %d", res.Order.ID)
	}
	if got := resolver.resolve.Load(); got != 2 {
		t.Fatalf("resolve calls = %d, want 2", got)
	}
}

var (
	sseRoute = endpoint.New[emptyReq, endpoint.NoBody](endpoint.Spec{
		OperationID:  "Stream",
		Method:       "GET",
		Path:         "/stream",
		ResponseKind: endpoint.KindSSE,
	})
	downloadRoute = endpoint.New[emptyReq, endpoint.NoBody](endpoint.Spec{
		OperationID:  "Download",
		Method:       "GET",
		Path:         "/download",
		ResponseKind: endpoint.KindDownload,
	})
)

type emptyReq struct{}

func TestStreamSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, ": heartbeat\n\nevent: tick\nid: 1\ndata: hello\ndata: world\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	events, errs, err := client.StreamSSE(context.Background(), c, sseRoute, &emptyReq{})
	if err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	var got []client.Event
	for event := range events {
		got = append(got, event)
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d: %+v", len(got), got)
	}
	if got[0].Event != "tick" || got[0].ID != "1" || string(got[0].Data) != "hello\nworld" {
		t.Fatalf("event = %+v", got[0])
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "a,b\n")
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	download, err := client.DownloadFile(context.Background(), c, downloadRoute, &emptyReq{})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	defer func() { _ = download.Body.Close() }()
	data, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "a,b\n" {
		t.Fatalf("body = %q", data)
	}
	if download.Filename != "report.csv" {
		t.Fatalf("filename = %q", download.Filename)
	}
}

func TestNewValidation(t *testing.T) {
	cases := []client.Config{
		{},
		{Service: "order"},
		{BaseURL: "ftp://example.com"},
		{BaseURL: "http://"},
		{BaseURL: "http://example.com", Retry: -1},
	}
	for i, cfg := range cases {
		if _, err := client.New(cfg); err == nil {
			t.Fatalf("case %d: expected an error", i)
		}
	}
}

func TestQueryEncoding(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"total":0,"list":[]}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	req := &orderrequest.ListOrdersRequest{UserID: 10001}
	req.Page = 2
	req.PageSize = 50
	req.Status = orderenum.OrderStatusPaid
	if _, err := client.Call(context.Background(), c, orderroute.ListOrders, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	for _, want := range []string{"page=2", "page_size=50", "user_id=10001", "status=paid"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q missing %q", query, want)
		}
	}
}

func TestUnwrapFormat(t *testing.T) {
	err := error(&client.APIError{Status: 404, Code: 40401, Message: "order not found"})
	if got := fmt.Sprint(err); got != "order not found (status=404, code=40401)" {
		t.Fatalf("message = %q", got)
	}
}

func isOrderNotFound(err error) bool {
	return client.CodeOf(err) == int(demoservice.ErrCodeOrderNotFound)
}
