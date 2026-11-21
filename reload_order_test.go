package gofy_test

import (
	"context"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
)

// TestModuleStopOrderIsTheSameAfterReload pins the invariant P3-02 questioned:
// a reload rebuilds a.started as [factory modules..., injected modules...],
// exactly like startup, and cleanup stops that list in reverse. The injected
// module therefore stops first in both trajectories.
// TestModuleStopOrderIsTheSameAfterReload 固定 P3-02 质疑的那个不变量：Reload 把
// a.started 重建为 [工厂模块..., 注入模块...]，与启动时一致，cleanup 按逆序停止。
// 因此注入模块在两条轨迹里都是先停的那个。
func TestModuleStopOrderIsTheSameAfterReload(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	factory := func() gofy.ModuleFactory {
		return func(*gofy.Runtime) (support.Module, error) {
			return &testModule{name: "factory-mod", order: events}, nil
		}
	}
	injected := &testModule{name: "injected-mod", order: events}

	app, err := gofy.New(
		gofy.WithName("module-order-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(factory()),
		gofy.WithModules(injected),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	if err := app.Reload(context.Background(), gofy.ReloadSpec{
		ModuleFactories: []gofy.ModuleFactory{factory()},
	}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}

	var stops []string
	for _, event := range events.snapshot() {
		if len(event) > 5 && event[:5] == "stop:" {
			stops = append(stops, event)
		}
	}
	// The last two stops are the shutdown of the final generation: the injected
	// module first, then the factory module, which is the startup order too.
	// 最后两条是最终一代的停机：注入模块先停，工厂模块后停，与启动时一致。
	if len(stops) < 2 {
		t.Fatalf("stops = %v, want at least two", stops)
	}
	last := stops[len(stops)-2:]
	if last[0] != "stop:injected-mod" || last[1] != "stop:factory-mod" {
		t.Fatalf("final stop order = %v, want [stop:injected-mod stop:factory-mod]", last)
	}
}
