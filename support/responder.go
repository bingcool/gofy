package support

import (
	"context"
	"errors"
	"net/http"

	"github.com/bingcool/gofy/apperr"
	"github.com/gin-gonic/gin"
)

const (
	// RequestIDKey is the gin.Context key holding the current request id.
	// RequestIDKey 是 gin.Context 中存放当前请求 ID 的 key；改用它之前确认
	// 上游中间件确实写入了该 key。
	RequestIDKey = "gofy.request_id"
	// DefaultRequestIDHeader is the header read and written by the RequestID
	// middleware.
	// DefaultRequestIDHeader 是 RequestID 中间件读写请求 ID 的响应头名称；
	// 响应体里的 req_id 也以此处取到的值为准。
	DefaultRequestIDHeader = "X-Trace-ID"
)

// Responder renders successes and errors. Implementations must be safe for
// concurrent use.
// Responder 负责渲染成功与错误响应；实现必须可并发使用，且不得在渲染时
// 再返回错误，否则调用方无法处理。
type Responder interface {
	Success(c *gin.Context, data any)
	Error(c *gin.Context, err error)
}

// ResponderConfig configures the default responder.
// ResponderConfig 配置默认 responder；零值表示成功码 0、消息 "success"、
// HTTP 状态按错误语义返回。
type ResponderConfig struct {
	SuccessCode  int    // default 0；成功响应码，默认 0
	SuccessMsg   string // default "success"；成功提示语，默认 "success"
	RequestIDKey string // default support.RequestIDKey；读取请求 ID 的键名，默认 support.RequestIDKey
	AlwaysOK     bool   // when true every response uses HTTP 200；为 true 时所有响应都返回 HTTP 200，不再按错误语义映射状态码
}

type envelope struct {
	Code    int               `json:"code"`
	Msg     string            `json:"msg"`
	Data    any               `json:"data"`
	ReqID   string            `json:"req_id"`
	Details map[string]string `json:"details,omitempty"`
}

type defaultResponder struct {
	successCode  int
	successMsg   string
	requestIDKey string
	alwaysOK     bool
}

// NewResponder builds the default responder. It is stateless and therefore
// safe for concurrent use.
// NewResponder 构造默认 responder；实例无可变状态，可在整个进程内共享，
// 配置在构造时固化，之后修改 ResponderConfig 不会影响已创建的实例。
func NewResponder(cfg ResponderConfig) Responder {
	if cfg.RequestIDKey == "" {
		cfg.RequestIDKey = RequestIDKey
	}
	if cfg.SuccessMsg == "" {
		cfg.SuccessMsg = "success"
	}
	return &defaultResponder{
		successCode:  cfg.SuccessCode,
		successMsg:   cfg.SuccessMsg,
		requestIDKey: cfg.RequestIDKey,
		alwaysOK:     cfg.AlwaysOK,
	}
}

func (r *defaultResponder) Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, envelope{
		Code:  r.successCode,
		Msg:   r.successMsg,
		Data:  data,
		ReqID: r.requestID(c),
	})
}

func (r *defaultResponder) Error(c *gin.Context, err error) {
	status, code, msg, details := r.mapError(err)
	if r.alwaysOK {
		status = http.StatusOK
	}
	body := envelope{
		Code:  code,
		Msg:   msg,
		Data:  nil,
		ReqID: r.requestID(c),
	}
	if len(details) > 0 {
		body.Details = details
	}
	c.JSON(status, body)
}

func (r *defaultResponder) mapError(err error) (status, code int, msg string, details map[string]string) {
	if e, ok := apperr.As(err); ok {
		status = e.Status
		if status < 100 || status > 599 {
			status = http.StatusInternalServerError
		}
		code = e.Code
		msg = e.Error()
		return status, code, msg, e.Details
	}
	switch {
	case errors.Is(err, context.Canceled):
		return 499, 49900, "client closed request", nil
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, 50400, "request timeout", nil
	default:
		return http.StatusInternalServerError, 50000, "internal server error", nil
	}
}

func (r *defaultResponder) requestID(c *gin.Context) string {
	if id := c.GetString(r.requestIDKey); id != "" {
		return id
	}
	return c.GetHeader(DefaultRequestIDHeader)
}
