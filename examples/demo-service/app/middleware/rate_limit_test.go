package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/endpoint"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/yourorg/demo-service/app/middleware"
)

// 数值直接取自契约：Limit/WindowSeconds 写 1/60，第二个请求就 429，
// 响应头带实际数值和 Retry-After。
func TestRateLimiterUsesContractNumbers(t *testing.T) {
	limiter := buildLimiter(t, spec(http.MethodGet, 1, endpoint.RateKeyUser))
	engine := newEngine(limiter, http.MethodGet, "/orders/:id")

	first := call(engine, http.MethodGet, "/orders/1", asUser("10001"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	if got := first.Header().Get("X-RateLimit-Limit"); got != "1" {
		t.Fatalf("X-RateLimit-Limit = %q, want 1", got)
	}

	second := call(engine, http.MethodGet, "/orders/1", asUser("10001"))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.Code)
	}
	if got := second.Header().Get("Retry-After"); got == "" {
		t.Fatal("429 response is missing Retry-After")
	}
}

// RateKeyUser 按已认证用户分桶：同一用户第二次超限，另一个用户不受影响。
func TestRateLimiterKeyByUser(t *testing.T) {
	limiter := buildLimiter(t, spec(http.MethodGet, 1, endpoint.RateKeyUser))
	engine := newEngine(limiter, http.MethodGet, "/orders")

	if rec := call(engine, http.MethodGet, "/orders", asUser("10001")); rec.Code != http.StatusOK {
		t.Fatalf("first user status = %d", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", asUser("10001")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request of the same user = %d, want 429", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", asUser("10002")); rec.Code != http.StatusOK {
		t.Fatalf("other user status = %d, want 200", rec.Code)
	}
}

// RateKeyAPIKey 按 X-API-Key 分桶；没有 API Key 时退回客户端 IP，
// 不同 IP 的匿名请求不会互相影响。
func TestRateLimiterKeyByAPIKey(t *testing.T) {
	limiter := buildLimiter(t, spec(http.MethodGet, 1, endpoint.RateKeyAPIKey))
	engine := newEngine(limiter, http.MethodGet, "/orders")

	if rec := call(engine, http.MethodGet, "/orders", withAPIKey("key-a")); rec.Code != http.StatusOK {
		t.Fatalf("first key-a status = %d", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", withAPIKey("key-a")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second key-a status = %d, want 429", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", withAPIKey("key-b")); rec.Code != http.StatusOK {
		t.Fatalf("key-b status = %d, want 200", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", fromIP("10.0.0.1")); rec.Code != http.StatusOK {
		t.Fatalf("anonymous ip 1 status = %d, want 200", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", fromIP("10.0.0.2")); rec.Code != http.StatusOK {
		t.Fatalf("anonymous ip 2 status = %d, want 200", rec.Code)
	}
}

// RateKeyRoute 不区分调用方：不同用户、不同 IP 也共享同一份额度。
func TestRateLimiterKeyByRoute(t *testing.T) {
	limiter := buildLimiter(t, spec(http.MethodGet, 1, endpoint.RateKeyRoute))
	engine := newEngine(limiter, http.MethodGet, "/orders")

	if rec := call(engine, http.MethodGet, "/orders", asUser("10001"), fromIP("10.0.0.1")); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders", asUser("10002"), fromIP("10.0.0.2")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rec.Code)
	}
}

// RateKeyIP（以及未声明 Key 的缺省情况）按客户端 IP 分桶。
func TestRateLimiterKeyByIP(t *testing.T) {
	for _, key := range []endpoint.RateKey{endpoint.RateKeyIP, ""} {
		limiter := buildLimiter(t, spec(http.MethodGet, 1, key))
		engine := newEngine(limiter, http.MethodGet, "/orders")

		if rec := call(engine, http.MethodGet, "/orders", fromIP("10.0.0.1")); rec.Code != http.StatusOK {
			t.Fatalf("key %q ip 1 first status = %d", key, rec.Code)
		}
		if rec := call(engine, http.MethodGet, "/orders", fromIP("10.0.0.2")); rec.Code != http.StatusOK {
			t.Fatalf("key %q ip 2 status = %d, want 200", key, rec.Code)
		}
		if rec := call(engine, http.MethodGet, "/orders", fromIP("10.0.0.1")); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("key %q ip 1 second status = %d, want 429", key, rec.Code)
		}
	}
}

// Bucket keys carry method + route, so exhausting one route's quota does not
// affect another route.
// 桶键带方法 + 路由：一条路由耗尽额度不影响另一条路由。
func TestRateLimiterBucketsPerRoute(t *testing.T) {
	limiter := buildLimiter(t, spec(http.MethodGet, 1, endpoint.RateKeyRoute))
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	engine.GET("/orders", limiter, ok)
	engine.GET("/products", limiter, ok)

	if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusOK {
		t.Fatalf("/orders first status = %d", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/products"); rec.Code != http.StatusOK {
		t.Fatalf("/products status = %d, want 200", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("/orders second status = %d, want 429", rec.Code)
	}
}

// 计数实例来自契约：RedisAlias 原样传给解析函数，未声明时用默认别名 ratelimit。
func TestRateLimiterResolvesDeclaredRedisAlias(t *testing.T) {
	cases := map[string]struct {
		alias string
		want  string
	}{
		"declared": {alias: "session", want: "session"},
		"default":  {alias: "", want: "ratelimit"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := testClient(t)
			var resolved []string
			limiter := middleware.RateLimiter{RedisClient: func(_ context.Context, alias string) (*goredis.Client, error) {
				resolved = append(resolved, alias)
				return client, nil
			}}
			engine := newEngine(limiter.Callback()(endpoint.Spec{
				OperationID: "Op",
				Method:      http.MethodGet,
				Path:        "/orders",
				RateLimit:   &endpoint.RateLimit{Limit: 1, WindowSeconds: 60, RedisAlias: tc.alias},
			}), http.MethodGet, "/orders")

			if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if len(resolved) != 1 || resolved[0] != tc.want {
				t.Fatalf("resolved aliases = %v, want [%s]", resolved, tc.want)
			}
		})
	}
}

// 契约没有声明 Spec.RateLimit 的路由不挂限流（adapter 也不会调用回调）。
func TestRateLimiterWithoutContractDeclaration(t *testing.T) {
	callback := newRateLimiter(t).Callback()
	if limiter := callback(endpoint.Spec{OperationID: "GetConfig", Method: http.MethodGet}); limiter != nil {
		t.Fatal("route without RateLimit got a limiter")
	}
}

// 契约数值非法（Limit 或 WindowSeconds 非正）时返回 503，而不是静默放行。
func TestRateLimiterFailsClosedOnInvalidContract(t *testing.T) {
	cases := map[string]*endpoint.RateLimit{
		"zero limit":      {WindowSeconds: 60, Key: endpoint.RateKeyIP},
		"zero window":     {Limit: 1, Key: endpoint.RateKeyIP},
		"negative limit":  {Limit: -1, WindowSeconds: 60, Key: endpoint.RateKeyIP},
		"negative window": {Limit: 1, WindowSeconds: -60, Key: endpoint.RateKeyIP},
	}
	for name, rateLimit := range cases {
		t.Run(name, func(t *testing.T) {
			limiter := buildLimiter(t, endpoint.Spec{OperationID: "Other", Method: http.MethodGet, RateLimit: rateLimit})
			engine := newEngine(limiter, http.MethodGet, "/other")
			if rec := call(engine, http.MethodGet, "/other"); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
		})
	}
}

// Redis 解析失败时返回 503；失败不缓存，连接恢复后下一个请求正常限流。
func TestRateLimiterFailsClosedUntilRedisResolves(t *testing.T) {
	client := testClient(t)
	unavailable := true
	limiter := middleware.RateLimiter{RedisClient: func(context.Context, string) (*goredis.Client, error) {
		if unavailable {
			return nil, errors.New("redis instance is not configured")
		}
		return client, nil
	}}
	engine := newEngine(limiter.Callback()(spec(http.MethodGet, 1, endpoint.RateKeyIP)), http.MethodGet, "/orders")

	if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	unavailable = false
	if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusOK {
		t.Fatalf("status after recovery = %d, want 200", rec.Code)
	}
	if rec := call(engine, http.MethodGet, "/orders"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request after recovery = %d, want 429", rec.Code)
	}
}

// spec 用契约形式描述一条路由的限流声明，窗口固定 60 秒。
func spec(method string, limit int, key endpoint.RateKey) endpoint.Spec {
	return endpoint.Spec{
		OperationID: "Op",
		Method:      method,
		Path:        "/orders",
		RateLimit:   &endpoint.RateLimit{Limit: limit, WindowSeconds: 60, Key: key},
	}
}

// buildLimiter 用契约声明构造一条路由的限流中间件，要求回调返回非 nil。
func buildLimiter(t *testing.T, spec endpoint.Spec) support.Middleware {
	t.Helper()
	limiter := newRateLimiter(t).Callback()(spec)
	if limiter == nil {
		t.Fatal("callback returned no limiter")
	}
	return limiter
}

// newRateLimiter 返回注入 miniredis 的限流器，与真实装配根一样在首次请求时
// 才解析客户端。
func newRateLimiter(t *testing.T) middleware.RateLimiter {
	t.Helper()
	client := testClient(t)
	return middleware.RateLimiter{RedisClient: func(context.Context, string) (*goredis.Client, error) {
		return client, nil
	}}
}

// testClient 返回一个连到本测试 miniredis 的客户端，随测试结束关闭。
func testClient(t *testing.T) *goredis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newEngine 注册一条带限流的测试路由。
func newEngine(limiter support.Middleware, method, path string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Handle(method, path, limiter, func(c *gin.Context) { c.Status(http.StatusOK) })
	return engine
}

// call 发一次请求，options 用于构造身份、来源 IP 或请求头。
func call(engine *gin.Engine, method, path string, options ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for _, option := range options {
		option(req)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func asUser(id string) func(*http.Request) {
	return func(req *http.Request) {
		*req = *req.WithContext(frameworkcontext.WithUser(req.Context(), frameworkcontext.User{ID: id}))
	}
}

func fromIP(ip string) func(*http.Request) {
	return func(req *http.Request) { req.RemoteAddr = ip + ":1234" }
}

func withAPIKey(key string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set("X-API-Key", key) }
}
