package gofy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"go.uber.org/zap"
)

func TestDuplicatePID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")
	self := os.Getpid()

	if _, ok := duplicatePID("", self); ok {
		t.Fatal("empty pid path reported a duplicate")
	}
	if _, ok := duplicatePID(filepath.Join(dir, "missing.pid"), self); ok {
		t.Fatal("missing pid file reported a duplicate")
	}

	if err := pidfile.Write(path, self, 0o600); err != nil {
		t.Fatalf("write own pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); ok {
		t.Fatal("own pid reported a duplicate after a self re-exec")
	}

	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
		t.Fatalf("write parent pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); !ok {
		t.Fatal("a live pid belonging to another process was not reported")
	}
}

func TestStopAcceptingRefusesNewRequestsButDrainsInFlight(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Slow") == "1" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	swap := newHandlerSwap(newGeneration(1, handler), app.admission)

	before := httptest.NewRecorder()
	swap.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/", nil))
	if before.Code != http.StatusOK {
		t.Fatalf("status before StopAccepting = %d, want 200", before.Code)
	}

	slowReq := httptest.NewRequest(http.MethodGet, "/slow", nil)
	slowReq.Header.Set("X-Slow", "1")
	slowRec := httptest.NewRecorder()
	slowDone := make(chan int, 1)
	go func() {
		swap.ServeHTTP(slowRec, slowReq)
		slowDone <- slowRec.Code
	}()
	<-entered

	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting: %v", err)
	}
	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting twice: %v", err)
	}

	after := httptest.NewRecorder()
	swap.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/", nil))
	if after.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after StopAccepting = %d, want 503", after.Code)
	}
	if got := after.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection = %q, want close", got)
	}

	close(release)
	if code := <-slowDone; code != http.StatusOK {
		t.Fatalf("in-flight request status = %d, want 200 (drain must not cut it)", code)
	}
}

func TestStopAcceptingBeforeServerExists(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !app.admission.isOpen() {
		t.Fatal("a new app must accept requests")
	}
	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting before Run: %v", err)
	}
	if app.admission.isOpen() {
		t.Fatal("gate stayed open before the server was built")
	}
	// The gate is the only piece that exists before the server, so the resume
	// side must work there too: that is exactly the window a startup-time
	// config change hits.
	// server 建好之前只有闸门存在，因此恢复侧也必须能在这里工作：启动期配置变更
	// 命中的正是这个窗口。
	if err := app.ResumeAccepting(context.Background()); err != nil {
		t.Fatalf("ResumeAccepting before Run: %v", err)
	}
	if !app.admission.isOpen() {
		t.Fatal("gate stayed closed after ResumeAccepting")
	}
}

func TestStopAcceptingOverRealListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	app, err := New(
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: port, Mode: "test"}),
		WithHealthEndpoints(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(ctx) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := app.StopAccepting(ctx); err != nil {
		t.Fatalf("StopAccepting: %v", err)
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("request after StopAccepting failed (listener should stay open): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("real HTTP status after StopAccepting = %d, want 503", resp.StatusCode)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestStartDaemonWaitsForChildReady pins A1's parent side: the detached child
// gets the startup pipe and the parent prints success only after the "ready"
// line. Without it an occupied port or a failed module start looked exactly like
// a successful start, because the child's own output goes to /dev/null.
// TestStartDaemonWaitsForChildReady 固定 A1 父进程一侧的行为：脱离的子进程拿到启动
// 管道，父进程只在读到 "ready" 之后才打印成功。没有这条通道时，端口被占或模块启动失败
// 与成功启动看起来完全一样，因为子进程自己的输出去了 /dev/null。
func TestStartDaemonWaitsForChildReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("start --daemon is not supported on windows")
	}
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })

	var gotArgs []string
	detachProcessNotify = func(_ string, args []string) (int, *os.File, error) {
		gotArgs = append([]string(nil), args...)
		return 4242, reportingPipe(t, "ready"), nil
	}

	app, err := New(WithName("daemon-start-test"), WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.start(context.Background(), commandline.StartOptions{
		Daemon:         true,
		StartupTimeout: time.Second,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != "--daemon=false" {
		t.Fatalf("child argv = %v, want a trailing --daemon=false", gotArgs)
	}
}

// TestStartDaemonReportsChildFailure makes the parent carry the child's reason
// instead of claiming success: a start that fails before reporting ready must
// surface the error the child wrote on the pipe.
// TestStartDaemonReportsChildFailure 要求父进程转达子进程给出的原因，而不是声称成功：
// 在上报就绪之前就失败的启动，必须把子进程写在管道上的错误暴露出来。
func TestStartDaemonReportsChildFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("start --daemon is not supported on windows")
	}
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })
	detachProcessNotify = func(string, []string) (int, *os.File, error) {
		return 4242, reportingPipe(t, "error: gofy: listen 127.0.0.1:8080: address already in use"), nil
	}

	app, err := New(WithName("daemon-start-test"), WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.start(context.Background(), commandline.StartOptions{Daemon: true, StartupTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("start = %v, want the child's listen error", err)
	}
}

// TestStartDaemonReportsSilentChildDeath covers the child that dies before
// writing anything: the closed pipe is the only evidence, and the parent must
// report it instead of success.
// TestStartDaemonReportsSilentChildDeath 覆盖还没写任何东西就退出的子进程：关闭的管道是
// 唯一证据，父进程必须报告失败而不是成功。
func TestStartDaemonReportsSilentChildDeath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("start --daemon is not supported on windows")
	}
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })
	detachProcessNotify = func(string, []string) (int, *os.File, error) {
		return 4242, reportingPipeEOF(t), nil
	}

	app, err := New(WithName("daemon-start-test"), WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.start(context.Background(), commandline.StartOptions{Daemon: true, StartupTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "exited before reporting ready") {
		t.Fatalf("start = %v, want the silent-death report", err)
	}
}

// TestRunReportsReadyToTheDaemonParent pins A1's child side: the HTTP run path
// writes "ready" only after the listener accepts connections and modules
// started, which is what lets the detached parent print success truthfully.
// TestRunReportsReadyToTheDaemonParent 固定 A1 子进程一侧的行为：HTTP 运行路径只在
// listener 已能接受连接、模块已启动之后写 "ready"，父进程因此才能如实打印成功。
func TestRunReportsReadyToTheDaemonParent(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()

	app, err := New(
		WithName("daemon-ready-test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: port, Mode: "test"}),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.notifier = &startupNotifier{file: writer}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()

	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("read startup report: %v", err)
	}
	if got := strings.TrimSpace(line); got != "ready" {
		t.Fatalf("startup report = %q, want %q", got, "ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// TestRunReportsStartupFailureToTheDaemonParent covers the child that fails
// after the handshake is set up: a listen error must reach the parent as
// "error: ..." rather than as a silent exit.
// TestRunReportsStartupFailureToTheDaemonParent 覆盖握手建立后才失败的子进程：监听错误
// 必须以 "error: ..." 到达父进程，而不是变成静默退出。
func TestRunReportsStartupFailureToTheDaemonParent(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()

	app, err := New(
		WithName("daemon-failure-test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: port, Mode: "test"}),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.notifier = &startupNotifier{file: writer}

	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded on an occupied port")
	}
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("read startup report: %v", err)
	}
	report := strings.TrimSpace(line)
	if !strings.HasPrefix(report, "error: ") || !strings.Contains(report, "listen") {
		t.Fatalf("startup report = %q, want an error naming the listen failure", report)
	}
}
