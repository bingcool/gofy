package middleware

import (
	"regexp"
	"strings"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// NameRequestID 是 RequestID 在契约路由里使用的中间件别名。
const NameRequestID = "request-id"

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// RequestID 读取（或生成）请求 ID，并保证同一个 ID 能传到下游服务：
//
//   - 上游请求带合法 X-Trace-ID 时直接复用；
//   - 没有或格式非法时生成 UUID；
//   - 写入 gin context（响应包络 req_id）、请求/响应头 X-Trace-ID；
//   - 写入 request context 的 support.ContextWithRequestID，业务可直接读取；
//   - 同时合并进 headerpropagation 的 X-Trace-Id，gofy-interface-service 的
//     客户端会自动把该值带给下游，不需要再手工调用 ContextWithRequestID。
func RequestID(header string) gin.HandlerFunc {
	header = strings.TrimSpace(header)
	if header == "" {
		header = support.DefaultRequestIDHeader
	}
	return func(c *gin.Context) {
		// 上游优先：只要 X-Trace-ID 合法就沿用它；即使业务配置了自定义头，
		// 也不会覆盖上游已经建立的 trace 链路。都没有时才生成新的。
		id := strings.TrimSpace(c.GetHeader(support.DefaultRequestIDHeader))
		if !requestIDPattern.MatchString(id) {
			id = strings.TrimSpace(c.GetHeader(header))
		}
		if !requestIDPattern.MatchString(id) {
			id = uuid.NewString()
		}

		c.Set(support.RequestIDKey, id)
		c.Request.Header.Set(header, id)
		if header != support.DefaultRequestIDHeader {
			// 自定义头模式下也把 X-Trace-ID 对齐，保证下游拿到的 trace 一致。
			c.Request.Header.Set(support.DefaultRequestIDHeader, id)
		}
		c.Writer.Header().Set(header, id)

		// 请求 ID 同时进入两层 context：
		//  1. support 私钥：handler 里 support.RequestIDFromContext(ctx) 可取；
		//  2. headerpropagation 契约 key：下游 interface-service 客户端自动转发
		//     X-Trace-Id；上游已有值时这里保持同一个值，不做二次生成。
		ctx := support.ContextWithRequestID(c.Request.Context(), id)
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderTraceID, id)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
