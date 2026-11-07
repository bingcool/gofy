package support

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SecurityHeadersConfig sets response headers that harden browser behavior.
// Zero values select the defaults documented on each field.
type SecurityHeadersConfig struct {
	// ContentTypeOptions is normally "nosniff".
	ContentTypeOptions string
	// FrameOptions is normally "DENY".
	FrameOptions string
	// ReferrerPolicy is normally "strict-origin-when-cross-origin".
	ReferrerPolicy string
	// HSTS enables Strict-Transport-Security for this duration. Zero disables
	// it; only enable it when the service is served over HTTPS.
	HSTS time.Duration
	// HSTSIncludeSubdomains adds includeSubDomains.
	HSTSIncludeSubdomains bool
	// HSTSPreload adds preload.
	HSTSPreload bool
	// ContentSecurityPolicy is emitted when non-empty.
	ContentSecurityPolicy string
	// PermissionsPolicy is emitted when non-empty.
	PermissionsPolicy string
	// Custom headers are applied last and may override the defaults above.
	Custom map[string]string
}

// DefaultSecurityHeadersConfig returns the headers gofy applies when
// WithSecurityHeaders is given a zero config.
func DefaultSecurityHeadersConfig() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		ContentTypeOptions: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	}
}

// Validate rejects values that would inject extra headers.
func (c SecurityHeadersConfig) Validate() error {
	values := []string{
		c.ContentTypeOptions, c.FrameOptions, c.ReferrerPolicy,
		c.ContentSecurityPolicy, c.PermissionsPolicy,
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("support: security header value must not contain newlines")
		}
	}
	if c.HSTS < 0 {
		return errors.New("support: hsts duration must not be negative")
	}
	for name, value := range c.Custom {
		if strings.TrimSpace(name) == "" {
			return errors.New("support: security header name must not be empty")
		}
		if strings.ContainsAny(name, "\r\n:") {
			return fmt.Errorf("support: invalid security header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("support: security header %q value must not contain newlines", name)
		}
	}
	return nil
}
