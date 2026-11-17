package nacos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeServer struct {
	mu sync.Mutex

	content      string
	changed      bool
	token        string
	username     string
	password     string
	loginCount   int
	getCount     int
	listenCount  int
	lastListen   string
	lastHoldTime string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case loginPath:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("username") != f.username || r.PostForm.Get("password") != f.password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.loginCount++
		f.mu.Unlock()
		fmt.Fprintf(w, `{"accessToken":%q,"tokenTtl":18000}`, f.token)

	case configPath:
		if !f.authorized(w, r) {
			return
		}
		f.mu.Lock()
		content := f.content
		f.getCount++
		f.mu.Unlock()
		if content == "" {
			http.Error(w, "config data not exist", http.StatusNotFound)
			return
		}
		io.WriteString(w, content)

	case listenerPath:
		if !f.authorized(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.lastListen = r.PostForm.Get("Listening-Configs")
		f.lastHoldTime = r.Header.Get("Long-Pulling-Timeout")
		changed := f.changed
		f.changed = false
		f.listenCount++
		f.mu.Unlock()
		if changed {
			io.WriteString(w, "gofy-service\x02GO_GROUP\x01")
			return
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-r.Context().Done():
		}

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServer) authorized(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	token := f.token
	f.mu.Unlock()
	if token == "" {
		return true
	}
	if r.URL.Query().Get("accessToken") != token || r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return false
	}
	return true
}

func (f *fakeServer) set(content string, changed bool) {
	f.mu.Lock()
	f.content = content
	f.changed = changed
	f.mu.Unlock()
}

func (f *fakeServer) listens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listenCount
}

func testConfig(t *testing.T, serverURL, file string) Config {
	t.Helper()
	return Config{
		Enabled:     true,
		Addr:        serverURL,
		Namespace:   "dev",
		DataID:      "gofy-service",
		Group:       "GO_GROUP",
		File:        file,
		Timeout:     2 * time.Second,
		PollTimeout: 100 * time.Millisecond,
		RetryDelay:  10 * time.Millisecond,
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := FromEnv().normalize()
	if cfg.Enabled {
		t.Fatal("Enabled = true, want false by default")
	}
	if cfg.baseURL() != "http://127.0.0.1:8848" {
		t.Fatalf("baseURL = %q", cfg.baseURL())
	}
	if cfg.Group != DefaultGroup || cfg.File != DefaultFile {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.Timeout != 10*time.Second || cfg.PollTimeout != 30*time.Second || cfg.RetryDelay != 3*time.Second {
		t.Fatalf("timeouts = %+v", cfg)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("NACOS_ENABLED", "1")
	t.Setenv("NACOS_ADDR", "10.0.0.2:8848")
	t.Setenv("NACOS_USERNAME", "nacos")
	t.Setenv("NACOS_PASSWORD", "secret")
	t.Setenv("NACOS_NAMESPACE", "public")
	t.Setenv("NACOS_DATA_ID", "gofy-service")
	t.Setenv("NACOS_GROUP", "GO_GROUP")
	t.Setenv("NACOS_FILE", "/tmp/out.yaml")
	t.Setenv("NACOS_TIMEOUT_MS", "5000")
	t.Setenv("NACOS_LISTENER_TIMEOUT_MS", "20000")
	t.Setenv("NACOS_LISTENER_FAILED_MS", "1500")

	cfg := FromEnv()
	if !cfg.Enabled || cfg.Addr != "http://10.0.0.2:8848" {
		t.Fatalf("addr = %q enabled = %v", cfg.Addr, cfg.Enabled)
	}
	if cfg.Namespace != "" {
		t.Fatalf("namespace = %q, want empty for public", cfg.Namespace)
	}
	if cfg.DataID != "gofy-service" || cfg.Group != "GO_GROUP" || cfg.File != "/tmp/out.yaml" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Timeout != 5*time.Second || cfg.PollTimeout != 20*time.Second || cfg.RetryDelay != 1500*time.Millisecond {
		t.Fatalf("timeouts = %+v", cfg)
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	t.Setenv("NACOS_USERNAME", "env-user")
	t.Setenv("NACOS_PASSWORD", "env-pass")
	t.Setenv("NACOS_GROUP", "ENV_GROUP")

	path := filepath.Join(t.TempDir(), "nacos.yaml")
	data := []byte(`nacos:
  enabled: true
  host: 10.0.0.1
  port: 8849
  namespace: dev
  data_id: gofy-service
  group: FILE_GROUP
  listener_timeout_ms: 5000
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false")
	}
	if cfg.baseURL() != "http://10.0.0.1:8849" {
		t.Fatalf("baseURL = %q", cfg.baseURL())
	}
	if cfg.Group != "FILE_GROUP" {
		t.Fatalf("Group = %q, want file value", cfg.Group)
	}
	if cfg.Username != "env-user" || cfg.Password != "env-pass" {
		t.Fatalf("credentials = %q/%q, want env fallback", cfg.Username, cfg.Password)
	}
	if cfg.PollTimeout != 5*time.Second {
		t.Fatalf("PollTimeout = %s", cfg.PollTimeout)
	}
	if cfg.File != DefaultFile {
		t.Fatalf("File = %q", cfg.File)
	}
}

func TestLoadConfigRejectsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nacos.yaml")
	if err := os.WriteFile(path, []byte("nacos: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted invalid YAML")
	}
}

func TestLoadMergesNacosAndApplicationFiles(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", `nacos:
  host: 10.0.0.2
  port: 8849
  username: nacos
  password: secret
  namespace: dev
`)
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  service_config:
    data_id: gofy-service
    group: GO_GROUP
    tenant: test
  enable_nacos_config_change_restart: true
  monitor_config_change:
    listener_timeout_ms: 15000
    failed_wait_ms: 1200
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true by default in the two-file layout")
	}
	if cfg.baseURL() != "http://10.0.0.2:8849" {
		t.Fatalf("baseURL = %q", cfg.baseURL())
	}
	if cfg.Username != "nacos" || cfg.Password != "secret" {
		t.Fatalf("credentials = %q/%q", cfg.Username, cfg.Password)
	}
	if cfg.Namespace != "dev" {
		t.Fatalf("Namespace = %q, want nacos.yaml namespace", cfg.Namespace)
	}
	if cfg.DataID != "gofy-service" || cfg.Group != "GO_GROUP" {
		t.Fatalf("service_config = %q/%q", cfg.DataID, cfg.Group)
	}
	if !cfg.Watch {
		t.Fatal("Watch = false")
	}
	if cfg.PollTimeout != 15*time.Second || cfg.RetryDelay != 1200*time.Millisecond {
		t.Fatalf("monitor = %s/%s", cfg.PollTimeout, cfg.RetryDelay)
	}
	if cfg.File != DefaultFile {
		t.Fatalf("File = %q", cfg.File)
	}
}

func TestLoadFallsBackToServiceTenant(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  service_config:
    data_id: gofy-service
    group: GO_GROUP
    tenant: test
  enable_nacos_config_change_restart: false
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Namespace != "test" {
		t.Fatalf("Namespace = %q, want application tenant", cfg.Namespace)
	}
	if cfg.Watch {
		t.Fatal("Watch = true, want false from enable_nacos_config_change_restart")
	}
}

func TestLoadTenantFromEnv(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("NACOS_TENANT", "prd")
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  service_config:
    data_id: gofy-service
    group: GO_GROUP
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Namespace != "prd" {
		t.Fatalf("Namespace = %q, want NACOS_TENANT", cfg.Namespace)
	}
}

func TestLoadFileWinsOverEnv(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("NACOS_ENABLED", "0")
	t.Setenv("NACOS_DATA_ID", "env-service")
	t.Setenv("NACOS_GROUP", "ENV_GROUP")
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  enabled: true
  service_config:
    data_id: gofy-service
    group: GO_GROUP
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want application.yaml enabled")
	}
	if cfg.DataID != "gofy-service" || cfg.Group != "GO_GROUP" {
		t.Fatalf("service_config = %q/%q, want file values", cfg.DataID, cfg.Group)
	}
}

func TestLoadExplicitDisableAndMissingApplicationFile(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  enabled: false\n  host: 10.0.0.2\n")
	missing := filepath.Join(dir, "application.yaml")

	cfg, err := Load(nacosPath, missing)
	if err != nil {
		t.Fatalf("Load with missing application.yaml: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("Enabled = true, want explicit disable from nacos.yaml")
	}
	if !cfg.Watch {
		t.Fatal("Watch = false, want the default to stay on")
	}
}

func clearNacosEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"NACOS_ENABLED", "NACOS_ADDR", "NACOS_HOST", "NACOS_PORT",
		"NACOS_USERNAME", "NACOS_PASSWORD", "NACOS_NAMESPACE", "NACOS_TENANT",
		"NACOS_DATA_ID", "NACOS_GROUP", "NACOS_FILE", "NACOS_CONFIG_CHANGE_STRATEGY",
		"NACOS_RESTART_GRACE_PERIOD", "NACOS_TIMEOUT_MS",
		"NACOS_LISTENER_TIMEOUT_MS", "NACOS_LISTENER_FAILED_MS",
		"NACOS_SERVICE_NAMESPACE_ID", "NACOS_SERVICE_GROUP_NAME",
		"NACOS_SERVICE_REGISTER_HOST", "NACOS_SERVICE_REGISTER_PORT",
		"NACOS_DISCOVERY_LOAD_BALANCER", "NACOS_DISCOVERY_CACHE_TTL",
		"NACOS_DISCOVERY_STALE_TTL", "NACOS_DISCOVERY_HEALTHY_ONLY",
		"NACOS_DISCOVERY_CLUSTERS", "LOCAL_NACOS_SERVICE_AUTO_SWITCH", "POD_IP",
	} {
		t.Setenv(key, "")
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyncWritesConfigFile(t *testing.T) {
	content := "env: dev\nhttp:\n  port: 8080\n"
	fake := &fakeServer{content: content}
	server := httptest.NewServer(fake)
	defer server.Close()

	file := filepath.Join(t.TempDir(), "config.yaml")
	cfg := testConfig(t, server.URL, file)

	result, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !result.Changed || result.File != file || result.Digest != digest([]byte(content)) {
		t.Fatalf("result = %+v", result)
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(written) != content {
		t.Fatalf("content = %q", written)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	result, err = Sync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if result.Changed {
		t.Fatal("second Sync rewrote an identical file")
	}
}

func TestSyncRejectsInvalidYAML(t *testing.T) {
	fake := &fakeServer{content: "env: [dev\n"}
	server := httptest.NewServer(fake)
	defer server.Close()

	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("env: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Sync(context.Background(), testConfig(t, server.URL, file))
	if err == nil {
		t.Fatal("Sync accepted invalid YAML")
	}
	written, readErr := os.ReadFile(file)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(written) != "env: dev\n" {
		t.Fatalf("file was replaced with %q", written)
	}
}

func TestSyncNotFound(t *testing.T) {
	server := httptest.NewServer(&fakeServer{})
	defer server.Close()

	file := filepath.Join(t.TempDir(), "config.yaml")
	_, err := Sync(context.Background(), testConfig(t, server.URL, file))
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("err = %v, want ErrConfigNotFound", err)
	}
}

func TestSyncDisabled(t *testing.T) {
	if _, err := Sync(context.Background(), Config{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestClientUsesCachedAccessToken(t *testing.T) {
	fake := &fakeServer{
		content:  "env: dev\n",
		token:    "token-1",
		username: "nacos",
		password: "secret",
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	cfg := testConfig(t, server.URL, filepath.Join(t.TempDir(), "config.yaml"))
	cfg.Username = "nacos"
	cfg.Password = "secret"
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Get(context.Background()); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	if fake.loginCount != 1 {
		t.Fatalf("loginCount = %d, want 1", fake.loginCount)
	}
}

func TestListenEncodesRequest(t *testing.T) {
	fake := &fakeServer{content: "env: dev\n", changed: true}
	server := httptest.NewServer(fake)
	defer server.Close()

	cfg := testConfig(t, server.URL, filepath.Join(t.TempDir(), "config.yaml"))
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	changed, err := client.Listen(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	want := "gofy-service\x02GO_GROUP\x02abc123\x02dev\x01"
	if fake.lastListen != want {
		t.Fatalf("Listening-Configs = %q, want %q", fake.lastListen, want)
	}
	// PollTimeout is 100ms, below the Nacos minimum, so the announced hold is
	// raised to 30s and must match what the client actually waits for.
	// PollTimeout 为 100ms，低于 Nacos 最小值，因此声明的挂起时长提升为 30s，
	// 并与客户端实际等待的时长一致。
	if fake.lastHoldTime != "30000" {
		t.Fatalf("Long-Pulling-Timeout = %q, want 30000", fake.lastHoldTime)
	}

	changed, err = client.Listen(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if changed {
		t.Fatal("changed = true without a remote change")
	}
}

// TestWatcherBacksOffOnFastNoChange pins the second half of C3: a server that
// answers "no change" without holding the request must not turn the watcher
// into a busy loop.
// TestWatcherBacksOffOnFastNoChange 固定 C3 的另一半：服务端不挂起就回答"没有变更"
// 时，watcher 不得退化成紧循环。
func TestWatcherBacksOffOnFastNoChange(t *testing.T) {
	fake := &fakeServer{content: "env: dev\n"}
	server := httptest.NewServer(fake)
	defer server.Close()

	file := filepath.Join(t.TempDir(), "config.yaml")
	cfg := testConfig(t, server.URL, file)
	cfg.RetryDelay = 150 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, cfg) }()
	defer func() {
		cancel()
		<-done
	}()

	waitFor(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(file)
		return err == nil && string(data) == "env: dev\n"
	}, "initial config written")

	before := fake.listens()
	time.Sleep(350 * time.Millisecond)
	after := fake.listens()
	// With a 150ms backoff the window fits two or three requests; a spinning
	// watcher issues dozens.
	// 150ms 退避下该窗口只容得下两三次请求；紧循环会发出几十次。
	if got := after - before; got > 5 {
		t.Fatalf("watcher issued %d no-change listens in 350ms, want backoff", got)
	}
}

func TestWatcherAppliesChanges(t *testing.T) {
	first := "env: dev\nhttp:\n  port: 8080\n"
	second := "env: dev\nhttp:\n  port: 9090\n"
	fake := &fakeServer{content: first}
	server := httptest.NewServer(fake)
	defer server.Close()

	file := filepath.Join(t.TempDir(), "config.yaml")
	cfg := testConfig(t, server.URL, file)

	events := make(chan Result, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, cfg, WithOnChange(func(_ context.Context, result Result) error {
			events <- result
			return nil
		}))
	}()

	waitFor(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(file)
		return err == nil && string(data) == first
	}, "initial config written")

	fake.set(second, true)
	select {
	case result := <-events:
		if !result.Changed || result.File != file {
			t.Fatalf("event = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for change event")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != second {
		t.Fatalf("file = %q, want %q", data, second)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Watch returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop after cancel")
	}
}

func TestRestartProcessRejectsMissingPIDFile(t *testing.T) {
	_, err := restartProcess(filepath.Join(t.TempDir(), "missing.pid"))
	if err == nil {
		t.Fatal("restartProcess accepted a missing pid file")
	}
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSnippetTruncates(t *testing.T) {
	long := strings.Repeat("x", maxSnippetSize+10)
	if got := snippet([]byte(long)); len(got) != maxSnippetSize {
		t.Fatalf("snippet length = %d", len(got))
	}
	if snippet(nil) != "empty response" {
		t.Fatal("empty snippet")
	}
}

// TestSyncClosesItsClient pins C1: the one-shot Sync builds a client with its
// own transport, so it must close its idle connections when the call ends. The
// server-side connection count is the observable part: an unclosed connection
// stays ESTABLISHED after Sync returns.
// TestSyncClosesItsClient 固定 C1：一次性 Sync 会构建持有独立 transport 的客户端，调用
// 结束时必须关闭其空闲连接。服务端连接数是可观察的证据：未关闭的连接在 Sync 返回后仍保持
// ESTABLISHED。
func TestSyncClosesItsClient(t *testing.T) {
	fake := &fakeServer{content: "env: dev\n"}
	server := httptest.NewUnstartedServer(fake)
	var open atomic.Int32
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	server.Start()
	defer server.Close()

	cfg := testConfig(t, server.URL, filepath.Join(t.TempDir(), "config.yaml"))
	if _, err := Sync(context.Background(), cfg); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return open.Load() == 0 }, "client connection closed")
}

// closeTrackingTransport records CloseIdleConnections calls without changing
// how requests are sent.
// closeTrackingTransport 记录 CloseIdleConnections 的调用次数，不改变请求发送方式。
type closeTrackingTransport struct {
	base   http.RoundTripper
	closed atomic.Int32
}

func (t *closeTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closed.Add(1)
}

// TestWatcherRunClosesItsClient pins the other C1 call site: Run owns the
// client created by NewWatcher, so every return path closes its idle
// connections. The explicit Close stays available for a watcher that is built
// but never run.
// TestWatcherRunClosesItsClient 固定 C1 的另一处调用点：Run 拥有 NewWatcher 创建的
// 客户端，每个返回路径都关闭其空闲连接；构建后从未启动的 watcher 仍可显式 Close。
func TestWatcherRunClosesItsClient(t *testing.T) {
	fake := &fakeServer{content: "env: dev\n"}
	server := httptest.NewServer(fake)
	defer server.Close()

	cfg := testConfig(t, server.URL, filepath.Join(t.TempDir(), "config.yaml"))
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	tracker := &closeTrackingTransport{base: http.DefaultTransport}
	w.client.http.Transport = tracker

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitFor(t, 5*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.getCount > 0
	}, "initial sync")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop after cancel")
	}
	if tracker.closed.Load() == 0 {
		t.Fatal("Run returned without closing its client")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("explicit Close: %v", err)
	}
}
