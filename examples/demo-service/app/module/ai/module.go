// Package ai is the demo's LLM entry point. It shows the three shapes a
// business module normally needs: a non-streaming call, a streaming call and a
// structured-output call, all addressed by the alias declared in config.yaml.
//
// The client is built inside the module factory, which means a Reload produces
// a new client from the new configuration. That is acceptable here because the
// client only owns an HTTP connection pool — there is no cursor or cache that
// must survive a configuration change.
//
// Package ai 是 demo 的 LLM 入口，演示业务模块最常见的三种用法：非流式、流式与
// 结构化输出，全部按 config.yaml 里声明的 alias 寻址。
//
// 客户端在模块工厂里构造，因此 Reload 会用新配置建一个新的。这里可以接受：客户端
// 只持有一个 HTTP 连接池，没有必须跨配置变更存活的游标或缓存。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support"
	supportai "github.com/bingcool/gofy/support/ai"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Module exposes the demo AI routes.
// Module 暴露 demo 的 AI 路由。
type Module struct {
	client *supportai.Client
	logger *zap.Logger
}

// NewModuleFactory builds the module from the llm_ai configuration. A broken
// configuration or a missing credential fails the startup here, not on the
// first user request.
// NewModuleFactory 用 llm_ai 配置构造模块。配置错误或凭证缺失在这里就让启动失败，
// 而不是等到第一个用户请求。
func NewModuleFactory(cfg supportai.Config) support.ModuleFactory {
	return func(rt *core.Runtime) (support.Module, error) {
		client, err := supportai.New(cfg, supportai.WithLogger(rt.Logger))
		if err != nil {
			return nil, err
		}
		return NewModule(client, rt.Logger), nil
	}
}

// NewModule wraps a built client.
// NewModule 包装一个已构造的客户端。
func NewModule(client *supportai.Client, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{client: client, logger: logger}
}

// Name returns the module name.
// Name 返回模块名。
func (m *Module) Name() string { return "ai" }

// Prefix mounts the module at the root: the handlers register fully-qualified
// paths, because these demo routes are not part of the contract-driven groups.
// Prefix 让模块挂在根前缀：这些 demo 路由不属于契约分组，处理器直接注册完整路径。
func (m *Module) Prefix() string { return "" }

// Register wires the examples. Auth and rate limiting are deliberately absent:
// these are local examples, and exposing an LLM endpoint with a shared key is
// exactly the kind of thing that must be an explicit decision, not a default.
// Register 挂载示例路由。这里刻意不加认证与限流：这些是本地示例，而"用一个共享 key
// 对外暴露 LLM 端点"必须是一个显式决定，不能成为默认行为。
func (m *Module) Register(r *support.Router) error {
	r.GET("/api/v1/demo/ai/models", m.listModels)
	r.POST("/api/v1/demo/ai/chat", m.chat)
	r.GET("/api/v1/demo/ai/stream", m.stream)
	r.POST("/api/v1/demo/ai/summary", m.summary)
	return nil
}

// Stop releases the client's idle connections.
// Stop 释放客户端的空闲连接。
func (m *Module) Stop(context.Context) error { return m.client.Close() }

// chatBody is the request body of the chat example.
// chatBody 是对话示例的请求体。
type chatBody struct {
	Prompt          string   `json:"prompt" binding:"required"`
	Model           string   `json:"model"`
	System          string   `json:"system"`
	Temperature     *float64 `json:"temperature"`
	MaxOutputTokens int      `json:"max_output_tokens"`
}

// listModels answers what the configuration actually built, which is the first
// thing to check when a request reports an unknown alias.
// listModels 回答配置实际构建了哪些模型——请求报"alias 不存在"时，这是第一个要
// 核对的地方。
func (m *Module) listModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"default": m.client.Default(),
		"models":  m.client.Aliases(),
	})
}

// chat is the non-streaming example.
// chat 是非流式示例。
func (m *Module) chat(c *gin.Context) {
	var body chatBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	resp, err := m.client.Chat(ctx, supportai.ChatRequest{
		Model:           body.Model,
		System:          body.System,
		Prompt:          body.Prompt,
		Temperature:     body.Temperature,
		MaxOutputTokens: body.MaxOutputTokens,
	})
	if err != nil {
		m.logger.Warn("demo ai chat failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"text":          resp.Text,
		"model":         resp.Model,
		"finish":        string(resp.FinishReason),
		"elapsed_ms":    resp.Elapsed.Milliseconds(),
		"input_tokens":  resp.Usage.InputTokens,
		"output_tokens": resp.Usage.OutputTokens,
		"total_tokens":  resp.Usage.TotalTokens,
	})
}

// stream forwards text deltas as server-sent events. The event payload is a
// JSON string so a delta containing newlines does not break the framing.
// stream 以 SSE 转发文本增量。事件体是一个 JSON 字符串，含换行的增量因此不会破坏
// 分帧。
func (m *Module) stream(c *gin.Context) {
	prompt := strings.TrimSpace(c.Query("prompt"))
	if prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt is required"})
		return
	}

	stream, err := m.client.Stream(c.Request.Context(), supportai.ChatRequest{
		Model:  c.Query("model"),
		Prompt: prompt,
	})
	if err != nil {
		m.logger.Warn("demo ai stream failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer func() { _ = stream.Close() }()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	for delta := range stream.Text() {
		fmt.Fprintf(c.Writer, "data: %s\n\n", jsonString(delta))
		c.Writer.Flush()
	}

	if err := stream.Err(); err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", jsonString(err.Error()))
		c.Writer.Flush()
		return
	}
	fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()

	if res := stream.Result(); res != nil {
		m.logger.Info("demo ai stream done",
			zap.Int("total_tokens", res.Usage.TotalTokens),
			zap.Int64("elapsed_ms", res.Elapsed.Milliseconds()),
		)
	}
}

// summaryBody is the structured-output request.
// summaryBody 是结构化输出的请求体。
type summaryBody struct {
	Text  string `json:"text" binding:"required"`
	Model string `json:"model"`
}

// summary is the structured-output example: the model's answer is decoded
// straight into a Go type instead of being parsed by hand.
// summary 是结构化输出示例：模型回答直接解码成 Go 类型，不再手工解析。
func (m *Module) summary(c *gin.Context) {
	var body summaryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	resp, err := supportai.ChatJSON[summaryResult](ctx, m.client, supportai.ChatRequest{
		Model: body.Model,
		// JSON 模式要求提示里出现 "json"，否则部分端点（含 DeepSeek）会直接拒绝。
		// JSON mode requires the word "json" in the prompt; some endpoints,
		// DeepSeek included, reject the request without it.
		System: "把输入总结成一句标题和最多三个标签，只输出 json 对象，字段为 title（字符串）与 tags（字符串数组）",
		Prompt: body.Text,
	})
	if err != nil {
		m.logger.Warn("demo ai summary failed", zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"result":     resp.Object,
		"model":      resp.Model,
		"elapsed_ms": resp.Elapsed.Milliseconds(),
	})
}

// summaryResult is the schema ChatJSON derives the JSON schema from.
// summaryResult 是 ChatJSON 用来推导 JSON schema 的类型。
type summaryResult struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

// jsonString renders s as a JSON string literal, so newlines and quotes inside
// a delta survive the SSE framing.
// jsonString 把 s 渲染成 JSON 字符串字面量，增量里的换行与引号因此能安全穿过 SSE
// 分帧。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
