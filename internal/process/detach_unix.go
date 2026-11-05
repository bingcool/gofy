//go:build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Detach starts executable in a new session with stdio redirected to
// /dev/null and returns its pid. The caller must not wait for the child.
func Detach(executable string, args []string) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("process: open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := exec.Command(executable, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("process: start %s: %w", executable, err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("process: release %d: %w", pid, err)
	}
	return pid, nil
}
