package ai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	supportai "github.com/bingcool/gofy/support/ai"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// stubGateway answers like an OpenAI-compatible endpoint. The demo module is
// tested against it so the handlers, the client and the wire format are all
// exercised without a real key.
// stubGateway 扮演 OpenAI 兼容端点。demo 模块对它测试，因此处理器、客户端与
// wire 格式都被覆盖，且不需要真实 key。
func stubGateway(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"model":  "qwen-max",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func newTestModule(t *testing.T, baseURL string) (*Module, *gin.Engine) {
	t.Helper()
	client, err := supportai.New(supportai.Config{
		Enabled:      true,
		DefaultModel: "qwen",
		Models: map[string]supportai.ModelConfig{
			"qwen": {
				Provider: supportai.ProviderOpenAICompatible,
				Model:    "qwen-max",
				BaseURL:  baseURL,
				APIKey:   "sk-demo-test-123456",
			},
		},
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	m := NewModule(client, zap.NewNop())
	engine.GET("/models", m.listModels)
	engine.POST("/chat", m.chat)
	return m, engine
}

func TestChatHandlerReturnsTextAndUsage(t *testing.T) {
	srv := stubGateway(t, "幂等就是执行多次等于执行一次")
	defer srv.Close()

	_, engine := newTestModule(t, srv.URL)

	body := bytes.NewBufferString(`{"prompt":"解释幂等","max_output_tokens":64}`)
	req := httptest.NewRequest(http.MethodPost, "/chat", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Text        string `json:"text"`
		Model       string `json:"model"`
		TotalTokens int    `json:"total_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Text != "幂等就是执行多次等于执行一次" {
		t.Errorf("text = %q", got.Text)
	}
	if got.Model != "qwen" {
		t.Errorf("model = %q, want the alias", got.Model)
	}
	if got.TotalTokens != 10 {
		t.Errorf("total_tokens = %d", got.TotalTokens)
	}
}

func TestChatHandlerRejectsEmptyPrompt(t *testing.T) {
	srv := stubGateway(t, "unused")
	defer srv.Close()

	_, engine := newTestModule(t, srv.URL)

	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestListModelsShowsConfiguredAliases(t *testing.T) {
	srv := stubGateway(t, "unused")
	defer srv.Close()

	_, engine := newTestModule(t, srv.URL)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Default string   `json:"default"`
		Models  []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Default != "qwen" || len(got.Models) != 1 || got.Models[0] != "qwen" {
		t.Fatalf("payload = %+v", got)
	}
}

// TestSummaryHandlerDecodesIntoType pins the point of ChatJSON: the handler
// receives a typed object, not a string it has to parse.
// TestSummaryHandlerDecodesIntoType 固化 ChatJSON 的意义：处理器拿到的是有类型的
// 对象，而不是需要自己解析的字符串。
func TestSummaryHandlerDecodesIntoType(t *testing.T) {
	srv := stubGateway(t, `{"title":"框架接入","tags":["go","ai"]}`)
	defer srv.Close()

	m, engine := newTestModule(t, srv.URL)
	engine.POST("/summary", m.summary)

	req := httptest.NewRequest(http.MethodPost, "/summary", bytes.NewBufferString(`{"text":"一篇关于框架接入的文章"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result summaryResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Result.Title != "框架接入" || len(got.Result.Tags) != 2 {
		t.Fatalf("result = %+v", got.Result)
	}
}
