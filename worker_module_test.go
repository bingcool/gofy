package gofy_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/worker"
)

// moduleFactory returns a factory for m, matching the shape an application uses
// to assemble its IO generation.
// moduleFactory 返回 m 的工厂，形状与应用装配 IO 代际时一致。
func moduleFactory(m support.Module) gofy.ModuleFactory {
	return func(*core.Runtime) (support.Module, error) { return m, nil }
}

// TestExecuteWorkerStartsModulesBeforeGroupsAndStopsAfter pins the two ordering
// constraints the hook exists for: Start runs before the first group (a tick
// resolves a handle on its first iteration), and Stop runs after the groups
// drained (a late handle must not point at a closed pool).
// TestExecuteWorkerStartsModulesBeforeGroupsAndStopsAfter 固定这个钩子存在的两条
// 顺序约束：Start 先于第一个分组（首个 tick 就要解析句柄），Stop 晚于分组 drain
// （迟到的句柄不能指向已关闭的池）。
func TestExecuteWorkerStartsModulesBeforeGroupsAndStopsAfter(t *testing.T) {
	rec := &recorder{}
	group, err := worker.NewGroup("order", worker.Func("scan", 1, func(ctx context.Context) error {
		rec.add("worker")
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-module-test"),
		gofy.WithWorkerGroups(group),
		gofy.WithWorkerModules(moduleFactory(&testModule{name: "io", order: rec})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.ExecuteArgs(ctx, []string{"worker", "--group=order"}) }()

	waitFor(t, func() bool { return len(rec.snapshot()) >= 2 }, "module and worker to start")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteArgs error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("worker command did not stop after cancellation")
	}

	got := strings.Join(rec.snapshot(), ",")
	want := "start:io,worker,stop:io"
	if got != want {
		t.Fatalf("lifecycle = %q, want %q", got, want)
	}
}

// TestExecuteWorkerModuleBuildFailureAbortsStart checks the fail-fast side: a
// factory that cannot build its module must stop the process before any worker
// exists, not leave it running with an unassembled generation.
// TestExecuteWorkerModuleBuildFailureAbortsStart 检查快速失败的一侧：建不出模块的
// 工厂必须在任何 worker 存在之前让进程停下，而不是让它带着未装配的代际继续运行。
func TestExecuteWorkerModuleBuildFailureAbortsStart(t *testing.T) {
	var started atomic.Int32
	group, err := worker.NewGroup("order", worker.Func("scan", 1, func(ctx context.Context) error {
		started.Add(1)
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	boom := errors.New("assembly failed")
	app, err := gofy.New(
		gofy.WithName("worker-module-test"),
		gofy.WithWorkerGroups(group),
		gofy.WithWorkerModules(func(*core.Runtime) (support.Module, error) { return nil, boom }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.ExecuteArgs(context.Background(), []string{"worker", "--group=order"})
	if !errors.Is(err, boom) {
		t.Fatalf("ExecuteArgs error = %v, want %v", err, boom)
	}
	if got := started.Load(); got != 0 {
		t.Fatalf("worker started %d times, want 0", got)
	}
}

// TestExecuteWorkerModuleStartFailureStopsEarlierModules checks the partial
// case: module A started, module B failed. A must be stopped by the abort path
// and the worker must never run.
// TestExecuteWorkerModuleStartFailureStopsEarlierModules 检查部分成功的情形：A 已
// 启动、B 失败。abort 路径必须停掉 A，且 worker 从未运行。
func TestExecuteWorkerModuleStartFailureStopsEarlierModules(t *testing.T) {
	rec := &recorder{}
	group, err := worker.NewGroup("order", worker.Func("scan", 1, func(ctx context.Context) error {
		rec.add("worker")
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	boom := errors.New("start failed")
	app, err := gofy.New(
		gofy.WithName("worker-module-test"),
		gofy.WithWorkerGroups(group),
		gofy.WithWorkerModules(
			moduleFactory(&testModule{name: "a", order: rec}),
			moduleFactory(&testModule{name: "b", order: rec, start: boom}),
		),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.ExecuteArgs(context.Background(), []string{"worker", "--group=order"})
	if !errors.Is(err, boom) {
		t.Fatalf("ExecuteArgs error = %v, want %v", err, boom)
	}
	got := strings.Join(rec.snapshot(), ",")
	if want := "start:a,start:b,stop:a"; got != want {
		t.Fatalf("lifecycle = %q, want %q", got, want)
	}
}

// TestWithWorkerModulesRejectsNil keeps a nil factory a construction error: it
// would panic on the first worker start, which is the least observable place.
// TestWithWorkerModulesRejectsNil 让 nil 工厂在构造期就失败：它会在第一次 worker
// 启动时 panic，那是最难观察的位置。
func TestWithWorkerModulesRejectsNil(t *testing.T) {
	if _, err := gofy.New(gofy.WithName("worker-module-test"), gofy.WithWorkerModules(nil)); err == nil {
		t.Fatalf("New with a nil worker module factory = nil error, want error")
	}
}

// TestExecuteWorkerModuleNamesMustBeUnique stops two factories from producing
// the same module name: cleanup stops by identity, but logs and diagnostics
// identify a module by name, and two of them would make both useless.
// TestExecuteWorkerModuleNamesMustBeUnique 阻止两个工厂产出同名模块：cleanup 按
// 身份停止，但日志与诊断按名字识别模块，重名会让两者都失去意义。
func TestExecuteWorkerModuleNamesMustBeUnique(t *testing.T) {
	rec := &recorder{}
	group, err := worker.NewGroup("order", worker.Func("scan", 1, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-module-test"),
		gofy.WithWorkerGroups(group),
		gofy.WithWorkerModules(
			moduleFactory(&testModule{name: "io", order: rec}),
			moduleFactory(&testModule{name: "io", order: rec}),
		),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.ExecuteArgs(context.Background(), []string{"worker", "--group=order"})
	if err == nil || !strings.Contains(err.Error(), "duplicate worker module") {
		t.Fatalf("ExecuteArgs error = %v, want duplicate module error", err)
	}
}
