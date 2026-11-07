package rocketmq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func registryConfig() Config {
	return Config{
		Endpoint:         "127.0.0.1:1",
		SendTimeout:      50 * time.Millisecond,
		SkipStartupCheck: true,
	}
}

func failingRegistryConfig() Config {
	cfg := registryConfig()
	cfg.Endpoint = "invalid-endpoint"
	return cfg
}

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

	if _, err := Configure(map[string]Config{" ": registryConfig()}); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := Configure(map[string]Config{"default": {}}); !errors.Is(err, ErrEndpointRequired) {
		t.Fatalf("Configure without an endpoint = %v, want ErrEndpointRequired", err)
	}
	reg, err := Configure(map[string]Config{"default": {Endpoint: "127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("Configure with an endpoint but no startup topics: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestConfigureAllowsSharedEndpoint(t *testing.T) {
	clearIORegistry(t)
	reg, err := Configure(map[string]Config{
		"order": registryConfig(),
		"pay":   registryConfig(),
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewIOReportsAliasAndDoesNotCacheFailures(t *testing.T) {
	clearIORegistry(t)
	reg, err := Configure(map[string]Config{"order": failingRegistryConfig()})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	ctx := context.Background()
	if _, err := reg.NewIO(ctx, "order"); err == nil {
		t.Fatal("unexpectedly opened an invalid endpoint")
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
	reg, err := NewRegistry(map[string]Config{"order": registryConfig()})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	ctx := context.Background()
	if _, err := NewIO(ctx, "order"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}
	reg.Install()
	if _, err := NewIO(ctx, "order"); err != nil {
		t.Fatalf("NewIO after Install: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearIORegistry(t)
	reg, err := Configure(map[string]Config{"default": registryConfig()})
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

	old, err := Configure(map[string]Config{"a": registryConfig()})
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := Configure(nil)
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
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
