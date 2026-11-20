package frameworkcontext

import "context"

// The tenant override and bypass markers live here, not in support/tenant,
// because support/sqlbuilder must read the same identity decision without
// importing gorm. support/tenant re-exports them as WithTenant / OverrideTenant
// and Without / Bypassed, so both the GORM plugin and the SQL builder resolve a
// context to the same tenant even when the identity sources disagree.
//
// 租户覆盖与绕过标记放在这里而不是 support/tenant，是因为 support/sqlbuilder 必须读取同一份
// 身份判定，又不能依赖 gorm。support/tenant 再把它们转出为 WithTenant / OverrideTenant 与
// Without / Bypassed，于是 GORM 插件与 SQL 构造器对同一个 ctx 得到同一个租户——即使身份来源
// 之间本身有冲突。
type (
	tenantBypassKey   struct{}
	tenantOverrideKey struct{}
)

// WithTenantBypass marks ctx so tenant-aware paths skip rewriting. It is a
// privileged operation: migrations, maintenance and cross-tenant reports.
// WithTenantBypass 标记 ctx，让租户感知路径跳过改写。这是特权操作，用于迁移、运维与跨租户报表。
func WithTenantBypass(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tenantBypassKey{}, true)
}

// TenantBypassed reports whether ctx was marked by WithTenantBypass.
// TenantBypassed 报告 ctx 是否被 WithTenantBypass 标记过。
func TenantBypassed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(tenantBypassKey{}).(bool)
	return v
}

// WithTenantOverride puts an explicit tenant into ctx. It wins over the
// identity returned by TenantID. An empty id removes the override.
// WithTenantOverride 把显式租户放进 ctx，优先级高于 TenantID 返回的身份。空 id 表示取消覆盖。
func WithTenantOverride(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tenantOverrideKey{}, id)
}

// TenantOverride returns the explicit tenant and whether it was set.
// TenantOverride 返回显式租户以及是否被设置过。
func TenantOverride(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(tenantOverrideKey{}).(string)
	return v, ok
}
