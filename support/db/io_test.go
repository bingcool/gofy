package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/db"
	"go.uber.org/zap"
)

func clearRegistry(t *testing.T) {
	t.Helper()
	reg, err := db.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := db.Configure(nil, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := db.NewIO(context.Background(), "default"); !errors.Is(err, db.ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearRegistry(t)

	if _, err := db.Configure(map[string]db.Config{" ": {DSN: "user:pass@tcp(127.0.0.1:3306)/app"}}, nil); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := db.Configure(map[string]db.Config{"default": {}}, nil); !errors.Is(err, db.ErrDSNRequired) {
		t.Fatalf("Configure(invalid) = %v, want ErrDSNRequired", err)
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearRegistry(t)
	cfg := db.Config{
		DSN:         "swoolefy:supersecret@tcp(127.0.0.1:1)/app?timeout=50ms",
		PingTimeout: 100 * time.Millisecond,
	}
	reg, err := db.Configure(map[string]db.Config{"order": cfg}, nil)
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

	if _, err := reg.NewIO(ctx, "missing"); !errors.Is(err, db.ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearRegistry(t)
	reg, err := db.NewRegistry(map[string]db.Config{
		"order": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := db.NewIO(ctx, "order"); !errors.Is(err, db.ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := db.NewIO(ctx, "order"); errors.Is(err, db.ErrUnknownInstance) {
		t.Fatal("NewIO still reports ErrUnknownInstance after Install")
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearRegistry(t)
	reg, err := db.Configure(map[string]db.Config{
		"order": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := db.NewIO(context.Background(), "order"); !errors.Is(err, db.ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearRegistry(t)

	old, err := db.Configure(map[string]db.Config{
		"a": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := db.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	if _, err := db.NewIO(context.Background(), "a"); !errors.Is(err, db.ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearRegistry(t)
	if _, err := db.Configure(nil, nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := db.CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := db.NewIO(context.Background(), "default"); !errors.Is(err, db.ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
}
