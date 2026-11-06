package middleware

import (
	"math"
	"strconv"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/transport"
	"github.com/gin-gonic/gin"
)

// RateLimit rejects requests above cfg.Limit inside cfg.Window. Every response
// carries X-RateLimit-* headers so clients can back off before being rejected;
// a rejection adds Retry-After and returns 429 with the shared envelope.
//
// A store failure returns 503 by default. Set FailOpen when availability of the
// store is less important than keeping the endpoint reachable.
func RateLimit(cfg transport.RateLimitConfig, responder transport.Responder) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if responder == nil {
		responder = transport.NewResponder(transport.ResponderConfig{})
	}
	keyFunc := cfg.KeyFunc
	if keyFunc == nil {
		keyFunc = defaultRateLimitKey
	}
	skip := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skip[p] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}
		res, err := cfg.Store.Allow(c.Request.Context(), keyFunc(c), cfg.Limit, cfg.Window)
		if err != nil {
			if cfg.FailOpen {
				c.Next()
				return
			}
			responder.Error(c, apperr.Unavailable("rate limiter unavailable"))
			c.Abort()
			return
		}

		c.Header("X-RateLimit-Limit", strconv.Itoa(res.Limit))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		if !res.ResetAt.IsZero() {
			c.Header("X-RateLimit-Reset", strconv.FormatInt(res.ResetAt.Unix(), 10))
		}
		if res.Allowed {
			c.Next()
			return
		}
		retry := int(math.Ceil(res.RetryAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		c.Header("Retry-After", strconv.Itoa(retry))
		responder.Error(c, apperr.TooManyRequests("too many requests"))
		c.Abort()
	}, nil
}

func defaultRateLimitKey(c *gin.Context) string {
	route := c.FullPath()
	if route == "" {
		route = c.Request.URL.Path
	}
	return c.ClientIP() + "|" + c.Request.Method + "|" + route
}
