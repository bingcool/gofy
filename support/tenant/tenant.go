// Package tenant adds row-level tenant isolation to GORM connections.
//
// A plugin installed on *gorm.DB appends `tenant_id = ?` to SELECT, UPDATE and
// DELETE for models that declare the column, and fills that column on INSERT.
// The tenant value comes from context.Context (frameworkcontext by default),
// never from a shared field: one plugin instance serves every request, so a
// value stored on the plugin would leak between tenants.
//
// This mirrors swoolefy's TenantLineInterceptor semantics — automatic filter,
// per-table opt-out, fail-closed on a missing tenant, explicit bypass — but
// uses GORM's structured clauses instead of rewriting SQL strings.
//
// 包 tenant 为 GORM 连接提供行级租户隔离。
//
// 安装在 *gorm.DB 上的插件会给声明了租户列的模型自动追加 `tenant_id = ?`
// （SELECT/UPDATE/DELETE）并在 INSERT 时填充该列。租户值来自 context.Context
// （默认取 frameworkcontext），绝不放在共享字段里：一个插件实例服务所有请求，
// 存在插件字段上的值会在租户之间串扰。
//
// 语义对齐 swoolefy 的 TenantLineInterceptor——自动过滤、按表跳过、缺租户失败关闭、
// 显式绕过——但用 GORM 的结构化 clause 实现，而不是改写 SQL 字符串。
package tenant

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/support/frameworkcontext"
)

// ErrTenantRequired is returned when a tenant-aware statement runs without a
// tenant ID in its context. The operation is rejected rather than executed
// unfiltered, so a missing identity can never expose another tenant's rows.
// ErrTenantRequired 在租户感知的语句缺少租户 ID 时返回。操作被拒绝，而不是不带
// 过滤地执行——身份缺失因此不可能暴露别的租户的数据。
var ErrTenantRequired = errors.New("tenant: tenant id is required")

// ErrPluginNil is returned by Instrument when db is nil.
// ErrPluginNil 在 Instrument 收到 nil db 时返回。
var ErrPluginNil = errors.New("tenant: gorm db is required")

// ErrTenantColumnRequired is returned when a Handler supplies an empty tenant
// column name. LookUpField("") finds nothing, so accepting it would make every
// model look "not tenant-aware" and silently disable isolation.
// ErrTenantColumnRequired 在 Handler 给出空租户列名时返回。LookUpField("") 找不到字段，
// 接受它会让每张表都显得"不带租户"，从而静默关掉隔离。
var ErrTenantColumnRequired = errors.New("tenant: handler column name is required")

// Without marks ctx so the next statement skips tenant rewriting. The scope is
// one statement chain, not the connection — connections are pooled and shared,
// so a bypass flag on the connection would leak to unrelated requests.
//
// This is a privileged operation: migrations, maintenance scripts and
// cross-tenant reports may use it; request handlers must not.
//
// Without 让 ctx 上的语句跳过租户改写。作用范围是一条语句链，不是连接——连接是
// 池化共享的，把绕过标记放连接上会泄漏给无关请求。
//
// 这是特权操作：迁移、运维脚本、跨租户报表可用；请求 handler 禁止调用。
func Without(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return frameworkcontext.WithTenantBypass(ctx)
}

// WithTenant puts an explicit tenant ID into ctx, overriding the identity
// source. Scripts, workers and tests use it instead of faking a JWT identity.
// An empty id removes the override.
// WithTenant 把显式租户 ID 放进 ctx，覆盖身份来源。脚本、worker 与测试用它，不必
// 伪造 JWT 身份。空 id 表示取消覆盖。
func WithTenant(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return frameworkcontext.WithTenantOverride(ctx, id)
}

// Bypassed reports whether ctx was marked by Without. It is exported so a second
// path that applies tenant isolation — support/sqlmodel for the SQL builder —
// reaches the same verdict as the plugin instead of guessing.
// Bypassed 报告 ctx 是否被 Without 标记过。它导出是因为另一条施加租户隔离的路径
// （SQL 构造器的 support/sqlmodel）必须与插件得出同一个结论，而不是自己猜。
func Bypassed(ctx context.Context) bool {
	return frameworkcontext.TenantBypassed(ctx)
}

// OverrideTenant returns the explicit tenant set by WithTenant, if any. The
// plugin checks it before the Handler, and so must every other path: a script
// that calls WithTenant intends it to win over whatever identity the context
// happens to carry.
// OverrideTenant 返回 WithTenant 设置的显式租户（若有）。插件在 Handler 之前检查它，
// 其它路径也必须如此：脚本调 WithTenant 就是要它压过 ctx 里碰巧带的身份。
func OverrideTenant(ctx context.Context) (string, bool) {
	return frameworkcontext.TenantOverride(ctx)
}
