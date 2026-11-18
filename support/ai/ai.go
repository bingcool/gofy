package ai

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goai "github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"go.uber.org/zap"
)

// Option customizes a Client.
// Option 定制 Client。
type Option func(*settings)

type settings struct {
	logger     *zap.Logger
	onUsage    func(UsageEvent)
	httpClient *http.Client
}

// WithLogger sets the logger used for call records and configuration warnings.
// A nil logger falls back to a no-op logger.
// WithLogger 设置调用记录与配置告警使用的 logger；nil 退化为 no-op。
func WithLogger(l *zap.Logger) Option {
	return func(s *settings) { s.logger = l }
}

// WithOnUsage registers a callback for token usage and call outcomes.
// WithOnUsage 注册 token 用量与调用结果的回调。
func WithOnUsage(fn func(UsageEvent)) Option {
	return func(s *settings) { s.onUsage = fn }
}

// WithHTTPClient injects a transport. The Client then does not own it: Close
// still closes nothing on it, so a caller sharing one pool across clients keeps
// control. Tests use it to route requests at an httptest server without
// touching configuration.
// WithHTTPClient 注入传输层。此后 Client 不拥有它，Close 不会关它的空闲连接，
// 因此多个 Client 共享一个连接池时控制权留在调用方；测试用它把请求导向 httptest。
func WithHTTPClient(c *http.Client) Option {
	return func(s *settings) { s.httpClient = c }
}

// Client holds one constructed model per configured alias. It is safe for
// concurrent use: the models and their option slices are read-only after New,
// and goai models are stateless between calls.
// Client 为每个配置的 alias 持有已构造好的模型。它可并发使用：New 之后模型与
// 选项切片只读，goai 的模型在调用之间无状态。
type Client struct {
	models    map[string]provider.LanguageModel
	embeds    map[string]provider.EmbeddingModel
	defaults  map[string][]goai.Option
	embedOpts map[string][]goai.Option
	meta      map[string]ModelConfig
	aliases   []string

	defaultAlias string
	logger       *zap.Logger
	onUsage      func(UsageEvent)
	secrets      []string

	httpClient *http.Client
	ownClient  bool

	closed    atomic.Bool
	closeOnce sync.Once
}

// New constructs every configured model. A failure on any alias fails the
// whole call: a client that silently dropped one model would turn a
// configuration mistake into a runtime "alias not found" much later.
//
// A disabled config — an absent llm_ai section, or enabled: false — returns
// ErrDisabled before anything is built. That is the only error this function
// returns for "there is no AI in this deployment".
//
// Key resolution — including reading ${VAR} in api_key — happens here, so a
// missing credential is a startup error.
// New 构造全部配置的模型。任一 alias 失败即整体失败：静默丢掉一个模型会把配置
// 错误推迟成很久以后的"alias 找不到"。
//
// 关闭的配置（缺少 llm_ai 段，或 enabled: false）在构建任何东西之前返回
// ErrDisabled。这是它面对"这个部署不接大模型"时唯一会返回的错误。
//
// 凭证解析（含读取 api_key 里的 ${VAR}）在这里完成，因此缺凭证是启动错误。
func New(cfg Config, opts ...Option) (*Client, error) {
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s := settings{logger: zap.NewNop()}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	if s.logger == nil {
		s.logger = zap.NewNop()
	}

	client := s.httpClient
	own := false
	if client == nil {
		built, err := newHTTPClient(cfg.HTTPClient)
		if err != nil {
			return nil, err
		}
		client = built
		own = true
	}

	c := &Client{
		models:       make(map[string]provider.LanguageModel, len(cfg.Models)),
		embeds:       make(map[string]provider.EmbeddingModel),
		defaults:     make(map[string][]goai.Option, len(cfg.Models)),
		embedOpts:    make(map[string][]goai.Option),
		meta:         make(map[string]ModelConfig, len(cfg.Models)),
		defaultAlias: cfg.DefaultModel,
		logger:       s.logger,
		onUsage:      s.onUsage,
		httpClient:   client,
		ownClient:    own,
	}

	aliases := make([]string, 0, len(cfg.Models))
	for alias := range cfg.Models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	c.aliases = aliases

	for _, alias := range aliases {
		m := cfg.Models[alias]

		key, err := resolveAPIKey(m)
		if err != nil {
			return nil, fmt.Errorf("ai: model %q: %w", alias, err)
		}

		model, err := newLanguageModel(m, key, client)
		if err != nil {
			return nil, fmt.Errorf("ai: model %q: %w", alias, err)
		}

		c.models[alias] = model
		c.defaults[alias] = m.callDefaults()
		c.meta[alias] = m
		if key != "" {
			c.secrets = append(c.secrets, key)
		}

		if strings.TrimSpace(m.Embedding.Model) != "" {
			embed, err := newEmbeddingModel(m, key, client)
			if err != nil {
				return nil, fmt.Errorf("ai: model %q: %w", alias, err)
			}
			c.embeds[alias] = embed
			c.embedOpts[alias] = m.embeddingOptions()
		}
	}

	return c, nil
}

// Close releases the idle connections of a pool the Client created. It is nil
// safe, idempotent, and does nothing to an injected client.
// Close 关闭 Client 自己创建的连接池里的空闲连接。nil 安全、可重复调用，对注入的
// client 不做任何事。
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.ownClient && c.httpClient != nil {
			c.httpClient.CloseIdleConnections()
		}
	})
	return nil
}

// Aliases returns the configured aliases in sorted order.
// Aliases 返回已配置的 alias，按字典序。
func (c *Client) Aliases() []string {
	if c == nil {
		return nil
	}
	return slices.Clone(c.aliases)
}

// Default returns the alias used when a request names none.
// Default 返回请求未指定 alias 时使用的名字。
func (c *Client) Default() string {
	if c == nil {
		return ""
	}
	return c.defaultAlias
}

// Chat runs one non-streaming generation.
// Chat 执行一次非流式生成。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	p, err := c.prepare(req)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	result, err := goai.GenerateText(ctx, p.model, p.opts...)
	elapsed := time.Since(started)
	if err != nil {
		return nil, c.fail(p.alias, elapsed, err)
	}

	resp := &ChatResponse{
		Text:         result.Text,
		Reasoning:    result.Reasoning,
		ToolCalls:    result.ToolCalls,
		FinishReason: result.FinishReason,
		Usage:        usageFrom(result.TotalUsage),
		ModelAlias:   p.alias,
		Elapsed:      elapsed,
	}
	c.report(UsageEvent{
		Alias:    p.alias,
		Provider: c.meta[p.alias].Provider,
		Model:    c.meta[p.alias].Model,
		Usage:    resp.Usage,
		Elapsed:  elapsed,
	})
	c.logCall(p.alias, resp.Usage, elapsed, result.FinishReason, "", req)
	return resp, nil
}

// ChatJSON runs a generation constrained to a JSON schema derived from T.
// It is a free function because Go methods cannot declare type parameters.
// ChatJSON 执行一次按 T 推导出 JSON schema 的生成。它是自由函数，因为 Go 的方法
// 不能声明类型参数。
func ChatJSON[T any](ctx context.Context, c *Client, req ChatRequest) (*JSONResponse[T], error) {
	p, err := c.prepare(req)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	result, err := goai.GenerateObject[T](ctx, p.model, p.opts...)
	elapsed := time.Since(started)
	if err != nil {
		return nil, c.fail(p.alias, elapsed, err)
	}

	resp := &JSONResponse[T]{
		Object:       result.Object,
		Usage:        usageFrom(result.Usage),
		FinishReason: result.FinishReason,
		ModelAlias:   p.alias,
		Elapsed:      elapsed,
	}
	c.report(UsageEvent{
		Alias:    p.alias,
		Provider: c.meta[p.alias].Provider,
		Model:    c.meta[p.alias].Model,
		Usage:    resp.Usage,
		Elapsed:  elapsed,
	})
	c.logCall(p.alias, resp.Usage, elapsed, result.FinishReason, "", req)
	return resp, nil
}

// Embed converts values into vectors using the alias' embedding model. An
// empty alias uses the default model, which must declare embedding.model.
// Embed 用 alias 的 embedding 模型把 values 转成向量。alias 留空表示默认模型，
// 该模型必须声明 embedding.model。
func (c *Client) Embed(ctx context.Context, alias string, values []string) (*EmbedResponse, error) {
	if c == nil {
		return nil, ErrClosed
	}
	if c.closed.Load() {
		return nil, ErrClosed
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = c.defaultAlias
	}
	if alias == "" {
		return nil, fmt.Errorf("%w: no model selected and no default_model configured", ErrModelNotFound)
	}
	embed, ok := c.embeds[alias]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoEmbedding, alias)
	}
	if len(values) == 0 {
		return nil, nil
	}

	started := time.Now()
	result, err := goai.EmbedMany(ctx, embed, values, c.embedOpts[alias]...)
	elapsed := time.Since(started)
	if err != nil {
		return nil, c.fail(alias, elapsed, err)
	}

	if want := c.meta[alias].Embedding.Dimensions; want > 0 {
		for i, vec := range result.Embeddings {
			if len(vec) != want {
				return nil, fmt.Errorf("%w: embedding %d has %d dimensions, want %d",
					ErrInvalidResponse, i, len(vec), want)
			}
		}
	}

	resp := &EmbedResponse{
		Embeddings: result.Embeddings,
		Usage:      usageFrom(result.Usage),
		ModelAlias: alias,
		Elapsed:    elapsed,
	}
	c.report(UsageEvent{
		Alias:    alias,
		Provider: c.meta[alias].Provider,
		Model:    c.meta[alias].Embedding.Model,
		Usage:    resp.Usage,
		Elapsed:  elapsed,
	})
	return resp, nil
}

// prepared is one resolved call: alias, model and the merged option list.
// prepared 是一次解析完成的调用：alias、模型与合并后的选项。
type prepared struct {
	alias string
	model provider.LanguageModel
	opts  []goai.Option
}

// prepare resolves the alias and merges the alias defaults with the
// per-request overrides. The defaults slice is cloned: appending to the shared
// slice would let two concurrent calls write the same backing array.
// prepare 解析 alias，并把 alias 默认值与单次覆盖合并。默认值切片必须克隆：直接
// 在共享切片上 append 会让两个并发调用写到同一个底层数组。
func (c *Client) prepare(req ChatRequest) (prepared, error) {
	if c == nil {
		return prepared{}, ErrClosed
	}
	if c.closed.Load() {
		return prepared{}, ErrClosed
	}

	alias := strings.TrimSpace(req.ModelAlias)
	if alias == "" {
		alias = c.defaultAlias
	}
	if alias == "" {
		return prepared{}, fmt.Errorf("%w: no model selected and no default_model configured", ErrModelNotFound)
	}
	model, ok := c.models[alias]
	if !ok {
		return prepared{}, fmt.Errorf("%w: %q (configured: %s)", ErrModelNotFound, alias, strings.Join(c.aliases, ", "))
	}

	opts := slices.Clone(c.defaults[alias])
	if req.System != "" {
		opts = append(opts, goai.WithSystem(req.System))
	}
	switch {
	case len(req.Messages) > 0:
		opts = append(opts, goai.WithMessages(req.Messages...))
	case strings.TrimSpace(req.Prompt) != "":
		opts = append(opts, goai.WithPrompt(req.Prompt))
	default:
		return prepared{}, ErrEmptyPrompt
	}
	if len(req.Tools) > 0 {
		opts = append(opts, goai.WithTools(req.Tools...))
	}
	if req.Temperature != nil {
		opts = append(opts, goai.WithTemperature(*req.Temperature))
	}
	if req.MaxOutputTokens > 0 {
		opts = append(opts, goai.WithMaxOutputTokens(req.MaxOutputTokens))
	}
	if req.TopP != nil {
		opts = append(opts, goai.WithTopP(*req.TopP))
	}
	if req.MaxSteps > 0 {
		opts = append(opts, goai.WithMaxSteps(req.MaxSteps))
	}
	if len(req.ProviderOptions) > 0 {
		// Merge instead of replace: a request usually tunes one key and should
		// not drop the alias' other provider options.
		// 合并而不是替换：请求通常只调一个键，不该抹掉 alias 的其它 provider 选项。
		merged := make(map[string]any, len(c.meta[alias].ProviderOptions)+len(req.ProviderOptions))
		for k, v := range c.meta[alias].ProviderOptions {
			merged[k] = v
		}
		for k, v := range req.ProviderOptions {
			merged[k] = v
		}
		opts = append(opts, goai.WithProviderOptions(merged))
	}

	return prepared{alias: alias, model: model, opts: opts}, nil
}

// errFor wraps a provider error with the alias and strips any credential that
// the provider echoed back. The chain is preserved, so errors.Is/errors.As and
// the status helpers keep working on the result.
// errFor 给 provider 错误补上 alias，并抹掉 provider 回显的任何凭证。错误链保留，
// 因此 errors.Is/errors.As 与状态码辅助对结果仍然有效。
func (c *Client) errFor(alias string, err error) error {
	if err == nil {
		return nil
	}
	return redactError(fmt.Errorf("ai: %s: %w", alias, err), c.secrets)
}

// fail wraps a provider error with the alias, redacts credentials and reports
// it. Every error leaving this package has passed through here or prepare.
// fail 给 provider 错误补上 alias、脱敏并上报。本包对外返回的错误都经过这里或
// prepare。
func (c *Client) fail(alias string, elapsed time.Duration, err error) error {
	wrapped := c.errFor(alias, err)
	c.report(UsageEvent{
		Alias:    alias,
		Provider: c.meta[alias].Provider,
		Model:    c.meta[alias].Model,
		Elapsed:  elapsed,
		Err:      wrapped,
	})
	if c.logger != nil {
		c.logger.Warn("llm call failed",
			zap.String("alias", alias),
			zap.String("provider", c.meta[alias].Provider),
			zap.String("model", c.meta[alias].Model),
			zap.Int64("elapsed_ms", elapsed.Milliseconds()),
			zap.Int("status_code", StatusCode(err)),
			zap.Bool("retryable", IsRetryable(err)),
			zap.Error(wrapped),
		)
	}
	return wrapped
}

// report invokes the usage callback, isolating the client from a panicking
// callback is not attempted: a user callback that panics is a bug that should
// surface, and the call has already completed.
// report 触发用量回调。这里不隔离回调 panic：用户回调 panic 是应当暴露的 bug，
// 而且此时调用已经结束。
func (c *Client) report(ev UsageEvent) {
	if c.onUsage == nil {
		return
	}
	c.onUsage(ev)
}

// logCall records one successful generation. Prompts and completions are only
// logged when the alias opted in, and then truncated.
// logCall 记录一次成功生成。prompt 与 completion 只在 alias 显式开启时记录，
// 且会截断。
func (c *Client) logCall(alias string, usage Usage, elapsed time.Duration, finish provider.FinishReason, text string, req ChatRequest) {
	if c.logger == nil {
		return
	}
	m := c.meta[alias]
	fields := []zap.Field{
		zap.String("alias", alias),
		zap.String("provider", m.Provider),
		zap.String("model", m.Model),
		zap.Int64("elapsed_ms", elapsed.Milliseconds()),
		zap.Int("input_tokens", usage.InputTokens),
		zap.Int("output_tokens", usage.OutputTokens),
		zap.String("finish_reason", string(finish)),
	}
	if m.LogPrompt {
		fields = append(fields, zap.String("prompt", truncate(req.Prompt, m.MaxLogChars)))
	}
	if m.LogCompletion && text != "" {
		fields = append(fields, zap.String("completion", truncate(text, m.MaxLogChars)))
	}
	c.logger.Info("llm call", fields...)
}

// callDefaults turns the alias configuration into goai call options. Zero
// values mean "leave it to the provider"; the pointer fields distinguish that
// from an explicit zero.
// callDefaults 把 alias 配置转成 goai 的调用选项。零值表示"交给 provider"，
// 指针字段用来与显式零值区分。
func (m ModelConfig) callDefaults() []goai.Option {
	opts := make([]goai.Option, 0, 14)
	if m.Timeout > 0 {
		opts = append(opts, goai.WithTimeout(m.Timeout))
	}
	if m.MaxRetries != nil {
		opts = append(opts, goai.WithMaxRetries(*m.MaxRetries))
	}
	if m.Temperature != nil {
		opts = append(opts, goai.WithTemperature(*m.Temperature))
	}
	if m.MaxOutputTokens > 0 {
		opts = append(opts, goai.WithMaxOutputTokens(m.MaxOutputTokens))
	}
	if m.TopP != nil {
		opts = append(opts, goai.WithTopP(*m.TopP))
	}
	if m.TopK > 0 {
		opts = append(opts, goai.WithTopK(m.TopK))
	}
	if m.FrequencyPenalty != nil {
		opts = append(opts, goai.WithFrequencyPenalty(*m.FrequencyPenalty))
	}
	if m.PresencePenalty != nil {
		opts = append(opts, goai.WithPresencePenalty(*m.PresencePenalty))
	}
	if m.Seed != 0 {
		opts = append(opts, goai.WithSeed(m.Seed))
	}
	if len(m.StopSequences) > 0 {
		opts = append(opts, goai.WithStopSequences(m.StopSequences...))
	}
	if m.MaxSteps > 0 {
		opts = append(opts, goai.WithMaxSteps(m.MaxSteps))
	}
	if m.ToolChoice != "" {
		opts = append(opts, goai.WithToolChoice(m.ToolChoice))
	}
	if m.SequentialToolExecution {
		opts = append(opts, goai.WithSequentialToolExecution())
	}
	if m.MaxParallelCalls > 0 {
		opts = append(opts, goai.WithMaxParallelCalls(m.MaxParallelCalls))
	}
	if len(m.ProviderOptions) > 0 {
		opts = append(opts, goai.WithProviderOptions(m.ProviderOptions))
	}
	if m.PromptCaching {
		opts = append(opts, goai.WithPromptCaching(true))
	}
	if m.CacheTTL != "" {
		opts = append(opts, goai.WithCacheTTL(m.CacheTTL))
	}
	return opts
}

// embeddingOptions carries the dimension hint to the provider.
// embeddingOptions 把维度提示传给 provider。
func (m ModelConfig) embeddingOptions() []goai.Option {
	if m.Embedding.Dimensions <= 0 {
		return nil
	}
	return []goai.Option{
		goai.WithEmbeddingProviderOptions(map[string]any{"dimensions": m.Embedding.Dimensions}),
	}
}
