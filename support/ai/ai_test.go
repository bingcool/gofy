package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// compatConfig points one alias at baseURL. The alias name deliberately differs
// from the wire model id, so the tests can tell which one is reported back.
// compatConfig 把一个 alias 指向 baseURL。alias 名刻意与 wire model id 不同，
// 测试据此分辨返回的是哪一个。
func compatConfig(baseURL string) Config {
	return Config{
		Enabled:      true,
		DefaultModel: "qwen",
		Models: map[string]ModelConfig{
			"qwen": {
				Provider:   ProviderOpenAICompatible,
				Model:      "qwen-max",
				BaseURL:    baseURL,
				APIKey:     "sk-test-key-123456",
				ProviderID: "qwen",
			},
		},
	}
}

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestValidate(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	zeroRetries := 0
	tooManyRetries := 11

	cases := []struct {
		name    string
		cfg     Config
		wantErr error
		// wantAny is for rows whose exact error is an implementation detail;
		// they only pin "this configuration must be rejected".
		wantAny bool
	}{
		{
			name: "absent section is not an error",
			// A deployment without LLM access has no llm_ai section at all; the
			// zero value must validate instead of demanding models.
			// 不接大模型的部署根本没有 llm_ai 段，零值必须直接通过，而不是要求
			// 提供 models。
			cfg: Config{},
		},
		{
			name: "disabled section skips every other check",
			cfg: Config{Enabled: false, Models: map[string]ModelConfig{
				"broken": {Provider: "nope"},
			}},
		},
		{
			name:    "enabled without models",
			cfg:     Config{Enabled: true},
			wantErr: ErrNoModels,
		},
		{
			name: "minimal compatible model",
			cfg:  compatConfig("https://example.com/v1"),
		},
		{
			name: "unknown provider",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: "ollama", Model: "llama3"},
			}},
			wantErr: ErrProviderUnknown,
		},
		{
			name: "compatible without base url",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: ProviderOpenAICompatible, Model: "qwen-max"},
			}},
			wantErr: ErrBaseURLRequired,
		},
		{
			name: "default model missing",
			cfg: Config{
				Enabled:      true,
				DefaultModel: "nope",
				Models:       compatConfig("https://example.com/v1").Models,
			},
			wantErr: ErrModelNotFound,
		},
		{
			name: "relative base url",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: ProviderOpenAICompatible, Model: "qwen-max", BaseURL: "/v1"},
			}},
			wantAny: true,
		},
		{
			name: "temperature out of range",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: ProviderOpenAI, Model: "gpt-4o-mini", APIKey: "sk-1234567890", Temperature: f(2.5)},
			}},
			wantAny: true,
		},
		{
			name: "retries out of range",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: ProviderOpenAI, Model: "gpt-4o-mini", APIKey: "sk-1234567890", MaxRetries: &tooManyRetries},
			}},
			wantAny: true,
		},
		{
			name: "alias with space",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"bad alias": {Provider: ProviderOpenAI, Model: "gpt-4o-mini", APIKey: "sk-1234567890"},
			}},
			wantAny: true,
		},
		{
			name: "zero retries is allowed",
			cfg: Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: ProviderOpenAI, Model: "gpt-4o-mini", APIKey: "sk-1234567890", MaxRetries: &zeroRetries},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantAny {
				if err == nil {
					t.Fatalf("Validate accepted %+v", tc.cfg)
				}
				return
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestResolveAPIKeyPrecedence(t *testing.T) {
	t.Setenv("TEST_AI_NAMED_KEY", "from-named-env")
	t.Setenv("DEEPSEEK_API_KEY", "from-process-env")

	base := ModelConfig{Provider: ProviderDeepSeek, Model: "deepseek-chat"}

	t.Run("literal api_key wins", func(t *testing.T) {
		m := base
		m.APIKey = "explicit"
		got, err := resolveAPIKey(m)
		if err != nil || got != "explicit" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("api_key ${VAR} is read", func(t *testing.T) {
		m := base
		m.APIKey = "${TEST_AI_NAMED_KEY}"
		got, err := resolveAPIKey(m)
		if err != nil || got != "from-named-env" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("api_key ${VAR} is expanded inside a longer value", func(t *testing.T) {
		m := base
		m.APIKey = "prefix-${TEST_AI_NAMED_KEY}"
		got, err := resolveAPIKey(m)
		if err != nil || got != "prefix-from-named-env" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("literal key containing $ is left alone", func(t *testing.T) {
		m := base
		m.APIKey = "sk-a$b-c"
		got, err := resolveAPIKey(m)
		if err != nil || got != "sk-a$b-c" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("provider env last", func(t *testing.T) {
		got, err := resolveAPIKey(base)
		if err != nil || got != "from-process-env" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("missing ${VAR} is an error", func(t *testing.T) {
		m := base
		m.APIKey = "${TEST_AI_ABSENT_KEY}"
		if _, err := resolveAPIKey(m); !errors.Is(err, ErrAPIKeyRequired) {
			t.Fatalf("err = %v, want ErrAPIKeyRequired", err)
		}
	})

	t.Run("compatible may run without a key", func(t *testing.T) {
		m := ModelConfig{Provider: ProviderOpenAICompatible, Model: "qwen-max", BaseURL: "https://example.com/v1"}
		got, err := resolveAPIKey(m)
		if err != nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

// TestNewRejectsDisabledConfig pins the switch: enabled:false — or no llm_ai
// section at all — is ErrDisabled at construction, not a client that fails on
// the first request.
// TestNewRejectsDisabledConfig 固化开关语义：enabled 为 false（或整段缺失）在构造期
// 返回 ErrDisabled，而不是一个要到第一次请求才失败的客户端。
func TestNewRejectsDisabledConfig(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("absent section: err = %v, want ErrDisabled", err)
	}
	disabled := compatConfig("https://example.com/v1")
	disabled.Enabled = false
	if _, err := New(disabled); !errors.Is(err, ErrDisabled) {
		t.Fatalf("enabled:false: err = %v, want ErrDisabled", err)
	}
}

// TestNewRequiresCredentialForHostedProviders pins fail-fast: a missing key is
// a startup error, not a 401 on the first request.
// TestNewRequiresCredentialForHostedProviders 固化 fail-fast：缺 key 是启动错误，
// 不是第一条请求的 401。
func TestNewRequiresCredentialForHostedProviders(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	for _, provider := range []string{ProviderOpenAI, ProviderDeepSeek} {
		t.Run(provider, func(t *testing.T) {
			_, err := New(Config{Enabled: true, Models: map[string]ModelConfig{
				"x": {Provider: provider, Model: "some-model"},
			}})
			if !errors.Is(err, ErrAPIKeyRequired) {
				t.Fatalf("err = %v, want ErrAPIKeyRequired", err)
			}
		})
	}
}

// TestNewReadsButNeverWritesEnvironment is the contract behind "no export_env":
// api_key: ${VAR} is resolved, and the process environment is left untouched.
// TestNewReadsButNeverWritesEnvironment 是"不做 export_env"的契约：api_key 里的
// ${VAR} 会被读取，而进程环境保持原样。
func TestNewReadsButNeverWritesEnvironment(t *testing.T) {
	t.Setenv("TEST_AI_RW_KEY", "sk-from-env-abcdef")
	before := envSnapshot()

	_, err := New(Config{Enabled: true, Models: map[string]ModelConfig{
		"qwen": {
			Provider: ProviderOpenAICompatible,
			Model:    "qwen-max",
			BaseURL:  "https://example.com/v1",
			APIKey:   "${TEST_AI_RW_KEY}",
		},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	after := envSnapshot()
	if !slices.Equal(before, after) {
		t.Fatalf("New changed the process environment:\nbefore=%v\nafter=%v", before, after)
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		t.Fatal("New exported a provider key")
	}
}

func envSnapshot() []string {
	env := os.Environ()
	slices.Sort(env)
	return env
}

func TestChatAgainstOpenAICompatibleEndpoint(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"qwen-max",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`)
	}))
	defer srv.Close()

	client := newTestClient(t, compatConfig(srv.URL))

	temp := 0.3
	resp, err := client.Chat(context.Background(), ChatRequest{
		Prompt:      "hi",
		System:      "be brief",
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test-key-123456" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if resp.Text != "hello there" {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.ModelAlias != "qwen" {
		t.Errorf("model_alias = %q, want the alias", resp.ModelAlias)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 4 || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	var sent struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		Messages    []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("decode request: %v (%s)", err, gotBody)
	}
	if sent.Model != "qwen-max" {
		t.Errorf("wire model = %q", sent.Model)
	}
	if sent.Temperature != 0.3 {
		t.Errorf("temperature = %v", sent.Temperature)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != "system" || sent.Messages[1].Content != "hi" {
		t.Errorf("messages = %+v", sent.Messages)
	}
}

func TestChatRejectsUnknownAliasAndEmptyPrompt(t *testing.T) {
	client := newTestClient(t, compatConfig("https://example.com/v1"))

	if _, err := client.Chat(context.Background(), ChatRequest{ModelAlias: "absent", Prompt: "hi"}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
	if _, err := client.Chat(context.Background(), ChatRequest{Prompt: "   "}); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("err = %v, want ErrEmptyPrompt", err)
	}
}

func TestStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"Hel"},"index":0}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"lo"},"index":0}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, c := range chunks {
			_, _ = fmt.Fprint(w, c)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	client := newTestClient(t, compatConfig(srv.URL))

	stream, err := client.Stream(context.Background(), ChatRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	for delta := range stream.Text() {
		text.WriteString(delta)
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	res := stream.Result()
	if res == nil || res.Text != "Hello" {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","model":"text-embedding-3-small",`+
			`"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],`+
			`"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer srv.Close()

	cfg := compatConfig(srv.URL)
	m := cfg.Models["qwen"]
	m.Embedding = EmbeddingConfig{Model: "text-embedding-3-small", Dimensions: 3}
	cfg.Models["qwen"] = m

	client := newTestClient(t, cfg)
	resp, err := client.Embed(context.Background(), "", []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 1 || len(resp.Embeddings[0]) != 3 {
		t.Fatalf("embeddings = %+v", resp.Embeddings)
	}
	if resp.ModelAlias != "qwen" {
		t.Errorf("model_alias = %q", resp.ModelAlias)
	}
}

// TestEmbedRejectsDimensionMismatch covers the case the configuration promises
// one width and the endpoint returns another: better an error than vectors that
// silently fail a later similarity check.
// TestEmbedRejectsDimensionMismatch 覆盖"配置声明一种宽度、端点返回另一种"：报错
// 好过让向量在后面相似度计算里静默出错。
func TestEmbedRejectsDimensionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","model":"m","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer srv.Close()

	cfg := compatConfig(srv.URL)
	m := cfg.Models["qwen"]
	m.Embedding = EmbeddingConfig{Model: "m", Dimensions: 3}
	cfg.Models["qwen"] = m

	client := newTestClient(t, cfg)
	if _, err := client.Embed(context.Background(), "qwen", []string{"hello"}); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
}

func TestEmbedWithoutEmbeddingModel(t *testing.T) {
	client := newTestClient(t, compatConfig("https://example.com/v1"))
	if _, err := client.Embed(context.Background(), "qwen", []string{"x"}); !errors.Is(err, ErrNoEmbedding) {
		t.Fatalf("err = %v, want ErrNoEmbedding", err)
	}
}

func TestErrorRedactionKeepsChain(t *testing.T) {
	key := "sk-secret-abcdef123456"
	client := &Client{secrets: []string{key}}

	sentinel := errors.New("upstream said no")
	cause := fmt.Errorf("401 for key %s: %w", key, sentinel)
	err := client.errFor("qwen", cause)

	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaks the key: %s", err)
	}
	if !strings.Contains(err.Error(), redacted) {
		t.Fatalf("error does not mark the redaction: %s", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("redaction broke the error chain")
	}
}

func TestTruncateCollapsesAndCuts(t *testing.T) {
	if got := truncate("a\nb   c", 100); got != "a b c" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("中文内容", 2); got != "中文..." {
		t.Fatalf("truncate = %q", got)
	}
}

func TestHostAllowedMatchesOnLabelBoundary(t *testing.T) {
	allow := []string{".aliyuncs.com", "internal.example"}
	if !hostAllowed("dashscope.aliyuncs.com", allow) {
		t.Fatal("expected suffix match")
	}
	if !hostAllowed("api.internal.example", allow) {
		t.Fatal("expected bare domain to match its subdomain")
	}
	if hostAllowed("evil-aliyuncs.com", allow) {
		t.Fatal("suffix must not match a longer label")
	}
}

func TestWithDefaultsFillsModelDefaults(t *testing.T) {
	cfg := compatConfig("https://example.com/v1").withDefaults()
	m := cfg.Models["qwen"]
	if m.Timeout != DefaultTimeout {
		t.Errorf("timeout = %v", m.Timeout)
	}
	if m.MaxRetries == nil || *m.MaxRetries != DefaultMaxRetries {
		t.Errorf("max_retries = %v", m.MaxRetries)
	}
	if m.MaxLogChars != DefaultMaxLogChars {
		t.Errorf("max_log_chars = %d", m.MaxLogChars)
	}
	if cfg.HTTPClient.MaxIdleConns != DefaultMaxIdleConns {
		t.Errorf("max_idle_conns = %d", cfg.HTTPClient.MaxIdleConns)
	}
}

// TestChatJSON drives the structured-output path. The stub answers with a JSON
// body regardless of the response_format it receives, which is exactly how a
// compatible gateway that ignores the schema behaves.
// TestChatJSON 验证结构化输出路径。桩服务不理会收到的 response_format，直接返回
// JSON 正文——这正是"忽略 schema 的兼容网关"的行为。
func TestChatJSON(t *testing.T) {
	var sawResponseFormat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawResponseFormat = strings.Contains(string(body), "response_format")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"qwen-max",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"{\"name\":\"Ada\",\"age\":36}"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":8,"completion_tokens":6,"total_tokens":14}}`)
	}))
	defer srv.Close()

	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	client := newTestClient(t, compatConfig(srv.URL))
	resp, err := ChatJSON[person](context.Background(), client, ChatRequest{Prompt: "who"})
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if resp.Object.Name != "Ada" || resp.Object.Age != 36 {
		t.Fatalf("object = %+v", resp.Object)
	}
	if !sawResponseFormat {
		t.Error("request did not carry a response_format")
	}
}

func TestUsageCallbackReceivesOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"qwen-max",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	var events []UsageEvent
	client, err := New(compatConfig(srv.URL), WithOnUsage(func(ev UsageEvent) {
		events = append(events, ev)
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Chat(context.Background(), ChatRequest{Prompt: "hi"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].Alias != "qwen" || events[0].Provider != ProviderOpenAICompatible || events[0].Model != "qwen-max" {
		t.Errorf("event = %+v", events[0])
	}
	if events[0].Err != nil {
		t.Errorf("unexpected error: %v", events[0].Err)
	}
}

func TestClosedClientRejectsCalls(t *testing.T) {
	client, err := New(compatConfig("https://example.com/v1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := client.Chat(context.Background(), ChatRequest{Prompt: "hi"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	var nilClient *Client
	if err := nilClient.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestCallTimeoutIsEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(600 * time.Millisecond):
		}
	}))
	defer srv.Close()

	cfg := compatConfig(srv.URL)
	m := cfg.Models["qwen"]
	m.Timeout = 100 * time.Millisecond
	cfg.Models["qwen"] = m

	client := newTestClient(t, cfg)

	started := time.Now()
	_, err := client.Chat(context.Background(), ChatRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %v, the per-call deadline was not applied", elapsed)
	}
}
