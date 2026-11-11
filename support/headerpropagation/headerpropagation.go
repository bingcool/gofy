// Package headerpropagation 采集入口请求中的白名单 Header 并写入 request context，
// 使下游服务调用可以把调用方身份和 trace id 继续传给下一个服务。
//
// 只有白名单内的 Header 会被透传；身份头仅当直连对端是配置的可信代理时才采信，
// 公网客户端无法通过自行发送 X-Tenant-Id 冒充其他租户。
package headerpropagation

import (
	"context"
	"net"
	"net/http"
	"strings"
)

const (
	// HeaderTraceID 是跨服务调用的分布式 trace id。
	HeaderTraceID = "X-Trace-Id"
	// HeaderUserID 是已认证用户 ID。
	HeaderUserID = "X-User-Id"
	// HeaderUserCode 是业务层的用户编号。
	HeaderUserCode = "X-User-Code"
	// HeaderTenantID 是请求所属租户 ID。
	HeaderTenantID = "X-Tenant-Id"
	// HeaderUserName 是已认证用户的显示名称。
	HeaderUserName = "X-User-Name"
	// HeaderClientIP 是原始调用方地址。
	HeaderClientIP = "X-Client-Ip"
	// HeaderUserAgent 标记发起请求的 SDK 或网关。
	HeaderUserAgent = "X-User-Agent"
)

// ContextKey 是存放透传 Header 的 context key。
//
// gofy-interface-service/apicore/client 读取同一个 key，这样客户端无需 import 服务端
// 即可转发其采集到的 Header。该字面量必须在两个仓库中保持一致；修改会破坏
// 跨部署边界的透传，因此应把它当作 API 契约对待。
const ContextKey = "gofy.header_propagation.headers"

// propagatedHeaders 是允许离开本进程的全部 Header 集合。
var propagatedHeaders = []string{
	HeaderTraceID,
	HeaderUserID,
	HeaderUserCode,
	HeaderTenantID,
	HeaderUserName,
	HeaderClientIP,
	HeaderUserAgent,
}

// identityHeaders 只信任可信代理写入的值；直连调用方携带的同名头会被忽略。
var identityHeaders = []string{
	HeaderUserID,
	HeaderUserCode,
	HeaderTenantID,
	HeaderUserName,
}

var allowed = func() map[string]bool {
	set := make(map[string]bool, len(propagatedHeaders))
	for _, name := range propagatedHeaders {
		set[name] = true
	}
	return set
}()

// CaptureIncoming 返回需要继续传给下游服务的白名单 Header。
// 请求本身没有 trace id 时，使用 traceID（通常是 req_id）兜底。
//
// remoteAddr 不是可信代理时，身份头会被丢弃，HeaderClientIP 改写为直连对端地址，
// 防止调用方冒充其他用户或租户。
func CaptureIncoming(in http.Header, traceID string, trustedProxies []string, remoteAddr string) http.Header {
	out := make(http.Header, len(propagatedHeaders))
	for _, name := range propagatedHeaders {
		if value := strings.TrimSpace(in.Get(name)); value != "" {
			out.Set(name, value)
		}
	}

	if strings.TrimSpace(traceID) != "" && out.Get(HeaderTraceID) == "" {
		out.Set(HeaderTraceID, strings.TrimSpace(traceID))
	}

	if !IsTrustedProxy(remoteAddr, trustedProxies) {
		for _, name := range identityHeaders {
			out.Del(name)
		}
		if ip := remoteIP(remoteAddr); ip != "" {
			out.Set(HeaderClientIP, ip)
		}
	}
	return out
}

// IsTrustedProxy 判断 remoteAddr 是否属于 trustedProxies。
// 条目可以是 IP 地址或 CIDR 网段；空列表表示不信任任何代理。
func IsTrustedProxy(remoteAddr string, trustedProxies []string) bool {
	ip := net.ParseIP(remoteIP(remoteAddr))
	if ip == nil {
		return false
	}
	for _, entry := range trustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if candidate := net.ParseIP(entry); candidate != nil && candidate.Equal(ip) {
			return true
		}
	}
	return false
}

// ContextWithHeaders 把 headers 合并到 ctx 中已有的透传 Header 并返回派生 context，
// 非白名单 key 会被忽略。认证完成后需要补充身份头时，应使用这个函数。
func ContextWithHeaders(ctx context.Context, headers http.Header) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	merged := HeadersFromContext(ctx)
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if !allowed[canonical] {
			continue
		}
		merged[canonical] = append([]string(nil), values...)
	}
	return context.WithValue(ctx, ContextKey, merged)
}

// ContextWithHeader 把一个白名单 Header 合并到 ctx。
func ContextWithHeader(ctx context.Context, name, value string) context.Context {
	return ContextWithHeaders(ctx, http.Header{name: {value}})
}

// ContextWithoutHeader 从 ctx 中删除一个透传 Header。
// 本地认证身份替换可信代理传来的旧值时使用。
func ContextWithoutHeader(ctx context.Context, name string) context.Context {
	if ctx == nil {
		return ctx
	}
	headers := HeadersFromContext(ctx)
	canonical := http.CanonicalHeaderKey(name)
	if !allowed[canonical] {
		return ctx
	}
	if _, ok := headers[canonical]; !ok {
		return ctx
	}
	delete(headers, canonical)
	return context.WithValue(ctx, ContextKey, headers)
}

// HeadersFromContext 返回 ctx 中透传 Header 的副本；
// ctx 没有任何透传头时返回空 Header。
func HeadersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return make(http.Header)
	}
	stored, _ := ctx.Value(ContextKey).(http.Header)
	out := make(http.Header, len(stored))
	for name, values := range stored {
		canonical := http.CanonicalHeaderKey(name)
		if !allowed[canonical] {
			continue
		}
		out[canonical] = append([]string(nil), values...)
	}
	return out
}

// HeaderFromContext 返回一个透传 Header 的值，不存在时返回 ""。
func HeaderFromContext(ctx context.Context, name string) string {
	return HeadersFromContext(ctx).Get(name)
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(addr), "[]")
	}
	return strings.TrimSpace(host)
}
