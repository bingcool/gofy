package ai

import (
	"errors"

	goai "github.com/zendev-sh/goai"
)

// Sentinel errors. Callers use errors.Is; the wrapped message carries the
// alias or host so an operator can act without a stack trace.
// 哨兵错误。调用方用 errors.Is 判断；包装信息带上 alias 或 host，运维不查栈也能定位。
var (
	// ErrDisabled means the llm_ai section is absent or has enabled: false.
	// New returns it instead of a half-working client, so a caller that forgot
	// the switch fails at construction rather than on the first request.
	// ErrDisabled 表示 llm_ai 段缺失或 enabled 为 false。New 返回它而不是一个
	// 半可用的客户端，漏了开关的调用方因此在构造期失败，而不是第一次请求。
	ErrDisabled = errors.New("ai: module is disabled")

	// ErrNoModels means the llm_ai section declared no models at all.
	// ErrNoModels 表示 llm_ai 段没有声明任何模型。
	ErrNoModels = errors.New("ai: no models configured")

	// ErrProviderUnknown means provider is not one of the supported names.
	// ErrProviderUnknown 表示 provider 不是受支持的名字。
	ErrProviderUnknown = errors.New("ai: unknown provider")

	// ErrModelNotFound means the requested alias is not configured.
	// ErrModelNotFound 表示请求的 alias 没有配置。
	ErrModelNotFound = errors.New("ai: model alias not found")

	// ErrAPIKeyRequired means no credential could be resolved for a provider
	// that requires one. It is raised during New, not on the first call.
	// ErrAPIKeyRequired 表示需要凭证的 provider 拿不到凭证；它在 New 阶段抛出，
	// 不是等到第一次调用。
	ErrAPIKeyRequired = errors.New("ai: api key is required")

	// ErrBaseURLRequired means openai_compatible was configured without a base
	// URL. compat has no default endpoint on purpose.
	// ErrBaseURLRequired 表示 openai_compatible 没有配置 base URL；compat 刻意
	// 不提供默认端点。
	ErrBaseURLRequired = errors.New("ai: base url is required")

	// ErrEmptyPrompt means a request carried neither prompt nor messages.
	// ErrEmptyPrompt 表示请求既没有 prompt 也没有 messages。
	ErrEmptyPrompt = errors.New("ai: prompt or messages required")

	// ErrNoEmbedding means the alias has no embedding model configured.
	// ErrNoEmbedding 表示该 alias 没有配置 embedding 模型。
	ErrNoEmbedding = errors.New("ai: model does not provide embeddings")

	// ErrInvalidResponse means the provider answered with something that does
	// not match the declared shape, such as a vector of the wrong dimension.
	// ErrInvalidResponse 表示 provider 的响应不符合声明的形状，例如向量维度不对。
	ErrInvalidResponse = errors.New("ai: invalid provider response")

	// ErrClosed means the client was closed before the call.
	// ErrClosed 表示客户端已关闭。
	ErrClosed = errors.New("ai: client is closed")
)

// IsRetryable reports whether err is a provider error that the same call could
// retry. It reads goai's own classification instead of re-deriving one from
// status codes, so the two layers cannot disagree.
//
// Note that goai already retries up to max_retries: this helper answers "may I
// start a new attempt", not "should the transport have retried".
// IsRetryable 判断 err 是否属于同一调用可以重试的 provider 错误。它直接读 goai 的
// 分类而不是自己按状态码再判一次，避免两层结论不一致。
//
// 注意 goai 已按 max_retries 重试过：这个辅助回答的是"我要不要再发起一次尝试"，
// 而不是"传输层该不该重试"。
func IsRetryable(err error) bool {
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsRetryable
	}
	return false
}

// IsAuthError reports whether err is an authentication or authorization
// failure. Retrying such a call cannot succeed, but a different alias can.
// IsAuthError 判断 err 是否为认证/授权失败。重试同一调用不会成功，但换一个
// alias 可以。
func IsAuthError(err error) bool {
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 401 || apiErr.StatusCode == 403
	}
	return false
}

// StatusCode returns the provider's HTTP status code, or 0 when err did not
// come from an API response.
// StatusCode 返回 provider 的 HTTP 状态码；err 不是 API 响应时返回 0。
func StatusCode(err error) int {
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}
