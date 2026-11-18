package nacos

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newRaceRegistrar builds a registrar pointed at fake, with a one-second
// heartbeat so a leaked loop becomes visible quickly.
// newRaceRegistrar 构造一个指向 fake 的注册器；心跳间隔取 1 秒，泄漏的循环能很快暴露。
func newRaceRegistrar(t *testing.T, fake *fakeNaming) *Registrar {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", server.URL, err)
	}
	port, _ := strconv.Atoi(u.Port())
	cfg := Config{Addr: server.URL, Port: port, Timeout: 2 * time.Second}
	cfg.Register = RegisterConfig{
		Enabled:           true,
		ServiceName:       "race-service",
		IP:                "10.0.0.9",
		Port:              18080,
		HeartbeatInterval: time.Second,
	}
	reg, err := NewRegistrar(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegistrar: %v", err)
	}
	return reg
}

// TestRegistrarStopWaitsForInFlightStart is the regression for the lifecycle
// race: Stop used to observe "started" during registration, flip it back, return
// success — and then Start would finish, start the heartbeat, and keep renewing
// an instance that had already been deregistered. Now Stop waits for Start.
// TestRegistrarStopWaitsForInFlightStart 是生命周期竞态的回归测试：过去 Stop 会在
// 注册过程中看到"已启动"，把它翻回去并成功返回；随后 Start 完成、启动心跳，继续为一个
// 已经注销的实例续约。现在 Stop 会等 Start 结束。
func TestRegistrarStopWaitsForInFlightStart(t *testing.T) {
	fake := &fakeNaming{
		registerGate:    make(chan struct{}),
		registerStarted: make(chan struct{}),
	}
	reg := newRaceRegistrar(t, fake)

	startErr := make(chan error, 1)
	go func() { startErr <- reg.Start(context.Background()) }()

	select {
	case <-fake.registerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Start never reached the register call")
	}

	stopErr := make(chan error, 1)
	go func() { stopErr <- reg.Stop(context.Background()) }()

	// Stop must not report success while the registration is still in flight.
	// 注册还在途中时，Stop 不能报告成功。
	select {
	case err := <-stopErr:
		t.Fatalf("Stop returned before Start finished: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(fake.registerGate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := <-stopErr; err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if reg.state != registrarStopped || reg.cancel != nil {
		t.Fatalf("state = %v, cancel set = %v; want stopped with no heartbeat", reg.state, reg.cancel != nil)
	}

	// A heartbeat that leaked past the Stop would beat within one interval.
	// 越过 Stop 泄漏出来的心跳会在一个间隔内发出请求。
	time.Sleep(1200 * time.Millisecond)
	registers, deregisters, beats, _ := fake.snapshot()
	if len(registers) != 1 || len(deregisters) != 1 {
		t.Fatalf("registers = %d, deregisters = %d; want 1 and 1", len(registers), len(deregisters))
	}
	if len(beats) != 0 {
		t.Fatalf("heartbeat ran after Stop returned: %d beats", len(beats))
	}
}

// TestRegistrarStopHonoursDeadlineWhileStartRegisters pins the other half of the
// contract: waiting for Start must not ignore the caller's deadline, and the
// registrar stays usable afterwards.
// TestRegistrarStopHonoursDeadlineWhileStartRegisters 钉住契约的另一半：等待 Start
// 不能无视调用方的 deadline，而且之后注册器仍然可用。
func TestRegistrarStopHonoursDeadlineWhileStartRegisters(t *testing.T) {
	fake := &fakeNaming{
		registerGate:    make(chan struct{}),
		registerStarted: make(chan struct{}),
	}
	reg := newRaceRegistrar(t, fake)

	startErr := make(chan error, 1)
	go func() { startErr <- reg.Start(context.Background()) }()
	select {
	case <-fake.registerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Start never reached the register call")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := reg.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want DeadlineExceeded", err)
	}

	close(fake.registerGate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := reg.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	registers, deregisters, _, _ := fake.snapshot()
	if len(registers) != 1 || len(deregisters) != 1 {
		t.Fatalf("registers = %d, deregisters = %d; want 1 and 1", len(registers), len(deregisters))
	}
}

// TestRegistrarStopWithCancelledContextStillDeregisters guards the shutdown
// behaviour the lifecycle slot must not break: a Stop whose context was already
// cancelled by the shutdown still deregisters, exactly as before.
// TestRegistrarStopWithCancelledContextStillDeregisters 守护生命周期操作位不能破坏的
// 关闭行为：关闭流程已经取消了 ctx，Stop 仍然要完成注销，与之前一致。
func TestRegistrarStopWithCancelledContextStillDeregisters(t *testing.T) {
	fake := &fakeNaming{}
	reg := newRaceRegistrar(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := reg.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadCtx, deadCancel := context.WithCancel(context.Background())
	deadCancel() // already cancelled, the way a shutdown context arrives
	if err := reg.Stop(deadCtx); err != nil {
		t.Fatalf("Stop with a cancelled context: %v", err)
	}

	registers, deregisters, _, _ := fake.snapshot()
	if len(registers) != 1 || len(deregisters) != 1 {
		t.Fatalf("registers = %d, deregisters = %d; want 1 and 1", len(registers), len(deregisters))
	}
	if reg.state != registrarStopped {
		t.Fatalf("state = %v, want stopped", reg.state)
	}
}

// TestRegistrarConcurrentStartRegistersOnce keeps idempotency honest under
// concurrency: the second Start joins the first instead of registering again.
// TestRegistrarConcurrentStartRegistersOnce 保证并发下仍是幂等的：第二个 Start 加入
// 第一个，而不是再注册一次。
func TestRegistrarConcurrentStartRegistersOnce(t *testing.T) {
	fake := &fakeNaming{
		registerGate:    make(chan struct{}),
		registerStarted: make(chan struct{}),
	}
	reg := newRaceRegistrar(t, fake)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = reg.Start(context.Background())
		}(i)
	}

	select {
	case <-fake.registerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Start never reached the register call")
	}
	time.Sleep(100 * time.Millisecond) // let the second caller queue on the lifecycle slot
	close(fake.registerGate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Start[%d]: %v", i, err)
		}
	}
	if err := reg.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if registers, _, _, _ := fake.snapshot(); len(registers) != 1 {
		t.Fatalf("registers = %d, want 1", len(registers))
	}
}
