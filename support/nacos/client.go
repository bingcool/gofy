package nacos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	configPath     = "/nacos/v1/cs/configs"
	listenerPath   = "/nacos/v1/cs/configs/listener"
	loginPath      = "/nacos/v1/auth/login"
	maxConfigSize  = 8 << 20
	maxSnippetSize = 512
)

var (
	// ErrDisabled is returned by Sync, Get, Listen and Watch when
	// Config.Enabled is false.
	// Config.Enabled 为 false 时，Sync、Get、Listen 和 Watch 都返回 ErrDisabled。
	ErrDisabled = errors.New("nacos: integration is disabled")
	// ErrConfigNotFound is returned when Nacos has no config for the
	// requested data_id/group/namespace.
	// Nacos 中没有对应 data_id/group/namespace 的配置时返回该错误。
	ErrConfigNotFound = errors.New("nacos: config not found")
)

// Client calls the Nacos Open API. It caches the access token when
// credentials are configured and is safe for concurrent use.
// Client 调用 Nacos Open API。配置了用户名和密码时会缓存 access token，
// 可安全并发使用。
type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// NewClient validates cfg and returns a client.
// NewClient 校验 cfg 并返回客户端；cfg 为空值时按默认值归一化。
func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, http: newHTTPClient()}, nil
}

// newHTTPClient returns a client that ignores the environment proxy. Nacos is
// reached by host or service address (often a pod IP or an intranet name), and
// routing those through HTTP_PROXY turns "registry unreachable" into the
// proxy's own error — or, worse, into a timeout that the discovery throttle
// then caches for a whole window.
// newHTTPClient 返回一个忽略环境代理的客户端。Nacos 用主机名或服务地址访问（常常是
// Pod IP 或内网域名），经 HTTP_PROXY 转发会把"注册中心不可达"变成代理自己的错误——
// 更糟的是变成一次超时，再被发现的节流窗口缓存下来。
func newHTTPClient() *http.Client {
	transport := defaultTransport()
	transport.Proxy = nil
	return &http.Client{Transport: transport}
}

// defaultTransport clones the process default so its dialer timeouts and HTTP/2
// settings survive. The replacement is not necessarily an *http.Transport — a
// test or an APM SDK may install its own RoundTripper — and a zero-value
// Transport would dial without any timeout, which is harder to diagnose than
// the panic it replaces. The fallback therefore carries its own dialer.
// defaultTransport 复制进程默认 Transport，保留拨号超时与 HTTP/2 设置。替换实现
// 不一定是 *http.Transport——测试或 APM SDK 可能安装自己的 RoundTripper——而零值
// Transport 的拨号没有超时，比它替代的 panic 更难排查，因此兜底自带 Dialer。
func defaultTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Config returns the normalized config used by the client.
// Config 返回客户端实际使用的归一化配置。
func (c *Client) Config() Config { return c.cfg }

// Close releases idle connections held by the client. The next request opens a
// new one, so it is only useful at shutdown — or at the end of a test that
// built its own client.
// Close 释放客户端持有的空闲连接；下一次请求会新建连接，因此它只在停机或测试结束
// 时才有意义。
func (c *Client) Close() error {
	if c == nil || c.http == nil {
		return nil
	}
	c.http.CloseIdleConnections()
	return nil
}

// Get fetches the configuration identified by the client config.
// Get 拉取客户端配置所指的 data_id/group/namespace 下的配置内容。
// 响应为空视为错误，不会返回空内容。
func (c *Client) Get(ctx context.Context) ([]byte, error) {
	if !c.cfg.Enabled {
		return nil, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	query := url.Values{
		"dataId": {c.cfg.DataID},
		"group":  {c.cfg.Group},
	}
	if c.cfg.Namespace != "" {
		query.Set("tenant", c.cfg.Namespace)
	}
	data, err := c.do(ctx, http.MethodGet, configPath, query, nil, nil)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("nacos: empty config: dataId=%s group=%s", c.cfg.DataID, c.cfg.Group)
	}
	return data, nil
}

// Listen blocks for up to PollTimeout and reports whether the remote config
// differs from digest (the hex MD5 of the cached content).
// Listen 最长阻塞 PollTimeout 后返回远端配置是否与 digest 不同，
// digest 为本地缓存内容的十六进制 MD5。
func (c *Client) Listen(ctx context.Context, digest string) (bool, error) {
	if !c.cfg.Enabled {
		return false, ErrDisabled
	}
	// The server holds the request for the effective hold. Nacos enforces a
	// minimum hold (10s on 2.x, 30s by default), so never undercut it; leave
	// room for the response round trip. The header carries the same value as
	// the context deadline: announcing the raw PollTimeout while waiting longer
	// made the two disagree.
	// 服务端按生效的挂起时长挂起请求。Nacos 有最小挂起时长（2.x 为 10s，默认
	// 30s），不要低于该值，并额外留出响应往返时间。请求头与 ctx 截止时间使用同一
	// 数值：此前头里报原始 PollTimeout、实际却等得更久，两者不一致。
	hold := c.listenHold()
	ctx, cancel := context.WithTimeout(ctx, hold+c.cfg.Timeout)
	defer cancel()

	listening := c.cfg.DataID + "\x02" + c.cfg.Group + "\x02" + digest + "\x02" + c.cfg.Namespace + "\x01"
	form := url.Values{"Listening-Configs": {listening}}
	header := http.Header{}
	header.Set("Long-Pulling-Timeout", strconv.FormatInt(hold.Milliseconds(), 10))

	data, err := c.do(ctx, http.MethodPost, listenerPath, nil, form, header)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(data)) > 0, nil
}

// listenHold is the server hold time Listen announces and waits for: the
// configured PollTimeout raised to the Nacos minimum. The watcher also uses it
// to tell a normal "no change" answer from a hold that was cut short.
// listenHold 是 Listen 声明并等待的服务端挂起时长：配置的 PollTimeout 提升到
// Nacos 的最小值。watcher 也用它区分正常的"没有变更"与提前中断的挂起。
func (c *Client) listenHold() time.Duration {
	if c.cfg.PollTimeout < 30*time.Second {
		return 30 * time.Second
	}
	return c.cfg.PollTimeout
}

func (c *Client) do(ctx context.Context, method, path string, query, form url.Values, header http.Header) ([]byte, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	if token != "" {
		if query == nil {
			query = make(url.Values)
		}
		query.Set("accessToken", token)
		if header == nil {
			header = make(http.Header)
		}
		header.Set("Authorization", "Bearer "+token)
	}

	endpoint := c.cfg.baseURL() + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("nacos: build request: %w", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nacos: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("nacos: read response: %w", err)
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("nacos: response exceeds %d bytes", maxConfigSize)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: dataId=%s group=%s namespace=%s", ErrConfigNotFound,
			c.cfg.DataID, c.cfg.Group, c.cfg.Namespace)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		c.invalidateToken()
		return nil, fmt.Errorf("nacos: %s %s: HTTP %d: %s", method, path,
			resp.StatusCode, snippet(data))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("nacos: %s %s: HTTP %d: %s", method, path,
			resp.StatusCode, snippet(data))
	}
	return data, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.cfg.Username == "" || c.cfg.Password == "" {
		return "", nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	form := url.Values{
		"username": {c.cfg.Username},
		"password": {c.cfg.Password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.baseURL()+loginPath,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("nacos: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("nacos: login: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("nacos: read login response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("nacos: login: HTTP %d: %s", resp.StatusCode, snippet(data))
	}

	var payload struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int64  `json:"tokenTtl"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("nacos: decode login response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("nacos: login response missing accessToken")
	}

	ttl := time.Duration(payload.TokenTTL) * time.Second
	if ttl <= 0 {
		ttl = 3 * time.Minute
	}
	if ttl > 30*time.Second {
		ttl -= 30 * time.Second
	}
	c.token = payload.AccessToken
	c.tokenExpiry = time.Now().Add(ttl)
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.tokenExpiry = time.Time{}
	c.mu.Unlock()
}

func snippet(data []byte) string {
	data = bytes.TrimSpace(data)
	if len(data) > maxSnippetSize {
		data = data[:maxSnippetSize]
	}
	if len(data) == 0 {
		return "empty response"
	}
	return string(data)
}
