package support

import "context"

// requestIDContextKey is the private key holding the request id in a request
// context.
// requestIDContextKey 是 request context 中存放请求 ID 的私有 key；使用私有类型
// 避免与其他包的 context 值冲突。
type requestIDContextKey struct{}

// ContextWithRequestID attaches the request id to ctx for business code and
// downstream calls.
// ContextWithRequestID 把请求 ID 附加到 ctx，供业务和下游调用读取；ctx 为 nil
// 时退化为 context.Background()，不会 panic。
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext returns the request id in ctx, or an empty string when
// absent.
// RequestIDFromContext 返回 ctx 中的请求 ID，不存在时返回空字符串；ctx 为 nil
// 时同样返回空字符串，调用方可据此判断是否需要自行生成 ID。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}
