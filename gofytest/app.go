// Package gofytest provides helpers for testing applications built with gofy.
package gofytest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/logging"
)

// App wraps an application handler for in-process HTTP tests.
type App struct {
	t       *testing.T
	app     *gofy.App
	handler http.Handler
}

// New builds an application without pid files, access logs or a real port,
// and registers t.Cleanup to shut it down.
func New(t *testing.T, opts ...gofy.Option) *App {
	t.Helper()
	all := make([]gofy.Option, 0, len(opts)+4)
	all = append(all, opts...)
	all = append(all,
		gofy.WithPIDFile(""),
		gofy.WithAccessLog(false),
		gofy.WithRecovery(true),
		gofy.WithLogger(logging.Nop()),
	)
	app, err := gofy.New(all...)
	if err != nil {
		t.Fatalf("gofytest: New: %v", err)
	}
	handler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatalf("gofytest: Handler: %v", err)
	}
	a := &App{t: t, app: app, handler: handler}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// Do serves req against the application.
func (a *App) Do(req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	a.handler.ServeHTTP(rr, req)
	return rr
}

// GET performs a GET request. headers are alternating key/value pairs.
func (a *App) GET(path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	setHeaders(req, headers)
	return a.Do(req)
}

// POSTJSON performs a POST request with a JSON body.
func (a *App) POSTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPost, path, body, headers)
}

// PUTJSON performs a PUT request with a JSON body.
func (a *App) PUTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPut, path, body, headers)
}

// PATCHJSON performs a PATCH request with a JSON body.
func (a *App) PATCHJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPatch, path, body, headers)
}

// DELETE performs a DELETE request.
func (a *App) DELETE(path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	setHeaders(req, headers)
	return a.Do(req)
}

func (a *App) jsonRequest(method, path string, body any, headers []string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			a.t.Fatalf("gofytest: encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	setHeaders(req, headers)
	return a.Do(req)
}

// Close shuts the application down.
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.app.Shutdown(ctx)
}

func setHeaders(req *http.Request, headers []string) {
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
}

// Status asserts the response status code.
func Status(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("gofytest: status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
	}
}

// JSON decodes the envelope's data field into T.
func JSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var zero T
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("gofytest: decode envelope: %v; body=%s", err, rr.Body.String())
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return zero
	}
	var out T
	if err := json.Unmarshal(envelope.Data, &out); err != nil {
		t.Fatalf("gofytest: decode data: %v; body=%s", err, rr.Body.String())
	}
	return out
}

// RawJSON decodes the full response envelope.
func RawJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := make(map[string]any)
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("gofytest: decode json: %v; body=%s", err, rr.Body.String())
	}
	return out
}
