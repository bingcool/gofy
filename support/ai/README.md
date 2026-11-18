# support/ai

配置驱动的 LLM 客户端，底层是 [goai](https://github.com/zendev-sh/goai)（已在
`v0.11.0` 上验证）。应用在 `config.yaml` 的 `llm_ai` 段声明若干具名模型
（alias），业务代码按 alias 调用，不再关心 provider、base URL 与凭证从哪来。

支持三类 provider：

| `provider` | 说明 |
|---|---|
| `openai` | 原生 OpenAI |
| `deepseek` | DeepSeek 官方接口 |
| `openai_compatible` | **任何讲 OpenAI 协议的自定义端点**：千问（DashScope 兼容模式）、文心（千帆 v2）、智谱、Moonshot、火山方舟，以及自建 vLLM / LiteLLM / OneAPI |

没有 `ollama`，也没有按厂商分的子包：一个 `openai_compatible` 就够，差异靠
`base_url`、`provider_options` 与模型自身能力表达。

## 快速开始

```yaml
llm_ai:
  enabled: true            # 总开关；false 或整段缺失时模块不启用
  default_model: deepseek-chat

  models:
    deepseek-chat:
      provider: deepseek
      model: deepseek-chat
      api_key: ${DEEPSEEK_API_KEY}
      temperature: 0.7
      max_output_tokens: 2048

    qwen-max:
      provider: openai_compatible
      model: qwen-max
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
      api_key: ${DASHSCOPE_API_KEY}
      provider_id: qwen
```

```go
client, err := ai.New(cfg.LLMAI, ai.WithLogger(logger))
if err != nil {
    return err // 关闭时是 ErrDisabled；其余是配置或凭证问题，都在这里暴露
}
defer client.Close()

resp, err := client.Chat(ctx, ai.ChatRequest{
    Prompt: "用一句话解释什么是幂等",
})
fmt.Println(resp.Text, resp.Usage.InputTokens, resp.Usage.OutputTokens)
```

流式：

```go
stream, err := client.Stream(ctx, ai.ChatRequest{Prompt: "写一段 Go 代码"})
if err != nil {
    return err
}
defer stream.Close() // 提前中止时必须调用，否则消费 goroutine 不会退出

n, stopped := 0, false
for delta := range stream.Text() {
    fmt.Print(delta) // 每收到一片就写出去，这就是"流式"的全部
    n++
    if n == 20 {
        stopped = true
        _ = stream.Close() // 先关再跳出，原因见下
        break
    }
}
if err := stream.Err(); err != nil && !stopped {
    return err
}
fmt.Println(stream.Result().Usage.TotalTokens)
```

三条规则，都踩过：

1. **`Text()` 与 `Chunks()` 二选一**。两者读的是同一个底层流，只能有一个消费者；
   需要工具调用、推理过程或结束事件时用 `Chunks()`（它返回 goai 的原始分块，
   也是本包唯一的 goai 类型泄漏点），只要文本就用 `Text()`。
2. **提前离开循环必须先 `Close()`**。消费 goroutine 会阻塞在向 64 容量的通道发送上，
   而 `Result()` 与 `Err()` 都要等它结束——直接 `break` 之后读结果是死等。
3. **自己取消的流不要按错误内容判断**。被取消的 HTTP 读取可能表现成
   `context.Canceled`、读错误或 provider 的 `ChunkError`；该看的是"谁取消的"
   （上面的 `stopped`，或 `ctx.Err()`），否则同一段代码会随机报错。

用量只在 `Result()` 里：流式分块只带文本，没有 token 计数；被中断的流那里是 0，
因为 provider 只在请求结束时结算。

可运行样例见 [`examples/demo-service/script/ai_stream.go`](../../examples/demo-service/script/ai_stream.go)：
`script ai-stream` 边收边打到 stdout，`--stop-after N` 演示提前中止，`--serve 127.0.0.1:8099`
把同一条流转成 SSE（一个增量一个 `event: delta`，末尾一个带用量的 `event: done`）。

结构化输出（Go 的方法不能带类型参数，因此是自由函数）：

```go
type Summary struct {
    Title string   `json:"title"`
    Tags  []string `json:"tags"`
}

resp, err := ai.ChatJSON[Summary](ctx, client, ai.ChatRequest{Prompt: "总结这篇文章"})
fmt.Println(resp.Object.Title)
```

嵌入：

```go
emb, err := client.Embed(ctx, "qwen-max", []string{"第一段", "第二段"})
```

## 配置

### 顶级字段

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | `false` | 总开关。false 或**整段缺失**时模块不启用：不构建客户端、不注册路由，其余字段也不校验 |
| `default_model` | 空 | 请求未指定 alias 时使用；非空必须是 `models` 的键 |
| `models` | 必填 | `alias → 模型配置` |
| `outbound_allowlist` | 空（不校验） | `base_url` 的 host 后缀白名单，按 label 边界匹配（`.aliyuncs.com` 不匹配 `evil-aliyuncs.com`） |
| `http_client` | — | 所有 alias 共用的连接池 |

### 模型字段

| 字段 | 默认 | 对应 goai |
|---|---|---|
| `provider` | 必填 | `openai` / `deepseek` / `openai_compatible` |
| `model` | 必填 | wire model id |
| `api_key` | — | `WithAPIKey`；可写字面量，也可写 `${VAR}` 占位，占位符在 `New` 时读取环境变量 |
| `base_url` | provider 默认 | `WithBaseURL`；`openai_compatible` **必填** |
| `headers` | — | `WithHeaders` |
| `timeout` | `60s` | `goai.WithTimeout`（单次生成，不是连接超时） |
| `max_retries` | `2` | `goai.WithMaxRetries`；`0` 表示不重试，因此是指针类型 |
| `provider_id` | provider 名 | `compat.WithProviderID`，只影响日志/指标标签 |
| `temperature` / `top_p` / `frequency_penalty` / `presence_penalty` | — | 同名 `goai.With*`；指针类型，`0` 是合法值 |
| `max_output_tokens` / `top_k` / `seed` / `stop_sequences` | — | 同名 `goai.With*` |
| `max_steps` / `tool_choice` / `sequential_tool_execution` / `max_parallel_calls` | — | 工具循环 |
| `provider_options` | — | 原样透传，键名由端点决定（`reasoning_effort`、`enable_thinking` 等） |
| `prompt_caching` / `cache_ttl` | `false` / — | `WithPromptCaching` / `WithCacheTTL` |
| `embedding.model` / `embedding.dimensions` | — / `0` | 声明该 alias 的 embedding 模型；`dimensions > 0` 时同时校验返回向量长度 |
| `log_prompt` / `log_completion` / `max_log_chars` | `false` / `false` / `512` | 默认不记录 prompt 与 completion |

开关的语义：`llm_ai` 整段缺失时解码得到零值，与 `enabled: false` 完全等价，因此
"这个部署不接大模型"不需要任何占位配置，也不会让启动失败。此时 `ai.New` 返回
`ErrDisabled`；调用方要么不构造，要么把它当作"没有 AI"处理，而不是当作错误上报。
`enabled: true` 但没写 `models` 才是配置错误（`ErrNoModels`）。

`http_client` 子段：`proxy`、`insecure_skip_verify`、`max_idle_conns`（100）、
`idle_conn_timeout`（90s）、`response_header_timeout`（30s）。刻意不设 client 级
Timeout：那个字段会限制整个响应体读取，长流会被切断。

## 凭证：只读，不写

本包**从不调用 `os.Setenv`**，也不会把凭证写进进程环境。`api_key` 一个字段，两条读取路径：

1. `api_key: <字面量>` —— 原样传给 `WithAPIKey`；
2. `api_key: ${VAR}` —— `New` 时读取并展开，变量未设置或为空即 `ErrAPIKeyRequired`
   （错误里带变量名，不带值）；
3. `api_key` 留空 —— 由 goai 读它自己的约定变量（`OPENAI_API_KEY` / `DEEPSEEK_API_KEY`），
   `New` 会先探测一次，缺失时返回 `ErrAPIKeyRequired`。

第 3 条有一个实现上的关键点：goai 只在 token source 为 nil 时才读环境变量，而
`StaticToken("")` 不是 nil。所以本包**只在 key 非空时才传 `WithAPIKey`**——无条件
传空值会静默关掉这层兜底。

`openai_compatible` 允许完全没有 key（内网网关常靠网络策略保护），此时不发送
`Authorization` 头。

如果希望**关闭模块时完全不依赖环境**（`enabled: false` 也能直接启动），把占位符写成
`$${VAR}`。`config.Load` 只认 `${VAR}`，`$$` 是它的转义：加载期把 `$${VAR}` 还原成
字面 `${VAR}` 交给本包，`New` 才去读环境变量——而 `New` 在关闭时根本不会被调用。
直接写 `${VAR}` 则会在 YAML 解析之前就被展开，与模块是否启用无关，关掉也照样要求变量存在。

密钥在 `New` 时固化到模型实例里，轮换需要重建客户端（重启，或后续加 `ReloadSpec`
钩子）。

## 可观测性

```go
client, _ := ai.New(cfg, ai.WithLogger(logger), ai.WithOnUsage(func(ev ai.UsageEvent) {
    // ev.Alias / ev.Provider / ev.Model / ev.Usage / ev.Elapsed / ev.Err
}))
```

回调在调用方 goroutine 上同步执行，不要阻塞。`support/ai` 不依赖 `support/otel`，
需要指标时在回调里写 meter：

| 指标 | 类型 |
|---|---|
| `llm_client_request_duration_seconds` | histogram，标签 `alias` / `provider` / `outcome` |
| `llm_client_tokens_total` | counter，标签 `alias` / `kind` |

日志默认只记录一次调用的元数据（alias、provider、model、耗时、token、finish_reason）；
prompt 与 completion 需要显式开启，且按 `max_log_chars` 截断、压成一行。错误只带
alias、状态码与 host，凭证在离开本包前经过 `redactedError` 抹除，错误链仍然保留。

## 错误分类

```go
var (
    ErrDisabled        = errors.New("ai: module is disabled")
    ErrNoModels        = errors.New("ai: no models configured")
    ErrProviderUnknown = errors.New("ai: unknown provider")
    ErrModelNotFound   = errors.New("ai: model alias not found")
    ErrAPIKeyRequired  = errors.New("ai: api key is required")
    ErrBaseURLRequired = errors.New("ai: base url is required")
    ErrEmptyPrompt     = errors.New("ai: prompt or messages required")
    ErrNoEmbedding     = errors.New("ai: model does not provide embeddings")
    ErrInvalidResponse = errors.New("ai: invalid provider response")
    ErrClosed          = errors.New("ai: client is closed")
)
```

`IsRetryable(err)` / `IsAuthError(err)` / `StatusCode(err)` 直接读 goai 的
`APIError`，不重新按状态码推导，避免两层判断不一致。

## 设计取舍

- **不做跨 provider 自动 fallback**。流式一旦输出首个 chunk 就无法重放，切换
  provider 会把两段不同模型的回答拼成一个；非流式切换也会让"这段回答由谁产生"
  不可解释。需要双活时由业务按 `IsRetryable` 自己决定重试哪个 alias。
- **不重试两层**。重试完全交给 `goai.WithMaxRetries`，本包不叠加。
- **消息类型直接用 `provider.Message`**。包装层只解决配置、别名与可观测性，不重新
  定义消息结构。
- **一个共享连接池**。按 alias 各建一个只会成倍增加空闲连接，而 `http_client` 的
  参数本来就是进程级偏好。

## 测试

全部走 `httptest`，不依赖真实 key 与网络，并且必须过 `-race`：

- `Validate` 表驱动：缺 provider/model/base_url、未知 provider、数值越界、alias 非法；
- 凭证优先级与"缺凭证是启动错误"；
- `New` 前后进程环境逐条比对，证明不写环境变量；
- Chat 的请求体（model / temperature / messages / Authorization）与响应映射；
- SSE 流式的增量拼接与 `Result()`；
- `ChatJSON` 的反序列化；
- Embedding 的维度校验；
- 错误脱敏后错误链仍然可用；
- 单次调用的 `timeout` 确实生效。

## 已知限制

- 兼容端点的差异真实存在：`tools` 支持程度、`max_tokens` 与
  `max_completion_tokens` 的选择、`reasoning_effort` 的位置都不完全一致。框架不做
  厂商判断，遇到 400 时用 `provider_options` 与 `compat.WithMaxCompletionTokens`
  的口子调整。
- 不支持流式结构化输出（`ChatJSON` 只有非流式）。
- 客户端配置在 `New` 时固定，不支持运行时热更新。
