package redis_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
)

func clearRegistry(t *testing.T) {
	t.Helper()
	reg, err := redis.Configure(nil)
	if err != nil {
		t.Fatalf("Configure(nil): %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
}

func TestNewIORequiresConfigure(t *testing.T) {
	reg, err := redis.Configure(nil)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := redis.NewIO(context.Background(), "default"); !errors.Is(err, redis.ErrNotConfigured) {
		t.Fatalf("NewIO before Configure = %v, want ErrNotConfigured", err)
	}
}

func TestConfigureValidatesInstances(t *testing.T) {
	clearRegistry(t)

	if _, err := redis.Configure(map[string]redis.Config{" ": {Addr: "127.0.0.1:6379"}}); err == nil {
		t.Fatal("Configure accepted an empty instance name")
	}
	if _, err := redis.Configure(map[string]redis.Config{"default": {}}); !errors.Is(err, redis.ErrAddrRequired) {
		t.Fatalf("Configure(invalid) = %v, want ErrAddrRequired", err)
	}
}

func TestNewIOCachesByName(t *testing.T) {
	clearRegistry(t)
	mr := miniredis.RunT(t)

	reg, err := redis.Configure(map[string]redis.Config{
		"default": {Addr: mr.Addr()},
		"cache":   {Addr: mr.Addr()},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	ctx := context.Background()
	first, err := reg.NewIO(ctx, "default")
	if err != nil {
		t.Fatalf("NewIO(default): %v", err)
	}
	second, err := redis.NewIO(ctx, "default")
	if err != nil {
		t.Fatalf("NewIO(default) via current: %v", err)
	}
	if first != second {
		t.Fatal("NewIO opened a second client for the same instance")
	}
	cache, err := redis.NewIO(ctx, "cache")
	if err != nil {
		t.Fatalf("NewIO(cache): %v", err)
	}
	if cache == first {
		t.Fatal("distinct aliases share one client")
	}
	if err := first.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if _, err := redis.NewIO(ctx, "missing"); !errors.Is(err, redis.ErrUnknownInstance) {
		t.Fatalf("NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
}

func TestNewRegistryInstallsOnDemand(t *testing.T) {
	clearRegistry(t)
	mr := miniredis.RunT(t)

	reg, err := redis.NewRegistry(map[string]redis.Config{"cache": {Addr: mr.Addr()}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := redis.NewIO(ctx, "cache"); !errors.Is(err, redis.ErrUnknownInstance) {
		t.Fatalf("NewRegistry installed the generation early: %v", err)
	}

	reg.Install()
	client, err := redis.NewIO(ctx, "cache")
	if err != nil {
		t.Fatalf("NewIO after Install: %v", err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRegistryCloseClearsCurrentGeneration(t *testing.T) {
	clearRegistry(t)
	mr := miniredis.RunT(t)

	reg, err := redis.Configure(map[string]redis.Config{"default": {Addr: mr.Addr()}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if _, err := reg.NewIO(context.Background(), "default"); err != nil {
		t.Fatalf("NewIO: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := redis.NewIO(context.Background(), "default"); !errors.Is(err, redis.ErrNotConfigured) {
		t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewerGenerationSurvivesOldClose(t *testing.T) {
	clearRegistry(t)
	first := miniredis.RunT(t)
	second := miniredis.RunT(t)

	old, err := redis.Configure(map[string]redis.Config{"a": {Addr: first.Addr()}})
	if err != nil {
		t.Fatalf("Configure(old): %v", err)
	}
	fresh, err := redis.Configure(map[string]redis.Config{"b": {Addr: second.Addr()}})
	if err != nil {
		t.Fatalf("Configure(new): %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close(old): %v", err)
	}

	ctx := context.Background()
	if _, err := redis.NewIO(ctx, "b"); err != nil {
		t.Fatalf("NewIO(b) after old Close: %v", err)
	}
	if _, err := redis.NewIO(ctx, "a"); !errors.Is(err, redis.ErrUnknownInstance) {
		t.Fatalf("NewIO(a) = %v, want ErrUnknownInstance from the new generation", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close(new): %v", err)
	}
}

func TestCloseAllIO(t *testing.T) {
	clearRegistry(t)
	mr := miniredis.RunT(t)

	if _, err := redis.Configure(map[string]redis.Config{"default": {Addr: mr.Addr()}}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	ctx := context.Background()
	client, err := redis.NewIO(ctx, "default")
	if err != nil {
		t.Fatalf("NewIO: %v", err)
	}
	if err := redis.CloseAllIO(); err != nil {
		t.Fatalf("CloseAllIO: %v", err)
	}
	if _, err := redis.NewIO(ctx, "default"); !errors.Is(err, redis.ErrNotConfigured) {
		t.Fatalf("NewIO after CloseAllIO = %v, want ErrNotConfigured", err)
	}
	if err := client.Ping(ctx).Err(); err == nil {
		t.Fatal("client still answers after CloseAllIO")
	}
}

func TestConcurrentFirstOpenReturnsOneClient(t *testing.T) {
	mr := miniredis.RunT(t)
	reg, err := redis.NewRegistry(map[string]redis.Config{"default": {Addr: mr.Addr()}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	const callers = 100
	clients := make([]*goredis.Client, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			clients[i], errs[i] = reg.NewIO(context.Background(), "default")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("NewIO[%d]: %v", i, errs[i])
		}
		if clients[i] != clients[0] {
			t.Fatalf("caller %d got a different client", i)
		}
	}
}

func TestRegistriesKeepTheirOwnClients(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	cfg := map[string]redis.Config{"default": {Addr: mr.Addr()}}

	first, err := redis.NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry(first): %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := redis.NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry(second): %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	firstClient, err := first.NewIO(ctx, "default")
	if err != nil {
		t.Fatalf("first.NewIO: %v", err)
	}
	secondClient, err := second.NewIO(ctx, "default")
	if err != nil {
		t.Fatalf("second.NewIO: %v", err)
	}
	if firstClient == secondClient {
		t.Fatal("two registries share one client")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}
	if err := firstClient.Ping(ctx).Err(); err == nil {
		t.Fatal("first client still answers after its registry closed")
	}
	if err := secondClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("second client broke when the first registry closed: %v", err)
	}
}

func TestCloseRacingNewIOLeavesNoOpenRegistry(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		reg, err := redis.NewRegistry(map[string]redis.Config{"default": {Addr: mr.Addr()}})
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
				client, err := reg.NewIO(ctx, "default")
				if err == nil && client == nil {
					t.Error("NewIO returned a nil client without an error")
				}
				if err != nil && !errors.Is(err, redis.ErrNotConfigured) {
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
		if _, err := reg.NewIO(ctx, "default"); !errors.Is(err, redis.ErrNotConfigured) {
			t.Fatalf("NewIO after Close = %v, want ErrNotConfigured", err)
		}
	}
}
