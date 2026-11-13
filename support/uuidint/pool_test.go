package uuidint_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/uuidint"
)

// counter reads the Redis counter value written by the Lua script.
// counter 读取 Lua 脚本写入的 Redis 计数器值，用来断言池实际消耗的号数。
func counter(t *testing.T, mr *miniredis.Miniredis, key string) int64 {
	t.Helper()
	raw, err := mr.Get(key)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse counter %q: %v", raw, err)
	}
	return n
}

func TestNewRejectsInvalidPool(t *testing.T) {
	cases := map[string]struct {
		size     int
		interval time.Duration
	}{
		"zero size":      {size: 0, interval: time.Second},
		"negative size":  {size: -1, interval: time.Second},
		"too large":      {size: uuidint.MaxIncr + 1, interval: time.Second},
		"too short tick": {size: 10, interval: 5 * time.Millisecond},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := uuidint.New("k", uuidint.WithPool(tc.size, tc.interval)); err == nil {
				t.Fatal("New accepted an invalid pool configuration")
			}
		})
	}
}

func TestPoolWarmsAndServesLocally(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(testClient(t, mr)),
		uuidint.WithPool(5, time.Hour), // only the warm-up batch is checked, the ticker refill never fires; 只验证预热那一批，不触发定时补货
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err := gen.StartPool(ctx); err != nil {
		t.Fatalf("StartPool: %v", err)
	}
	defer gen.StopPool()

	if got := counter(t, mr, "uuid-key"); got != 5 {
		t.Fatalf("counter after warm fill = %d, want 5", got)
	}

	ids, err := gen.GetBatch(ctx, 3)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Fatalf("pooled ids are not consecutive: %v", ids)
		}
	}
	if got := counter(t, mr, "uuid-key"); got != 5 {
		t.Fatalf("counter after pooled GetBatch = %d, want 5 (no Redis call)", got)
	}

	if _, err := gen.Get(ctx); err != nil {
		t.Fatalf("Get from pool: %v", err)
	}
	if got := counter(t, mr, "uuid-key"); got != 5 {
		t.Fatalf("counter after pooled Get = %d, want 5", got)
	}

	// The fifth ID still comes from the pool; Redis is only touched once the
	// buffer is exhausted.
	// 第 5 个仍来自池；池耗尽后回落到 Redis。
	if _, err := gen.Get(ctx); err != nil {
		t.Fatalf("Get last pooled id: %v", err)
	}
	if got := counter(t, mr, "uuid-key"); got != 5 {
		t.Fatalf("counter after draining the pool = %d, want 5", got)
	}
	gen.StopPool()
	before := counter(t, mr, "uuid-key")
	if _, err := gen.Get(ctx); err != nil {
		t.Fatalf("Get after StopPool: %v", err)
	}
	if got := counter(t, mr, "uuid-key"); got != before+1 {
		t.Fatalf("counter after direct Get = %d, want %d", got, before+1)
	}
}

func TestPoolRefillsOnTicker(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(testClient(t, mr)),
		uuidint.WithPool(2, 10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := gen.StartPool(context.Background()); err != nil {
		t.Fatalf("StartPool: %v", err)
	}
	defer gen.StopPool()

	deadline := time.Now().Add(2 * time.Second)
	for counter(t, mr, "uuid-key") < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("counter = %d, want it to reach 4 via ticker refill", counter(t, mr, "uuid-key"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStopPoolWithoutStartIsSafe(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(testClient(t, mr)),
		uuidint.WithPool(10, time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gen.StopPool()

	if _, err := gen.Get(context.Background()); err != nil {
		t.Fatalf("Get without a started pool: %v", err)
	}
}

func TestStartPoolWithoutPoolIsNoop(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key", uuidint.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gen.StartPool(context.Background()); err != nil {
		t.Fatalf("StartPool without WithPool: %v", err)
	}
	gen.StopPool()
	if _, err := gen.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestGetBatchReturnsPartialPoolOnFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(testClient(t, mr)),
		uuidint.WithPool(2, time.Hour),
		uuidint.WithRetry(1, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gen.StartPool(context.Background()); err != nil {
		t.Fatalf("StartPool: %v", err)
	}
	defer gen.StopPool()

	mr.Close() // 池里已有 2 个号，剩余部分必须回 Redis，必然失败

	ids, err := gen.GetBatch(context.Background(), 5)
	if err == nil {
		t.Fatal("GetBatch succeeded with Redis down")
	}
	if !errors.Is(err, uuidint.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if len(ids) != 2 {
		t.Fatalf("partial ids = %v, want the 2 buffered ones", ids)
	}
}

func TestPoolConcurrentGets(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(testClient(t, mr)),
		uuidint.WithPool(50, 10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gen.StartPool(context.Background()); err != nil {
		t.Fatalf("StartPool: %v", err)
	}
	defer gen.StopPool()

	const workers, perWorker = 10, 20
	results := make(chan int64, workers*perWorker)
	errs := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				id, err := gen.Get(context.Background())
				if err != nil {
					errs <- err
					return
				}
				results <- id
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Get: %v", err)
	}
	seen := make(map[int64]struct{}, workers*perWorker)
	for id := range results {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d unique ids, want %d", len(seen), workers*perWorker)
	}
}
