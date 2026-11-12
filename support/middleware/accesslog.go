package middleware

import (
	"net/url"
	"strings"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// NameAccessLog 是 AccessLog 在契约路由里使用的中间件别名：契约分组写
// group.Use("access-log")，App 启动时把实例注册进 middleware.Registry。
const NameAccessLog = "access-log"

// AccessLogOptions 控制访问日志的内容与阈值。零值可直接使用：未设置的字段由
// withDefaults 补齐，与 gofy 的默认行为一致。
type AccessLogOptions struct {
	// SkipPaths 是完全不记录日志的路径，按 URL.Path 精确匹配；默认跳过
	// 健康检查，避免探针请求把日志刷满。
	SkipPaths []string
	// SlowThreshold 是慢请求阈值：耗时超过它的 2xx/3xx 请求按 Warn 记录，
	// 方便在日志系统里直接按级别筛出慢调用。
	SlowThreshold time.Duration
	// LogRequestID 为 true 时记录 RequestID 中间件写入的 req_id。
	LogRequestID bool
	// LogUserAgent 为 true 时记录 User-Agent。
	LogUserAgent bool
	// LogClientIP 为 true 时记录客户端 IP（按 gin 的代理信任规则解析）。
	LogClientIP bool
	// MaxPathLength 是 path / query 字段的最大长度，超出部分截断，
	// 防止超长 URL 撑大日志。
	MaxPathLength int
	// LogQuery 为 true 时记录脱敏后的 query string：敏感 key 的值统一替换成
	// ***，token、密码不会进入日志。
	LogQuery bool
	// SensitiveQuery 是需要脱敏的 query key，大小写不敏感；nil 表示使用
	// DefaultSensitiveQueryKeys，显式传入空切片表示关闭脱敏（不推荐）。
	SensitiveQuery []string
}

// DefaultAccessLogOptions 返回 gofy 应用默认使用的访问日志配置：跳过健康
// 检查，其余可选字段全部记录。
func DefaultAccessLogOptions() AccessLogOptions {
	return AccessLogOptions{
		SkipPaths:      []string{"/healthz", "/readyz"},
		SlowThreshold:  500 * time.Millisecond,
		LogRequestID:   true,
		LogUserAgent:   true,
		LogClientIP:    true,
		MaxPathLength:  1024,
		LogQuery:       true,
		SensitiveQuery: DefaultSensitiveQueryKeys(),
	}
}

// DefaultSensitiveQueryKeys 返回默认脱敏的 query key，覆盖常见的口令、token、
// 签名与 API Key 字段。
func DefaultSensitiveQueryKeys() []string {
	return []string{
		"password", "passwd", "pwd", "pass",
		"secret", "client_secret", "private_key",
		"token", "access_token", "refresh_token", "id_token",
		"authorization", "api_key", "apikey", "api-key",
		"sign", "signature", "credential", "credentials",
	}
}

// withDefaults 把零值字段补齐为默认值。切片用 nil 判断而不是 len==0，因此
// 调用方可以显式传空切片来关闭对应能力（例如 SensitiveQuery 传空切片即
// 不做脱敏）。
func (o AccessLogOptions) withDefaults() AccessLogOptions {
	out := o
	if out.SkipPaths == nil {
		out.SkipPaths = []string{"/healthz", "/readyz"}
	}
	if out.SlowThreshold == 0 {
		out.SlowThreshold = 500 * time.Millisecond
	}
	if out.MaxPathLength == 0 {
		out.MaxPathLength = 1024
	}
	if out.SensitiveQuery == nil {
		out.SensitiveQuery = DefaultSensitiveQueryKeys()
	}
	return out
}

// AccessLog 记录每个请求的方法、路径、状态码、响应字节数和耗时，并按状态码
// 与耗时分流级别：
//
//   - 5xx 记 Error；
//   - 4xx 与超过 SlowThreshold 的请求记 Warn；
//   - 其余记 Info。
//
// 必须装在 Recovery 外层：内层 panic 被 Recovery 转成 500 后，这条请求仍会
// 出现在访问日志里；如果顺序反过来，panic 请求不会留下任何访问记录。也建议
// 装在 RequestID / tracing 之后，日志才能带上 req_id / trace_id。
func AccessLog(logger *zap.Logger, opts AccessLogOptions) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	opts = opts.withDefaults()
	skip := make(map[string]struct{}, len(opts.SkipPaths))
	for _, p := range opts.SkipPaths {
		skip[p] = struct{}{}
	}
	sensitive := make(map[string]struct{}, len(opts.SensitiveQuery))
	for _, k := range opts.SensitiveQuery {
		sensitive[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		path := c.Request.URL.Path
		if len(path) > opts.MaxPathLength {
			path = path[:opts.MaxPathLength]
		}
		status := c.Writer.Status()
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", status),
			zap.Int("bytes", c.Writer.Size()),
			zap.Float64("latency_ms", float64(latency.Microseconds())/1000),
		}
		// query 先脱敏再截断：截断只能限制长度，不能阻止敏感值落盘。
		if opts.LogQuery && c.Request.URL.RawQuery != "" {
			query := sanitizeQuery(c.Request.URL.RawQuery, sensitive)
			if len(query) > opts.MaxPathLength {
				query = query[:opts.MaxPathLength]
			}
			fields = append(fields, zap.String("query", query))
		}
		if opts.LogRequestID {
			if id := c.GetString(support.RequestIDKey); id != "" {
				fields = append(fields, zap.String("req_id", id))
			}
		}
		// 启用 tracing 时补上 trace_id / span_id，日志可与链路追踪对齐。
		if sc := trace.SpanContextFromContext(c.Request.Context()); sc.IsValid() {
			fields = append(fields,
				zap.String("trace_id", sc.TraceID().String()),
				zap.String("span_id", sc.SpanID().String()),
			)
		}
		if opts.LogClientIP {
			fields = append(fields, zap.String("client_ip", c.ClientIP()))
		}
		if opts.LogUserAgent {
			fields = append(fields, zap.String("user_agent", c.Request.UserAgent()))
		}

		switch {
		case status >= 500:
			logger.Error("http access", fields...)
		case status >= 400:
			logger.Warn("http access", fields...)
		case latency > opts.SlowThreshold:
			logger.Warn("http access", fields...)
		default:
			logger.Info("http access", fields...)
		}
	}
}

// sanitizeQuery 把敏感 key 的值替换成 *** 后重新编码，保证日志里是一个长度
// 可控的字段，而不是攻击者可控的原始文本。query 解析失败时直接返回 ***，
// 不输出任何原始内容：畸形 query 可能把敏感值藏在 ParseQuery 无法安全切分
// 的形式里。
func sanitizeQuery(raw string, sensitive map[string]struct{}) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "***"
	}
	for key := range values {
		if _, ok := sensitive[strings.ToLower(key)]; ok {
			values[key] = []string{"***"}
		}
	}
	return values.Encode()
}
