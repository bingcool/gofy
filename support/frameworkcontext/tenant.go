package frameworkcontext

import (
	"context"
	"strings"
)

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
// identity returned by TenantID. An empty or blank id removes the override.
// WithTenantOverride 把显式租户放进 ctx，优先级高于 TenantID 返回的身份。空或纯空白
// 的 id 表示取消覆盖。
func WithTenantOverride(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tenantOverrideKey{}, id)
}

// TenantOverride returns the explicit tenant and whether one is in effect. An
// empty or blank value is not an override: it falls back to TenantID like a
// context that never carried one, which is what "an empty id removes the
// override" has to mean — context.Context cannot delete a key.
// TenantOverride 返回显式租户以及是否生效。空或纯空白值不算覆盖：它会像从未设置过一样
// 回退到 TenantID，这也正是"空 id 取消覆盖"唯一能实现的语义——context.Context 无法删除键。
func TenantOverride(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(tenantOverrideKey{}).(string)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// EffectiveTenant is the identity rule every tenant-aware path applies: an
// explicit override wins, an empty or blank override falls back to TenantID,
// and the result is trimmed once here so no consumer normalizes on its own.
// ok=false means the request has no tenant. support/sqlbuilder reads identity
// directly and uses this function; the Handler-based paths go through
// tenant.ResolveTenant, which applies the same rule to a Handler.
// EffectiveTenant 是所有租户感知路径统一使用的身份规则：显式 override 优先，空或纯空白
// override 回退到 TenantID，结果在这里统一 TrimSpace 一次，消费者不再各自归一化。
// ok=false 表示请求没有租户。support/sqlbuilder 直接读身份，用这个函数；基于 Handler 的
// 路径走 tenant.ResolveTenant，它把同一规则施加到 Handler 上。
func EffectiveTenant(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if id, ok := TenantOverride(ctx); ok {
		return id, true
	}
	id := strings.TrimSpace(TenantID(ctx))
	return id, id != ""
}
