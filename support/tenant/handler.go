package tenant

import (
	"context"
	"strings"

	"github.com/bingcool/gofy/support/frameworkcontext"
)

// DefaultColumn is the tenant column name used by ContextHandler.
// DefaultColumn 是 ContextHandler 使用的租户列名。
const DefaultColumn = "tenant_id"

// Handler supplies tenant metadata. Implementations must be stateless: the
// plugin is shared by all requests, so a tenant value must come from ctx and
// must never be cached in a Handler field.
// Handler 提供租户元信息。实现必须无状态：插件被所有请求共享，租户值只能来自 ctx，
// 绝不能缓存在 Handler 的字段里。
type Handler interface {
	// TenantID returns the current tenant. An empty result means "no tenant"
	// and triggers fail-closed on a tenant-aware statement.
	// TenantID 返回当前租户。空结果表示"无租户"，在租户感知的语句上触发失败关闭。
	TenantID(ctx context.Context) string
	// Column is the tenant column name.
	// Column 是租户列名。
	Column() string
	// IgnoreTable decides whether to skip a table when GORM cannot parse a
	// schema for it (Table("x") without a model, raw SQL). Returning false
	// keeps fail-closed behaviour for those statements too.
	// IgnoreTable 在 GORM 无法解析 schema 时（无 model 的 Table("x")、原生 SQL）
	// 决定是否跳过该表。返回 false 会让这类语句同样保持失败关闭。
	IgnoreTable(table string) bool
}

// ContextHandler reads the tenant from frameworkcontext, which merges the
// locally authenticated identity (JWT) with trusted upstream headers such as
// X-Tenant-Id. This is the default and keeps the SQL layer on the same identity
// source as the rest of the framework.
// ContextHandler 从 frameworkcontext 读取租户；后者合并本地认证身份（JWT）与可信
// 上游透传头（如 X-Tenant-Id）。它是默认实现，使 SQL 层与框架其余部分共用同一身份来源。
type ContextHandler struct{}

// TenantID implements Handler.
func (ContextHandler) TenantID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return frameworkcontext.TenantID(ctx)
}

// Column implements Handler.
func (ContextHandler) Column() string { return DefaultColumn }

// IgnoreTable implements Handler. It never skips: without schema information
// it is safer to require a tenant than to silently return all rows.
// IgnoreTable 实现 Handler。它从不跳过：拿不到 schema 时，要求租户比静默返回全部行更安全。
func (ContextHandler) IgnoreTable(string) bool { return false }

// FuncHandler adapts plain functions into a Handler, so an application can
// change the column or the identity source without defining a type.
// FuncHandler 把普通函数适配成 Handler，应用想改列名或身份来源时无需定义新类型。
type FuncHandler struct {
	TenantIDFunc  func(context.Context) string
	ColumnName    string
	IgnoreTableFn func(string) bool
}

// TenantID implements Handler.
func (h FuncHandler) TenantID(ctx context.Context) string {
	if h.TenantIDFunc == nil {
		return ContextHandler{}.TenantID(ctx)
	}
	return h.TenantIDFunc(ctx)
}

// Column implements Handler.
func (h FuncHandler) Column() string {
	col := strings.TrimSpace(h.ColumnName)
	if col == "" {
		return DefaultColumn
	}
	return col
}

// IgnoreTable implements Handler.
func (h FuncHandler) IgnoreTable(table string) bool {
	if h.IgnoreTableFn == nil {
		return false
	}
	return h.IgnoreTableFn(table)
}
