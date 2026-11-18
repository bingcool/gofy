// Package ai provides a configuration-driven LLM client on top of
// github.com/zendev-sh/goai. An application declares a set of named models
// (aliases) once, and business code refers to them by that name instead of
// carrying provider details around.
//
// The package supports three providers: OpenAI, DeepSeek and any endpoint that
// speaks the OpenAI wire protocol ("openai_compatible": Qwen, Ernie, GLM,
// Moonshot, a self-hosted vLLM or LiteLLM gateway). Vendor differences are
// expressed through per-alias options and provider_options rather than code
// branches.
//
// Package ai 在 goai 之上提供"配置驱动"的 LLM 客户端：应用一次声明若干具名模型
// （alias），业务代码只按名字引用，不再关心 provider 细节。
//
// 支持三类 provider：OpenAI、DeepSeek，以及任何讲 OpenAI 协议的端点
// （openai_compatible：千问、文心、智谱、Moonshot、自建 vLLM/LiteLLM 网关）。
// 厂商差异通过每个 alias 的参数与 provider_options 表达，而不是代码分支。
package ai

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Supported provider names. There is deliberately no per-vendor constant: any
// other OpenAI-compatible endpoint is configured as ProviderOpenAICompatible
// with its own base_url.
// 支持的 provider 名。这里刻意不为厂商单列常量：其他 OpenAI 兼容端点一律用
// ProviderOpenAICompatible 加各自的 base_url 表达。
const (
	ProviderOpenAI           = "openai"
	ProviderDeepSeek         = "deepseek"
	ProviderOpenAICompatible = "openai_compatible"
)

// Defaults applied by withDefaults. They are duplicated in the yaml tags so
// config.Load produces the same values before New ever sees the struct.
// withDefaults 使用的默认值；yaml tag 里同步一份，使 config.Load 在 New 之前
// 就得到相同的结果。
const (
	DefaultTimeout     = 60 * time.Second
	DefaultMaxRetries  = 2
	DefaultMaxLogChars = 512

	DefaultMaxIdleConns          = 100
	DefaultIdleConnTimeout       = 90 * time.Second
	DefaultResponseHeaderTimeout = 30 * time.Second
)

// providerNames is the ordered list used in error messages.
// providerNames 是错误信息里使用的有序列表。
var providerNames = []string{ProviderOpenAI, ProviderDeepSeek, ProviderOpenAICompatible}

// Config is the llm_ai section of an application's config.yaml.
// Config 是应用 config.yaml 里的 llm_ai 段。
type Config struct {
	// Enabled turns the module on. A missing llm_ai section decodes to the zero
	// value, so "no section at all" and "enabled: false" mean the same thing:
	// the module is not registered and New reports ErrDisabled. Nothing else is
	// validated in that state, which lets a deployment keep a prepared section
	// in the file while the switch stays off.
	// Enabled 控制模块是否启用。缺少整个 llm_ai 段时解码得到零值，因此"根本没有
	// 这一段"与"enabled: false"含义相同：模块不注册，New 返回 ErrDisabled。关闭时
	// 不再校验其它字段，因此部署可以保留一份准备好的配置而把开关关着。
	Enabled bool `yaml:"enabled"`

	// DefaultModel is the alias used when a request names none. It must be a
	// key of Models when set.
	// DefaultModel 是请求未指定 alias 时使用的名字；非空时必须是 Models 的键。
	DefaultModel string `yaml:"default_model"`

	// Models maps the name business code uses to one provider configuration.
	// Models 把业务代码使用的名字映射到一组 provider 配置。
	Models map[string]ModelConfig `yaml:"models"`

	// OutboundAllowlist restricts base_url hosts to these suffixes. Empty means
	// no check. It guards against a configuration that was edited to point at
	// an internal address, not against user input.
	// OutboundAllowlist 把 base_url 的 host 限制在这些后缀内；为空表示不校验。
	// 它防的是"配置被改到内网地址"这类事故，不是防用户输入。
	OutboundAllowlist []string `yaml:"outbound_allowlist"`

	// HTTPClient configures the single connection pool shared by every alias.
	// Per-model pools would multiply idle connections for no benefit.
	// HTTPClient 配置所有 alias 共用的连接池；按模型各建一个只会成倍增加空闲连接。
	HTTPClient HTTPClientConfig `yaml:"http_client"`
}

// ModelConfig describes one alias.
// ModelConfig 描述一个 alias。
type ModelConfig struct {
	// Provider selects the wire protocol: openai, deepseek or
	// openai_compatible.
	// Provider 选择协议实现：openai、deepseek 或 openai_compatible。
	Provider string `yaml:"provider"`

	// Model is the wire model id the endpoint expects.
	// Model 是端点认识的 wire model id。
	Model string `yaml:"model"`

	// APIKey is passed to the provider explicitly. A literal value is used
	// as-is; ${VAR} is read from the environment. Reading is the only thing
	// this package does with the environment: it never writes.
	//
	// Timing matters. This package resolves ${VAR} in New, but gofy's
	// config.Load expands ${VAR} before the YAML is parsed, so with that
	// loader the value arrives already substituted and a missing variable
	// fails at load. Write $${VAR} when the placeholder must survive loading
	// and be resolved in New — that is what lets a disabled llm_ai section
	// start without the variable being set.
	// APIKey 显式传给 provider。普通值原样使用；${VAR} 表示从环境变量读取。
	// 本包对环境变量只读，从不写。
	//
	// 时机很重要：本包在 New 里解析 ${VAR}，而 gofy 的 config.Load 在 YAML 解析
	// 之前就展开 ${VAR}，用它加载时到达这里的值已被替换、变量缺失会在加载期报错。
	// 要让占位符穿过加载、留到 New 再解析，写 $${VAR}——这正是"llm_ai 关闭时不
	// 需要设置变量"的做法。
	APIKey string `yaml:"api_key"`

	// BaseURL overrides the endpoint. Required for openai_compatible, optional
	// for the other two (their defaults are baked into goai).
	// BaseURL 覆盖端点地址；openai_compatible 必填，另两个可留空走 goai 内置默认值。
	BaseURL string `yaml:"base_url"`

	// Headers are added to every request (tenant routing, gateway keys).
	// Headers 附加到每个请求（租户路由、网关标识等）。
	Headers map[string]string `yaml:"headers"`

	// Timeout bounds one generation. It is a call-level deadline, not a
	// connection timeout.
	// Timeout 限制单次生成，是调用级 deadline，不是连接超时。
	Timeout time.Duration `yaml:"timeout" default:"60s"`

	// MaxRetries is the retry budget for transient errors. A nil value means
	// DefaultMaxRetries; 0 is a meaningful "do not retry", which is why this is
	// a pointer.
	// MaxRetries 是瞬时错误的重试预算。nil 表示 DefaultMaxRetries；0 是合法的
	// "不重试"，因此用指针。
	MaxRetries *int `yaml:"max_retries"`

	// ProviderID only affects log and metric labels for openai_compatible.
	// ProviderID 只影响 openai_compatible 的日志与指标标签。
	ProviderID string `yaml:"provider_id"`

	// Generation defaults, overridable per request. Numeric fields are
	// pointers where zero is a meaningful value.
	// 生成默认参数，可被单次请求覆盖。零值有意义的数值字段用指针。
	Temperature      *float64 `yaml:"temperature"`
	MaxOutputTokens  int      `yaml:"max_output_tokens"`
	TopP             *float64 `yaml:"top_p"`
	TopK             int      `yaml:"top_k"`
	FrequencyPenalty *float64 `yaml:"frequency_penalty"`
	PresencePenalty  *float64 `yaml:"presence_penalty"`
	Seed             int      `yaml:"seed"`
	StopSequences    []string `yaml:"stop_sequences"`

	// Tool loop and provider passthrough.
	// 工具循环与 provider 透传。
	MaxSteps                int            `yaml:"max_steps"`
	ToolChoice              string         `yaml:"tool_choice"`
	SequentialToolExecution bool           `yaml:"sequential_tool_execution"`
	MaxParallelCalls        int            `yaml:"max_parallel_calls"`
	ProviderOptions         map[string]any `yaml:"provider_options"`

	// Prompt caching.
	// Prompt 缓存。
	PromptCaching bool   `yaml:"prompt_caching"`
	CacheTTL      string `yaml:"cache_ttl"`

	// Embedding declares an optional embedding model for this alias.
	// Embedding 声明该 alias 可选的 embedding 模型。
	Embedding EmbeddingConfig `yaml:"embedding"`

	// LogPrompt and LogCompletion are off by default: prompts carry personal
	// data and completion bodies are large. When enabled both are truncated to
	// MaxLogChars and collapsed to one line.
	// LogPrompt 与 LogCompletion 默认关闭：prompt 可能含个人信息，completion 体积大。
	// 开启后都会按 MaxLogChars 截断并压成一行。
	LogPrompt     bool `yaml:"log_prompt"`
	LogCompletion bool `yaml:"log_completion"`
	MaxLogChars   int  `yaml:"max_log_chars" default:"512"`
}

// HTTPClientConfig configures the shared transport.
// HTTPClientConfig 配置共享的传输层。
type HTTPClientConfig struct {
	// Proxy is an http:// or socks5:// URL. Empty uses the environment.
	// Proxy 是 http:// 或 socks5:// 地址；留空走环境变量。
	Proxy string `yaml:"proxy"`

	// InsecureSkipVerify disables TLS verification. Development only.
	// InsecureSkipVerify 关闭 TLS 校验，仅用于开发环境。
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`

	MaxIdleConns          int           `yaml:"max_idle_conns" default:"100"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout" default:"90s"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout" default:"30s"`
}

// EmbeddingConfig declares an embedding model under an alias.
// EmbeddingConfig 在一个 alias 下声明 embedding 模型。
type EmbeddingConfig struct {
	// Model is the embedding model id. Empty means the alias offers no
	// embeddings, and Embed reports ErrNoEmbedding.
	// Model 是 embedding 模型 id；留空表示该 alias 不提供 embedding，Embed 会返回
	// ErrNoEmbedding。
	Model string `yaml:"model"`

	// Dimensions, when positive, is sent to the provider and also verified
	// against the returned vector length.
	// Dimensions 大于 0 时既透传给 provider，也用来校验返回向量的长度。
	Dimensions int `yaml:"dimensions"`
}

// withDefaults returns a copy with every default filled in. It never mutates
// the receiver, so a caller's map stays untouched.
// withDefaults 返回补齐默认值后的副本，从不修改接收者，调用方的 map 保持原样。
func (c Config) withDefaults() Config {
	if len(c.Models) > 0 {
		models := make(map[string]ModelConfig, len(c.Models))
		for alias, m := range c.Models {
			models[alias] = m.withDefaults()
		}
		c.Models = models
	}
	c.HTTPClient = c.HTTPClient.withDefaults()
	return c
}

// withDefaults is the single-model counterpart of Config.withDefaults.
// withDefaults 是 Config.withDefaults 的单模型版本。
func (m ModelConfig) withDefaults() ModelConfig {
	if m.Timeout == 0 {
		m.Timeout = DefaultTimeout
	}
	if m.MaxRetries == nil {
		retries := DefaultMaxRetries
		m.MaxRetries = &retries
	}
	if m.MaxLogChars == 0 {
		m.MaxLogChars = DefaultMaxLogChars
	}
	return m
}

// withDefaults fills the transport defaults.
// withDefaults 补齐传输层默认值。
func (h HTTPClientConfig) withDefaults() HTTPClientConfig {
	if h.MaxIdleConns == 0 {
		h.MaxIdleConns = DefaultMaxIdleConns
	}
	if h.IdleConnTimeout == 0 {
		h.IdleConnTimeout = DefaultIdleConnTimeout
	}
	if h.ResponseHeaderTimeout == 0 {
		h.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	return h
}

// Validate checks structure only. It does not build a client, read the
// environment or modify the receiver.
//
// A disabled config validates successfully regardless of its contents: the
// module is off, so an unfinished model entry must not keep the process from
// starting.
// Validate 只做结构校验：不建客户端、不读环境变量、不修改接收者。
//
// 关闭的配置无论内容如何都校验通过：模块没有启用，一份没填完的模型配置不该拦住
// 进程启动。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Models) == 0 {
		return ErrNoModels
	}
	if c.DefaultModel != "" {
		if _, ok := c.Models[c.DefaultModel]; !ok {
			return fmt.Errorf("%w: default_model %q", ErrModelNotFound, c.DefaultModel)
		}
	}
	for alias, m := range c.Models {
		if err := validateAlias(alias); err != nil {
			return err
		}
		if err := c.validateModel(alias, m); err != nil {
			return err
		}
	}
	return nil
}

// validateModel checks one alias; the alias is included so the operator knows
// which entry is wrong.
// validateModel 校验单个 alias，错误里带上 alias 让运维知道改哪一条。
func (c Config) validateModel(alias string, m ModelConfig) error {
	if m.Provider == "" {
		return fmt.Errorf("ai: model %q: %w", alias, ErrProviderUnknown)
	}
	if !isSupportedProvider(m.Provider) {
		return fmt.Errorf("ai: model %q: %w: %q (supported: %s)",
			alias, ErrProviderUnknown, m.Provider, strings.Join(providerNames, ", "))
	}
	if strings.TrimSpace(m.Model) == "" {
		return fmt.Errorf("ai: model %q: model id is required", alias)
	}
	if m.Provider == ProviderOpenAICompatible && strings.TrimSpace(m.BaseURL) == "" {
		// compat has no default base URL: without one the model would fail on
		// the first call instead of at startup.
		// compat 没有默认 base URL：缺了它要等到第一次调用才报错，而不是启动时。
		return fmt.Errorf("ai: model %q: %w", alias, ErrBaseURLRequired)
	}
	if m.BaseURL != "" {
		u, err := url.Parse(m.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("ai: model %q: base_url %q must be an absolute http(s) URL", alias, m.BaseURL)
		}
		if len(c.OutboundAllowlist) > 0 && !hostAllowed(u.Hostname(), c.OutboundAllowlist) {
			return fmt.Errorf("ai: model %q: base_url host %q is not in outbound_allowlist", alias, u.Hostname())
		}
	}
	if h := strings.TrimSpace(m.Headers["Host"]); h != "" {
		return fmt.Errorf("ai: model %q: set the host through base_url, not headers", alias)
	}
	if m.Timeout < 0 {
		return fmt.Errorf("ai: model %q: timeout must not be negative", alias)
	}
	if m.MaxRetries != nil && (*m.MaxRetries < -1 || *m.MaxRetries > 10) {
		return fmt.Errorf("ai: model %q: max_retries must be between -1 and 10", alias)
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("ai: model %q: temperature must be within [0,2]", alias)
	}
	if m.TopP != nil && (*m.TopP < 0 || *m.TopP > 1) {
		return fmt.Errorf("ai: model %q: top_p must be within [0,1]", alias)
	}
	if m.MaxOutputTokens < 0 {
		return fmt.Errorf("ai: model %q: max_output_tokens must not be negative", alias)
	}
	if m.MaxLogChars < 0 {
		return fmt.Errorf("ai: model %q: max_log_chars must not be negative", alias)
	}
	if m.Embedding.Dimensions < 0 {
		return fmt.Errorf("ai: model %q: embedding.dimensions must not be negative", alias)
	}
	return nil
}

// validateAlias rejects names that would be ambiguous in logs, metric labels
// or error messages.
// validateAlias 拒绝在日志、指标标签或错误信息里有歧义的名字。
func validateAlias(alias string) error {
	if strings.TrimSpace(alias) == "" {
		return fmt.Errorf("ai: model alias must not be empty")
	}
	if alias != strings.TrimSpace(alias) {
		return fmt.Errorf("ai: model alias %q must not have surrounding spaces", alias)
	}
	for _, r := range alias {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return fmt.Errorf("ai: model alias %q must not contain spaces or control characters", alias)
		}
	}
	return nil
}

// hostAllowed reports whether host matches one of the suffixes. A suffix
// matches on a label boundary, so ".aliyuncs.com" does not match
// "evil-aliyuncs.com".
// hostAllowed 判断 host 是否命中某个后缀；后缀按 label 边界匹配，
// ".aliyuncs.com" 不会命中 "evil-aliyuncs.com"。
func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(host)
	for _, s := range allow {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if host == s {
			return true
		}
		suffix := s
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// isSupportedProvider reports whether name is one of the three providers.
// isSupportedProvider 判断 name 是否属于三类受支持的 provider。
func isSupportedProvider(name string) bool {
	for _, p := range providerNames {
		if p == name {
			return true
		}
	}
	return false
}
