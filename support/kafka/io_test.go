package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func clearIORegistry(t *testing.T) {
	t.Helper()
	reg, err := Configure(nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := Configure(nil)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := NewIO(context.Background(), "default"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearIORegistry(t)

	if _, err := Configure(map[string]Config{" ": {Brokers: []string{"127.0.0.1:9092"}}}); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := Configure(map[string]Config{"default": {}}); err == nil {
		t.Fatal("Configure accepted an invalid config")
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearIORegistry(t)
	cfg := Config{
		Brokers:     []string{"127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
	}
	reg, err := Configure(map[string]Config{"order": cfg})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	ctx := context.Background()
	if _, err := reg.NewIO(ctx, "order"); err == nil {
		t.Skip("unexpectedly connected to 127.0.0.1:1")
	} else if !strings.Contains(err.Error(), `"order"`) {
		t.Fatalf("error = %v, want the instance name", err)
	}
	if _, err := reg.NewIO(ctx, "order"); err == nil {
		t.Fatal("a failed instance was cached as connected")
	}

	if _, err := reg.NewIO(ctx, "missing"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearIORegistry(t)
	reg, err := NewRegistry(map[string]Config{"order": {
		Brokers:     []string{"127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := NewIO(ctx, "order"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := NewIO(ctx, "order"); errors.Is(err, ErrUnknownInstance) {
		t.Fatal("NewIO still reports ErrUnknownInstance after Install")
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearIORegistry(t)
	reg, err := Configure(map[string]Config{"default": {
		Brokers:     []string{"127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
	}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := NewIO(context.Background(), "default"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearIORegistry(t)

	old, err := Configure(map[string]Config{"a": {
		Brokers:     []string{"127.0.0.1:1"},
		DialTimeout: 50 * time.Millisecond,
	}})
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := Configure(nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	if _, err := NewIO(context.Background(), "a"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearIORegistry(t)
	if _, err := Configure(nil); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := NewIO(context.Background(), "default"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
}
