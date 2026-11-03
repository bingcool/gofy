package gofy

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext returns a context cancelled by SIGINT or SIGTERM.
// The application itself never registers signal handlers implicitly.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
