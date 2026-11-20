package middleware

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// NameAccessLog is the middleware alias AccessLog uses on contract routes: a
// contract group writes group.Use("access-log") and App registers the instance
// into middleware.Registry at startup.
// NameAccessLog 是 AccessLog 在契约路由里使用的中间件别名：契约分组写
// group.Use("access-log")，App 启动时把实例注册进 middleware.Registry。
const NameAccessLog = "access-log"

// AccessLogOptions controls what the access log records and its thresholds.
// The zero value is usable as is: unset fields are filled in by withDefaults,
// matching gofy's default behaviour.
// AccessLogOptions 控制访问日志的内容与阈值。零值可直接使用：未设置的字段由
// withDefaults 补齐，与 gofy 的默认行为一致。
type AccessLogOptions struct {
	// SkipPaths lists paths that are never logged, matched exactly on URL.Path;
	// health checks are skipped by default so probes do not flood the log.
	// SkipPaths 是完全不记录日志的路径，按 URL.Path 精确匹配；默认跳过
	// 健康检查，避免探针请求把日志刷满。
	SkipPaths []string
	// SlowThreshold is the slow-request threshold: 2xx/3xx requests slower than
	// it are logged at Warn, so slow calls can be filtered by level.
	// SlowThreshold 是慢请求阈值：耗时超过它的 2xx/3xx 请求按 Warn 记录，
	// 方便在日志系统里直接按级别筛出慢调用。
	SlowThreshold time.Duration
	// LogRequestID records the req_id written by the RequestID middleware when
	// true.
	// LogRequestID 为 true 时记录 RequestID 中间件写入的 req_id。
	LogRequestID bool
	// LogUserAgent records the User-Agent header when true.
	// LogUserAgent 为 true 时记录 User-Agent。
	LogUserAgent bool
	// LogClientIP records the client IP when true, resolved with gin's proxy
	// trust rules.
	// LogClientIP 为 true 时记录客户端 IP（按 gin 的代理信任规则解析）。
	LogClientIP bool
	// MaxPathLength caps the path / query fields; longer values are truncated so
	// an oversized URL cannot bloat the log.
	// MaxPathLength 是 path / query 字段的最大长度，超出部分截断，
	// 防止超长 URL 撑大日志。
	MaxPathLength int
	// LogQuery records the sanitized query string when true: values of sensitive
	// keys are replaced with ***, so tokens and passwords never reach the log.
	// LogQuery 为 true 时记录脱敏后的 query string：敏感 key 的值统一替换成
	// ***，token、密码不会进入日志。
	LogQuery bool
	// SensitiveQuery lists the query keys to sanitize, case-insensitively; nil
	// means DefaultSensitiveQueryKeys, while an explicit empty slice turns
	// sanitization off (not recommended).
	// SensitiveQuery 是需要脱敏的 query key，大小写不敏感；nil 表示使用
	// DefaultSensitiveQueryKeys，显式传入空切片表示关闭脱敏（不推荐）。
	SensitiveQuery []string
}

// DefaultAccessLogOptions returns the access-log configuration gofy apps use by
// default: health checks are skipped and every optional field is recorded.
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

// DefaultSensitiveQueryKeys returns the query keys sanitized by default,
// covering the common password, token, signature and API key fields.
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

// Validate rejects an option set AccessLog cannot honour. Only MaxPathLength has
// an invalid value: a negative limit makes the truncation test always true and
// the slice expression panic, so it is reported here instead of at the first
// request. Zero is valid and means "use the default".
// Validate 拒绝 AccessLog 无法执行的配置。只有 MaxPathLength 存在非法值：负数会让
// 截断判断恒真、切片表达式 panic，所以在这里报错而不是等到第一个请求。零值合法，
// 表示使用默认值。
func (o AccessLogOptions) Validate() error {
	if o.MaxPathLength < 0 {
		return fmt.Errorf("middleware: access log max path length must not be negative, got %d", o.MaxPathLength)
	}
	return nil
}

// withDefaults fills the zero-value fields with defaults. Slices are checked
// with nil rather than len==0, so a caller can pass an explicit empty slice to
// turn the matching capability off (for example an empty SensitiveQuery means
// no sanitization is done).
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
	// Non-positive, not just zero: AccessLog cannot return an error, and a
	// negative limit would panic on every request. Validate rejects it at
	// construction time for configured apps; this clamp keeps a direct caller
	// that skipped Validate from taking the process down.
	// 判非正而不只是零：AccessLog 无法返回错误，负数会在每个请求上 panic。配置过的应用
	// 由 Validate 在构造时报错；这里的兜底让跳过 Validate 的直接调用方不至于打挂进程。
	if out.MaxPathLength <= 0 {
		out.MaxPathLength = 1024
	}
	if out.SensitiveQuery == nil {
		out.SensitiveQuery = DefaultSensitiveQueryKeys()
	}
	return out
}

// AccessLog records method, path, status, response bytes and latency for every
// request and picks the log level from status and latency:
//
//   - 5xx is logged at Error;
//   - 4xx and requests slower than SlowThreshold at Warn;
//   - everything else at Info.
//
// It must be installed outside Recovery: once Recovery has turned an inner panic
// into a 500, that request still shows up in the access log; in the reverse
// order panic requests leave no access record at all. Installing it after
// RequestID / tracing is recommended as well, so the log carries req_id /
// trace_id.
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
		// Sanitize before truncating: truncation only bounds length, it cannot
		// stop sensitive values from being written.
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
		// With tracing enabled add trace_id / span_id so logs line up with traces.
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

// sanitizeQuery replaces the values of sensitive keys with *** and re-encodes
// the query, so the log holds one length-bounded field instead of attacker
// controlled raw text. When the query cannot be parsed it returns *** and no
// raw content at all: a malformed query may hide sensitive values in a form
// ParseQuery cannot split safely.
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
