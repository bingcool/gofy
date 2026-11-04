package transport

import (
	"errors"
	"slices"
	"time"
)

// CORSConfig describes cross-origin policy. An empty AllowOrigins disables
// CORS entirely.
type CORSConfig struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// Validate reports configurations that would be unsafe or meaningless.
func (c CORSConfig) Validate() error {
	if c.MaxAge < 0 {
		return errors.New("transport: cors max age must not be negative")
	}
	if c.AllowCredentials {
		if slices.Contains(c.AllowOrigins, "*") {
			return errors.New(`transport: cors cannot combine AllowCredentials with wildcard origin "*"`)
		}
	}
	return nil
}
