package ai

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/compat"
	"github.com/zendev-sh/goai/provider/deepseek"
	"github.com/zendev-sh/goai/provider/openai"
)

// newHTTPClient builds the pool shared by every alias. It sets no client-wide
// Timeout on purpose: that field also bounds the whole response body, which
// would cut a long stream, while the per-call deadline is goai.WithTimeout.
// newHTTPClient 构建所有 alias 共用的连接池。它刻意不设 client 级 Timeout：那个
// 字段会限制整个响应体的读取，长流会被切断；单次截止时间由 goai.WithTimeout 负责。
func newHTTPClient(cfg HTTPClientConfig) (*http.Client, error) {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	if cfg.Proxy != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("ai: http_client.proxy %q is not a valid URL", cfg.Proxy)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("ai: http_client.proxy scheme %q must be http, https or socks5", u.Scheme)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	if cfg.InsecureSkipVerify {
		tlsCfg := transport.TLSClientConfig
		if tlsCfg == nil {
			tlsCfg = &tls.Config{}
		} else {
			tlsCfg = tlsCfg.Clone()
		}
		tlsCfg.InsecureSkipVerify = true
		transport.TLSClientConfig = tlsCfg
	}
	transport.MaxIdleConns = cfg.MaxIdleConns
	transport.IdleConnTimeout = cfg.IdleConnTimeout
	transport.ResponseHeaderTimeout = cfg.ResponseHeaderTimeout
	return &http.Client{Transport: transport}, nil
}

// providerEnvKey is the variable goai itself reads when no key is supplied.
// Probing it in New turns a missing credential into a startup error instead of
// a 401 on the first request.
// providerEnvKey 是未显式提供 key 时 goai 自己读的变量。在 New 里探测它，可以把
// "缺凭证"变成启动错误，而不是第一条请求的 401。
func providerEnvKey(name string) string {
	switch name {
	case ProviderOpenAI:
		return "OPENAI_API_KEY"
	case ProviderDeepSeek:
		return "DEEPSEEK_API_KEY"
	default:
		return ""
	}
}

// apiKeyEnvPattern matches the ${VAR} form accepted by api_key. It is
// deliberately the same shape as config/env's placeholder so operators only
// have one syntax to remember.
// apiKeyEnvPattern 匹配 api_key 接受的 ${VAR} 形式，与 config 包的占位符同形，
// 运维只需记一种写法。
var apiKeyEnvPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// resolveAPIKey applies the documented precedence: api_key (literal or
// ${VAR}), then the provider's conventional environment variable. An empty
// result for openai_compatible is legitimate — such gateways are often
// protected by network policy rather than a token.
//
// This is the only place the package reads the environment, and it reads
// nothing else. It never writes.
// resolveAPIKey 按文档顺序解析凭证：api_key（字面量或 ${VAR}）→ provider 约定变量。
// openai_compatible 返回空是合法的——这类网关通常靠网络策略保护而不是 token。
//
// 这是本包唯一读取环境变量的地方，除此之外不读；也从不在环境里写任何东西。
func resolveAPIKey(m ModelConfig) (string, error) {
	if raw := strings.TrimSpace(m.APIKey); raw != "" {
		key, err := expandAPIKey(raw)
		if err != nil {
			return "", err
		}
		if key != "" {
			return key, nil
		}
	}
	name := providerEnvKey(m.Provider)
	if name == "" {
		return "", nil
	}
	if key := strings.TrimSpace(os.Getenv(name)); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("%w: set api_key (${%s})", ErrAPIKeyRequired, name)
}

// expandAPIKey substitutes ${VAR} with the variable's value. An unset or empty
// variable is an error: a placeholder that silently resolved to "" would only
// surface as a 401 later. Values without a placeholder pass through unchanged,
// so a literal key is never misinterpreted.
// expandAPIKey 把 ${VAR} 替换成变量值。变量未设置或为空即报错：静默解析成空串的
// 占位符要等到 401 才暴露。不含占位符的值原样返回，字面量 key 不会被误解析。
func expandAPIKey(raw string) (string, error) {
	missing := ""
	out := apiKeyEnvPattern.ReplaceAllStringFunc(raw, func(match string) string {
		name := match[2 : len(match)-1]
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			if missing == "" {
				missing = name
			}
			return ""
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("%w: %s is empty", ErrAPIKeyRequired, missing)
	}
	return strings.TrimSpace(out), nil
}

// newLanguageModel builds the chat model for one alias. key is already
// resolved; an empty key is only meaningful for openai_compatible.
//
// Passing WithAPIKey only when the key is non-empty is load-bearing: goai
// resolves its own environment variable only while its token source is nil, and
// StaticToken("") is not nil. An empty option would therefore silently disable
// the documented fallback.
// newLanguageModel 为一个 alias 构建对话模型。key 已解析完成；空 key 只对
// openai_compatible 有意义。
//
// "只在 key 非空时传 WithAPIKey" 是关键：goai 只在 token source 为 nil 时才读自己的
// 环境变量，而 StaticToken("") 不是 nil。传一个空 option 会静默关掉这层兜底。
func newLanguageModel(m ModelConfig, key string, client *http.Client) (provider.LanguageModel, error) {
	switch m.Provider {
	case ProviderOpenAI:
		opts := []openai.Option{openai.WithHTTPClient(client)}
		if key != "" {
			opts = append(opts, openai.WithAPIKey(key))
		}
		if m.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(m.BaseURL))
		}
		if len(m.Headers) > 0 {
			opts = append(opts, openai.WithHeaders(m.Headers))
		}
		return openai.Chat(m.Model, opts...), nil

	case ProviderDeepSeek:
		opts := []deepseek.Option{deepseek.WithHTTPClient(client)}
		if key != "" {
			opts = append(opts, deepseek.WithAPIKey(key))
		}
		if m.BaseURL != "" {
			opts = append(opts, deepseek.WithBaseURL(m.BaseURL))
		}
		if len(m.Headers) > 0 {
			opts = append(opts, deepseek.WithHeaders(m.Headers))
		}
		return deepseek.Chat(m.Model, opts...), nil

	case ProviderOpenAICompatible:
		opts := []compat.Option{
			compat.WithBaseURL(m.BaseURL),
			compat.WithHTTPClient(client),
		}
		if key != "" {
			opts = append(opts, compat.WithAPIKey(key))
		}
		if m.ProviderID != "" {
			opts = append(opts, compat.WithProviderID(m.ProviderID))
		}
		if len(m.Headers) > 0 {
			opts = append(opts, compat.WithHeaders(m.Headers))
		}
		return compat.Chat(m.Model, opts...), nil
	}
	return nil, fmt.Errorf("%w: %q (supported: %s)", ErrProviderUnknown, m.Provider, strings.Join(providerNames, ", "))
}

// newEmbeddingModel builds the embedding model declared under an alias.
// newEmbeddingModel 构建 alias 下声明的 embedding 模型。
func newEmbeddingModel(m ModelConfig, key string, client *http.Client) (provider.EmbeddingModel, error) {
	switch m.Provider {
	case ProviderOpenAI:
		opts := []openai.Option{openai.WithHTTPClient(client)}
		if key != "" {
			opts = append(opts, openai.WithAPIKey(key))
		}
		if m.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(m.BaseURL))
		}
		if len(m.Headers) > 0 {
			opts = append(opts, openai.WithHeaders(m.Headers))
		}
		return openai.Embedding(m.Embedding.Model, opts...), nil

	case ProviderDeepSeek:
		// deepseek has no embedding API; refusing here beats a confusing 404.
		// deepseek 没有 embedding 接口；在这里拒绝好过一个莫名其妙的 404。
		return nil, fmt.Errorf("ai: deepseek does not provide an embedding API")

	case ProviderOpenAICompatible:
		opts := []compat.Option{
			compat.WithBaseURL(m.BaseURL),
			compat.WithHTTPClient(client),
		}
		if key != "" {
			opts = append(opts, compat.WithAPIKey(key))
		}
		if m.ProviderID != "" {
			opts = append(opts, compat.WithProviderID(m.ProviderID))
		}
		if len(m.Headers) > 0 {
			opts = append(opts, compat.WithHeaders(m.Headers))
		}
		return compat.Embedding(m.Embedding.Model, opts...), nil
	}
	return nil, fmt.Errorf("%w: %q (supported: %s)", ErrProviderUnknown, m.Provider, strings.Join(providerNames, ", "))
}
