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

// TestDBIOResolvesTheFrameworkDefault pins the one special case in the unified
// entry point: "default" is opened by the framework and stays out of the
// registry, so DBIO has to resolve it from the generation itself.
// TestDBIOResolvesTheFrameworkDefault 固定统一入口里唯一的特例："default" 由框架
// 打开、不在 registry 里，因此 DBIO 必须从这一代自身解析它。
func TestDBIOResolvesTheFrameworkDefault(t *testing.T) {
	prev := Current()
	t.Cleanup(func() { Publish(prev) })

	defaultDB := &gorm.DB{}
	PublishDB(defaultDB)
	ctx := context.Background()

	got, err := Current().DBIO(ctx, "default")
	if err != nil {
		t.Fatalf("DBIO(default): %v", err)
	}
	if got != defaultDB {
		t.Fatal("DBIO(default) returned a different handle than the framework default")
	}
	// 名字两侧空白会被裁剪，调用方不必自己 trim。
	if got, err := Current().DBIO(ctx, " default "); err != nil || got != defaultDB {
		t.Fatalf("DBIO(\" default \") = %v, %v", got, err)
	}

	// 没有默认库的快照（NewSet(nil)）仍然报未配置，而不是返回 nil 句柄。
	PublishDB(nil)
	if _, err := Current().DBIO(ctx, "default"); !errors.Is(err, gofydb.ErrNotConfigured) {
		t.Fatalf("DBIO(default) without a default DB = %v, want ErrNotConfigured", err)
	}
}
