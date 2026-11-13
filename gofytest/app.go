// Package gofytest provides helpers for testing applications built with gofy.
// 包 gofytest 为基于 gofy 构建的应用提供测试辅助工具。
// 它通过进程内 HTTP 调用驱动 Handler，不需要真实端口、pid 文件等外部依赖。
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
	"github.com/bingcool/gofy/logger"
)

// App wraps an application handler for in-process HTTP tests.
// App 包装应用 handler，用于进程内 HTTP 测试；测试结束前应调用 Close 释放资源。
type App struct {
	t       *testing.T
	app     *gofy.App
	handler http.Handler
}

// New builds an application without pid files, access logs or a real port,
// and registers t.Cleanup to shut it down.
// New 构建一个不使用 pid 文件、访问日志和真实端口的应用，并注册 t.Cleanup 在测试结束时关闭它。
// 传入的 opts 先被应用，随后被测试专用的默认选项覆盖，因此测试中无法再开启 pid 文件或访问日志。
func New(t *testing.T, opts ...gofy.Option) *App {
	t.Helper()
	all := make([]gofy.Option, 0, len(opts)+4)
	all = append(all, opts...)
	all = append(all,
		gofy.WithPIDFile(""),
		gofy.WithAccessLog(false),
		gofy.WithRecovery(true),
		gofy.WithLogger(logger.Nop()),
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
// Do 用给定请求调用应用并返回记录器；请求需由调用方自行构造。
func (a *App) Do(req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	a.handler.ServeHTTP(rr, req)
	return rr
}

// GET performs a GET request. headers are alternating key/value pairs.
// GET 发起 GET 请求。headers 为交替出现的键值对，长度为奇数时最后一个键会被忽略。
func (a *App) GET(path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	setHeaders(req, headers)
	return a.Do(req)
}

// POSTJSON performs a POST request with a JSON body.
// POSTJSON 发起带 JSON 请求体的 POST 请求，并自动设置 Content-Type。
func (a *App) POSTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPost, path, body, headers)
}

// PUTJSON performs a PUT request with a JSON body.
// PUTJSON 发起带 JSON 请求体的 PUT 请求，并自动设置 Content-Type。
func (a *App) PUTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPut, path, body, headers)
}

// PATCHJSON performs a PATCH request with a JSON body.
// PATCHJSON 发起带 JSON 请求体的 PATCH 请求，并自动设置 Content-Type。
func (a *App) PATCHJSON(path string, body any, headers ...string) *httptest.ResponseRecorder {
	return a.jsonRequest(http.MethodPatch, path, body, headers)
}

// DELETE performs a DELETE request.
// DELETE 发起 DELETE 请求；headers 同样为交替出现的键值对。
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
// Close 关闭应用；内部使用 5 秒超时的 context，超时后仍未排空会返回错误。
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
// Status 断言响应状态码，不匹配时立即终止测试并打印响应体。
func Status(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("gofytest: status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
	}
}

// JSON decodes the envelope's data field into T.
// JSON 把响应信封中的 data 字段解码为 T；data 缺失或为 null 时返回零值，便于断言可选字段。
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
// RawJSON 解码完整响应信封，便于断言 code、msg 等外层字段。
func RawJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := make(map[string]any)
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("gofytest: decode json: %v; body=%s", err, rr.Body.String())
	}
	return out
}
