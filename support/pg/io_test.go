package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/pg"
	"go.uber.org/zap"
)

func clearRegistry(t *testing.T) {
	t.Helper()
	reg, err := pg.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := pg.Configure(nil, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := pg.NewIO(context.Background(), "default"); !errors.Is(err, pg.ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearRegistry(t)

	if _, err := pg.Configure(map[string]pg.Config{" ": {DSN: "postgres://user:pass@127.0.0.1:5432/app"}}, nil); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := pg.Configure(map[string]pg.Config{"default": {}}, nil); !errors.Is(err, pg.ErrDSNRequired) {
		t.Fatalf("Configure(invalid) = %v, want ErrDSNRequired", err)
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearRegistry(t)
	cfg := pg.Config{
		DSN:         "postgres://app:supersecret@127.0.0.1:1/app?sslmode=disable&connect_timeout=1",
		PingTimeout: 2 * time.Second,
	}
	reg, err := pg.Configure(map[string]pg.Config{"order": cfg}, nil)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	ctx := context.Background()
	_, err = reg.NewIO(ctx, "order")
	if err == nil {
		t.Skip("unexpectedly connected to 127.0.0.1:1")
	}
	if !strings.Contains(err.Error(), `"order"`) {
		t.Fatalf("error = %v, want the instance name", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaked the password: %v", err)
	}
	if _, err := reg.NewIO(ctx, "order"); err == nil {
		t.Fatal("a failed instance was cached as connected")
	}

	if _, err := reg.NewIO(ctx, "missing"); !errors.Is(err, pg.ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearRegistry(t)
	reg, err := pg.NewRegistry(map[string]pg.Config{
		"order": {DSN: "postgres://user:pass@127.0.0.1:1/app?sslmode=disable&connect_timeout=1", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := pg.NewIO(ctx, "order"); !errors.Is(err, pg.ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := pg.NewIO(ctx, "order"); errors.Is(err, pg.ErrUnknownInstance) {
		t.Fatal("NewIO still reports ErrUnknownInstance after Install")
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearRegistry(t)
	reg, err := pg.Configure(map[string]pg.Config{
		"order": {DSN: "postgres://user:pass@127.0.0.1:1/app?sslmode=disable&connect_timeout=1", PingTimeout: time.Second},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := pg.NewIO(context.Background(), "order"); !errors.Is(err, pg.ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearRegistry(t)

	old, err := pg.Configure(map[string]pg.Config{
		"a": {DSN: "postgres://user:pass@127.0.0.1:1/app?sslmode=disable&connect_timeout=1", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := pg.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	if _, err := pg.NewIO(context.Background(), "a"); !errors.Is(err, pg.ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearRegistry(t)
	if _, err := pg.Configure(nil, nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := pg.CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := pg.NewIO(context.Background(), "default"); !errors.Is(err, pg.ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
}
