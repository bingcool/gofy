//go:build windows

package process

// Detach is not supported on Windows.
// Windows 上不支持 Detach，调用总是返回 ErrUnsupported；需要后台常驻请改用系统服务机制。
func Detach(string, []string) (int, error) { return 0, ErrUnsupported }
