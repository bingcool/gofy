package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPConfig describes a downstream HTTP check.
// HTTPConfig 描述一个下游 HTTP 检查。
type HTTPConfig struct {
	// URL is the endpoint to call. Only http and https are accepted, so a typo
	// fails at construction instead of becoming a permanently red probe.
	// URL 是要调用的端点。只接受 http 与 https，拼写错误在构造期就失败，而不是变成
	// 永远飘红的探针。
	URL string

	// Method defaults to GET. A HEAD probe is cheaper when the dependency does not
	// need to build a body for a liveness answer.
	// Method 默认 GET。依赖为存活应答构造响应体不划算时，用 HEAD 更便宜。
	Method string

	// Client is optional; nil builds a default client with a short header timeout
	// so a stalled peer cannot pin a goroutine beyond the per-check timeout.
	// Client 可选；nil 会构建默认客户端，带较短的响应头超时，避免对端卡住时协程被拖过
	// 单检查超时。
	Client *http.Client

	// AcceptedStatus defaults to 2xx and 3xx: a redirect that ends at a healthy
	// page describes a dependency that is up.
	// AcceptedStatus 默认 2xx 与 3xx：最终落到健康页面的重定向说明依赖是活的。
	AcceptedStatus func(status int) bool

	// Timeout is the per-check timeout this checker declares; zero means
	// DefaultPoolTimeout.
	// Timeout 是本检查器声明的单检查超时；零值表示 DefaultPoolTimeout。
	Timeout time.Duration
}

// NewHTTP builds a checker that calls cfg.URL and treats an accepted status as
// healthy.
// NewHTTP 构建调用 cfg.URL、把可接受状态视为健康的检查器。
func NewHTTP(name string, cfg HTTPConfig) (Checker, error) {
	target := strings.TrimSpace(cfg.URL)
	if target == "" {
		return nil, errors.New("health: http checker requires a URL")
	}
	u, err := url.Parse(target)
	if err != nil {
		// The parse error already quotes the offending text, and this branch only
		// runs during construction, never inside a snapshot.
		// 解析错误本身已引用出错文本，且这一支只在构造期执行，不会进入快照。
		return nil, fmt.Errorf("health: http checker URL %q: %w", target, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("health: http checker URL %q must use http or https", safeURL(target))
	}

	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodGet
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          16,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 2 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultPoolTimeout
	}

	accepted := cfg.AcceptedStatus
	if accepted == nil {
		accepted = func(status int) bool { return status >= 200 && status < 400 }
	}

	return &httpChecker{
		name:     name,
		target:   target,
		method:   method,
		client:   client,
		timeout:  timeout,
		accepted: accepted,
	}, nil
}

// safeURL renders target without the parts that can carry credentials: a query
// string may hold a token and userinfo holds a password. Scheme, host and path
// are enough to recognise the endpoint.
// safeURL 渲染 target 时去掉可能携带凭据的部分：query 可能带 token，userinfo 带密码。
// scheme、host 与 path 已足够辨认端点。
func safeURL(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return "<unparsable url>"
	}
	clone := *u
	clone.RawQuery = ""
	clone.ForceQuery = false
	clone.Fragment = ""
	clone.RawFragment = ""
	clone.User = nil
	return clone.String()
}

type httpChecker struct {
	name     string
	target   string
	method   string
	client   *http.Client
	timeout  time.Duration
	accepted func(status int) bool
}

func (c *httpChecker) Name() string { return c.name }

// CheckTimeout implements TimeoutProvider.
// CheckTimeout 实现 TimeoutProvider。
func (c *httpChecker) CheckTimeout() time.Duration { return c.timeout }

func (c *httpChecker) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, c.method, c.target, nil)
	if err != nil {
		return fmt.Errorf("http probe: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// Do wraps transport failures in *url.Error, whose text repeats the whole
		// URL — including a query string that may carry a token. The error ends up
		// in the /health snapshot, so only the operation and the cause are kept;
		// the check name is what identifies the dependency.
		// Do 把传输错误包在 *url.Error 里，其文本会重复完整 URL——其中的 query 可能携带
		// token。错误会进入 /health 快照，因此只保留操作与原因；是哪个依赖失败由检查名
		// 标识。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("http probe %s: %w", urlErr.Op, urlErr.Err)
		}
		return fmt.Errorf("http probe: %w", err)
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused, then close. The
		// body itself never matters for a health answer.
		// 有界地排空响应体以便连接复用，然后关闭。响应体内容对健康结论没有意义。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	if !c.accepted(resp.StatusCode) {
		return fmt.Errorf("http probe: status %d", resp.StatusCode)
	}
	return nil
}
