package gofy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/support/worker"
	"go.uber.org/zap"
)

func TestWorkerPIDPath(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		group string
		want  string
	}{
		{"empty", "", "order", ""},
		{"relative", "app/demo-service.pid", "order", "app/demo-service-worker-order.pid"},
		{"absolute", "/tmp/demo.pid", "settle", "/tmp/demo-worker-settle.pid"},
		{"no extension", "/tmp/demo", "order", "/tmp/demo-worker-order"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerPIDPath(tc.path, tc.group); got != tc.want {
				t.Fatalf("workerPIDPath(%q, %q) = %q, want %q", tc.path, tc.group, got, tc.want)
			}
		})
	}
}

func TestWorkerDescriptorsResolveConcurrency(t *testing.T) {
	order, err := worker.NewGroup("order",
		worker.Func("scan", 1, func(context.Context) error { return nil }),
		worker.Func("notify", 3, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	settle, err := worker.NewGroup("settle",
		worker.Func("reconcile", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}

	app, err := New(
		WithName("worker-test"),
		WithWorkerGroups(order, settle),
		WithWorkerConcurrency(map[string]int{"order/scan": 4}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := app.workerDescriptors()
	if len(got) != 2 {
		t.Fatalf("groups = %d, want 2", len(got))
	}
	concurrency := map[string]int{}
	for _, g := range got {
		for _, w := range g.Workers {
			concurrency[g.Name+"/"+w.Name] = w.Concurrency
		}
	}
	// scan is overridden by the option, notify keeps the count Func carries,
	// reconcile declares nothing beyond one.
	// scan 被选项覆盖，notify 保留 Func 自带的计数，reconcile 只声明 1 个。
	want := map[string]int{
		"order/scan":       4,
		"order/notify":     3,
		"settle/reconcile": 1,
	}
	for key, n := range want {
		if concurrency[key] != n {
			t.Fatalf("concurrency[%s] = %d, want %d (all: %v)", key, concurrency[key], n, concurrency)
		}
	}
}

func TestWithWorkerConcurrencyRejectsBadInput(t *testing.T) {
	cases := []map[string]int{
		{"order": 1},
		{"order/scan/extra": 1},
		{"/scan": 1},
		{"order/": 1},
		{"order/scan": 0},
	}
	for _, c := range cases {
		if _, err := New(WithName("worker-test"), WithWorkerConcurrency(c)); err == nil {
			t.Fatalf("New(%v) = nil error, want validation failure", c)
		}
	}
}

func TestWithWorkerGroupsRejectsDuplicate(t *testing.T) {
	build := func() *worker.Group {
		g, err := worker.NewGroup("order", worker.Func("scan", 1, func(context.Context) error { return nil }))
		if err != nil {
			t.Fatalf("NewGroup: %v", err)
		}
		return g
	}
	if _, err := New(WithName("worker-test"), WithWorkerGroups(build(), build())); err == nil {
		t.Fatalf("New with two identical groups = nil error, want failure")
	}
}

// TestStopWorkerTargetsTheGroupPidFile is the property that keeps `stop
// --worker=order` from killing the API server: with only the HTTP pid file
// present, stopping the group must report "not running" against the suffixed
// path and leave the HTTP file untouched.
// TestStopWorkerTargetsTheGroupPidFile 是 `stop --worker=order` 不会杀掉 API 服务
// 的关键：只存在 HTTP 的 pid 文件时，停止分组必须针对带后缀的路径报"未运行"，并且
// 完全不碰 HTTP 的那个文件。
func TestStopWorkerTargetsTheGroupPidFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	app, err := New(
		WithName("stop-worker-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.stop(context.Background(), commandline.StopOptions{Worker: "order"})
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stop worker: got %v, want ErrNotRunning", err)
	}
	if !strings.Contains(err.Error(), "demo-worker-order.pid") {
		t.Fatalf("error does not name the group pid file: %v", err)
	}
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("HTTP pid file was touched: %v", statErr)
	}
}

// TestStopWorkerWildcardUsesTheAllPidFile pins the name the wildcard run writes:
// `stop --worker=*` has to resolve `-worker-all.pid`, otherwise a process started
// with `--group=*` could never be stopped by the flag that started it.
// TestStopWorkerWildcardUsesTheAllPidFile 固定通配符启动写的文件名：
// `stop --worker=*` 必须解析到 `-worker-all.pid`，否则用 `--group=*` 起
// 来的进程没法用配套的旗标停掉。
func TestStopWorkerWildcardUsesTheAllPidFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	app, err := New(
		WithName("stop-worker-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.stop(context.Background(), commandline.StopOptions{Worker: commandline.WorkerGroupAll})
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stop worker wildcard: got %v, want ErrNotRunning", err)
	}
	if !strings.Contains(err.Error(), "demo-worker-all.pid") {
		t.Fatalf("error does not name the wildcard pid file: %v", err)
	}
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("HTTP pid file was touched: %v", statErr)
	}
}

// TestSelectWorkerGroups covers the two shapes --group accepts: one name, or the
// wildcard that returns every registered group without mutating the registry.
// TestSelectWorkerGroups 覆盖 --group 的两种形态：单个名字，或返回全部已注册分组且
// 不改动注册表的通配符。
func TestSelectWorkerGroups(t *testing.T) {
	build := func(name string) *worker.Group {
		t.Helper()
		g, err := worker.NewGroup(name, worker.Func(name+"-tick", 1, func(context.Context) error { return nil }))
		if err != nil {
			t.Fatalf("NewGroup(%s): %v", name, err)
		}
		return g
	}
	app, err := New(
		WithName("worker-test"),
		WithWorkerGroups(build("order"), build("report")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	all, err := app.selectWorkerGroups(commandline.WorkerGroupAll)
	if err != nil {
		t.Fatalf("selectWorkerGroups(*): %v", err)
	}
	if len(all) != 2 || all[0].Name() != "order" || all[1].Name() != "report" {
		t.Fatalf("selectWorkerGroups(*) = %v, want order and report in order", groupNames(all))
	}
	if got := len(app.opts.workerGroups); got != 2 {
		t.Fatalf("registered groups = %d after selection, want 2", got)
	}

	// The alias exists because a shell expands an unquoted *, so it has to
	// select exactly what the wildcard selects.
	// 别名的存在是因为 shell 会展开不加引号的 *，因此它必须与通配符选中完全相同的东西。
	alias, err := app.selectWorkerGroups(commandline.WorkerGroupAllAlias)
	if err != nil {
		t.Fatalf("selectWorkerGroups(all): %v", err)
	}
	if len(alias) != 2 || alias[0].Name() != "order" || alias[1].Name() != "report" {
		t.Fatalf("selectWorkerGroups(all) = %v, want order and report in order", groupNames(alias))
	}

	one, err := app.selectWorkerGroups("report")
	if err != nil {
		t.Fatalf("selectWorkerGroups(report): %v", err)
	}
	if len(one) != 1 || one[0].Name() != "report" {
		t.Fatalf("selectWorkerGroups(report) = %v, want just report", groupNames(one))
	}

	if _, err := app.selectWorkerGroups("missing"); !errors.Is(err, ErrWorkerGroupNotFound) {
		t.Fatalf("selectWorkerGroups(missing) = %v, want ErrWorkerGroupNotFound", err)
	}
}

func groupNames(groups []*worker.Group) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.Name()
	}
	return out
}

// TestWorkerHealthReportsState covers the probe contract: 503 before the group
// starts, 200 while it runs, 503 again while it drains.
// TestWorkerHealthReportsState 覆盖探针契约：分组启动前 503，运行中 200，drain 时
// 再次 503。
func TestWorkerHealthReportsState(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	h := newWorkerHealth(addr)
	if err := h.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.stop()

	code := func() int {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("starting: %d, want 503", got)
	}
	h.setReady()
	if got := code(); got != http.StatusOK {
		t.Fatalf("ready: %d, want 200", got)
	}
	h.setStopping()
	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("stopping: %d, want 503", got)
	}
}

func TestWithWorkerHealthValidatesAddr(t *testing.T) {
	if _, err := New(WithWorkerHealth("127.0.0.1")); err == nil {
		t.Fatal("New accepted an address without a port")
	}
	app, err := New(WithWorkerHealth(":9099"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app.opts.workerHealth != ":9099" {
		t.Fatalf("workerHealth = %q, want :9099", app.opts.workerHealth)
	}
}

// TestWithWorkerConcurrencyRejectsUnknownWorker guards the failure mode that
// multi-group setups make easy: a renamed worker leaves a stale key behind, and
// without this check the only symptom is that worker running one instance.
// TestWithWorkerConcurrencyRejectsUnknownWorker 守住多分组下容易出现的失败：worker
// 改名后留下过期键，没有这道校验时唯一的表现就是那个 worker 只跑一个实例。
func TestWithWorkerConcurrencyRejectsUnknownWorker(t *testing.T) {
	group, err := worker.NewGroup("order",
		worker.Func("scan", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}

	for _, key := range []string{"order/missing", "settle/scan", "order/scanx"} {
		_, err := New(
			WithName("worker-test"),
			WithWorkerGroups(group),
			WithWorkerConcurrency(map[string]int{key: 2}),
		)
		if err == nil {
			t.Fatalf("New accepted concurrency key %q that matches no worker", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error %v does not name the offending key %q", err, key)
		}
	}
}

// TestWithWorkerConcurrencyValidatesAfterGroupsAreKnown pins that the check runs
// once every option has been applied: with several groups registered in
// different places, the concurrency map may be declared before the group it
// addresses.
// TestWithWorkerConcurrencyValidatesAfterGroupsAreKnown 固定"所有选项应用完才校验"：
// 分组可能在多个地方注册，并发映射完全可能先于它要覆盖的分组声明。
func TestWithWorkerConcurrencyValidatesAfterGroupsAreKnown(t *testing.T) {
	group, err := worker.NewGroup("order",
		worker.Func("scan", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := New(
		WithName("worker-test"),
		WithWorkerConcurrency(map[string]int{"order/scan": 3}),
		WithWorkerGroups(group),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, g := range app.workerDescriptors() {
		for _, w := range g.Workers {
			if g.Name+"/"+w.Name == "order/scan" && w.Concurrency != 3 {
				t.Fatalf("order/scan concurrency = %d, want 3", w.Concurrency)
			}
		}
	}
}

// TestDaemonWorkerRefusesWhenAlreadyRunning checks the pre-flight that the
// daemon path needs: the child's stdio goes to /dev/null, so a duplicate has to
// be refused while the parent can still print why.
// TestDaemonWorkerRefusesWhenAlreadyRunning 校验 daemon 路径需要的前置检查：子进程的
// stdio 指向 /dev/null，重复实例必须在父进程还能说明原因时就挡住。
func TestDaemonWorkerRefusesWhenAlreadyRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemonizing a worker is not supported on windows")
	}
	order, err := worker.NewGroup("order", worker.Func("scan", 1, func(context.Context) error { return nil }))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := os.WriteFile(workerPIDPath(pidPath, "order"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	app, err := New(
		WithName("daemon-worker-test"),
		WithPIDFile(pidPath),
		WithWorkerGroups(order),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.runWorker(context.Background(), commandline.WorkerOptions{Group: "order", Daemon: true})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("runWorker daemon = %v, want ErrAlreadyRunning", err)
	}
}
