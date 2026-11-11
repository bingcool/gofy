package container

import (
	"fmt"
	"strings"
	"testing"
)

func TestLazyValueBuildsOnce(t *testing.T) {
	var value lazyValue[int]
	builds := 0
	build := func() int {
		builds++
		return 7
	}
	if got := value.Get(build); got != 7 {
		t.Fatalf("Get = %d, want 7", got)
	}
	if got := value.Get(build); got != 7 {
		t.Fatalf("second Get = %d, want 7", got)
	}
	if builds != 1 {
		t.Fatalf("build ran %d times, want 1", builds)
	}
}

// 构造期间重新取用同一个槽说明依赖成环，必须 panic 而不是让 sync.Once 死锁。
func TestLazyValueDetectsCircularDependency(t *testing.T) {
	first := newLazyValue[int]("first")
	second := newLazyValue[int]("second")

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected a circular dependency panic")
		}
		msg := fmt.Sprint(recovered)
		if !strings.Contains(msg, "circular dependency") || !strings.Contains(msg, "first") {
			t.Fatalf("panic = %q, want circular dependency naming first", msg)
		}
	}()

	first.Get(func() int {
		return second.Get(func() int {
			return first.Get(func() int { return 0 })
		})
	})
}
