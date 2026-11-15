package gofy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
)

// generationStateKey is the context key the test module uses to publish "which
// generation of state am I". The real demo pins an IO snapshot the same way.
type generationStateKey struct{}

// boundModule is a module whose handler and bound state belong to the same
// generation: handler reports m.gen, binder pins m.gen. A request that reads a
// different number from the two sources is exactly the bug P1-01 fixes.
type boundModule struct {
	name     string
	gen      int
	startErr error
	release  chan struct{}

	entered   chan struct{}
	enterOnce sync.Once

	stops atomic.Int32
}

func (m *boundModule) Name() string { return m.name }

func (m *boundModule) Register(r *support.Router) error {
	r.GET("/value", func(c *gin.Context) {
		pinned, _ := c.Request.Context().Value(generationStateKey{}).(int)
		m.signalEntered()
		if m.release != nil {
			<-m.release
		}
		c.String(http.StatusOK, strconv.Itoa(m.gen)+":"+strconv.Itoa(pinned))
	})
	return nil
}

func (m *boundModule) Start(context.Context) error { return m.startErr }

func (m *boundModule) Stop(context.Context) error {
	m.stops.Add(1)
	return nil
}

func (m *boundModule) RequestBinder() support.RequestBinderFunc {
	gen := m.gen
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, generationStateKey{}, gen)
	}
}

func (m *boundModule) signalEntered() {
	if m.entered == nil {
		return
	}
	m.enterOnce.Do(func() { close(m.entered) })
}

func binderFactory(m *boundModule) gofy.ModuleFactory {
	return func(*gofy.Runtime) (support.Module, error) { return m, nil }
}

// fetchText is getText without t.Fatalf, so it may run on a goroutine.
func fetchText(t *testing.T, port int, path string) (string, error) {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// TestRequestBinderPinsTheGenerationsOwnState blocks a request inside
// generation 1, reloads to generation 2 and checks both directions: the new
// request sees the new handler together with the new state, and the blocked
// request finishes on the handler and the state it entered with.
func TestRequestBinderPinsTheGenerationsOwnState(t *testing.T) {
	port := freePort(t)
	first := &boundModule{
		name:    "binder",
		gen:     1,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app, err := gofy.New(
		gofy.WithName("binder-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(binderFactory(first)),
		gofy.WithShutdownTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	blocked := make(chan string, 1)
	blockedErr := make(chan error, 1)
	go func() {
		body, err := fetchText(t, port, "/binder/value")
		if err != nil {
			blockedErr <- err
			return
		}
		blocked <- body
	}()

	select {
	case <-first.entered:
	case err := <-blockedErr:
		t.Fatalf("blocked request failed before entering the handler: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached generation 1")
	}

	second := &boundModule{name: "binder", gen: 2}
	reloadErr := make(chan error, 1)
	go func() {
		reloadErr <- app.Reload(context.Background(), gofy.ReloadSpec{
			ModuleFactories: []gofy.ModuleFactory{binderFactory(second)},
		})
	}()

	// The new generation is published while the old request is still blocked,
	// so polling for "2:2" is exactly "the swap happened, the old lease is
	// still held".
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, err := fetchText(t, port, "/binder/value")
		if err != nil {
			t.Fatalf("request during reload: %v", err)
		}
		if body == "2:2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reload never published generation 2, last body %q", body)
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-reloadErr:
		t.Fatalf("Reload returned while the previous generation still had a request in flight: %v", err)
	default:
	}

	close(first.release)
	select {
	case got := <-blocked:
		if got != "1:1" {
			t.Fatalf("in-flight request = %q, want handler 1 with state 1", got)
		}
	case err := <-blockedErr:
		t.Fatalf("in-flight request failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not finish")
	}

	select {
	case err := <-reloadErr:
		if err != nil {
			t.Fatalf("Reload: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload did not finish after the last in-flight request drained")
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
}

// TestFailedReloadKeepsTheServingGenerationsState covers the second half of
// P1-01: a reload whose IO module already started but whose next module fails
// must not disturb the generation that is serving. The next request keeps
// reading handler 1 with state 1, and the half-built module is stopped.
func TestFailedReloadKeepsTheServingGenerationsState(t *testing.T) {
	port := freePort(t)
	first := &boundModule{name: "binder", gen: 1}
	app, err := gofy.New(
		gofy.WithName("binder-failed-reload"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(binderFactory(first)),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	if got := getText(t, port, "/binder/value"); got != "1:1" {
		t.Fatalf("before reload = %q", got)
	}

	halfBuilt := &boundModule{name: "binder", gen: 2}
	failing := &boundModule{name: "failing", gen: 0, startErr: errors.New("start failed")}
	err = app.Reload(context.Background(), gofy.ReloadSpec{
		ModuleFactories: []gofy.ModuleFactory{binderFactory(halfBuilt), binderFactory(failing)},
	})
	if err == nil {
		t.Fatal("Reload succeeded although the second module failed to start")
	}

	if got := getText(t, port, "/binder/value"); got != "1:1" {
		t.Fatalf("after failed reload = %q, want handler 1 with state 1", got)
	}
	if n := halfBuilt.stops.Load(); n != 1 {
		t.Fatalf("half-built module stopped %d times, want 1", n)
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
}
