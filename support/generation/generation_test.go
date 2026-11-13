package generation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type handle struct {
	id int
}

func TestPublishAndCurrent(t *testing.T) {
	set := NewSet[handle]("test")
	if got := set.Current(); got != nil {
		t.Fatalf("fresh set Current() = %v, want nil", got)
	}

	first := &handle{id: 1}
	set.Publish(first)
	if got := set.Current(); got != first {
		t.Fatal("Current did not return the published generation")
	}

	set.Publish(nil)
	if got := set.Current(); got != nil {
		t.Fatal("Publish(nil) did not withdraw the generation")
	}
}

func TestNewSetName(t *testing.T) {
	if got := NewSet[handle]("  io  ").Name(); got != "io" {
		t.Fatalf("Name() = %q, want trimmed %q", got, "io")
	}
	if got := NewSet[handle]("").Name(); got != "generation" {
		t.Fatalf("Name() = %q, want %q", got, "generation")
	}
}

func TestWithContextPrefersPinnedGeneration(t *testing.T) {
	set := NewSet[handle]("test")
	pinned := &handle{id: 1}
	set.Publish(pinned)

	ctx := context.Background()
	if got := set.WithContext(ctx); got != pinned {
		t.Fatal("WithContext did not fall back to the published generation")
	}

	ctx = set.WithSnapshot(ctx, pinned)
	successor := &handle{id: 2}
	set.Publish(successor)
	if got := set.WithContext(ctx); got != pinned {
		t.Fatal("WithContext did not keep the generation pinned on the context")
	}
	if got := set.WithContext(context.Background()); got != successor {
		t.Fatal("WithContext without a pin did not see the new generation")
	}

	// A nil pin must not override the fallback.
	if got := set.WithContext(set.WithSnapshot(context.Background(), nil)); got != successor {
		t.Fatal("WithContext honoured a nil pinned generation")
	}
}

func TestWithSnapshotNilContext(t *testing.T) {
	set := NewSet[handle]("test")
	v := &handle{id: 1}
	ctx := set.WithSnapshot(nil, v) //nolint:staticcheck // nil ctx is what this test checks
	if got := set.WithContext(ctx); got != v {
		t.Fatal("WithSnapshot did not pin the generation on a nil context")
	}
}

func TestWithContextPanicsWithoutGeneration(t *testing.T) {
	set := NewSet[handle]("app/io")

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("WithContext did not panic without a published generation")
		}
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "app/io") {
			t.Fatalf("panic message = %v, want it to name the set", recovered)
		}
	}()
	set.WithContext(context.Background())
}

func TestCompareAndSwap(t *testing.T) {
	set := NewSet[handle]("test")
	current := &handle{id: 1}
	set.Publish(current)

	if set.CompareAndSwap(&handle{id: 1}, nil) {
		t.Fatal("CompareAndSwap swapped over a different generation")
	}
	if set.Current() != current {
		t.Fatal("CompareAndSwap changed the generation on a failed swap")
	}

	successor := &handle{id: 2}
	set.Publish(successor)
	if set.CompareAndSwap(current, nil) {
		t.Fatal("a stopped generation withdrew its successor")
	}
	if set.Current() != successor {
		t.Fatal("the successor is no longer published")
	}

	if !set.CompareAndSwap(successor, nil) {
		t.Fatal("CompareAndSwap did not withdraw its own generation")
	}
	if set.Current() != nil {
		t.Fatal("the generation was not withdrawn")
	}
}

func TestMiddlewarePinsGenerationPerRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	set := NewSet[handle]("test")
	pinned := &handle{id: 1}
	set.Publish(pinned)

	var got *handle
	engine := gin.New()
	engine.Use(set.Middleware())
	engine.GET("/", func(c *gin.Context) {
		// 模拟请求处理中发生 Reload：当前代已被下一代替换。
		set.Publish(&handle{id: 2})
		got = set.WithContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got != pinned {
		t.Fatal("the handler did not see the generation pinned at request entry")
	}
}

func TestMiddlewareLeavesRequestWithoutGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	set := NewSet[handle]("test")

	called := false
	engine := gin.New()
	engine.Use(set.Middleware())
	engine.GET("/", func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("the middleware did not pass the request through")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSetsAreIsolated(t *testing.T) {
	first := NewSet[handle]("first")
	second := NewSet[handle]("second")
	v := &handle{id: 1}
	first.Publish(v)

	if got := second.Current(); got != nil {
		t.Fatal("publishing into one set leaked into another")
	}

	ctx := first.WithSnapshot(context.Background(), v)
	if got := first.WithContext(ctx); got != v {
		t.Fatal("the owning set did not read its pinned generation")
	}

	// second 既没有当前代、也没有自己的固定值，不能读到 first 的固定值。
	defer func() {
		if recover() == nil {
			t.Fatal("one set read a generation pinned for another set")
		}
	}()
	second.WithContext(ctx)
}
