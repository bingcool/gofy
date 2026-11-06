package middleware

import (
	"net/url"
	"strings"
	"time"

	"github.com/bingcool/gofy/transport"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AccessLogOptions controls the access log middleware.
type AccessLogOptions struct {
	SkipPaths     []string
	SlowThreshold time.Duration
	LogRequestID  bool
	LogUserAgent  bool
	LogClientIP   bool
	MaxPathLength int
	// LogQuery includes the sanitized query string. Values of sensitive keys
	// are replaced with *** so tokens and passwords never reach the log.
	LogQuery bool
	// SensitiveQuery lists query keys to redact, matched case-insensitively.
	// Nil means DefaultSensitiveQueryKeys; an empty non-nil slice disables
	// redaction (not recommended).
	SensitiveQuery []string
}

// DefaultAccessLogOptions returns the options used by the gofy application:
// health checks are skipped and every optional field is logged.
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

// DefaultSensitiveQueryKeys returns the query keys redacted by the access log.
func DefaultSensitiveQueryKeys() []string {
	return []string{
		"password", "passwd", "pwd", "pass",
		"secret", "client_secret", "private_key",
		"token", "access_token", "refresh_token", "id_token",
		"authorization", "api_key", "apikey", "api-key",
		"sign", "signature", "credential", "credentials",
	}
}

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

// AccessLog logs method, path, status, bytes and latency for every request.
// It must be installed outside Recovery so panics are still logged as 500.
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
		if opts.LogQuery && c.Request.URL.RawQuery != "" {
			query := sanitizeQuery(c.Request.URL.RawQuery, sensitive)
			if len(query) > opts.MaxPathLength {
				query = query[:opts.MaxPathLength]
			}
			fields = append(fields, zap.String("query", query))
		}
		if opts.LogRequestID {
			if id := c.GetString(transport.RequestIDKey); id != "" {
				fields = append(fields, zap.String("req_id", id))
			}
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

// sanitizeQuery redacts sensitive values and re-encodes the query so the log
// holds one bounded field instead of attacker-controlled raw text.
func sanitizeQuery(raw string, sensitive map[string]struct{}) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		// A malformed query may hide a secret in a form ParseQuery cannot
		// tokenize safely, so log nothing from it.
		return "***"
	}
	for key := range values {
		if _, ok := sensitive[strings.ToLower(key)]; ok {
			values[key] = []string{"***"}
		}
	}
	return values.Encode()
}
