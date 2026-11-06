package transport

import (
	"errors"
	"strings"

	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/gin-gonic/gin"
)

// JWTClaimsKey is the gin.Context key holding verified *jwtauth.Claims.
const JWTClaimsKey = "gofy.jwt_claims"

// JWTAuthConfig enables bearer-token verification for module routes.
//
// Routes registered on the application's root router require a valid token.
// Health endpoints and unmatched paths are not affected, so probes keep
// working and unknown URLs still return 404.
type JWTAuthConfig struct {
	// Parser performs token verification. It is required.
	Parser *jwtauth.Parser
	// Header is the header carrying the token; default "Authorization".
	Header string
	// Scheme is the expected prefix; default "Bearer". An empty-looking token
	// after the scheme is rejected.
	Scheme string
	// Optional lets requests without a valid token continue. Routes that need
	// authentication must then check JWTClaims and return 401 themselves.
	Optional bool
	// SkipPaths lists exact URL paths that bypass verification.
	SkipPaths []string
	// ContextKey overrides where claims are stored; default JWTClaimsKey.
	ContextKey string
}

// Validate reports configurations the middleware cannot work with.
func (c JWTAuthConfig) Validate() error {
	if c.Parser == nil {
		return errors.New("transport: jwt auth parser is required")
	}
	if strings.ContainsAny(c.Header, "\r\n") || strings.ContainsAny(c.Scheme, "\r\n") {
		return errors.New("transport: jwt auth header and scheme must not contain newlines")
	}
	return nil
}

// JWTClaims returns the claims stored by the JWT middleware, if any.
func JWTClaims(c *gin.Context) (*jwtauth.Claims, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Get(JWTClaimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*jwtauth.Claims)
	return claims, ok
}
