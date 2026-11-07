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
	RequestIDKey = "gofy.request_id"
	// DefaultRequestIDHeader is the header read and written by the RequestID
	// middleware.
	DefaultRequestIDHeader = "X-Request-ID"
)

// Responder renders successes and errors. Implementations must be safe for
// concurrent use.
type Responder interface {
	Success(c *gin.Context, data any)
	Error(c *gin.Context, err error)
}

// ResponderConfig configures the default responder.
type ResponderConfig struct {
	SuccessCode  int    // default 0
	SuccessMsg   string // default "success"
	RequestIDKey string // default support.RequestIDKey
	AlwaysOK     bool   // when true every response uses HTTP 200
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
