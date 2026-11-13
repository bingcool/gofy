// Package apperr defines the business error model shared by gofy handlers
// and the default HTTP responder.
// 包 apperr 定义 gofy handler 与默认 HTTP 响应器共享的业务错误模型。
package apperr

import (
	"errors"
	"net/http"
)

// Error is a business error carrying both a business code and an HTTP status.
// Details holds optional field-level validation messages.
// Error 是同时携带业务码和 HTTP 状态的业务错误，Details 保存可选的字段级校验
// 信息。
type Error struct {
	Code    int
	Status  int
	Message string
	Details map[string]string

	cause error
}

// Error implements the error interface. It returns only the public message;
// the internal cause is available through Unwrap.
// Error 实现 error 接口，只返回对外可见的 message；内部 cause 通过 Unwrap 读取。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if text := http.StatusText(e.Status); text != "" {
		return text
	}
	return "error"
}

// Unwrap exposes the internal cause to errors.Is / errors.As.
// Unwrap 把内部 cause 暴露给 errors.Is / errors.As。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WithCause returns a copy of e carrying cause. The receiver is not modified.
// WithCause 返回携带 cause 的副本，接收者本身不被修改。
func (e *Error) WithCause(err error) *Error {
	cp := e.clone()
	cp.cause = err
	return cp
}

// WithDetails returns a copy of e carrying details. The receiver and the
// provided map are not modified.
// WithDetails 返回携带 details 的副本，接收者与传入的 map 都不会被修改。
func (e *Error) WithDetails(details map[string]string) *Error {
	cp := e.clone()
	if details != nil {
		cp.Details = make(map[string]string, len(details))
		for k, v := range details {
			cp.Details[k] = v
		}
	}
	return cp
}

func (e *Error) clone() *Error {
	if e == nil {
		return &Error{Status: http.StatusInternalServerError, Code: 50000}
	}
	cp := *e
	if e.Details != nil {
		cp.Details = make(map[string]string, len(e.Details))
		for k, v := range e.Details {
			cp.Details[k] = v
		}
	}
	return &cp
}

// New creates an Error with an explicit business code, HTTP status and message.
// New 用显式的业务码、HTTP 状态和 message 创建 Error。
func New(code, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// BadRequest returns a 400 with business code 40000.
// BadRequest 返回 HTTP 400、业务码 40000 的错误。
func BadRequest(message string) *Error { return New(40000, http.StatusBadRequest, message) }

// InvalidArgument returns a 400 with business code 40001.
// InvalidArgument 返回 HTTP 400、业务码 40001 的错误。
func InvalidArgument(message string) *Error { return New(40001, http.StatusBadRequest, message) }

// Unauthorized returns a 401 with business code 40100.
// Unauthorized 返回 HTTP 401、业务码 40100 的错误。
func Unauthorized(message string) *Error { return New(40100, http.StatusUnauthorized, message) }

// Forbidden returns a 403 with business code 40300.
// Forbidden 返回 HTTP 403、业务码 40300 的错误。
func Forbidden(message string) *Error { return New(40300, http.StatusForbidden, message) }

// NotFound returns a 404 with business code 40400.
// NotFound 返回 HTTP 404、业务码 40400 的错误。
func NotFound(message string) *Error { return New(40400, http.StatusNotFound, message) }

// MethodNotAllowed returns a 405 with business code 40500.
// MethodNotAllowed 返回 HTTP 405、业务码 40500 的错误。
func MethodNotAllowed(message string) *Error { return New(40500, http.StatusMethodNotAllowed, message) }

// Conflict returns a 409 with business code 40900.
// Conflict 返回 HTTP 409、业务码 40900 的错误。
func Conflict(message string) *Error { return New(40900, http.StatusConflict, message) }

// TooManyRequests returns a 429 with business code 42900.
// TooManyRequests 返回 HTTP 429、业务码 42900 的错误。
func TooManyRequests(message string) *Error { return New(42900, http.StatusTooManyRequests, message) }

// Internal returns a 500 with business code 50000.
// Internal 返回 HTTP 500、业务码 50000 的错误。
func Internal(message string) *Error { return New(50000, http.StatusInternalServerError, message) }

// Unavailable returns a 503 with business code 50300.
// Unavailable 返回 HTTP 503、业务码 50300 的错误。
func Unavailable(message string) *Error { return New(50300, http.StatusServiceUnavailable, message) }

// As extracts an *Error from an error chain.
// As 从错误链中提取 *Error。
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
