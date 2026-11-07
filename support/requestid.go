package support

import "context"

// requestIDContextKey 是 request context 中存放请求 ID 的私有 key。
type requestIDContextKey struct{}

// ContextWithRequestID 把请求 ID 附加到 context，供业务和下游调用读取。
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext 返回 context 中的请求 ID；不存在时返回空字符串。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}
