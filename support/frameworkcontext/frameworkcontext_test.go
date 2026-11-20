package frameworkcontext_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/jwtauth"
)

// After the local identity is written, stale identity in the propagated headers
// must be replaced while non-identity headers stay unchanged.
// 写入本地身份后，透传头中的旧身份必须被替换，非身份头保持不变。
func TestWithUserAlignsPropagatedHeaders(t *testing.T) {
	ctx := headerpropagation.ContextWithHeaders(context.Background(), http.Header{
		headerpropagation.HeaderUserID:   {"stale-user"},
		headerpropagation.HeaderTenantID: {"stale-tenant"},
		headerpropagation.HeaderUserCode: {"stale-code"},
		headerpropagation.HeaderUserName: {"Stale"},
		headerpropagation.HeaderTraceID:  {"trace-1"},
	})

	ctx = frameworkcontext.WithUser(ctx, frameworkcontext.User{
		ID:       "user-1",
		TenantID: "tenant-1",
	})

	user, ok := frameworkcontext.UserFromContext(ctx)
	if !ok || user.ID != "user-1" || user.TenantID != "tenant-1" {
		t.Fatalf("user = %+v, ok = %v", user, ok)
	}
	if got := frameworkcontext.UserID(ctx); got != "user-1" {
		t.Fatalf("UserID = %q", got)
	}
	if got := frameworkcontext.TenantID(ctx); got != "tenant-1" {
		t.Fatalf("TenantID = %q", got)
	}
	if got := frameworkcontext.UserCode(ctx); got != "" {
		t.Fatalf("stale code leaked: %q", got)
	}
	if got := frameworkcontext.UserName(ctx); got != "" {
		t.Fatalf("stale name leaked: %q", got)
	}
	if got := frameworkcontext.TraceID(ctx); got != "trace-1" {
		t.Fatalf("trace id = %q", got)
	}

	headers := frameworkcontext.Headers(ctx)
	if headers.Get(headerpropagation.HeaderUserID) != "user-1" {
		t.Fatalf("propagated user = %q", headers.Get(headerpropagation.HeaderUserID))
	}
	if headers.Get(headerpropagation.HeaderTenantID) != "tenant-1" {
		t.Fatalf("propagated tenant = %q", headers.Get(headerpropagation.HeaderTenantID))
	}
}

// 没有本地认证时，所有语义 getter 回落到可信透传头。
func TestIdentityFallsBackToPropagatedHeaders(t *testing.T) {
	ctx := headerpropagation.ContextWithHeaders(context.Background(), http.Header{
		headerpropagation.HeaderUserID:    {"user-1"},
		headerpropagation.HeaderUserCode:  {"uc-1"},
		headerpropagation.HeaderTenantID:  {"tenant-1"},
		headerpropagation.HeaderUserName:  {"Alice"},
		headerpropagation.HeaderTraceID:   {"trace-1"},
		headerpropagation.HeaderClientIP:  {"198.51.100.4"},
		headerpropagation.HeaderUserAgent: {"gateway"},
	})

	if _, ok := frameworkcontext.UserFromContext(ctx); ok {
		t.Fatal("header-only request must not report a local user")
	}
	if got := frameworkcontext.UserID(ctx); got != "user-1" {
		t.Fatalf("UserID = %q", got)
	}
	if got := frameworkcontext.UserCode(ctx); got != "uc-1" {
		t.Fatalf("UserCode = %q", got)
	}
	if got := frameworkcontext.TenantID(ctx); got != "tenant-1" {
		t.Fatalf("TenantID = %q", got)
	}
	if got := frameworkcontext.UserName(ctx); got != "Alice" {
		t.Fatalf("UserName = %q", got)
	}
	if got := frameworkcontext.TraceID(ctx); got != "trace-1" {
		t.Fatalf("TraceID = %q", got)
	}
	if got := frameworkcontext.ClientIP(ctx); got != "198.51.100.4" {
		t.Fatalf("ClientIP = %q", got)
	}
	if got := frameworkcontext.UserAgent(ctx); got != "gateway" {
		t.Fatalf("UserAgent = %q", got)
	}
}

// A tenant left over from upstream must not be propagated downstream when the
// local identity carries no tenant.
// 本地身份没有租户时，上游残留的租户不能继续下发给下游。
func TestLocalIdentityClearsPropagatedTenant(t *testing.T) {
	ctx := headerpropagation.ContextWithHeaders(context.Background(), http.Header{
		headerpropagation.HeaderUserID:   {"upstream-user"},
		headerpropagation.HeaderTenantID: {"upstream-tenant"},
	})
	ctx = frameworkcontext.WithUser(ctx, frameworkcontext.User{ID: "local-user"})

	if got := frameworkcontext.UserID(ctx); got != "local-user" {
		t.Fatalf("UserID = %q", got)
	}
	if got := frameworkcontext.TenantID(ctx); got != "" {
		t.Fatalf("stale tenant leaked: %q", got)
	}
}

// ID 为空的身份被忽略，getter 仍返回透传值。
func TestUserWithoutIDIsIgnored(t *testing.T) {
	base := headerpropagation.ContextWithHeader(context.Background(), headerpropagation.HeaderUserID, "upstream-user")
	ctx := frameworkcontext.WithUser(base, frameworkcontext.User{ID: "  "})

	if _, ok := frameworkcontext.UserFromContext(ctx); ok {
		t.Fatal("empty user was stored")
	}
	if got := frameworkcontext.UserID(ctx); got != "upstream-user" {
		t.Fatalf("UserID = %q", got)
	}
}

// 快照和 claims 都是深拷贝，外部修改不会影响 context 中的身份。
func TestUserSnapshotIsIsolated(t *testing.T) {
	claims := map[string]any{"role": "admin"}
	ctx := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID:     "user-1",
		Claims: claims,
	})

	claims["role"] = "changed"
	user, _ := frameworkcontext.UserFromContext(ctx)
	if got := user.StringClaim("role"); got != "admin" {
		t.Fatalf("stored claim changed: %q", got)
	}

	user.Claims["role"] = "mutated"
	again, _ := frameworkcontext.UserFromContext(ctx)
	if got := again.StringClaim("role"); got != "admin" {
		t.Fatalf("context mutated through snapshot: %q", got)
	}
}

// claim 映射优先级为 user_id > uid > sub，其余字段按约定名称读取。
func TestUserFromClaims(t *testing.T) {
	user := frameworkcontext.UserFromClaims(&jwtauth.Claims{
		Subject: "subject-1",
		Raw: map[string]any{
			"user_id":   "user-1",
			"uid":       "uid-1",
			"tenant_id": "tenant-1",
			"user_code": "uc-1",
			"user_name": "Alice",
		},
	})
	if user.ID != "user-1" || user.TenantID != "tenant-1" || user.Code != "uc-1" || user.Name != "Alice" {
		t.Fatalf("user = %+v", user)
	}
	if user.Via != "jwt" {
		t.Fatalf("via = %q", user.Via)
	}

	fallback := frameworkcontext.UserFromClaims(&jwtauth.Claims{Subject: "subject-1", Raw: map[string]any{}})
	if fallback.ID != "subject-1" {
		t.Fatalf("subject fallback = %q", fallback.ID)
	}

	if user := frameworkcontext.UserFromClaims(nil); user.ID != "" {
		t.Fatalf("nil claims = %+v", user)
	}
}

// 数字型 claim（json.Number）也能映射为用户 ID 和租户。
func TestUserFromClaimsAcceptsNumericIDs(t *testing.T) {
	user := frameworkcontext.UserFromClaims(&jwtauth.Claims{
		Raw: map[string]any{
			"uid":       json.Number("1001"),
			"tenant_id": json.Number("9"),
		},
	})
	if user.ID != "1001" || user.TenantID != "9" {
		t.Fatalf("user = %+v", user)
	}
}

// TestUserIDUint64 covers the numeric accessor services use when their user
// table has a bigint primary key: a decimal identity converts, everything else
// reports "no usable id" instead of turning into 0 silently.
// TestUserIDUint64 覆盖 service 在用户表用 bigint 主键时使用的数值取值器：十进制身份能
// 转换，其余情况报告"没有可用 id"，而不是静默变成 0。
func TestUserIDUint64(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want uint64
		ok   bool
	}{
		{
			name: "local numeric id",
			ctx:  frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: "42"}),
			want: 42,
			ok:   true,
		},
		{
			name: "local non-numeric id",
			ctx:  frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: "auth0|abc"}),
		},
		{
			name: "empty identity",
			ctx:  context.Background(),
		},
		{
			name: "propagated numeric id",
			ctx: headerpropagation.ContextWithHeaders(context.Background(), http.Header{
				headerpropagation.HeaderUserID: {"7"},
			}),
			want: 7,
			ok:   true,
		},
		{
			name: "overflow",
			ctx:  frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: "18446744073709551616"}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := frameworkcontext.UserIDUint64(tc.ctx)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("UserIDUint64 = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestUserIDUint32 pins the 32-bit boundary: the largest uint32 converts, one
// past it reports false instead of wrapping to 0.
// TestUserIDUint32 钉住 32 位边界：uint32 最大值能转换，再大一点返回 false 而不是
// 回绕成 0。
func TestUserIDUint32(t *testing.T) {
	withID := func(id string) context.Context {
		return frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: id})
	}
	cases := []struct {
		name string
		ctx  context.Context
		want uint32
		ok   bool
	}{
		{name: "in range", ctx: withID("42"), want: 42, ok: true},
		{name: "max uint32", ctx: withID("4294967295"), want: 4294967295, ok: true},
		{name: "one past max", ctx: withID("4294967296")},
		{name: "non-numeric id", ctx: withID("auth0|abc")},
		{name: "empty identity", ctx: context.Background()},
		{
			name: "propagated numeric id",
			ctx: headerpropagation.ContextWithHeaders(context.Background(), http.Header{
				headerpropagation.HeaderUserID: {"7"},
			}),
			want: 7,
			ok:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := frameworkcontext.UserIDUint32(tc.ctx)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("UserIDUint32 = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
