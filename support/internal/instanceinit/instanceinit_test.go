package instanceinit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newSlots(t *testing.T, closeFn func(string, *int) error) *Slots[*int] {
	t.Helper()
	if closeFn == nil {
		closeFn = func(string, *int) error { return nil }
	}
	return New(closeFn)
}

func TestGetCoalescesConcurrentFirstOpen(t *testing.T) {
	slots := newSlots(t, nil)
	var opens atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	open := func(context.Context) (*int, error) {
		opens.Add(1)
		once.Do(func() { close(started) })
		<-release
		value := 1
		return &value, nil
	}

	const callers = 100
	results := make([]*int, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value, err := slots.Get(context.Background(), "order", open)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			results[i] = value
		}(i)
	}

	<-started
	// Give the remaining callers time to reach the in-flight attempt; they must
	// join it instead of starting their own.
	time.Sleep(50 * time.Millisecond)
	if got := opens.Load(); got != 1 {
		t.Fatalf("open calls = %d, want 1", got)
	}
	close(release)
	wg.Wait()

	if got := opens.Load(); got != 1 {
		t.Fatalf("open calls after completion = %d, want 1", got)
	}
	for i := 1; i < callers; i++ {
		if results[i] != results[0] {
			t.Fatalf("caller %d got a different value", i)
		}
	}
}

func TestDifferentNamesOpenInParallel(t *testing.T) {
	slots := newSlots(t, nil)
	started := make(chan string, 2)
	release := make(chan struct{})
	open := func(name string) func(context.Context) (*int, error) {
		return func(context.Context) (*int, error) {
			started <- name
			<-release
			value := 1
			return &value, nil
		}
	}

	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if _, err := slots.Get(context.Background(), name, open(name)); err != nil {
				t.Errorf("Get(%s): %v", name, err)
			}
		}(name)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("different names were serialized behind one opener")
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("openers started = %v", seen)
	}
	close(release)
	wg.Wait()
}

func TestFailedOpenIsRetried(t *testing.T) {
	slots := newSlots(t, nil)
	var opens atomic.Int32
	open := func(context.Context) (*int, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		value := 2
		return &value, nil
	}

	if _, err := slots.Get(context.Background(), "a", open); err == nil {
		t.Fatal("first Get did not report the failure")
	}
	value, err := slots.Get(context.Background(), "a", open)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if *value != 2 || opens.Load() != 2 {
		t.Fatalf("retry value=%v opens=%d", *value, opens.Load())
	}
}

func TestCloseReleasesValueOpenedAfterClose(t *testing.T) {
	var closed atomic.Int32
	slots := newSlots(t, func(string, *int) error {
		closed.Add(1)
		return nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	open := func(context.Context) (*int, error) {
		close(started)
		<-release
		value := 1
		return &value, nil
	}

	errs := make(chan error, 1)
	go func() {
		_, err := slots.Get(context.Background(), "order", open)
		errs <- err
	}()
	<-started
	if err := slots.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(release)

	if err := <-errs; !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close = %v, want ErrClosed", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("close calls = %d, want 1", got)
	}
	if _, err := slots.Get(context.Background(), "order", open); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close = %v, want ErrClosed", err)
	}
}

func TestPublishSkipsClosedSlots(t *testing.T) {
	slots := newSlots(t, nil)
	if err := slots.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	published := false
	slots.Publish(func() { published = true })
	if published {
		t.Fatal("Publish ran after Close")
	}
}
