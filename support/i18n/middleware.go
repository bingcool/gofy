package i18n

import (
	"github.com/gin-gonic/gin"
)

// Middleware negotiates a language for each request and installs the matching
// localizer in the request context.
//
// Candidate sources, highest priority first: the lang/locale query parameters,
// the X-Locale/X-Language headers, then the standard Accept-Language header.
// Only candidates naming a loaded language count — an unsupported one is
// ignored and the next source applies — and an empty list resolves to the
// configured default language.
//
// The localizer goes into the request context that Translate reads, and the
// request's own context is replaced so services called with c.Request.Context()
// resolve the same language. Handlers therefore only need Translate after this
// middleware has run. A disabled bundle yields a pass-through middleware, so
// wiring i18n unconditionally is fine and a deployment without the section is
// unaffected.
// Middleware 为每个请求协商语言，并把对应的 Localizer 写入请求上下文。
//
// 候选来源优先级从高到低：lang/locale 查询参数、X-Locale/X-Language 请求头、
// 标准的 Accept-Language 请求头。只有命中已加载语言的候选才有效——不支持的候选
// 被忽略并继续看下一个来源——空列表则解析为配置的默认语言。
//
// Localizer 写入 Translate 读取的请求上下文，同时替换请求自身的 context，
// 使以 c.Request.Context() 调用的 service 也解析到同一语言。因此 handler 只需在
// 本中间件之后调用 Translate。禁用态的 bundle 得到原样放行的中间件，因此可以
// 无条件装配 i18n，缺少配置段的部署不受影响。
func Middleware(b *Bundle) gin.HandlerFunc {
	if b.messages == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		localizer := b.localizer(
			c.Query("lang"),
			c.Query("locale"),
			c.GetHeader("X-Locale"),
			c.GetHeader("X-Language"),
			c.GetHeader("Accept-Language"),
		)
		c.Request = c.Request.WithContext(withLocalizer(c.Request.Context(), localizer))
		c.Next()
	}
}
