package appio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gofydb "github.com/bingcool/gofy/support/db"
	gofyredis "github.com/bingcool/gofy/support/redis"
	"github.com/bingcool/gofy/support/sqlite"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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

// TestDBConnectJoinsTheAmbientTransaction pins the only difference between
// DBConnect and DBIO, and the reason both exist: DBConnect obeys the transaction
// carried by ctx, DBIO deliberately ignores it.
// TestDBConnectJoinsTheAmbientTransaction 固定 DBConnect 与 DBIO 唯一的区别，也是
// 两者并存的原因：DBConnect 服从 ctx 中的事务，DBIO 刻意忽略它。
func TestDBConnectJoinsTheAmbientTransaction(t *testing.T) {
	prev := Current()
	t.Cleanup(func() { Publish(prev) })

	db, err := sqlite.New(context.Background(), sqlite.Config{DSN: ":memory:", LogLevel: "silent"}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(db); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	PublishDB(db)

	// 没有事务：返回这一代的连接，并且已经绑定 ctx，调用方不必再补 WithContext。
	plain, err := Current().DBConnect(context.Background(), "default")
	if err != nil {
		t.Fatalf("DBConnect without a transaction: %v", err)
	}
	if plain.Statement.ConnPool != db.Statement.ConnPool {
		t.Fatal("DBConnect outside a transaction did not use the generation's connection")
	}
	if plain.Statement.Context == nil {
		t.Fatal("DBConnect returned a handle without a context bound")
	}

	// 有事务：返回同一个事务，而不是连接池。
	err = gofydb.Transaction(context.Background(), db, func(ctx context.Context) error {
		tx := gofydb.WithContext(ctx, nil)
		if tx == nil {
			t.Fatal("Transaction did not put a handle into ctx")
		}
		inTx, err := Current().DBConnect(ctx, "default")
		if err != nil {
			return err
		}
		if inTx.Statement.ConnPool != tx.Statement.ConnPool {
			t.Fatal("DBConnect did not return the ambient transaction")
		}
		if inTx.Statement.ConnPool == db.Statement.ConnPool {
			t.Fatal("DBConnect returned the pool instead of the transaction")
		}
		// DBIO 在同一个 ctx 下仍然只给本代连接——这不是缺陷，是它存在的理由。
		own, err := Current().DBIO(ctx, "default")
		if err != nil {
			return err
		}
		if own != db {
			t.Fatal("DBIO should ignore the ambient transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}
