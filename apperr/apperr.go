// Package apperr defines the business error model shared by gofy handlers
// and the default HTTP responder.
package apperr

import (
	"errors"
	"net/http"
)

// Error is a business error carrying both a business code and an HTTP status.
// Details holds optional field-level validation messages.
type Error struct {
	Code    int
	Status  int
	Message string
	Details map[string]string

	cause error
}

// Error implements the error interface. It returns only the public message;
// the internal cause is available through Unwrap.
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
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WithCause returns a copy of e carrying cause. The receiver is not modified.
func (e *Error) WithCause(err error) *Error {
	cp := e.clone()
	cp.cause = err
	return cp
}

// WithDetails returns a copy of e carrying details. The receiver and the
// provided map are not modified.
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
func New(code, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// BadRequest returns a 400 with business code 40000.
func BadRequest(message string) *Error { return New(40000, http.StatusBadRequest, message) }

// InvalidArgument returns a 400 with business code 40001.
func InvalidArgument(message string) *Error { return New(40001, http.StatusBadRequest, message) }

// Unauthorized returns a 401 with business code 40100.
func Unauthorized(message string) *Error { return New(40100, http.StatusUnauthorized, message) }

// Forbidden returns a 403 with business code 40300.
func Forbidden(message string) *Error { return New(40300, http.StatusForbidden, message) }

// NotFound returns a 404 with business code 40400.
func NotFound(message string) *Error { return New(40400, http.StatusNotFound, message) }

// MethodNotAllowed returns a 405 with business code 40500.
func MethodNotAllowed(message string) *Error { return New(40500, http.StatusMethodNotAllowed, message) }

// Conflict returns a 409 with business code 40900.
func Conflict(message string) *Error { return New(40900, http.StatusConflict, message) }

// TooManyRequests returns a 429 with business code 42900.
func TooManyRequests(message string) *Error { return New(42900, http.StatusTooManyRequests, message) }

// Internal returns a 500 with business code 50000.
func Internal(message string) *Error { return New(50000, http.StatusInternalServerError, message) }

// Unavailable returns a 503 with business code 50300.
func Unavailable(message string) *Error { return New(50300, http.StatusServiceUnavailable, message) }

// As extracts an *Error from an error chain.
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
