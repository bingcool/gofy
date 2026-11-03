package apperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/bingcool/gofy/apperr"
)

func TestConstructorsMapToStatusAndCode(t *testing.T) {
	cases := []struct {
		name   string
		err    *apperr.Error
		status int
		code   int
	}{
		{"bad request", apperr.BadRequest("bad"), http.StatusBadRequest, 40000},
		{"invalid argument", apperr.InvalidArgument("invalid"), http.StatusBadRequest, 40001},
		{"unauthorized", apperr.Unauthorized("auth"), http.StatusUnauthorized, 40100},
		{"forbidden", apperr.Forbidden("forbidden"), http.StatusForbidden, 40300},
		{"not found", apperr.NotFound("missing"), http.StatusNotFound, 40400},
		{"conflict", apperr.Conflict("conflict"), http.StatusConflict, 40900},
		{"too many requests", apperr.TooManyRequests("slow down"), http.StatusTooManyRequests, 42900},
		{"internal", apperr.Internal("boom"), http.StatusInternalServerError, 50000},
		{"unavailable", apperr.Unavailable("down"), http.StatusServiceUnavailable, 50300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Status != tc.status {
				t.Fatalf("status = %d, want %d", tc.err.Status, tc.status)
			}
			if tc.err.Code != tc.code {
				t.Fatalf("code = %d, want %d", tc.err.Code, tc.code)
			}
		})
	}
}

func TestErrorFallsBackToStatusText(t *testing.T) {
	err := apperr.New(999, http.StatusTeapot, "")
	if got, want := err.Error(), http.StatusText(http.StatusTeapot); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	var nilErr *apperr.Error
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil error message = %q, want empty", got)
	}
}

func TestWithCauseReturnsCopy(t *testing.T) {
	cause := errors.New("db down")
	base := apperr.Internal("internal")
	withCause := base.WithCause(cause)

	if base.Unwrap() != nil {
		t.Fatal("WithCause mutated the receiver")
	}
	if !errors.Is(withCause, cause) {
		t.Fatal("cause is not reachable through errors.Is")
	}
	if withCause.Code != base.Code || withCause.Status != base.Status || withCause.Message != base.Message {
		t.Fatal("WithCause changed the error identity")
	}
}

func TestWithDetailsCopiesMap(t *testing.T) {
	details := map[string]string{"id": "invalid"}
	base := apperr.InvalidArgument("invalid argument")
	withDetails := base.WithDetails(details)

	if base.Details != nil {
		t.Fatal("WithDetails mutated the receiver")
	}
	details["id"] = "changed"
	if got := withDetails.Details["id"]; got != "invalid" {
		t.Fatalf("details shared the caller map: %q", got)
	}

	// Mutating a returned error must not affect a sibling derived from the
	// same receiver.
	sibling := base.WithDetails(map[string]string{"id": "other"})
	sibling.Details["id"] = "mutated"
	if got := withDetails.Details["id"]; got != "invalid" {
		t.Fatalf("WithDetails shared maps between copies: %q", got)
	}
}

func TestAsFindsWrappedError(t *testing.T) {
	target := apperr.NotFound("order not found")
	wrapped := fmt.Errorf("handler: %w", target)

	got, ok := apperr.As(wrapped)
	if !ok {
		t.Fatal("As did not find the error")
	}
	if got != target {
		t.Fatalf("As returned %p, want %p", got, target)
	}

	if _, ok := apperr.As(errors.New("plain")); ok {
		t.Fatal("As matched a plain error")
	}
}
