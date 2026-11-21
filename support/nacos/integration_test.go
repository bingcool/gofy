package nacos_test

// Real-Nacos integration: config publish/watch and naming register, list and
// deregister. Gated on GOFY_TEST_NACOS_ADDR; without it everything here skips.
// The config side publishes through Nacos's open HTTP API because the gofy
// client is a reader by design — it watches and applies, never writes.
// 真实 Nacos 集成：配置发布/监听与命名服务的注册、发现、注销。由
// GOFY_TEST_NACOS_ADDR 显式开启。配置侧通过 Nacos open HTTP API 发布——gofy
// 客户端在设计中只读：负责监听与应用，从不写配置。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/nacos"
)

// pollWindow bounds every server-notification wait: Nacos applies writes
// asynchronously and the tests poll instead of sleeping a fixed span.
// pollWindow 限定所有等待服务端通知的窗口：Nacos 异步应用写入，测试用轮询
// 而不是固定 sleep。
const pollWindow = 30 * time.Second

func nacosBaseURL(t *testing.T) string {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("GOFY_TEST_NACOS_ADDR"))
	if addr == "" {
		t.Skip("GOFY_TEST_NACOS_ADDR is not set")
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}

// publishConfig writes a config through the open API. The client under test is
// intentionally read-only, so the test drives the write side itself.
// publishConfig 通过 open API 写配置。被测客户端刻意只读，写侧由测试自己驱动。
func publishConfig(t *testing.T, base string, dataID, group, content string) {
	t.Helper()
	form := url.Values{}
	form.Set("dataId", dataID)
	form.Set("group", group)
	form.Set("content", content)

	req, err := http.NewRequest(http.MethodPost, base+"/nacos/v1/cs/configs", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build publish request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user := os.Getenv("GOFY_TEST_NACOS_USERNAME"); user != "" {
		// Authenticated deployments log in once and append the token.
		// 开启鉴权的部署先登录一次，后续请求附加 token。
		token := nacosLogin(t, base, user, os.Getenv("GOFY_TEST_NACOS_PASSWORD"))
		form.Set("accessToken", token)
		req.URL.RawQuery = url.Values{"accessToken": []string{token}}.Encode()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("publish config: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "true") {
		t.Fatalf("publish config status=%d body=%s", resp.StatusCode, body)
	}
}

func nacosLogin(t *testing.T, base, username, password string) string {
	t.Helper()
	form := url.Values{"username": []string{username}, "password": []string{password}}
	resp, err := http.DefaultClient.PostForm(base+"/nacos/v1/auth/login", form)
	if err != nil {
		t.Fatalf("nacos login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		t.Fatalf("nacos login response: status=%d body=%s err=%v", resp.StatusCode, body, err)
	}
	return parsed.AccessToken
}

func TestIntegrationWatchReceivesConfigChange(t *testing.T) {
	base := nacosBaseURL(t)
	dataID := "gofy-itx-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".yaml"
	group := "DEFAULT_GROUP"

	publishConfig(t, base, dataID, group, "replicas: 1\n")

	cfg := nacos.Config{
		Enabled:     true,
		Addr:        base,
		DataID:      dataID,
		Group:       group,
		File:        filepath.Join(t.TempDir(), "application.yaml"),
		Timeout:     5 * time.Second,
		PollTimeout: 20 * time.Second,
		RetryDelay:  500 * time.Millisecond,
		Watch:       true,
		// Reload is a no-op without a host callback, which keeps the test
		// process alive; the file and the callback are the assertions.
		// reload 策略在无宿主回调时是空操作，测试进程因此存活；文件内容与
		// 回调才是断言对象。
		ChangeStrategy: nacos.StrategyReload,
	}

	changes := make(chan struct{}, 4)
	watcher, err := nacos.NewWatcher(cfg, nacos.WithOnChange(func(_ context.Context, _ nacos.Result) error {
		// Result carries file/digest, not content; the file rewrite is the
		// real assertion, the callback only proves the apply path ran.
		// Result 携带文件与摘要而非内容；文件重写才是真正的断言，回调只证明
		// 应用路径跑过。
		select {
		case changes <- struct{}{}:
		default:
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- watcher.Run(ctx) }()

	// The initial sync (prime) must materialise the remote config locally.
	// 初始同步（prime）必须把远端配置落到本地文件。
	waitFor(t, 15*time.Second, "initial sync", func() bool {
		data, err := os.ReadFile(cfg.File)
		return err == nil && strings.Contains(string(data), "replicas: 1")
	})

	// Publish a change; the watcher must rewrite the file and fire OnChange.
	// 发布变更；watcher 必须重写文件并触发 OnChange。
	publishConfig(t, base, dataID, group, "replicas: 2\n")
	waitFor(t, pollWindow, "config change applied", func() bool {
		data, err := os.ReadFile(cfg.File)
		if err != nil || !strings.Contains(string(data), "replicas: 2") {
			return false
		}
		select {
		case <-changes:
			return true
		default:
			return false
		}
	})

	cancel()
	select {
	case err := <-runErr:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("watcher run: %v", err)
		}
	case <-time.After(5 * time.Second):
		// Run may block inside a long poll; cancelling ctx releases it shortly
		// after the deadline above. Nothing to assert here.
		// Run 可能阻塞在长轮询里；上面的 ctx 取消会在截止后很快释放它。
	}
}

func TestIntegrationRegisterListDeregister(t *testing.T) {
	base := nacosBaseURL(t)

	client, err := nacos.NewNamingClient(nacos.Config{
		Addr: base,
		Port: 8848,
	})
	if err != nil {
		t.Fatalf("NewNamingClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	service := "gofy-itx-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	group := "DEFAULT_GROUP"
	inst := nacos.Instance{
		IP:          "127.0.0.1",
		Port:        18001,
		Weight:      1,
		Healthy:     true,
		Enabled:     true,
		Ephemeral:   false, // survive without heartbeats for the short test window
		ServiceName: service,
		Group:       group,
	}

	if err := client.RegisterInstance(context.Background(), inst); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	t.Cleanup(func() { _ = client.DeregisterInstance(context.Background(), inst) })

	// Discovery reflects the registration after the server's notification
	// delay.
	// 服务端的通知延迟过后，发现接口应看到该实例。
	waitFor(t, 15*time.Second, "registered instance visible", func() bool {
		got, err := client.ListInstances(context.Background(), nacos.InstanceQuery{
			ServiceName: service,
			Group:       group,
		})
		if err != nil {
			return false
		}
		return containsInstance(got, inst)
	})

	if err := client.DeregisterInstance(context.Background(), inst); err != nil {
		t.Fatalf("DeregisterInstance: %v", err)
	}

	// Deregistration removes the instance; polling instead of a single sleep
	// keeps the test honest about the server's async cleanup.
	// 注销会移除实例；轮询而不是单次 sleep，如实对待服务端的异步清理。
	waitFor(t, 15*time.Second, "deregistered instance gone", func() bool {
		got, err := client.ListInstances(context.Background(), nacos.InstanceQuery{
			ServiceName: service,
			Group:       group,
		})
		if err != nil {
			return false
		}
		return !containsInstance(got, inst)
	})
}

func containsInstance(list []nacos.Instance, want nacos.Instance) bool {
	for _, got := range list {
		if got.IP == want.IP && got.Port == want.Port && got.ServiceName == want.ServiceName {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, window time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, window)
}
