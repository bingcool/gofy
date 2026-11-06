// Package frameworkcontext 提供请求身份的统一读取入口。
//
// 它合并两类身份来源：
//
//   - 本地认证身份：由 JWT 中间件或自定义认证写入；
//   - 可信上游服务透传的白名单身份头。
//
// 本地身份优先，透传头作为兜底，与 swoolefy 的 FrameworkContext 语义一致。
// 两类身份都存放在 context.Context 中，因此 handler、Service 层和下游客户端
// 读取到的始终是同一份身份数据。
//
// body 或 query 中名为 user_id 的参数不属于身份来源，请使用本包的 getter。
package frameworkcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/jwtauth"
)

type userKey struct{}

// User 是本地认证通过后的身份快照。WithUser 保存的是副本，
// 之后修改原始值不会影响已经写入请求 context 的身份。
type User struct {
	// ID 是用户主键；ID 为空的 User 不会被写入 context。
	ID string
	// TenantID 是请求所属租户；空字符串表示没有租户。
	TenantID string
	// Code 是业务层的用户编号。
	Code string
	// Name 是用户显示名称。
	Name string
	// Via 记录凭证通道，例如 "jwt"。
	Via string
	// Claims 保存已校验的 token 原始载荷，供业务读取自定义声明。
	Claims map[string]any
}

// Claim 返回一个原始 claim 值。
func (u User) Claim(name string) (any, bool) {
	if u.Claims == nil {
		return nil, false
	}
	v, ok := u.Claims[name]
	return v, ok
}

// StringClaim 返回字符串 claim；claim 不存在或类型不是字符串时返回 ""。
func (u User) StringClaim(name string) string {
	v, _ := u.Claim(name)
	s, _ := v.(string)
	return s
}

func (u User) clone() User {
	out := u
	if u.Claims != nil {
		out.Claims = make(map[string]any, len(u.Claims))
		for key, value := range u.Claims {
			out.Claims[key] = value
		}
	}
	return out
}

// WithUser 写入本地认证身份，并把透传上下文中的身份头对齐到该身份，
// 使下游服务收到相同的用户和租户。本地身份没有提供的身份字段会被清除，
// 避免把上游请求中捕获的残留值继续下发。
//
// ID 为空的 User 视为无效身份，直接返回原 ctx。
func WithUser(ctx context.Context, user User) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	user.ID = strings.TrimSpace(user.ID)
	if user.ID == "" {
		return ctx
	}
	user.TenantID = strings.TrimSpace(user.TenantID)
	user.Code = strings.TrimSpace(user.Code)
	user.Name = strings.TrimSpace(user.Name)
	if user.Via == "" {
		user.Via = "auth"
	}

	ctx = context.WithValue(ctx, userKey{}, user.clone())

	for _, name := range []string{
		headerpropagation.HeaderUserID,
		headerpropagation.HeaderUserCode,
		headerpropagation.HeaderTenantID,
		headerpropagation.HeaderUserName,
	} {
		ctx = headerpropagation.ContextWithoutHeader(ctx, name)
	}
	ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderUserID, user.ID)
	if user.TenantID != "" {
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderTenantID, user.TenantID)
	}
	if user.Code != "" {
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderUserCode, user.Code)
	}
	if user.Name != "" {
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderUserName, user.Name)
	}
	return ctx
}

// UserFromContext 返回本地认证身份。只收到上游透传头的下游服务会得到 false；
// 这类场景请使用 UserID 等语义 getter，它们会自动回落到可信透传头。
func UserFromContext(ctx context.Context) (User, bool) {
	if ctx == nil {
		return User{}, false
	}
	user, ok := ctx.Value(userKey{}).(User)
	if !ok || user.ID == "" {
		return User{}, false
	}
	return user.clone(), true
}

// UserFromClaims 把已校验的 JWT claims 转换为 User。
//
// ID 依次取 "user_id"、"uid" 和注册主体 "sub"；租户、编号、名称分别取
// "tenant_id"、"user_code"、"user_name"（缺失时回落到 "name"）。
// 字符串和数字类型的 claim 都支持，其余业务声明仍可通过 Claims 读取。
func UserFromClaims(claims *jwtauth.Claims) User {
	if claims == nil {
		return User{}
	}
	id := firstNonEmpty(
		claimString(claims, "user_id"),
		claimString(claims, "uid"),
		claims.Subject,
	)
	return User{
		ID:       id,
		TenantID: claimString(claims, "tenant_id"),
		Code:     claimString(claims, "user_code"),
		Name:     firstNonEmpty(claimString(claims, "user_name"), claimString(claims, "name")),
		Via:      "jwt",
		Claims:   claims.Raw,
	}
}

// claimString 把 claim 转成文本。token 中的用户 ID 常用数字表示，
// jwtauth 会把它们解码成 json.Number，这里一并兼容。
func claimString(claims *jwtauth.Claims, name string) string {
	v, ok := claims.Get(name)
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// UserID 优先返回本地认证用户 ID；没有本地身份时返回可信上游透传的值，
// 两者都不存在时返回 ""。
func UserID(ctx context.Context) string {
	if user, ok := UserFromContext(ctx); ok {
		return user.ID
	}
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderUserID)
}

// TenantID 优先返回本地身份的租户 ID，其次返回透传值。
func TenantID(ctx context.Context) string {
	if user, ok := UserFromContext(ctx); ok {
		return user.TenantID
	}
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTenantID)
}

// UserCode 优先返回本地身份的用户编号，其次返回透传值。
func UserCode(ctx context.Context) string {
	if user, ok := UserFromContext(ctx); ok {
		return user.Code
	}
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderUserCode)
}

// UserName 优先返回本地身份的用户名，其次返回透传值。
func UserName(ctx context.Context) string {
	if user, ok := UserFromContext(ctx); ok {
		return user.Name
	}
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderUserName)
}

// TraceID 返回透传上下文中的 trace id。
func TraceID(ctx context.Context) string {
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTraceID)
}

// ClientIP 返回透传上下文中的原始客户端地址。
func ClientIP(ctx context.Context) string {
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderClientIP)
}

// UserAgent 返回透传上下文中的 SDK 或网关标识。
func UserAgent(ctx context.Context) string {
	return headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderUserAgent)
}

// Header 返回一个透传请求头的值。
func Header(ctx context.Context, name string) string {
	return headerpropagation.HeaderFromContext(ctx, name)
}

// Headers 返回全部透传请求头的副本，可安全修改。
func Headers(ctx context.Context) http.Header {
	return headerpropagation.HeadersFromContext(ctx)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
