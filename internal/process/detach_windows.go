//go:build windows

package process

// Detach is not supported on Windows.
func Detach(string, []string) (int, error) { return 0, ErrUnsupported }
