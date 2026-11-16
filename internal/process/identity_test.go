package process_test

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/bingcool/gofy/internal/process"
)

// TestTokenIsStableAndPositive pins the two properties callers rely on: asking
// twice about the same pid gives the same answer (otherwise every stop would
// look like a recycled pid), and a nonsense pid gives no token at all.
// TestTokenIsStableAndPositive 固定调用方依赖的两个性质：对同一 pid 询问两次得到相同
// 答案（否则每次 stop 都像遇到了复用的 pid），而无意义的 pid 不给出 token。
func TestTokenIsStableAndPositive(t *testing.T) {
	first := process.Token(os.Getpid())
	if first == "" {
		t.Skipf("platform %s provides no process token", runtime.GOOS)
	}
	if second := process.Token(os.Getpid()); second != first {
		t.Fatalf("Token changed between calls: %q then %q", first, second)
	}
	if got := process.Token(0); got != "" {
		t.Fatalf("Token(0) = %q, want empty", got)
	}
}

// TestTokenDistinguishesProcesses covers the other half: two live processes must
// not share a token, or the identity check would pass for the wrong process.
// TestTokenDistinguishesProcesses 覆盖另一半：两个活着的进程不能共用 token，否则身份
// 校验会为错误的进程放行。
func TestTokenDistinguishesProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sleep to spawn")
	}
	self := process.Token(os.Getpid())
	if self == "" {
		t.Skip("platform provides no process token")
	}

	cmd := exec.Command("/bin/sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Skipf("spawn helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	other := process.Token(cmd.Process.Pid)
	if other == "" {
		t.Fatalf("no token for child process %d", cmd.Process.Pid)
	}
	if other == self {
		t.Fatalf("child process %d shares this process's token %q", cmd.Process.Pid, other)
	}
}
