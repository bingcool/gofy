package frameworkcontext_test

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/headerpropagation"
)

// identityContext returns a ctx carrying a locally authenticated user.
// identityContext 返回携带本地认证身份的 ctx。
func identityContext(tenantID string) context.Context {
	return frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID: "u-1", TenantID: tenantID,
	})
}

// TestTenantOverrideNormalization pins the contract both consumer paths rely
// on: the override is trimmed on read, and an empty or blank value is not an
// override at all — it falls back to the identity instead of masking it.
// TestTenantOverrideNormalization 固定两条消费路径共同依赖的约定：override 在读取时
// 去空白；空或纯空白值根本不算覆盖——它回退到身份来源，而不是把身份盖住。
func TestTenantOverrideNormalization(t *testing.T) {
	cases := []struct {
		name   string
		ctx    context.Context
		wantID string
		wantOK bool
	}{
		{name: "nil context", ctx: nil},
		{name: "no override", ctx: identityContext("a")},
		{name: "empty override", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), "")},
		{name: "blank override", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), "   ")},
		{name: "padded override", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), " b "), wantID: "b", wantOK: true},
		{name: "override without identity", ctx: frameworkcontext.WithTenantOverride(context.Background(), "b"), wantID: "b", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := frameworkcontext.TenantOverride(tc.ctx)
			if got != tc.wantID || ok != tc.wantOK {
				t.Fatalf("TenantOverride = %q, %v; want %q, %v", got, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

// TestEffectiveTenant covers the rule the builder path applies: override first,
// then the identity, one trim, and "" meaning "no tenant". The propagated-header
// case is here because a programmatic ContextWithHeader does not trim its value
// (only CaptureIncoming does), so the padding must be removed at read time.
// TestEffectiveTenant 覆盖构造器路径使用的规则：override 优先，其次身份，统一去除空白，
// "" 表示没有租户。透传头那一例是因为程序化调用 ContextWithHeader 不会去空白（只有
// CaptureIncoming 会），所以空白必须在读取时去掉。
func TestEffectiveTenant(t *testing.T) {
	propagated := headerpropagation.ContextWithHeader(context.Background(), headerpropagation.HeaderTenantID, " a ")
	cases := []struct {
		name   string
		ctx    context.Context
		wantID string
		wantOK bool
	}{
		{name: "nil context", ctx: nil},
		{name: "identity only", ctx: identityContext("a"), wantID: "a", wantOK: true},
		{name: "propagated header padded", ctx: propagated, wantID: "a", wantOK: true},
		{name: "empty override falls back to identity", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), ""), wantID: "a", wantOK: true},
		{name: "blank override falls back to identity", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), " \t "), wantID: "a", wantOK: true},
		{name: "padded override wins trimmed", ctx: frameworkcontext.WithTenantOverride(identityContext("a"), " b "), wantID: "b", wantOK: true},
		{name: "no identity and no override", ctx: context.Background()},
		{name: "no identity and empty override", ctx: frameworkcontext.WithTenantOverride(context.Background(), "")},
		{name: "no identity and blank override", ctx: frameworkcontext.WithTenantOverride(context.Background(), "   ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := frameworkcontext.EffectiveTenant(tc.ctx)
			if got != tc.wantID || ok != tc.wantOK {
				t.Fatalf("EffectiveTenant = %q, %v; want %q, %v", got, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}
