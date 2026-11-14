package appio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gofydb "github.com/bingcool/gofy/support/db"
	gofyredis "github.com/bingcool/gofy/support/redis"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestMiddlewarePinsSnapshotPerRequest(t *testing.T) {
	prev := Current()
	t.Cleanup(func() { Publish(prev) })

	pinned := &Snapshot{}
	Publish(pinned)

	var got *Snapshot
	engine := gin.New()
	engine.Use(Middleware())
	engine.GET("/", func(c *gin.Context) {
		// 模拟请求处理中发生 Reload：快照已被下一代替换。
		Publish(nil)
		got = WithContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got != pinned {
		t.Fatal("the handler did not see the snapshot pinned at request entry")
	}
	if Current() != nil {
		t.Fatal("test did not withdraw the published snapshot")
	}
}

func TestWithContextPanicsWithoutGeneration(t *testing.T) {
	prev := Current()
	t.Cleanup(func() { Publish(prev) })
	Publish(nil)

	defer func() {
		if recover() == nil {
			t.Fatal("WithContext did not panic without a published generation")
		}
	}()
	WithContext(context.Background())
}

func TestSnapshotNewIORequiresGenerationRegistry(t *testing.T) {
	prev := Current()
	t.Cleanup(func() { Publish(prev) })
	Publish(nil)
	ctx := context.Background()

	var absent *Snapshot
	if _, err := absent.DBIO(ctx, "order"); !errors.Is(err, gofydb.ErrNotConfigured) {
		t.Fatalf("nil snapshot DBIO = %v, want ErrNotConfigured", err)
	}
	if _, err := absent.RedisIO(ctx, "ratelimit"); !errors.Is(err, gofyredis.ErrNotConfigured) {
		t.Fatalf("nil snapshot RedisIO = %v, want ErrNotConfigured", err)
	}

	// 脚本/单测发布只带 db.default 的快照，没有命名实例注册表。
	PublishDB(&gorm.DB{})
	if _, err := Current().DBIO(ctx, "order"); !errors.Is(err, gofydb.ErrNotConfigured) {
		t.Fatalf("DBIO on a registry-less snapshot = %v, want ErrNotConfigured", err)
	}
	if _, err := Current().RedisIO(ctx, "ratelimit"); !errors.Is(err, gofyredis.ErrNotConfigured) {
		t.Fatalf("RedisIO on a registry-less snapshot = %v, want ErrNotConfigured", err)
	}
}
