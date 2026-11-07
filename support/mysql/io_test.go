package mysql_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/mysql"
	"go.uber.org/zap"
)

func clearRegistry(t *testing.T) {
	t.Helper()
	reg, err := mysql.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := mysql.Configure(nil, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := mysql.NewIO(context.Background(), "default"); !errors.Is(err, mysql.ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearRegistry(t)

	if _, err := mysql.Configure(map[string]mysql.Config{" ": {DSN: "user:pass@tcp(127.0.0.1:3306)/app"}}, nil); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := mysql.Configure(map[string]mysql.Config{"default": {}}, nil); !errors.Is(err, mysql.ErrDSNRequired) {
		t.Fatalf("Configure(invalid) = %v, want ErrDSNRequired", err)
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearRegistry(t)
	cfg := mysql.Config{
		DSN:         "swoolefy:supersecret@tcp(127.0.0.1:1)/app?timeout=50ms",
		PingTimeout: 100 * time.Millisecond,
	}
	reg, err := mysql.Configure(map[string]mysql.Config{"order": cfg}, nil)
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

	if _, err := reg.NewIO(ctx, "missing"); !errors.Is(err, mysql.ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearRegistry(t)
	reg, err := mysql.NewRegistry(map[string]mysql.Config{
		"order": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := mysql.NewIO(ctx, "order"); !errors.Is(err, mysql.ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := mysql.NewIO(ctx, "order"); errors.Is(err, mysql.ErrUnknownInstance) {
		t.Fatal("NewIO still reports ErrUnknownInstance after Install")
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearRegistry(t)
	reg, err := mysql.Configure(map[string]mysql.Config{
		"order": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := mysql.NewIO(context.Background(), "order"); !errors.Is(err, mysql.ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearRegistry(t)

	old, err := mysql.Configure(map[string]mysql.Config{
		"a": {DSN: "user:pass@tcp(127.0.0.1:1)/app?timeout=50ms", PingTimeout: time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := mysql.Configure(nil, nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	if _, err := mysql.NewIO(context.Background(), "a"); !errors.Is(err, mysql.ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearRegistry(t)
	if _, err := mysql.Configure(nil, nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := mysql.CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := mysql.NewIO(context.Background(), "default"); !errors.Is(err, mysql.ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
}
