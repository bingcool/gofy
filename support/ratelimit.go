package support

import (
	"errors"
	"time"

	"github.com/bingcool/gofy/support/ratelimit"
	"github.com/gin-gonic/gin"
)

// RateLimitKeyFunc derives the bucket key for a request.
type RateLimitKeyFunc func(*gin.Context) string

// RateLimitConfig enables request limiting for module routes.
type RateLimitConfig struct {
	// Store counts requests. It is required; use ratelimit.NewMemoryStore for
	// a single process, or inject a shared implementation.
	Store ratelimit.Store
	// Limit is the number of requests allowed per Window per key. Required.
	Limit int
	// Window is the sliding window length. Required.
	Window time.Duration
	// KeyFunc groups requests into buckets. The default uses client IP, method
	// and route, so one hot endpoint cannot exhaust another.
	KeyFunc RateLimitKeyFunc
	// SkipPaths lists exact URL paths that bypass limiting.
	SkipPaths []string
	// FailOpen continues the request when Store returns an error. The default
	// fails closed with 503, which avoids removing protection during an outage.
	FailOpen bool
}

// Validate reports configurations the middleware cannot work with.
func (c RateLimitConfig) Validate() error {
	if c.Store == nil {
		return errors.New("support: rate limit store is required")
	}
	if c.Limit <= 0 {
		return errors.New("support: rate limit must be positive")
	}
	if c.Window <= 0 {
		return errors.New("support: rate limit window must be positive")
	}
	return nil
}
