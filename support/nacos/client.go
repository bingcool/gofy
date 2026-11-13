package nacos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return &Client{cfg: cfg, http: &http.Client{}}, nil
}

// Config returns the normalized config used by the client.
// Config 返回客户端实际使用的归一化配置。
func (c *Client) Config() Config { return c.cfg }

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
	// The server holds the request for PollTimeout. Nacos enforces a minimum
	// hold (10s on 2.x, 30s by default), so never undercut it; leave room for
	// the response round trip.
	// 服务端会按 PollTimeout 挂起请求。Nacos 有最小挂起时长（2.x 为 10s，
	// 默认 30s），不要低于该值，并额外留出响应往返时间。
	hold := c.cfg.PollTimeout
	if hold < 30*time.Second {
		hold = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, hold+c.cfg.Timeout)
	defer cancel()

	listening := c.cfg.DataID + "\x02" + c.cfg.Group + "\x02" + digest + "\x02" + c.cfg.Namespace + "\x01"
	form := url.Values{"Listening-Configs": {listening}}
	header := http.Header{}
	header.Set("Long-Pulling-Timeout", strconv.FormatInt(c.cfg.PollTimeout.Milliseconds(), 10))

	data, err := c.do(ctx, http.MethodPost, listenerPath, nil, form, header)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(data)) > 0, nil
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
