package support_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
)

type getRequest struct {
	ID uint64 `uri:"id" validate:"required,min=1" message:"required:订单ID不能为空,min:订单ID非法"`
}

type getResponse struct {
	ID uint64 `json:"id"`
}

type createRequest struct {
	UserID uint64 `json:"user_id" validate:"required,min=1" message:"required:用户ID不能为空,min:用户ID非法"`
	Amount int64  `json:"amount" validate:"required,min=1" message:"required:金额不能为空,min:金额必须大于0"`
}

type emptyRequest struct{}

func newEngine(t *testing.T, cfg support.RouterConfig) (*gin.Engine, *support.Router) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))
	return engine, support.NewRouter(engine, cfg)
}

func serve(engine *gin.Engine, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func decode(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := make(map[string]any)
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return out
}

func codeOf(t *testing.T, body map[string]any) int {
	t.Helper()
	raw, ok := body["code"].(float64)
	if !ok {
		t.Fatalf("response has no numeric code: %v", body)
	}
	return int(raw)
}

func TestRouterExposesResponderValidatorAndBinding(t *testing.T) {
	_, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	if router.Responder() == nil {
		t.Fatal("Responder() is nil")
	}
	if router.Validator() == nil {
		t.Fatal("Validator() is nil")
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"user_id":3,"amount":10}`))
	c.Request.Header.Set("Content-Type", "application/json")
	var req createRequest
	if err := router.BindAndValidate(c, &req); err != nil {
		t.Fatalf("BindAndValidate: %v", err)
	}
	if req.UserID != 3 || req.Amount != 10 {
		t.Fatalf("req = %+v", req)
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"/":           "",
		"api":         "/api",
		"/api/":       "/api",
		"  /api/v1/ ": "/api/v1",
	}
	for in, want := range cases {
		if got := support.NormalizePrefix(in); got != want {
			t.Fatalf("NormalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRouterGroupsAndPrefix(t *testing.T) {
	_, root := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	sub := root.Group("order")
	if got, want := sub.Prefix(), "/api/v1/order"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
	if got, want := root.Group("").Prefix(), "/api/v1"; got != want {
		t.Fatalf("empty group prefix = %q, want %q", got, want)
	}
}

func TestSuccessEnvelopeAndRequestID(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	support.GET(router.Group("/order"), "/orders/:id",
		func(_ context.Context, req *getRequest) (*getResponse, error) {
			return &getResponse{ID: req.ID}, nil
		})

	rr := serve(engine, http.MethodGet, "/api/v1/order/orders/42", "", support.DefaultRequestIDHeader, "req-123")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body)
	}
	if got := rr.Header().Get(support.DefaultRequestIDHeader); got != "req-123" {
		t.Fatalf("response request id = %q, want req-123", got)
	}
	body := decode(t, rr)
	if codeOf(t, body) != 0 {
		t.Fatalf("code = %v, want 0", body["code"])
	}
	if body["msg"] != "success" {
		t.Fatalf("msg = %v, want success", body["msg"])
	}
	if body["req_id"] != "req-123" {
		t.Fatalf("req_id = %v, want req-123", body["req_id"])
	}
	data, _ := body["data"].(map[string]any)
	if data["id"] != float64(42) {
		t.Fatalf("data.id = %v, want 42", data["id"])
	}
}

func TestBindingFailureReturnsInvalidRequest(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	support.GET(router.Group("/order"), "/orders/:id",
		func(_ context.Context, req *getRequest) (*getResponse, error) {
			return &getResponse{ID: req.ID}, nil
		})

	rr := serve(engine, http.MethodGet, "/api/v1/order/orders/not-a-number", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body)
	}
	body := decode(t, rr)
	if codeOf(t, body) != 40002 {
		t.Fatalf("code = %v, want 40002", body["code"])
	}
	if body["msg"] != "invalid request" {
		t.Fatalf("msg = %v, want invalid request", body["msg"])
	}
}

func TestValidationFailureReturnsDetails(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	support.POST(router.Group("/order"), "/orders",
		func(_ context.Context, req *createRequest) (*getResponse, error) {
			return &getResponse{ID: req.UserID}, nil
		})

	rr := serve(engine, http.MethodPost, "/api/v1/order/orders", `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body)
	}
	body := decode(t, rr)
	if codeOf(t, body) != 40001 {
		t.Fatalf("code = %v, want 40001", body["code"])
	}
	if body["msg"] != "用户ID不能为空; 金额不能为空" {
		t.Fatalf("msg = %v, want joined message tags", body["msg"])
	}
	details, _ := body["details"].(map[string]any)
	if details["user_id"] != "用户ID不能为空" {
		t.Fatalf("details.user_id = %v, want custom message", details["user_id"])
	}
	if details["amount"] != "金额不能为空" {
		t.Fatalf("details.amount = %v, want custom message", details["amount"])
	}
}

func TestAppErrorMapping(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	group := router.Group("/order")
	support.GET(group, "/missing", func(context.Context, *emptyRequest) (*getResponse, error) {
		return nil, apperr.NotFound("order not found")
	})
	support.GET(group, "/boom", func(context.Context, *emptyRequest) (*getResponse, error) {
		return nil, errors.New("database exploded: secret dsn")
	})
	support.GET(group, "/cancelled", func(context.Context, *emptyRequest) (*getResponse, error) {
		return nil, context.Canceled
	})
	support.GET(group, "/timeout", func(context.Context, *emptyRequest) (*getResponse, error) {
		return nil, context.DeadlineExceeded
	})

	cases := []struct {
		path   string
		status int
		code   int
	}{
		{"/api/v1/order/missing", http.StatusNotFound, 40400},
		{"/api/v1/order/boom", http.StatusInternalServerError, 50000},
		{"/api/v1/order/cancelled", 499, 49900},
		{"/api/v1/order/timeout", http.StatusGatewayTimeout, 50400},
	}
	for _, tc := range cases {
		rr := serve(engine, http.MethodGet, tc.path, "")
		if rr.Code != tc.status {
			t.Fatalf("%s status = %d, want %d", tc.path, rr.Code, tc.status)
		}
		body := decode(t, rr)
		if codeOf(t, body) != tc.code {
			t.Fatalf("%s code = %v, want %d", tc.path, body["code"], tc.code)
		}
		if strings.Contains(rr.Body.String(), "secret dsn") {
			t.Fatalf("%s leaked the internal error", tc.path)
		}
	}
}

func TestNoContentReturns204(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	support.NoContent(router.Group("/order"), http.MethodDelete, "/orders/:id",
		func(_ context.Context, req *getRequest) error {
			if req.ID != 9 {
				return apperr.NotFound("missing")
			}
			return nil
		})

	rr := serve(engine, http.MethodDelete, "/api/v1/order/orders/9", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("204 response has a body: %q", rr.Body.String())
	}

	rr = serve(engine, http.MethodDelete, "/api/v1/order/orders/8", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestAlwaysOKKeepsBusinessCode(t *testing.T) {
	engine, router := newEngine(t, support.RouterConfig{
		Prefix:    "/api/v1",
		Responder: support.NewResponder(support.ResponderConfig{AlwaysOK: true}),
	})
	support.GET(router.Group("/order"), "/missing", func(context.Context, *emptyRequest) (*getResponse, error) {
		return nil, apperr.NotFound("order not found")
	})

	rr := serve(engine, http.MethodGet, "/api/v1/order/missing", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := codeOf(t, decode(t, rr)); got != 40400 {
		t.Fatalf("code = %d, want 40400", got)
	}
}

func TestBodyLimitMapsTo413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))
	engine.Use(middleware.BodyLimit(16))
	router := support.NewRouter(engine, support.RouterConfig{Prefix: "/api/v1"})
	support.POST(router.Group("/order"), "/orders",
		func(_ context.Context, req *createRequest) (*getResponse, error) {
			return &getResponse{ID: req.UserID}, nil
		})

	payload := `{"user_id":1,"amount":100,"padding":"` + strings.Repeat("x", 64) + `"}`
	rr := serve(engine, http.MethodPost, "/api/v1/order/orders", payload)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rr.Code, rr.Body)
	}
	if got := codeOf(t, decode(t, rr)); got != 41300 {
		t.Fatalf("code = %d, want 41300", got)
	}
}

func TestDefaultsAreAppliedBeforeValidation(t *testing.T) {
	type listRequest struct {
		Page int `form:"page" json:"page" default:"1" validate:"min=1" message:"min:页码必须大于0"`
		Size int `form:"size" json:"size" default:"20" validate:"min=1,max=100" message:"max:每页最多100条"`
	}
	engine, router := newEngine(t, support.RouterConfig{Prefix: "/api/v1"})
	support.GET(router.Group("/order"), "/orders",
		func(_ context.Context, req *listRequest) (*listRequest, error) {
			return req, nil
		})

	rr := serve(engine, http.MethodGet, "/api/v1/order/orders", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body)
	}
	data, _ := decode(t, rr)["data"].(map[string]any)
	if data["page"] != float64(1) || data["size"] != float64(20) {
		t.Fatalf("defaults not applied: %v", data)
	}

	rr = serve(engine, http.MethodGet, "/api/v1/order/orders?size=101", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	details, _ := decode(t, rr)["details"].(map[string]any)
	if details["size"] != "每页最多100条" {
		t.Fatalf("details.size = %v", details["size"])
	}
}
