// Package jwtauth verifies HMAC-signed JSON Web Tokens. It is deliberately
// small: it does not issue tokens, does not support asymmetric algorithms and
// does not touch the network. Callers keep the signing secret in their own
// configuration.
package jwtauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"slices"
	"strings"
	"time"
)

var (
	// ErrMalformed means the token is not a three-part compact JWS.
	ErrMalformed = errors.New("jwtauth: malformed token")
	// ErrUnsupportedAlgorithm means the header algorithm is unknown or not
	// allowed by the parser configuration.
	ErrUnsupportedAlgorithm = errors.New("jwtauth: unsupported algorithm")
	// ErrSignature means the HMAC does not match the token payload.
	ErrSignature = errors.New("jwtauth: invalid signature")
	// ErrExpired means the exp claim is in the past.
	ErrExpired = errors.New("jwtauth: token expired")
	// ErrNotYetValid means the nbf claim is in the future.
	ErrNotYetValid = errors.New("jwtauth: token not valid yet")
	// ErrIssuer means the iss claim does not match the configured issuer.
	ErrIssuer = errors.New("jwtauth: unexpected issuer")
	// ErrAudience means the aud claim does not contain the configured audience.
	ErrAudience = errors.New("jwtauth: unexpected audience")
	// ErrMissingExpiry means RequireExpiry is set and the token has no exp.
	ErrMissingExpiry = errors.New("jwtauth: token has no expiry")
)

// Config describes how tokens are verified.
type Config struct {
	// Secret is the HMAC key. It must not be empty.
	Secret []byte
	// Algorithms lists the accepted "alg" values. Supported values are HS256,
	// HS384 and HS512; an empty list accepts HS256.
	Algorithms []string
	// Issuer, when set, must equal the token's iss claim.
	Issuer string
	// Audience, when set, must be present in the token's aud claim.
	Audience string
	// Leeway tolerates small clock differences for exp and nbf.
	Leeway time.Duration
	// RequireExpiry rejects tokens without an exp claim.
	RequireExpiry bool
	// Clock overrides time.Now; tests use it to make expiry deterministic.
	Clock func() time.Time
}

// Claims is the verified payload of a token.
type Claims struct {
	Subject   string
	Issuer    string
	Audience  []string
	ExpiresAt time.Time
	NotBefore time.Time
	IssuedAt  time.Time
	ID        string
	// Raw holds the full payload so callers can read application claims such
	// as roles or tenant ids.
	Raw map[string]any
}

// Get returns a raw claim value.
func (c *Claims) Get(key string) (any, bool) {
	if c == nil || c.Raw == nil {
		return nil, false
	}
	v, ok := c.Raw[key]
	return v, ok
}

// String returns a string claim, or "" when the claim is missing or has
// another type.
func (c *Claims) String(key string) string {
	v, _ := c.Get(key)
	s, _ := v.(string)
	return s
}

// Parser verifies tokens with a fixed configuration. It is safe for
// concurrent use.
type Parser struct {
	secret        []byte
	algorithms    map[string]func() hash.Hash
	issuer        string
	audience      string
	leeway        time.Duration
	requireExpiry bool
	clock         func() time.Time
}

// NewParser validates cfg and builds a parser.
func NewParser(cfg Config) (*Parser, error) {
	if len(cfg.Secret) == 0 {
		return nil, errors.New("jwtauth: secret is required")
	}
	if cfg.Leeway < 0 {
		return nil, errors.New("jwtauth: leeway must not be negative")
	}
	algs := cfg.Algorithms
	if len(algs) == 0 {
		algs = []string{"HS256"}
	}
	supported := map[string]func() hash.Hash{
		"HS256": sha256.New,
		"HS384": sha512.New384,
		"HS512": sha512.New,
	}
	allowed := make(map[string]func() hash.Hash, len(algs))
	for _, alg := range algs {
		name := strings.ToUpper(strings.TrimSpace(alg))
		fn, ok := supported[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, alg)
		}
		allowed[name] = fn
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Parser{
		secret:        append([]byte(nil), cfg.Secret...),
		algorithms:    allowed,
		issuer:        cfg.Issuer,
		audience:      cfg.Audience,
		leeway:        cfg.Leeway,
		requireExpiry: cfg.RequireExpiry,
		clock:         clock,
	}, nil
}

type header struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

// Parse verifies token and returns its claims.
func (p *Parser) Parse(token string) (*Claims, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, ErrMalformed
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header encoding", ErrMalformed)
	}
	var h header
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return nil, fmt.Errorf("%w: header json", ErrMalformed)
	}
	alg := strings.ToUpper(strings.TrimSpace(h.Algorithm))
	hashFn, ok := p.algorithms[alg]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, h.Algorithm)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: signature encoding", ErrMalformed)
	}
	mac := hmac.New(hashFn, p.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrSignature
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload encoding", ErrMalformed)
	}
	dec := json.NewDecoder(strings.NewReader(string(payloadBytes)))
	dec.UseNumber()
	raw := make(map[string]any)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: payload json", ErrMalformed)
	}

	claims := &Claims{Raw: raw}
	claims.Subject, _ = raw["sub"].(string)
	claims.Issuer, _ = raw["iss"].(string)
	claims.ID, _ = raw["jti"].(string)
	claims.Audience = audienceOf(raw["aud"])

	now := p.clock()
	if exp, ok := numericDate(raw["exp"]); ok {
		claims.ExpiresAt = exp
		if now.After(exp.Add(p.leeway)) {
			return nil, fmt.Errorf("%w: expired at %s", ErrExpired, exp.UTC().Format(time.RFC3339))
		}
	} else if p.requireExpiry {
		return nil, ErrMissingExpiry
	}
	if nbf, ok := numericDate(raw["nbf"]); ok {
		claims.NotBefore = nbf
		if now.Add(p.leeway).Before(nbf) {
			return nil, fmt.Errorf("%w: valid from %s", ErrNotYetValid, nbf.UTC().Format(time.RFC3339))
		}
	}
	if iat, ok := numericDate(raw["iat"]); ok {
		claims.IssuedAt = iat
	}
	if p.issuer != "" && claims.Issuer != p.issuer {
		return nil, fmt.Errorf("%w: %q", ErrIssuer, claims.Issuer)
	}
	if p.audience != "" && !contains(claims.Audience, p.audience) {
		return nil, fmt.Errorf("%w: %v", ErrAudience, claims.Audience)
	}
	return claims, nil
}

func audienceOf(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func numericDate(v any) (time.Time, bool) {
	var seconds float64
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return time.Time{}, false
		}
		seconds = f
	case float64:
		seconds = t
	default:
		return time.Time{}, false
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	return time.Unix(whole, nanos).UTC(), true
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
