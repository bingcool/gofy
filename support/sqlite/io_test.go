package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bingcool/gofy/support/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func clearRegistry(t *testing.T) {
	t.Helper()
	reg, err := sqlite.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

// tempDSN 每次返回独立的临时库路径，测试之间互不影响。
func tempDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "app.db")
}

// badDSN 指向不存在的目录，用来验证建连失败的处理。
func badDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing-dir", "app.db")
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := sqlite.Configure(nil, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := sqlite.NewIO(context.Background(), "default"); !errors.Is(err, sqlite.ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearRegistry(t)

	if _, err := sqlite.Configure(map[string]sqlite.Config{" ": {DSN: "app.db"}}, nil); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := sqlite.Configure(map[string]sqlite.Config{"default": {}}, nil); !errors.Is(err, sqlite.ErrDSNRequired) {
		t.Fatalf("Configure(invalid) = %v, want ErrDSNRequired", err)
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearRegistry(t)
	reg, err := sqlite.Configure(map[string]sqlite.Config{"order": {DSN: badDSN(t)}}, nil)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	ctx := context.Background()
	_, err = reg.NewIO(ctx, "order")
	if err == nil {
		t.Skip("unexpectedly opened a database in a missing directory")
	}
	if !strings.Contains(err.Error(), `"order"`) {
		t.Fatalf("error = %v, want the instance name", err)
	}
	if _, err := reg.NewIO(ctx, "order"); err == nil {
		t.Fatal("a failed instance was cached as connected")
	}

	if _, err := reg.NewIO(ctx, "missing"); !errors.Is(err, sqlite.ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewIOCachesPool(t *testing.T) {
	clearRegistry(t)
	if _, err := sqlite.Configure(map[string]sqlite.Config{"order": {DSN: tempDSN(t)}}, nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	ctx := context.Background()
	first, err := sqlite.NewIO(ctx, "order")
	if err != nil {
		t.Fatalf("first NewIO: %v", err)
	}
	second, err := sqlite.NewIO(ctx, "order")
	if err != nil {
		t.Fatalf("second NewIO: %v", err)
	}
	if first != second {
		t.Fatal("NewIO opened a second pool for the same instance")
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearRegistry(t)
	dsn := tempDSN(t)
	reg, err := sqlite.NewRegistry(map[string]sqlite.Config{"order": {DSN: dsn}}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := sqlite.NewIO(ctx, "order"); !errors.Is(err, sqlite.ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := sqlite.NewIO(ctx, "order"); err != nil {
		t.Fatalf("NewIO after Install: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearRegistry(t)
	reg, err := sqlite.Configure(map[string]sqlite.Config{"order": {DSN: tempDSN(t)}}, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := sqlite.NewIO(context.Background(), "order"); !errors.Is(err, sqlite.ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearRegistry(t)

	old, err := sqlite.Configure(map[string]sqlite.Config{"a": {DSN: tempDSN(t)}}, nil)
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := sqlite.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	if _, err := sqlite.NewIO(context.Background(), "a"); !errors.Is(err, sqlite.ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearRegistry(t)
	if _, err := sqlite.Configure(nil, nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := sqlite.CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := sqlite.NewIO(context.Background(), "default"); !errors.Is(err, sqlite.ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
}

func TestConcurrentFirstOpenReturnsOnePool(t *testing.T) {
	reg, err := sqlite.NewRegistry(map[string]sqlite.Config{"main": {DSN: tempDSN(t)}}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	const callers = 100
	pools := make([]*gorm.DB, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			pools[i], errs[i] = reg.NewIO(context.Background(), "main")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("NewIO[%d]: %v", i, errs[i])
		}
		if pools[i] != pools[0] {
			t.Fatalf("caller %d got a different pool", i)
		}
	}
}

func TestRegistriesKeepTheirOwnPools(t *testing.T) {
	ctx := context.Background()
	first, err := sqlite.NewRegistry(map[string]sqlite.Config{"main": {DSN: tempDSN(t)}}, nil)
	if err != nil {
		t.Fatalf("NewRegistry(first): %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := sqlite.NewRegistry(map[string]sqlite.Config{"main": {DSN: tempDSN(t)}}, nil)
	if err != nil {
		t.Fatalf("NewRegistry(second): %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	firstPool, err := first.NewIO(ctx, "main")
	if err != nil {
		t.Fatalf("first.NewIO: %v", err)
	}
	secondPool, err := second.NewIO(ctx, "main")
	if err != nil {
		t.Fatalf("second.NewIO: %v", err)
	}
	if firstPool == secondPool {
		t.Fatal("two registries share one pool")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}
	if err := secondPool.Exec("CREATE TABLE t (id INTEGER)").Error; err != nil {
		t.Fatalf("second pool broke when the first registry closed: %v", err)
	}
}

func TestCloseRacingNewIOLeavesNoOpenRegistry(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		reg, err := sqlite.NewRegistry(map[string]sqlite.Config{"main": {DSN: ":memory:"}}, nil)
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				pool, err := reg.NewIO(ctx, "main")
				if err == nil && pool == nil {
					t.Error("NewIO returned a nil pool without an error")
				}
				if err != nil && !errors.Is(err, sqlite.ErrNotConfigured) {
					t.Errorf("NewIO during Close = %v", err)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = reg.Close()
		}()
		close(start)
		wg.Wait()
		if _, err := reg.NewIO(ctx, "main"); !errors.Is(err, sqlite.ErrNotConfigured) {
			t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
		}
	}
}
