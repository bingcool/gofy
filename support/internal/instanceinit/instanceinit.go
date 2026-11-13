// Package instanceinit coordinates lazy, per-name initialization for the named
// instance registries in support/db, support/pg, support/sqlite, support/redis
// and the message-queue packages.
//
// The registry owns configuration and lifecycle; this helper owns the map of
// already-opened values plus the in-flight attempts. Different names open in
// parallel, concurrent callers of one name share a single attempt, and a failed
// attempt is never cached, so the next call retries.
package instanceinit

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Get once Close has started. Registries translate it
// to their own ErrNotConfigured.
var ErrClosed = errors.New("instanceinit: closed")

// errPanicked is stored for waiters when an open call panics, so they do not
// receive a zero value. The panic itself still reaches the original caller.
var errPanicked = errors.New("instanceinit: open panicked")

// Slots holds the values opened by name and the attempts in flight.
type Slots[T any] struct {
	mu      sync.Mutex
	values  map[string]T
	opening map[string]*attempt[T]
	closeFn func(name string, value T) error
	closed  bool
}

// attempt is one in-flight open. done is closed after value and err are set;
// waiters read them without the mutex because the channel close provides the
// happens-before edge.
type attempt[T any] struct {
	done  chan struct{}
	value T
	err   error
}

// New builds empty slots. closeFn releases one value and is called both by
// Close and when an open completes after Close started.
func New[T any](closeFn func(name string, value T) error) *Slots[T] {
	if closeFn == nil {
		closeFn = func(string, T) error { return nil }
	}
	return &Slots[T]{
		values:  make(map[string]T),
		opening: make(map[string]*attempt[T]),
		closeFn: closeFn,
	}
}

// Get returns the value registered under name, opening it through open on first
// use. Concurrent calls for the same name wait for the first attempt; different
// names run independently. A failed attempt is dropped, so a later call retries.
func (s *Slots[T]) Get(ctx context.Context, name string, open func(context.Context) (T, error)) (T, error) {
	var zero T
	if s == nil {
		return zero, ErrClosed
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return zero, ErrClosed
	}
	if value, ok := s.values[name]; ok {
		s.mu.Unlock()
		return value, nil
	}
	if call, ok := s.opening[name]; ok {
		s.mu.Unlock()
		<-call.done
		return call.value, call.err
	}
	call := &attempt[T]{done: make(chan struct{})}
	if s.opening == nil {
		s.opening = make(map[string]*attempt[T])
	}
	s.opening[name] = call
	s.mu.Unlock()

	return s.open(ctx, name, call, open)
}

// open runs one attempt outside the lock and publishes or discards its result.
func (s *Slots[T]) open(ctx context.Context, name string, call *attempt[T], open func(context.Context) (T, error)) (value T, err error) {
	completed := false
	defer func() {
		s.mu.Lock()
		delete(s.opening, name)
		closed := s.closed
		switch {
		case !completed:
			call.err = errPanicked
		case err != nil:
			call.err = err
		case closed:
			call.err = ErrClosed
		default:
			s.values[name] = value
			call.value = value
		}
		s.mu.Unlock()

		// Close already snapshotted the values it owns, so an open that finishes
		// after Close must release its value; otherwise the pool leaks. The
		// opener gets the same ErrClosed its waiters see instead of a value from
		// a generation that is shutting down.
		if completed && err == nil && closed {
			call.err = errors.Join(ErrClosed, s.closeFn(name, value))
			var zero T
			value = zero
			err = call.err
		}
		close(call.done)
	}()

	value, err = open(ctx)
	completed = true
	return value, err
}

// Publish runs fn while the slots are still open, so Install cannot make a
// registry current after its Close has started.
func (s *Slots[T]) Publish(fn func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		fn()
	}
}

// Close stops new opens and releases every value. An open that is already
// running releases its own value when it finishes.
func (s *Slots[T]) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	values := s.values
	s.values = nil
	s.mu.Unlock()

	var errs []error
	for name, value := range values {
		if err := s.closeFn(name, value); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
