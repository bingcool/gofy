package gofy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
)

// captureStdout runs fn with os.Stdout redirected, so a test can assert on what
// an operator would see without the table landing in the test log.
// captureStdout 在 fn 执行期间把 os.Stdout 重定向，测试因此可以断言运维看到的内容，
// 而那张家表格不会落进测试日志。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(data)
}

// A legacy pid file carries no token, so the process may be alive yet be a
// different one. status must not report that as a healthy "running".
// 旧格式 pid 文件没有 token，进程可能活着但已不是原来那个。status 不能把它报成健康的 running。
func TestDescribePIDFileLegacyIsUnverified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy pid file: %v", err)
	}
	got := describePIDFile(context.Background(), path, "")
	if got.Status != statusUnverified {
		t.Fatalf("status = %q, want %q", got.Status, statusUnverified)
	}
	if got.Verified {
		t.Fatal("a tokenless pid file must not be marked verified")
	}
	if shown := displayStatus(got); shown != statusUnverified {
		t.Fatalf("displayStatus = %q, want %q", shown, statusUnverified)
	}
	if reason := statusReason(got); !strings.Contains(reason, "--force") {
		t.Fatalf("statusReason = %q, want it to mention --force", reason)
	}
}

// A wildcard listener is not dialable as-is, and the probe runs on the same
// host by construction, so 0.0.0.0 has to become loopback.
// 通配地址不能照原样拨号，而探测按定义跑在同一台主机上，因此 0.0.0.0 必须换回环。
func TestHealthURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "wildcard host", in: "0.0.0.0:8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "port only", in: ":9091", want: "http://127.0.0.1:9091/healthz"},
		{name: "loopback", in: "127.0.0.1:8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "explicit host", in: "192.168.1.10:8080", want: "http://192.168.1.10:8080/healthz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthURL(tc.in); got != tc.want {
				t.Fatalf("healthURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// "did not answer" and "not configured" must stay distinguishable: the first is
// a symptom, the second is how the framework ships.
// "没有应答"与"没有配置"必须分开：前者是症状，后者是框架的默认状态。
func TestProbeHealth(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer ok.Close()

	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer notReady.Close()

	// Reserve an address and release it, so the probe hits a closed port instead
	// of a real listener.
	// 先占一个地址再释放，探测因此打到一个关掉的端口而不是真实监听。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closed := listener.Addr().String()
	_ = listener.Close()

	cases := []struct {
		name string
		addr string
		want string
	}{
		{name: "disabled", addr: "", want: "disabled"},
		{name: "ok", addr: strings.TrimPrefix(ok.URL, "http://"), want: "ok"},
		{name: "not ready", addr: strings.TrimPrefix(notReady.URL, "http://"), want: "not ready (503)"},
		{name: "unreachable", addr: closed, want: "unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeHealth(context.Background(), tc.addr); got != tc.want {
				t.Fatalf("probeHealth(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// describePIDFile has to separate four answers that look alike from the outside:
// no file, a pid that exited, a pid the kernel handed to somebody else, and a
// live pid we cannot prove is ours.
// describePIDFile 必须区分四种从外面看很像的情况：文件不在、pid 已退出、pid 已被内核
// 分配给别人、活着的 pid 但无法证明是我们的。
func TestDescribePIDFileStates(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	missing := describePIDFile(ctx, filepath.Join(dir, "missing.pid"), "")
	if missing.Status != statusMissing {
		t.Fatalf("missing: status = %q", missing.Status)
	}

	runningPath := filepath.Join(dir, "running.pid")
	if err := pidfile.WriteRecord(runningPath, pidfile.NewRecord(os.Getpid()), 0o600); err != nil {
		t.Fatalf("write running pid: %v", err)
	}
	running := describePIDFile(ctx, runningPath, "")
	if running.Status != statusRunning || running.PID != os.Getpid() {
		t.Fatalf("running: status/pid = %q/%d", running.Status, running.PID)
	}
	if running.Health != "disabled" {
		t.Fatalf("running: health = %q, want disabled without an address", running.Health)
	}

	// A token that cannot belong to this process stands in for a recycled pid.
	// 一个不可能属于本进程的 token 用来代替"pid 被回收"。
	stalePath := filepath.Join(dir, "stale.pid")
	staleRecord := pidfile.Record{PID: os.Getpid(), Token: "not-this-process"}
	if err := pidfile.WriteRecord(stalePath, staleRecord, 0o600); err != nil {
		t.Fatalf("write stale pid: %v", err)
	}
	if got := describePIDFile(ctx, stalePath, ""); got.Status != statusStale {
		t.Fatalf("stale: status = %q", got.Status)
	}

	// No token means "unknown", never "matches": the process is alive but the
	// file cannot prove its identity, so status reports a distinct state rather
	// than a healthy "running".
	// 没有 token 表示"未知"，绝不是"匹配"：进程活着但文件无法证明身份，因此 status 报独立状态，
	// 而不是健康的 running。
	unknownPath := filepath.Join(dir, "unknown.pid")
	if err := pidfile.WriteRecord(unknownPath, pidfile.Record{PID: os.Getpid()}, 0o600); err != nil {
		t.Fatalf("write unknown pid: %v", err)
	}
	unknown := describePIDFile(ctx, unknownPath, "")
	if unknown.Status != statusUnverified || unknown.Verified {
		t.Fatalf("unknown: status/verified = %q/%v", unknown.Status, unknown.Verified)
	}
	if got := displayStatus(unknown); got != statusUnverified {
		t.Fatalf("displayStatus = %q", got)
	}

	stoppedPath := filepath.Join(dir, "stopped.pid")
	if err := pidfile.WriteRecord(stoppedPath, pidfile.Record{PID: 999999}, 0o600); err != nil {
		t.Fatalf("write stopped pid: %v", err)
	}
	if got := describePIDFile(ctx, stoppedPath, ""); got.Status != statusStopped {
		t.Fatalf("stopped: status = %q", got.Status)
	}
}

// `status` on a live pid file must exit 0 and name the process; `worker status`
// must find the per-group file rather than only the configured path.
// 对活着的 pid 文件执行 `status` 必须退出 0 并报出进程；`worker status` 必须找到按分组
// 命名的文件，而不是只看配置的那个路径。
func TestStatusReportsRunningProcesses(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "demo.pid")
	if err := pidfile.WriteRecord(base, pidfile.NewRecord(os.Getpid()), 0o600); err != nil {
		t.Fatalf("write base pid: %v", err)
	}

	app, err := New(WithName("demo"), WithPIDFile(base))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	out := captureStdout(t, func() {
		if err := app.status(context.Background(), commandline.StatusOptions{}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "运行中") || !strings.Contains(out, "demo") {
		t.Fatalf("status output = %q", out)
	}

	// A worker started as its own process writes its own file; a status that
	// read only the configured path would report "not running" here.
	// 单独启动的 worker 写自己的文件；只读配置路径的 status 在这里会报"未运行"。
	workerPath := workerPIDPath(base, "group3")
	if err := pidfile.WriteRecord(workerPath, pidfile.NewRecord(os.Getpid()), 0o600); err != nil {
		t.Fatalf("write worker pid: %v", err)
	}
	out = captureStdout(t, func() {
		if err := app.workerStatus(context.Background(), commandline.WorkerOptions{}); err != nil {
			t.Fatalf("worker status: %v", err)
		}
	})
	if !strings.Contains(out, "group3") {
		t.Fatalf("worker status output = %q", out)
	}

	// Selecting a group that never wrote a file is an error, not an empty table:
	// silently reporting nothing would look like "all good".
	// 选中一个从未写过文件的分组是错误而不是空表：静默无输出会被读成"一切正常"。
	err = captureStatusErr(t, func() error {
		return app.workerStatus(context.Background(), commandline.WorkerOptions{Group: "absent"})
	})
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("absent group: err = %v, want ErrNotRunning", err)
	}
}

func captureStatusErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = fn() })
	return err
}
