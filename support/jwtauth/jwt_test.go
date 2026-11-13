package jwtauth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/jwtauth"
)

var secret = []byte("a-very-long-test-secret")

func sign(t *testing.T, alg string, secret []byte, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func newParser(t *testing.T, clock func() time.Time) *jwtauth.Parser {
	t.Helper()
	p, err := jwtauth.NewParser(jwtauth.Config{
		Secret:   secret,
		Issuer:   "gofy",
		Audience: "orders",
		Leeway:   5 * time.Second,
		Clock:    clock,
	})
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	return p
}

func TestParseValidToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	claims, err := newParser(t, func() time.Time { return now }).Parse(sign(t, "HS256", secret, map[string]any{
		"sub":   "user-7",
		"iss":   "gofy",
		"aud":   []string{"other", "orders"},
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
		"roles": "admin",
	}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.Subject != "user-7" {
		t.Fatalf("subject = %q", claims.Subject)
	}
	if claims.Issuer != "gofy" {
		t.Fatalf("issuer = %q", claims.Issuer)
	}
	if len(claims.Audience) != 2 || claims.Audience[1] != "orders" {
		t.Fatalf("audience = %v", claims.Audience)
	}
	if claims.String("roles") != "admin" {
		t.Fatalf("roles = %q", claims.String("roles"))
	}
	if !claims.ExpiresAt.Equal(time.Unix(now.Add(time.Hour).Unix(), 0)) {
		t.Fatalf("exp = %v", claims.ExpiresAt)
	}
}

func TestParseRejections(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	parser := newParser(t, func() time.Time { return now })

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{
			name: "wrong secret",
			token: sign(t, "HS256", []byte("another-secret"), map[string]any{
				"iss": "gofy", "aud": "orders", "exp": now.Add(time.Hour).Unix(),
			}),
			want: jwtauth.ErrSignature,
		},
		{
			name: "alg none",
			token: sign(t, "none", secret, map[string]any{
				"iss": "gofy", "aud": "orders", "exp": now.Add(time.Hour).Unix(),
			}),
			want: jwtauth.ErrUnsupportedAlgorithm,
		},
		{
			name:  "malformed",
			token: "not-a-jwt",
			want:  jwtauth.ErrMalformed,
		},
		{
			name: "expired",
			token: sign(t, "HS256", secret, map[string]any{
				"iss": "gofy", "aud": "orders", "exp": now.Add(-time.Minute).Unix(),
			}),
			want: jwtauth.ErrExpired,
		},
		{
			name: "not yet valid",
			token: sign(t, "HS256", secret, map[string]any{
				"iss": "gofy", "aud": "orders", "exp": now.Add(time.Hour).Unix(),
				"nbf": now.Add(time.Minute).Unix(),
			}),
			want: jwtauth.ErrNotYetValid,
		},
		{
			name: "wrong issuer",
			token: sign(t, "HS256", secret, map[string]any{
				"iss": "other", "aud": "orders", "exp": now.Add(time.Hour).Unix(),
			}),
			want: jwtauth.ErrIssuer,
		},
		{
			name: "wrong audience",
			token: sign(t, "HS256", secret, map[string]any{
				"iss": "gofy", "aud": "billing", "exp": now.Add(time.Hour).Unix(),
			}),
			want: jwtauth.ErrAudience,
		},
		{
			name: "missing expiry",
			token: sign(t, "HS256", secret, map[string]any{
				"iss": "gofy", "aud": "orders",
			}),
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parser.Parse(tc.token)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Parse(%s) = %v, want success when RequireExpiry is false", tc.name, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

func TestParseExpiryLeewayAndRequirement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	parser := newParser(t, func() time.Time { return now })

	// Expired 3s ago, inside the 5s leeway.
	// 3 秒前过期，仍在 5 秒的 leeway 容忍范围内。
	token := sign(t, "HS256", secret, map[string]any{
		"iss": "gofy", "aud": "orders", "exp": now.Add(-3 * time.Second).Unix(),
	})
	if _, err := parser.Parse(token); err != nil {
		t.Fatalf("Parse with leeway: %v", err)
	}

	strict, err := jwtauth.NewParser(jwtauth.Config{
		Secret:        secret,
		RequireExpiry: true,
		Clock:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := strict.Parse(sign(t, "HS256", secret, map[string]any{"sub": "x"})); !errors.Is(err, jwtauth.ErrMissingExpiry) {
		t.Fatalf("Parse without exp = %v, want ErrMissingExpiry", err)
	}
}

func TestNewParserValidation(t *testing.T) {
	if _, err := jwtauth.NewParser(jwtauth.Config{}); err == nil {
		t.Fatal("NewParser accepted an empty secret")
	}
	if _, err := jwtauth.NewParser(jwtauth.Config{Secret: secret, Algorithms: []string{"RS256"}}); !errors.Is(err, jwtauth.ErrUnsupportedAlgorithm) {
		t.Fatalf("NewParser(RS256) = %v, want ErrUnsupportedAlgorithm", err)
	}
	if _, err := jwtauth.NewParser(jwtauth.Config{Secret: secret, Leeway: -time.Second}); err == nil {
		t.Fatal("NewParser accepted a negative leeway")
	}
}
