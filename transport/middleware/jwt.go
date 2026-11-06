package middleware

import (
	"strings"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/bingcool/gofy/transport"
	"github.com/gin-gonic/gin"
)

// JWTAuth verifies a bearer token and stores the claims in the gin context.
// Rejections use the shared responder so clients see the standard envelope;
// the token error itself is never exposed.
func JWTAuth(cfg transport.JWTAuthConfig, responder transport.Responder) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if responder == nil {
		responder = transport.NewResponder(transport.ResponderConfig{})
	}
	headerName := cfg.Header
	if headerName == "" {
		headerName = "Authorization"
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "Bearer"
	}
	contextKey := cfg.ContextKey
	if contextKey == "" {
		contextKey = transport.JWTClaimsKey
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
		token, found := bearerToken(c.GetHeader(headerName), scheme)
		if found {
			claims, err := cfg.Parser.Parse(token)
			if err == nil {
				c.Set(contextKey, claims)
				c.Next()
				return
			}
		}
		if cfg.Optional {
			c.Next()
			return
		}
		c.Header("WWW-Authenticate", `Bearer realm="api"`)
		responder.Error(c, apperr.Unauthorized("unauthorized"))
		c.Abort()
	}, nil
}

// Claims returns verified claims for the configured context key.
func Claims(c *gin.Context, contextKey string) (*jwtauth.Claims, bool) {
	if c == nil {
		return nil, false
	}
	if contextKey == "" {
		contextKey = transport.JWTClaimsKey
	}
	v, ok := c.Get(contextKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*jwtauth.Claims)
	return claims, ok
}

func bearerToken(header, scheme string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	if scheme == "" {
		return header, true
	}
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	rest := header[len(scheme):]
	if rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	token := strings.TrimSpace(rest)
	return token, token != ""
}
