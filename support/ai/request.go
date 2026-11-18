package ai

import (
	"time"

	goai "github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// ChatRequest is one generation. Model names an alias from the configuration;
// every overridable parameter keeps the zero value meaning "use the alias
// default", which is why the two float fields are pointers.
// ChatRequest 描述一次生成。Model 是配置里的 alias；可覆盖参数的零值一律表示
// "用该 alias 的默认值"，两个浮点字段因此用指针。
type ChatRequest struct {
	// Model is the alias. Empty uses Config.DefaultModel.
	// Model 是 alias；留空使用 Config.DefaultModel。
	Model string

	// System is the system instruction.
	// System 是系统指令。
	System string

	// Messages and Prompt are alternatives. Messages wins when both are set;
	// at least one is required.
	// Messages 与 Prompt 二选一，同时设置时以 Messages 为准；两者至少要有一个。
	Messages []provider.Message
	Prompt   string

	// Tools enables goai's automatic tool loop.
	// Tools 开启 goai 的自动工具循环。
	Tools []goai.Tool

	// Per-request overrides.
	// 单次请求的覆盖值。
	Temperature     *float64
	MaxOutputTokens int
	TopP            *float64
	MaxSteps        int
	ProviderOptions map[string]any
}

// ChatResponse is the provider-independent result of a generation.
// ChatResponse 是与 provider 无关的生成结果。
type ChatResponse struct {
	Text         string
	Reasoning    string
	ToolCalls    []provider.ToolCall
	FinishReason provider.FinishReason
	Usage        Usage
	// Model is the alias that served the request, not the wire model id: the
	// caller already knows the latter from its configuration.
	// Model 是服务本次请求的 alias，而不是 wire model id：后者调用方从配置里就能看到。
	Model   string
	Elapsed time.Duration
}

// JSONResponse is the structured-output counterpart of ChatResponse.
// JSONResponse 是 ChatResponse 的结构化输出版本。
type JSONResponse[T any] struct {
	Object       T
	Usage        Usage
	FinishReason provider.FinishReason
	Model        string
	Elapsed      time.Duration
}

// EmbedResponse is the result of Embed.
// EmbedResponse 是 Embed 的结果。
type EmbedResponse struct {
	Embeddings [][]float64
	Usage      Usage
	Model      string
	Elapsed    time.Duration
}

// Usage is a copy of provider.Usage so callers of this package do not have to
// import the provider package just to read token counts.
// Usage 是 provider.Usage 的副本，调用方不必为了读 token 数去 import provider 包。
type Usage struct {
	InputTokens      int
	OutputTokens     int
	TotalTokens      int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}

// usageFrom converts goai's counters.
// usageFrom 转换 goai 的计数。
func usageFrom(u provider.Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		ReasoningTokens:  u.ReasoningTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

// UsageEvent is what WithOnUsage receives after every call, successful or not.
// The callback runs synchronously on the calling goroutine: it must not block,
// and it must not call back into the Client.
// UsageEvent 是 WithOnUsage 在每次调用后（无论成败）收到的内容。回调在调用方
// goroutine 上同步执行：不能阻塞，也不能回调 Client 自身。
type UsageEvent struct {
	Alias    string
	Provider string
	Model    string
	Usage    Usage
	Elapsed  time.Duration
	// Err is the already-redacted error, or nil on success.
	// Err 是已脱敏的错误；成功时为 nil。
	Err error
}
