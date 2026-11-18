# support/ai 模块设计方案（goai 接入）

> 状态：**阶段一/二已实现**（`support/ai` + demo 样例 + 真实 DeepSeek 验证）。基线：gofy `7244689`、goai `v0.11.0`（2026-11-18 实测拉取）。
> 目标：在 `support/ai` 提供框架级 LLM 接入，配置入口为应用 `config.yaml` 的顶级段 `llm_ai`。
> 参考实现：swoolefy 的 `Support/Neuron` + `Support/AI`（底层 neuron-ai）。

---

## 1. 摘要（结论先行）

1. **goai 适合直接接入**：纯 Go、依赖只有 `golang.org/x/oauth2` 与 `coder/websocket`（实测 `go.mod`），要求 go 1.25，gofy 是 go 1.26，无版本阻塞。
2. **配置分层**：`llm_ai.default_model` + `llm_ai.models.<alias>`。alias 是业务使用的名字（`deepseek-chat`、`qwen-max`），与 provider 的 wire model id 解耦。
3. **首期只做 3 个 provider**：`openai`、`deepseek`、`openai_compatible`。不做 `ollama`，也不为任何厂商写专属分支——千问、文心、智谱、Moonshot、自建 vLLM/LiteLLM 统一走 `openai_compatible`（底层是 goai 的通用 OpenAI 兼容实现，见 §4.1）。
4. **不写进程环境变量**：不做 `export_env`。凭证只通过 provider 构造函数显式传入；需要从环境取时把 `api_key` 写成 `${VAR}`（只读，不写）。理由与实现见 §5。
5. **不做跨 provider 自动 fallback**：确认为明确的非目标，不是待办（见 §10）。
6. **首期由业务自持**：沿用 demo 的 `app/appio` 模式接线，框架不加 `gofy.WithAI`（见 §8）。
7. **配置项从 goai 的 option 反向抽离**：每个 yaml 字段都能对应到一个具体的 goai option（见 §4.2 映射表），不做"框架自创语义"。

---

## 2. 事实基线

### 2.1 goai v0.11.0 实际 API（已核对源码，非文档推测）

```go
// 核心调用：模型是 interface，不是字符串
func GenerateText(ctx context.Context, model provider.LanguageModel, opts ...Option) (*TextResult, error)
func StreamText(ctx context.Context, model provider.LanguageModel, opts ...Option) (*TextStream, error)
func GenerateObject[T any](ctx context.Context, model provider.LanguageModel, opts ...Option) (*ObjectResult[T], error)
func Embed(ctx context.Context, model provider.EmbeddingModel, value string, opts ...Option) (*EmbedResult, error)
func EmbedMany(ctx context.Context, model provider.EmbeddingModel, values []string, opts ...Option) (*EmbedManyResult, error)
```

```go
// 模型构造：所有 provider 签名一致
func Chat(modelID string, opts ...Option) provider.LanguageModel
func Embedding(modelID string, opts ...Option) provider.EmbeddingModel
```

返回值形状：

| 类型 | 关键字段 |
|---|---|
| `TextResult` | `Text`、`Reasoning`、`ToolCalls`、`Steps`、`TotalUsage` |
| `ObjectResult[T]` | `Object T`、`Usage`、`FinishReason`、`Response` |
| `EmbedResult` | `Embedding []float64`、`Usage` |
| `provider.Usage` | `InputTokens`、`OutputTokens`、`TotalTokens`、`ReasoningTokens`、`CacheReadTokens`、`CacheWriteTokens` |
| `TextStream` | `Stream() <-chan provider.StreamChunk`、`TextStream() <-chan string`、`Result() *TextResult`、`Err() error` |

调用级 option（`goai.Option`，与模型构造无关）：

`WithPrompt`、`WithSystem`、`WithMessages`、`WithTools`、`WithMaxSteps`、`WithMaxRetries`、`WithTimeout`、`WithHeaders`、`WithTemperature`、`WithMaxOutputTokens`、`WithTopP`、`WithTopK`、`WithFrequencyPenalty`、`WithPresencePenalty`、`WithSeed`、`WithStopSequences`、`WithToolChoice`、`WithProviderOptions`、`WithPromptCaching`、`WithCacheTTL`、`WithSequentialToolExecution`、`WithMaxParallelCalls`、`WithRetryObserver`，以及生命周期 hook（`WithOnRequest` / `WithOnResponse` / `WithOnFinish` / `WithOnStepFinish` / `WithOnToolCall` / `WithOnPanic`）。

首期三个 provider 的构造 option（源码实测，三者高度一致）：

| provider 包 | 构造 option |
|---|---|
| `provider/openai` | `WithAPIKey`、`WithBaseURL`、`WithHeaders`、`WithHTTPClient`、`WithTokenSource`、`WithUseMaxCompletionTokens` |
| `provider/deepseek` | `WithAPIKey`、`WithBaseURL`、`WithHeaders`、`WithHTTPClient`、`WithTokenSource` |
| `provider/compat` | `WithBaseURL`（**必填**）、`WithAPIKey`（可选）、`WithTokenSource`、`WithHeaders`、`WithHTTPClient`、`WithProviderID`、`WithMaxCompletionTokens` |

两个关键事实（后面 §4.3 / §5 都依赖它们）：

- `compat.Chat` 的注释与实现都明确：**没有默认 base URL，也没有默认凭证**；`BaseURLRequired: true`、`TokenRequired: false`，未配置凭证时不发 `Authorization` 头。这就是"任意 OpenAI 兼容端点"的正确载体。
- `openai` / `deepseek` 的 `Chat` 只在 **`tokenSource == nil`** 时才读 `OPENAI_API_KEY` / `DEEPSEEK_API_KEY`（`provider/openai/openai.go:120-126`、`provider/deepseek/deepseek.go:53-58`）。**无条件传 `WithAPIKey("")` 会得到非 nil 的 `StaticToken("")`，从而关掉这层兜底**——这是必须写进实现的细节，不是文档脚注。

默认 base URL：`openai` 为 `https://api.openai.com/v1`，`deepseek` 为 `https://api.deepseek.com`；且两者仅在 base URL 仍等于默认值时才读 `*_BASE_URL` 环境变量。

**OTel 集成不在主模块**：`v0.11.0` 的 `go.mod` 只有三个依赖，根目录没有 otel 文件。官网提到的 OTel/Langfuse 是独立子模块。若要与 `support/otel` 打通，只能通过 `WithOnRequest` / `WithOnResponse` / `WithOnFinish` 自行造 span。

### 2.2 swoolefy 的可借鉴与不可借鉴

| swoolefy 做法 | Go 侧结论 |
|---|---|
| `ai_model_providers[alias] = {provider FQCN, key, model, baseUri, parameters}` | **借鉴**：alias → 一组配置，业务按 alias 调用 |
| **env > 配置 > 默认值** 三层解析 | **部分借鉴**：只做"读"，不做"写"。`api_key` 一个字段承载字面量与 `${VAR}`（§5） |
| 凭证缺失 fail-fast，不静默降级 | **借鉴**：`New` 阶段就报错 |
| `provider_fallback.order` + 仅瞬时错误切换，流式仅首 chunk 前 | **不借鉴**：明确非目标（§10） |
| SSRF `outbound_url_allowlist` + `baseUri` 白名单 + 启动期 health check | **借鉴**：`base_url` 校验 scheme/host，可选 allowlist |
| Embedding 维度一致性 + `allow_fake_embeddings` | **部分借鉴**：维度校验做；fake 用 httptest 替代，不引入配置开关 |
| 反射按构造参数名绑定配置 | **不借鉴**：Go 用显式结构体 + `switch provider` |
| **AI 模块不写 `putenv`/`$_ENV`** | **重要事实**：swoolefy 也没写环境变量，因为它把 key/model 显式传给 Provider 构造器。Go 同理（§5） |
| 无 retry / 无 token 统计 / 无 prompt 日志 | **Go 侧补齐**：token usage 已在 `provider.Usage`，直接透出 |

### 2.3 gofy 的硬约束（新增 support 包必须遵守）

1. 不得 import 根包 `github.com/bingcool/gofy`、`app/config`；**不得 import `gofy/logger`**，logger 通过参数或 `WithLogger` 注入 `*zap.Logger`。
2. 公共包禁止 `func init()`、包级可变状态、`panic` / `os.Exit` / `log.Fatal`。
3. `Config` 同时提供 `yaml` + `default` tag 与未导出 `withDefaults()`，两处默认值必须一致；`Validate()` 只校验、不建连、不改接收者。
4. 凭据永不进入错误与日志：自己实现 redact；错误只带 provider/model/base_url 的 host/status。
5. `Close` 必须 nil 安全且可重复调用。
6. 所有 IO 透传 ctx 并设默认超时。
7. 测试零外部依赖：`httptest` + `openai_compatible` 指向本地 server；必须过 `-race`。
8. 必须提供 `support/ai/README.md`，并同步根 README 清单与 CHANGELOG。

---

## 3. 包结构

```
support/ai/
  ai.go          Client：模型注册表、Chat/Stream/ChatJSON/Embed、Close
  config.go      Config / ModelConfig / Provider 常量 / Validate / withDefaults
  provider.go    provider 构造注册表：map[string]factory + 各 provider 的参数装配
  request.go     ChatRequest / ChatResponse / Usage 等调用侧类型
  errors.go      哨兵错误与错误分类（可重试 / 不可重试）
  redact.go      凭据脱敏辅助
  README.md
  ai_test.go / config_test.go / provider_test.go（httptest，不触网）
```

不引入 `support/internal/instanceinit`：它不是"多连接实例"模型，而是"alias → 已构造好的 model"的只读映射，构造期一次性完成，没有懒连接与缓存状态。

---

## 4. 配置设计

### 4.1 yaml 形态

```yaml
# 顶级段：LLM 接入。alias 是业务代码使用的名字，与 provider 的 model id 解耦。
llm_ai:
  # 总开关。false 或整段缺失时模块不启用：不构建客户端、不注册路由，
  # 其余字段也不再校验（可以先保留一份写好的配置而把开关关着）。
  enabled: true

  # 业务不指定 alias 时使用的默认模型；必须是 models 里的键
  default_model: deepseek-chat

  # 可选：base_url 的 host 后缀白名单，为空表示不校验（见 §4.2 / §4.3）
  # outbound_allowlist: [".aliyuncs.com", ".baidubce.com", ".internal"]

  models:
    # ---- 国内直连：DeepSeek 官方 provider ----
    deepseek-chat:
      provider: deepseek
      model: deepseek-chat
      api_key: ${DEEPSEEK_API_KEY}
      timeout: 60s
      max_retries: 2
      temperature: 0.7
      max_output_tokens: 4096
      stop_sequences: ["\n\n"]

    # ---- 原生 OpenAI ----
    gpt-4o-mini:
      provider: openai
      model: gpt-4o-mini
      api_key: ${OPENAI_API_KEY}
      base_url: https://api.openai.com/v1    # 可选，留空用 provider 默认
      headers:
        X-Tenant: order-service
      temperature: 0.2
      max_output_tokens: 2048

    # ---- 自定义 OpenAI 兼容模型：千问 ----
    qwen-max:
      provider: openai_compatible
      model: qwen-max
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
      api_key: ${DASHSCOPE_API_KEY}
      provider_id: qwen                      # 只用于日志与指标标签
      timeout: 90s

    # ---- 自定义 OpenAI 兼容模型：文心 ----
    ernie:
      provider: openai_compatible
      model: ernie-4.0-8k
      base_url: https://qianfan.baidubce.com/v2
      api_key: ${QIANFAN_API_KEY}
      provider_id: qianfan
      max_output_tokens: 2048

    # ---- 自建网关也可用同一路径（无 key 时不发 Authorization 头）----
    local-gateway:
      provider: openai_compatible
      model: qwen2.5-14b-instruct
      base_url: http://llm-gateway.internal:8000/v1
      api_key: ${LLM_GATEWAY_KEY}            # 变量不存在时报错；也可整个留空
      timeout: 120s
```

`openai_compatible` 是**一条**能力，不是一组厂商枚举。常见端点的 base URL 参考（示例，以其官方文档为准；变更频繁，配置里以实际为准）：

| 服务 | base_url 示例 |
|---|---|
| 通义千问（DashScope 兼容模式） | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| 文心（百度千帆 v2） | `https://qianfan.baidubce.com/v2` |
| 智谱 GLM | `https://open.bigmodel.cn/api/paas/v4` |
| Moonshot Kimi | `https://api.moonshot.cn/v1` |
| 火山方舟（豆包） | `https://ark.cn-beijing.volces.com/api/v3` |
| 自建 vLLM / LiteLLM / OneAPI | `http://<host>:<port>/v1` |

要点：这些端点都不需要加代码分支。它们与 OpenAI 的差异（是否接受 `max_tokens`、是否支持 `tools`、`reasoning_effort` 等）由"模型自身能力 + `provider_options` 透传"处理，框架不做厂商判断。

### 4.2 字段表与 goai 映射

**`llm_ai` 顶级字段**

| yaml 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | 总开关。false 或**整段缺失**时模块不启用，其余字段不校验 |
| `default_model` | string | 空 | 业务不指定 alias 时使用；非空必须是 `models` 的键 |
| `models` | map | 必填 | `alias → ModelConfig`，alias 即业务调用时用的名字 |
| `outbound_allowlist` | []string | 空（不校验） | `base_url` 的 host 后缀白名单；非空时任何不在名单内的 host 启动即报错。防的是"配置被改到内网地址"这类事故，不是防用户输入 |
| `http_client` | 子段 | 见下 | 所有 alias 共用的连接池；它是进程级偏好，按模型各建一个只会成倍增加空闲连接 |

**`models.<alias>` — 连接、认证与可靠性**（`api_key`/`base_url`/`headers`/`provider_id` 作用于构造期；`timeout`/`max_retries` 作用于每次调用）

| yaml 字段 | 类型 | 默认 | 对应 goai | 说明 |
|---|---|---|---|---|
| `provider` | string | 必填 | 选择 provider 构造 | 仅 `openai` / `deepseek` / `openai_compatible` |
| `model` | string | 必填 | `Chat(modelID, ...)` 首参 | wire model id，即网关认的模型名 |
| `api_key` | string | — | `WithAPIKey` | 字面量原样使用；`${VAR}` 占位表示从环境读取，由 `ai.New` 解析。注意 gofy `config.Load` 在 YAML 解析**之前**就会展开 `${VAR}`，写 `$${VAR}` 才能把解析推迟到 `New` |
| `base_url` | string | provider 默认 | `WithBaseURL` | `openai_compatible` **必填**；`openai`/`deepseek` 留空用内置默认 |
| `headers` | map | — | `WithHeaders` | 附加请求头（租户、网关路由等） |
| `timeout` | duration | `60s` | `goai.WithTimeout` | **单次生成**超时，不是连接超时 |
| `max_retries` | int | `2` | `goai.WithMaxRetries` | 瞬时错误重试；`-1` 表示不限（不推荐） |
| `provider_id` | string | provider 名 | `compat.WithProviderID` | 只影响日志/指标标签；`openai`/`deepseek` 忽略 |

**`llm_ai.http_client` 子段**（影响连接层，全模型共享一个连接池）

| yaml 字段 | 默认 | 说明 |
|---|---|---|
| `http_client.proxy` | — | `http://`/`socks5://`，设置 `Transport.Proxy` |
| `http_client.insecure_skip_verify` | `false` | 跳过 TLS 校验，仅开发环境 |
| `http_client.max_idle_conns` | `100` | 连接池大小 |
| `http_client.idle_conn_timeout` | `90s` | 空闲连接回收 |
| `http_client.response_header_timeout` | `30s` | 首字节超时 |

**生成默认参数**（调用期，可被单次请求覆盖）

| yaml 字段 | 类型 | goai option |
|---|---|---|
| `temperature` | *float64 | `WithTemperature` |
| `max_output_tokens` | int | `WithMaxOutputTokens` |
| `top_p` | *float64 | `WithTopP` |
| `top_k` | int | `WithTopK` |
| `frequency_penalty` | *float64 | `WithFrequencyPenalty` |
| `presence_penalty` | *float64 | `WithPresencePenalty` |
| `seed` | int | `WithSeed` |
| `stop_sequences` | []string | `WithStopSequences` |

> 数值型用指针（`*float64`）区分"未设置"与"显式 0"——`temperature: 0` 是合法且常用值，裸 `float64` 的零值会让它消失。

**工具与结构化**

| yaml 字段 | 默认 | goai option |
|---|---|---|
| `max_steps` | `0`（不循环） | `WithMaxSteps` |
| `tool_choice` | — | `WithToolChoice` |
| `sequential_tool_execution` | `false` | `WithSequentialToolExecution` |
| `max_parallel_calls` | `0` | `WithMaxParallelCalls` |
| `provider_options` | — | `WithProviderOptions`，原样透传（`reasoning_effort`、`enable_thinking` 等；键名由端点决定，框架不校验） |

**缓存**

| yaml 字段 | 默认 | goai option |
|---|---|---|
| `prompt_caching` | `false` | `WithPromptCaching` |
| `cache_ttl` | — | `WithCacheTTL`（`5m`/`1h`，provider 相关） |

**Embedding 子段**

| 字段 | 默认 | 说明 |
|---|---|---|
| `embedding.model` | — | 留空表示该 alias 不提供 embedding |
| `embedding.dimensions` | `0` | `goai.WithEmbeddingProviderOptions(map[string]any{"dimensions": n})`；>0 时校验返回向量长度 |

### 4.3 校验规则（`Validate`）

- `enabled: false` 或整段缺失 → 直接通过，不再检查任何字段（模块没有启用，一份没填完的配置不该拦住启动）。
- `enabled: true` 时 `models` 非空（否则 `ErrNoModels`）；`default_model` 若非空必须是 `models` 的键。
- 每个 alias：`provider` 必须在注册表内（否则 `ErrProviderUnknown`，错误里列出三个合法值）；`model` 非空。
- `openai_compatible`：`base_url` 必填（`ErrBaseURLRequired`），否则会得到一个"没有默认地址"的模型，错误要到第一次调用才出现。
- 所有 `base_url`：`url.Parse` 成功、scheme 为 `http|https`、host 非空；可选 `outbound_allowlist` 校验 host 后缀（沿用 swoolefy 的 SSRF 思路，默认不启用）。
- 凭证：`openai` / `deepseek` 必须能解析到 key（`api_key` 的字面量或 `${VAR}` > 该 provider 的约定环境变量），全部缺失时 `ErrAPIKeyRequired`，**在 `New` 阶段报错**；`openai_compatible` 允许无 key（网关在集群内、由网络策略保护）。
- 数值范围：`temperature` ∈ [0,2]、`top_p` ∈ [0,1]、`max_output_tokens` > 0、`max_retries` ∈ [-1,10]、`timeout` > 0。
- alias 名不得含空格与控制字符（它会进日志与指标标签，也会成为 map 键）。

---

## 5. 凭证与环境变量：只读，不写

### 5.1 决策

**不做 `export_env`，也不调用 `os.Setenv`。** 这一条已确认，不再作为可选项。

理由（四条都成立，不是理论担忧）：

1. **进程级污染**：同一进程里其他库（或用户自己的 SDK）会读到被我们改过的 `OPENAI_API_KEY`；
2. **多模型冲突**：两个 alias 都指向 openai 但用不同 key/网关时，后写的覆盖先写的，故障极难定位；
3. **测试串扰**：`t.Setenv` 只能部分隔离，`-race` 下并发 setenv/getenv 的语义仍依赖进程锁；
4. **审计困难**：环境变量可被 `/proc/<pid>/environ`、崩溃转储、子进程继承带走。

### 5.2 "从环境取凭证"仍然支持，只是方向相反

读环境变量不是问题，写才是。`api_key` 一个字段，三条路径：

| 方式 | 时机 | 适用 |
|---|---|---|
| `api_key: <字面量>` | 直接用 | 本地调试、或凭证由配置管理工具注入 |
| `api_key: ${VAR}` | `ai.New` **构造期**读取（但 gofy `config.Load` 会先展开，见下） | 推荐。配置自解释，错误信息带变量名 |
| `api_key` 留空 | goai 内部 `os.Getenv` 兜底 | K8s Secret 注入 env 的部署方式；`New` 会先探测一次给出明确错误 |

有一个容易踩的细节：`${VAR}` 展开发生在 **YAML 解析之前**，所以（1）注释里出现占位符原文同样会被要求提供变量——实测中一条被注释掉的示例键（`# api_key: ${DASHSCOPE_API_KEY}`）让整份配置加载失败；（2）即使 `llm_ai.enabled` 为 false，只要文件里有 `${VAR}`，加载期就必须有该变量。

第二条要绕开就把占位符写成 `$${VAR}`：`config.Load` 只认 `${VAR}`，`$$` 是它的转义，加载期会还原成字面 `${VAR}` 交给本包，`New` 才去读环境变量——而 `New` 在关闭时不会被调用。示例配置里凡是注释或"关闭也能启动"的场景都用这种方式，demo 的 `config.yaml` 中有对照说明。

第三种依赖 goai 的一个细节：它**只在 `tokenSource == nil` 时**才读环境变量。因此实现上有一条硬性要求：

```go
// 只在有值时才传，否则 StaticToken("") 会让 goai 的 env 兜底永远不生效。
if key != "" {
    opts = append(opts, openai.WithAPIKey(key))
}
```

`${VAR}` 的处理同理：展开成功就 `WithAPIKey(value)`，变量缺失或为空就 `ErrAPIKeyRequired`（错误里带变量名，不带值）。

### 5.3 与密钥轮换的关系

显式传参意味着密钥在 `New` 时固化到 model 实例里，轮换需要重建客户端（重启，或后续加 `ReloadSpec` 钩子）。这与 `telemetry` / `diagnostics` 的现状一致，本期不做热轮换；README 要写明。

---

## 6. 客户端 API

```go
package ai

type Client struct {
    models       map[string]provider.LanguageModel
    embeds       map[string]provider.EmbeddingModel
    defaults     map[string]ModelConfig
    defaultAlias string
    logger       *zap.Logger
    onUsage      func(UsageEvent)
}

func New(cfg Config, opts ...Option) (*Client, error)
func (c *Client) Close() error                    // nil 安全、幂等
func (c *Client) Aliases() []string
func (c *Client) Default() string

// 非流式
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

// 流式：调用方 range 通道，结束后必须读 Err()
func (c *Client) Stream(ctx context.Context, req ChatRequest) (*Stream, error)

// 结构化输出：泛型，等价于 goai.GenerateObject
func ChatJSON[T any](ctx context.Context, c *Client, req ChatRequest) (*JSONResponse[T], error)

// 嵌入
func (c *Client) Embed(ctx context.Context, alias string, values []string) (*EmbedResponse, error)
```

调用侧类型：

```go
type ChatRequest struct {
    ModelAlias string           // alias（不是 wire model id），空表示 default_model
    System   string
    Messages []provider.Message // 与 goai 同类型，不另造消息模型
    Prompt   string             // 与 Messages 二选一
    Tools    []goai.Tool

    // 单次覆盖（nil/零值表示用模型默认）
    Temperature      *float64
    MaxOutputTokens  int
    TopP             *float64
    MaxSteps         int
    ProviderOptions  map[string]any
}

type ChatResponse struct {
    Text         string
    Reasoning    string
    ToolCalls    []provider.ToolCall
    FinishReason provider.FinishReason
    Usage        Usage          // 从 provider.Usage 映射
    ModelAlias   string         // 实际使用的 alias（不是 wire model id）
    Elapsed      time.Duration
}
```

`Stream` 包装 `goai.StreamText`，暴露 `Text() <-chan string`、`Chunks() <-chan provider.StreamChunk`、`Result() *ChatResponse`、`Err() error`、`Close()`。

**为什么消息类型直接用 `provider.Message`**：另造一层 DTO 会迫使每次调用做双向转换，而 goai 的消息模型已经是稳定公开类型；包装层只解决"配置/别名/可观测性"，不解决"消息长什么样"。

---

## 7. provider 构造注册表

```go
type factory func(ModelConfig, buildContext) (provider.LanguageModel, error)

var languageFactories = map[string]factory{
    "openai":            newOpenAI,
    "deepseek":          newDeepSeek,
    "openai_compatible": newOpenAICompatible, // goai 的 provider/compat
}
```

- `buildContext` 携带 `*http.Client`（由 `http_client` 子段构造，全模型共享一个连接池）与 `*zap.Logger`。
- 未知 provider → `ErrProviderUnknown`，错误信息列出已支持的名字。
- 新增 provider = 加一个 factory + 一个常量，不动 Client。
- **fail-fast**：任一 alias 构造失败，`New` 返回错误（没有需要关闭的连接，但保持语义一致）。

为什么仍保留 `deepseek` 的专属构造：goai 为它定义了正确的默认 base URL 与 wire 细节，比走通用兼容路径少踩坑；成本只是一个 20 行的 factory。**但它不是必需品**——`provider: openai_compatible` + `base_url: https://api.deepseek.com` 同样可用，配置里可以按团队习惯统一。

---

## 8. 与 App 的集成

### 8.1 本期：业务自持（已实现）

落点是独立的模块工厂，而不是 `app/appio` + container：

```yaml
# examples/demo-service/config.yaml
llm_ai:
  default_model: deepseek-chat
  models:
    deepseek-chat: { ... }
```

```go
// app/config/config.go
type Config struct {
    // ...
    LLMAI supportai.Config `yaml:"llm_ai"`
}

// worker/group3/ai_check.go：客户端在 worker 真正运行时按配置构造，
// 巡检每隔一段时间把每个 alias 调用一遍，把各自的文本与 token 数写进日志。
client, err := supportai.New(cfg.LLMAI, supportai.WithLogger(logger))
resp, err := client.Chat(ctx, supportai.ChatRequest{Prompt: "解释幂等"})
```

为什么不开一组 demo HTTP 路由：AI 调用绝大多数落在 worker / script 里，而一条只为
"证明客户端能用"的路由会顺带把 LLM 暴露到业务端口上（没有认证、没有限流，等于用
共享 key 对外提供推理服务）。`worker --group=group3` 的巡检覆盖同一件事——配置、
凭证、端点是否都通——却不引入这个攻击面。

客户端也不进模块生命周期：它没有跨配置变更必须存活的状态，构造在使用方（worker
或 script）即可；HTTP 进程因此完全不碰 `support/ai` 的构造。巡检的客户端在 run
函数里才建，`Register` 在 HTTP 进程里也会执行，那一代不该校验一份用不到的凭证。

`goai` 只被用到它的应用编译进来；不使用 AI 的应用零成本：`llm_ai` 整段缺失或
`enabled: false` 时没有任何使用方去构造，`support/ai` 的 `New` 也就不会发生。

### 8.2 暂不做：`gofy.WithAI` + `Runtime.AI`

```go
// 未来形态，本期不实现
gofy.WithAI(cfg ai.Config)     // 类型别名 AIConfig
rt.AI() *ai.Client
```

判断标准与 `WithDiagnostics` 一致：出现**第二个**使用方、且接线代码开始重复时再做。现在做会让"不用 AI 的应用也要编译 goai"从可选变成默认。

---

## 9. 可观测性

### 9.1 Token 用量

`provider.Usage` 已经齐全（含 cache read/write、reasoning）。Client 提供：

```go
type UsageEvent struct {
    Alias, Provider, Model string
    Usage                  Usage
    Elapsed                time.Duration
    Err                    error
}
ai.WithOnUsage(func(UsageEvent))   // 同步回调，禁止阻塞
```

### 9.2 与 metrics 联动（可选）

`support/ai` 不直接依赖 `support/otel`（避免 otel 反向依赖 ai）。由使用方在 `WithOnUsage` 里往 meter 写：

| 指标 | 类型 |
|---|---|
| `llm_client_request_duration_seconds` | histogram，标签 `alias` / `provider` / `outcome` |
| `llm_client_tokens_total` | counter，标签 `alias` / `kind`（input/output/reasoning/cache_read） |

### 9.3 日志边界

- 默认**不记录** prompt 与 completion（可能含个人信息/密钥）。
- 可选 `log_prompt: false` / `log_completion: false`，开启时按 `max_log_chars`（默认 512）截断并单行化。
- 每条日志必带 `alias`、`provider`、`model`、`elapsed_ms`、`input_tokens`、`output_tokens`。
- 错误只带 `provider`、`model`、`status_code`、`host`；`api_key` 永不出现（`redact.go` 兜底：任何回显字符串先过 `redactKey`）。

---

## 10. 错误与可靠性

```go
var (
    ErrProviderUnknown = errors.New("ai: unknown provider")
    ErrModelNotFound   = errors.New("ai: model alias not found")
    ErrAPIKeyRequired  = errors.New("ai: api key is required")
    ErrBaseURLRequired = errors.New("ai: base url is required")
    ErrInvalidResponse = errors.New("ai: invalid provider response")
)
```

- 重试交给 `goai.WithMaxRetries`（瞬时错误），我们**不叠加**第二层重试，避免重试次数相乘。
- 超时：`timeout` 是单次生成的整体超时，直接映射 `WithTimeout`；`context` 仍然优先（调用方取消立即返回）。
- 错误分类辅助：`IsRetryable(err)`（429 / 5xx / 网络）与 `IsAuthError(err)`（401/403），供业务决定是否换 alias 重试。
- **不做跨 provider 自动 fallback——明确的非目标**。不是因为难，而是因为语义：流式一旦输出首个 chunk 就无法重放，切换 provider 会把两段不同模型的回答拼成一个；而非流式切换也会让"这个回答由谁产生"变得不可解释。业务确实需要双活时，自己按 `IsRetryable` 决定重试哪个 alias，语义留在业务侧。

---

## 11. 测试策略

| 层 | 方法 |
|---|---|
| 配置 | 表驱动 `Validate` 用例：缺 provider/model/key、`openai_compatible` 缺 base_url、未知 provider、base_url 非法、数值越界、default_model 不存在 |
| 构造 | 凭证优先级（`api_key` 字面量 > `api_key: ${VAR}` 展开 > provider 环境变量）；`openai_compatible` 无 key 允许；`openai` 无 key 报 `ErrAPIKeyRequired` |
| 调用链路 | `httptest.NewServer` 模拟 OpenAI Chat Completions；`provider: openai_compatible` + `base_url` 指向它，断言请求体（model/temperature/messages）、返回解析与 usage 映射 |
| 流式 | httptest 返回 SSE 分块，断言 `Text()` 拼接与 `Result()` |
| 结构化 | 返回固定 JSON，`ChatJSON[T]` 反序列化正确；非法 JSON 返回 `ErrInvalidResponse` |
| 环境变量 | 断言**任何路径都不写**进程环境变量（`New` 前后 `os.LookupEnv` 一致）；`api_key` 为空时不追加 `WithAPIKey`，goai 的 env 兜底仍生效（否则 `StaticToken("")` 会静默关掉它） |
| 脱敏 | 构造一个包含 key 的错误，断言 `err.Error()` 不含 key 明文 |

全部走 `-race`，不依赖真实 API key 与网络。

---

## 12. 实施阶段

| 阶段 | 内容 | 产出 |
|---|---|---|
| **一** | `Config` + `Validate` + 3 个 provider factory + `Client.Chat` + usage + redact + `support/ai/README.md` + 测试 | `support/ai` 可用 |
| **二** | `Stream` / `ChatJSON` / `Embed` + `http_client` 子段 + demo 用例（`app/config` + `worker/group3/ai_check.go`） | `worker --group=group3` 的巡检能验证"配置 → 凭证 → 端点"整条链路 |
| **三** | `WithOnUsage` + metrics 联动示例 + 根 README / CHANGELOG | 对外文档完整 |
| **四（按需）** | `gofy.WithAI` + `Runtime.AI`、OTel span（用 hooks 自建） | 出现第二个使用方再做 |

---

## 13. 风险与开放问题

1. **goai 版本演进快**（当前 v0.11.0，近一个月多次 minor）：`support/ai` 必须把 goai 的 option 收在 `provider.go` 一处，升级时只改这一层；README 标注已验证版本。
2. **`openai_compatible` 没有默认 base_url**：配置缺失时必须报错，不能退化成"打到 OpenAI"。
3. **兼容端点的差异是真实存在的，而且已经实测到一个**：DeepSeek 只接受 `response_format=json_object`，收到 `json_schema` 会直接返回 `This response_format type is unavailable now`。配置里加 `provider_options: {structuredOutputs: false}` 即可降级，不需要改代码——这正是"差异靠配置表达"的验证点。其余如千问/文心的 `tools` 支持程度、`max_tokens` 与 `max_completion_tokens` 的选择、`reasoning_effort` 的位置同样不一致；`compat.WithMaxCompletionTokens` 是另一处备好的口子。
4. **流式取消**：`Stream` 必须支持调用方 `ctx` 取消并释放底层连接；需测"取消后 goroutine 不泄漏"（`-race` + `goleak` 风格断言）。
5. **多实例与热更新**：AI 客户端配置在启动时固定，`Reload` 不重建（与 telemetry/diagnostics 一致）；密钥轮换需要重启（§5.3）。
6. **凭证由谁保管**：本模块只接受显式字符串，不管密钥来源。生产建议 `${VAR}` + K8s Secret；如果团队后续要接 KMS/Vault，扩展点是 `WithTokenSource`（goai 已支持动态 token），不需要改配置结构。

---

## 14. 已确认的决策记录

| # | 决策 | 结论 |
|---|---|---|
| 1 | provider 范围 | 只做 `openai` / `deepseek` / `openai_compatible`；不做 `ollama`，不为千问/文心等写厂商专属分支 |
| 2 | 跨 provider fallback | **不做**，明确的非目标（§10） |
| 3 | `export_env` | **不做**，也不写任何进程环境变量；只读（§5） |
| 4 | yaml 层级 | `default_model` + `models.<alias>` |
| 5 | 首期集成方式 | demo 自持（不经框架托管），不加 `gofy.WithAI`；具体接线位置见 #8 |
| 6 | `http_client` 位置 | 实现时从"每模型"上移为 `llm_ai.http_client`：它是进程级偏好，按模型各建一个连接池只会成倍增加空闲连接 |
| 7 | 兼容端点差异 | 一律用 `provider_options` 表达，不加厂商分支；已实测 DeepSeek 需要 `structuredOutputs: false` |
| 8 | `enabled` 开关 | 新增顶级 `enabled`：false 或整段缺失时模块不启用、其余字段不校验，`New` 返回 `ErrDisabled`；`enabled: true` 但没有 `models` 才是 `ErrNoModels` |
| 9 | demo 用法位置 | 不开 HTTP 路由：AI 调用主要落在 worker / script，客户端在用到它的地方构造即可（零跨代状态）。`worker/group3/ai_check.go` 是一个巡检 worker，遍历所有 alias（demo 里 `deepseek-chat` 与 `deepseek-compat` 各一条，后者的 provider 是 `openai_compatible`），`llm_ai.enabled` 为 true 才注册、客户端在 run 里才建，因此 HTTP 进程既不校验凭证也不暴露 LLM 端点 |
