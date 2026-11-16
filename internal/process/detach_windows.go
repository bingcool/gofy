//go:build windows

package process

import "os"

// Detach is not supported on Windows.
// Windows 上不支持 Detach，调用总是返回 ErrUnsupported；需要后台常驻请改用系统服务机制。
func Detach(string, []string) (int, error) { return 0, ErrUnsupported }

// DetachWithNotify is not supported on Windows either; the startup channel needs
// inherited pipes, which daemon mode does not have there.
// Windows 上同样不支持 DetachWithNotify；启动通道需要继承的管道，而后台模式在那里不存在。
func DetachWithNotify(string, []string) (int, *os.File, error) { return 0, nil, ErrUnsupported }
