package gofy

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsRestartIgnorableError(t *testing.T) {
	ignorable := []error{
		ErrNotRunning,
		ErrPIDFileDisabled,
		ErrUnsupported,
		fmt.Errorf("stop: %w", ErrNotRunning),
	}
	for _, err := range ignorable {
		if !isRestartIgnorableError(err) {
			t.Errorf("isRestartIgnorableError(%v) = false, want true", err)
		}
	}

	fatal := []error{
		ErrStopTimeout,
		ErrAlreadyRunning,
		errors.New("boom"),
	}
	for _, err := range fatal {
		if isRestartIgnorableError(err) {
			t.Errorf("isRestartIgnorableError(%v) = true, want false", err)
		}
	}
}
