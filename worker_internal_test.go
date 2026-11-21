package gofy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/worker"
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

// TestStopWorkerWildcardStopsEveryWorkerPIDFile pins what the wildcard means:
// groups may have been started as separate processes, so `stop --worker=*` and
// `worker stop` have to stop every pid file the workers left behind, not only
// the one the wildcard run wrote.
// TestStopWorkerWildcardStopsEveryWorkerPIDFile 固定通配符的语义：分组可能是分别启动的，
// `stop --worker=*` 与 `worker stop` 必须停掉 worker 留下的每个 pid 文件，而不是只有
// 通配符启动写的那个。
func TestStopWorkerWildcardStopsEveryWorkerPIDFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	write := func(path string, pid int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// The HTTP pid file holds this live test process and must survive.
	write(pidPath, os.Getpid())
	// The worker files hold pids that cannot be alive, so the stop reports them
	// as not running without signalling anything real.
	orderPID := workerPIDPath(pidPath, "order")
	allPID := workerPIDPath(pidPath, commandline.WorkerAllPIDToken)
	write(orderPID, 999999)
	write(allPID, 999998)

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
	for _, pid := range []string{"999999", "999998"} {
		if !strings.Contains(err.Error(), pid) {
			t.Fatalf("error does not mention pid %s, so that file was skipped: %v", pid, err)
		}
	}
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("HTTP pid file was touched: %v", statErr)
	}
	// A pid that is gone leaves no pid file behind.
	if _, statErr := os.Stat(orderPID); !os.IsNotExist(statErr) {
		t.Fatalf("stale worker pid file survived the stop: %v", statErr)
	}
}

// TestStopWorkerWildcardReportsPartialFailure pins the case that used to be
// swallowed: one worker pid file is stale while another worker ignores SIGTERM.
// The joined error must not read as "nothing was running", or `worker restart`
// starts a second process on top of the one that is still alive.
// TestStopWorkerWildcardReportsPartialFailure 固定过去被吞掉的情形：一个 worker 的
// pid 文件已过期，另一个 worker 忽略 SIGTERM。合并后的错误不能读成"本来就没在运行"，
// 否则 `worker restart` 会在仍活着的进程之上再起一个。
func TestStopWorkerWildcardReportsPartialFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestStopHelperProcess")
	cmd.Env = append(os.Environ(), "GOFY_STOP_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("spawn helper: %v", err)
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "ready" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("helper readiness: line=%q err=%v", ready, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	orderPID := workerPIDPath(pidPath, "order")
	if err := pidfile.WriteRecord(orderPID, pidfile.NewRecord(cmd.Process.Pid), 0o644); err != nil {
		t.Fatalf("write worker pid file: %v", err)
	}
	// This one cannot be alive, so stop reports ErrNotRunning for it.
	stalePID := workerPIDPath(pidPath, commandline.WorkerAllPIDToken)
	if err := os.WriteFile(stalePID, []byte(strconv.Itoa(999999)), 0o644); err != nil {
		t.Fatalf("write stale pid file: %v", err)
	}

	app, err := New(
		WithName("stop-worker-partial-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.stop(context.Background(), commandline.StopOptions{
		Worker:  commandline.WorkerGroupAll,
		Timeout: 150 * time.Millisecond,
	})
	if !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("stop worker wildcard: got %v, want ErrStopTimeout", err)
	}
	if isRestartIgnorableError(err) {
		t.Fatalf("a partial stop failure was treated as nothing to stop: %v", err)
	}
}

// TestWorkerPIDFiles lists only the worker files derived from one pid path, and
// recovers the group name the operator needs in the message.
// TestWorkerPIDFiles 只列出由一个 pid 路径派生的 worker 文件，并还原消息里需要的分组名。
func TestWorkerPIDFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "demo.pid")
	for _, name := range []string{"demo-worker-order.pid", "demo-worker-all.pid", "demo.pid", "other-worker-x.pid"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	files, err := workerPIDFiles(base)
	if err != nil {
		t.Fatalf("workerPIDFiles: %v", err)
	}
	if len(files) != 2 || filepath.Base(files[0]) != "demo-worker-all.pid" || filepath.Base(files[1]) != "demo-worker-order.pid" {
		t.Fatalf("workerPIDFiles = %v, want the all and order files only, sorted", files)
	}
	if got := workerGroupFromPIDFile(base, files[0]); got != commandline.WorkerAllPIDToken {
		t.Fatalf("workerGroupFromPIDFile(all) = %q", got)
	}
	if got := workerGroupFromPIDFile(base, files[1]); got != "order" {
		t.Fatalf("workerGroupFromPIDFile(order) = %q", got)
	}
}

// TestStopWorkersWithoutPIDFiles keeps the failure honest: "nothing to stop" has
// to name the pattern that was searched, otherwise the operator cannot tell a
// wrong workdir from a wrong group name.
// TestStopWorkersWithoutPIDFiles 让失败保持可解释："没有可停的进程"必须指出搜索过的
// 模式，否则运维分不清是工作目录不对还是分组名不对。
func TestStopWorkersWithoutPIDFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
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
	if !strings.Contains(err.Error(), "demo-worker-*") {
		t.Fatalf("error does not name the searched pattern: %v", err)
	}
}

// TestDaemonWorkerRefusesWhenAlreadyRunning checks the pre-flight that the
// daemon path needs: the child's stdio goes to /dev/null, so a duplicate has to
// be refused while the parent can still print why. The record must carry this
// process's identity token: a bare pid number is unverifiable and therefore
// treated as stale, so only an IdentityMatch blocks the start.
// TestDaemonWorkerRefusesWhenAlreadyRunning 校验 daemon 路径需要的前置检查：子进程的
// stdio 指向 /dev/null，重复实例必须在父进程还能说明原因时就挡住。记录必须带本进程的
// 身份 token：裸 pid 数字无法验证，会被当作陈旧记录覆盖，只有 IdentityMatch 才挡住启动。
func TestDaemonWorkerRefusesWhenAlreadyRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemonizing a worker is not supported on windows")
	}
	order, err := worker.NewGroup("order", worker.Func("scan", 1, func(context.Context) error { return nil }))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := pidfile.WriteRecord(workerPIDPath(pidPath, "order"), pidfile.NewRecord(os.Getpid()), 0o644); err != nil {
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

// TestWorkerHealthReportsState covers the split probe contract: liveness stays
// 200 through starting, ready and draining, while readiness is 503 until the
// group reports and turns 503 again once the drain begins.
// TestWorkerHealthReportsState 覆盖拆开后的探针契约：liveness 在 starting、ready 与
// draining 都是 200；readiness 在分组上报前 503，drain 开始后再次 503。
func TestWorkerHealthReportsState(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	h := newWorkerHealth(addr, nil)
	if err := h.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer h.stop()

	code := func(path string) int {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := code("/healthz"); got != http.StatusOK {
		t.Fatalf("starting liveness: %d, want 200", got)
	}
	if got := code("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("starting readiness: %d, want 503", got)
	}

	h.setReady()
	if got := code("/healthz"); got != http.StatusOK {
		t.Fatalf("ready liveness: %d, want 200", got)
	}
	if got := code("/readyz"); got != http.StatusOK {
		t.Fatalf("ready readiness: %d, want 200", got)
	}

	h.setStopping()
	if got := code("/healthz"); got != http.StatusOK {
		t.Fatalf("draining liveness: %d, want 200", got)
	}
	if got := code("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("draining readiness: %d, want 503", got)
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

// TestDaemonizeWorkerStartsTheWorkerCommand pins the fix for the recursive
// restart: the detached child receives the start form, never the command the
// parent was invoked with. A child that re-ran `worker restart` would stop
// again, hit the confirmation prompt with stdin on /dev/null and return, while
// the parent had already printed success.
// TestDaemonizeWorkerStartsTheWorkerCommand 固定"restart 递归"的修复：脱离的子进程
// 收到的是启动形态，而不是父进程当初收到的命令。再跑一次 `worker restart` 的子进程会重
// 复 stop，在 stdin 指向 /dev/null 的确认提示上直接返回，而父进程已经打印了成功。
func TestDaemonizeWorkerStartsTheWorkerCommand(t *testing.T) {
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })

	var gotExe string
	var gotArgs []string
	detachProcessNotify = func(executable string, args []string) (int, *os.File, error) {
		gotExe = executable
		gotArgs = append([]string(nil), args...)
		return 4242, reportingPipe(t, "ready"), nil
	}

	group, err := worker.NewGroup("order",
		worker.Func("tick", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := New(WithName("daemon-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// What a default `worker restart` carries: daemon, no --force, plus the
	// stop-only timeout that must not reach the child.
	// 默认 `worker restart` 携带的选项：daemon、没有 --force，以及不能传给子进程的
	// stop 专用超时。
	err = app.daemonizeWorker([]*worker.Group{group}, "", commandline.WorkerOptions{
		Group:   "order",
		Daemon:  true,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("daemonizeWorker: %v", err)
	}

	if gotExe == "" {
		t.Fatal("detach received no executable")
	}
	want := []string{"worker", "--group=order", "--daemon=false"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("child argv = %v, want %v", gotArgs, want)
	}
	for _, arg := range gotArgs {
		if strings.Contains(arg, "restart") {
			t.Fatalf("child argv carries %q: the detached child must start the group, not repeat the restart", arg)
		}
	}
}

// TestClaimWorkerPIDIsExclusive pins the fix for the check-then-write race: a
// second claim of the same group must fail while the first holds it, even
// inside one process, because the pid file alone cannot express "mine".
// TestClaimWorkerPIDIsExclusive 固定"先检查再写入"竞态的修复：第一个声明还持有时，
// 同一分组的第二次声明必须失败——即使在同一进程内，因为仅凭 pid 文件无法表达"归我"。
func TestClaimWorkerPIDIsExclusive(t *testing.T) {
	app, err := New(WithName("lock-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pidPath := filepath.Join(t.TempDir(), "demo-worker-order.pid")

	release, err := app.claimWorkerPID(pidPath)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	if _, err := app.claimWorkerPID(pidPath); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second claim = %v, want ErrAlreadyRunning", err)
	}

	// The pid file still names this live process, so a restart is simulated the
	// way it actually looks: the old process is gone and its pid is stale.
	// pid 文件里仍写着当前这个活着的进程，因此按真实重启的样子模拟：旧进程已消失，它
	// 留下的 pid 是陈旧的。
	release()
	if err := os.WriteFile(pidPath, []byte("0"), 0o644); err != nil {
		t.Fatalf("rewrite pid file: %v", err)
	}
	releaseAgain, err := app.claimWorkerPID(pidPath)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	releaseAgain()
}

// TestClaimWorkerPIDOverwritesAStaleFile keeps the crash path working: a pid
// file whose process is gone must not block the next start, and neither must
// the lock file left behind.
// TestClaimWorkerPIDOverwritesAStaleFile 保证崩溃路径可用：进程已不存在的 pid 文件
// 不能挡住下一次启动，残留的锁文件同样不能。
func TestClaimWorkerPIDOverwritesAStaleFile(t *testing.T) {
	app, err := New(WithName("lock-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pidPath := filepath.Join(t.TempDir(), "demo-worker-order.pid")
	// A pid that cannot be alive: 0 is rejected by the kernel for signals and
	// process.Alive reports false for it.
	// 一个不可能活着的 pid：内核拒绝向 0 发信号，process.Alive 对它返回 false。
	if err := os.WriteFile(pidPath, []byte("0"), 0o644); err != nil {
		t.Fatalf("write stale pid: %v", err)
	}

	release, err := app.claimWorkerPID(pidPath)
	if err != nil {
		t.Fatalf("claim over stale pid file: %v", err)
	}
	release()

	written, err := pidfile.Read(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if written != os.Getpid() {
		t.Fatalf("pid file = %d, want %d", written, os.Getpid())
	}
}

// TestStopRefusesARecycledPid pins P2-03: a pid file outlives a crash, and the
// kernel recycles pids, so liveness alone must not authorize a signal. The file
// here names a live child but carries a token that cannot be its own — exactly
// what a recycled pid looks like — and stop must leave that child alone.
// TestStopRefusesARecycledPid 固定 P2-03：pid 文件会比崩溃的进程活得久，而内核会回收
// pid，因此仅凭"存活"不能授权发信号。这里的文件指向一个活着的子进程，但携带的 token 不可能
// 属于它——这正是 pid 被复用时的样子——stop 必须放过这个子进程。
func TestStopRefusesARecycledPid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("spawn helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if process.Token(cmd.Process.Pid) == "" {
		t.Skip("platform provides no process token; the case cannot be constructed")
	}

	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	record := pidfile.Record{
		PID:        cmd.Process.Pid,
		Executable: "/gone/demo-service",
		Token:      "0.000000:0.000000",
	}
	if err := pidfile.WriteRecord(pidPath, record, 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	app, err := New(
		WithName("stop-recycled-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.stop(context.Background(), commandline.StopOptions{})
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stop: got %v, want ErrNotRunning for a recycled pid", err)
	}
	if !process.Alive(cmd.Process.Pid) {
		t.Fatal("stop signalled a pid that no longer belongs to this application")
	}
	if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
		t.Fatalf("stale pid file was not removed: %v", statErr)
	}
}

// reportingPipe returns the read end of a pipe that already holds one startup
// report, which is what the detached child writes on the other end.
// reportingPipe 返回一个已经写入一行启动报告的管道的读端——也就是脱离的子进程在另一端写下的
// 内容。
func reportingPipe(t *testing.T, line string) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := writer.WriteString(line + "\n"); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

// TestDaemonizeWorkerReportsChildFailure is the P2-01 property: creating the
// child is not success. A child that fails to start names the reason, and the
// parent command must fail with that reason instead of printing "started in the
// background".
// TestDaemonizeWorkerReportsChildFailure 是 P2-01 的性质：创建子进程不等于成功。启动失败的
// 子进程会说明原因，父命令必须带着这个原因失败，而不是打印"已在后台启动"。
func TestDaemonizeWorkerReportsChildFailure(t *testing.T) {
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })
	detachProcessNotify = func(string, []string) (int, *os.File, error) {
		return 4242, reportingPipe(t, "error: gopy: listen 127.0.0.1:8080: address already in use"), nil
	}

	group, err := worker.NewGroup("order",
		worker.Func("tick", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := New(WithName("daemon-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.daemonizeWorker([]*worker.Group{group}, "", commandline.WorkerOptions{Group: "order", Daemon: true})
	if err == nil {
		t.Fatal("daemonizeWorker reported success for a child that failed to start")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("error does not carry the child's reason: %v", err)
	}
}

// TestDaemonizeWorkerReportsSilentChildDeath covers the child that dies before
// writing anything: the parent sees EOF and must not call that a success.
// TestDaemonizeWorkerReportsSilentChildDeath 覆盖什么都没写就退出的子进程：父进程读到
// EOF，不能把这种情况当成成功。
func TestDaemonizeWorkerReportsSilentChildDeath(t *testing.T) {
	prev := detachProcessNotify
	t.Cleanup(func() { detachProcessNotify = prev })
	detachProcessNotify = func(string, []string) (int, *os.File, error) {
		return 4242, reportingPipeEOF(t), nil
	}

	group, err := worker.NewGroup("order",
		worker.Func("tick", 1, func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := New(WithName("daemon-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.daemonizeWorker([]*worker.Group{group}, "", commandline.WorkerOptions{Group: "order", Daemon: true})
	if err == nil {
		t.Fatal("daemonizeWorker reported success for a child that exited before reporting")
	}
	if !strings.Contains(err.Error(), "exited before reporting ready") {
		t.Fatalf("error does not explain the silent exit: %v", err)
	}
}

// reportingPipeEOF is the closed-empty variant: no bytes, immediate EOF.
// reportingPipeEOF 是"关闭且为空"的变体：没有字节，立即 EOF。
func reportingPipeEOF(t *testing.T) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}
