package gofy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"go.uber.org/zap"
)

// A legacy pid file cannot prove the pid still belongs to the process that
// wrote it. Without --force, stop must refuse instead of signalling on
// liveness alone — here the target is the test process itself, so a wrong
// implementation would kill the test.
// 旧格式 pid 文件无法证明该 pid 仍是写下它的进程。没有 --force 时 stop 必须拒绝，
// 而不是只凭存活发信号——这里的目标就是测试进程本身，实现错了会把测试杀掉。
func TestStopLegacyPIDFileRequiresForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	path := filepath.Join(t.TempDir(), "legacy.pid")
	legacy := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy pid file: %v", err)
	}
	app, err := New(
		WithName("stop-legacy-test"),
		WithPIDFile(path),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.stopPIDFile(context.Background(), path, "", false, time.Second); !errors.Is(err, ErrIdentityUnverified) {
		t.Fatalf("stop without --force error = %v, want ErrIdentityUnverified", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("stop removed or damaged the pid file: %v", statErr)
	}
}

// TestStopRemovesPIDFileAfterGracefulExit pins A3 on the ordinary path: a
// process that exits on SIGTERM without cleaning up its own pid file must not
// leave the file for the next start to reject as a live stale pid.
// TestStopRemovesPIDFileAfterGracefulExit 固定 A3 的常规路径：收到 SIGTERM 后退出、
// 但自己没清理 pid 文件的进程，不能把文件留给下一次启动当成"仍然活着"的陈旧 pid。
func TestStopRemovesPIDFileAfterGracefulExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("spawn helper: %v", err)
	}
	// stop waits on liveness, which stays true while the child is an unreaped
	// zombie, so the test has to reap it concurrently.
	// stop 依赖存活探测，而子进程在被回收前以僵尸形态继续"存活"，测试必须并发回收它。
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := pidfile.WriteRecord(pidPath, pidfile.NewRecord(cmd.Process.Pid), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	app, err := New(
		WithName("stop-cleanup-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := app.stopPIDFile(context.Background(), pidPath, "", false, 5*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
		t.Fatalf("pid file survived a confirmed exit: %v", statErr)
	}
}

// TestStopRemovesPIDFileAfterSIGKILL pins A3 on the escalated path: a process
// that ignores SIGTERM is killed, never runs its cleanup, and the file must be
// gone anyway.
// TestStopRemovesPIDFileAfterSIGKILL 固定 A3 的升级路径：忽略 SIGTERM 的进程被 SIGKILL
// 杀死，根本不会执行清理，文件仍必须消失。
func TestStopRemovesPIDFileAfterSIGKILL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestStopHelperProcess")
	cmd.Env = append(os.Environ(), "GOFY_STOP_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("spawn helper: %v", err)
	}
	// Read the readiness line before the trap is assumed to be installed; the
	// helper prints it after ignoring SIGTERM.
	// 在假定 trap 已装好之前先读就绪行；helper 在忽略 SIGTERM 之后才打印它。
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "ready" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("helper readiness: line=%q err=%v", ready, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	pidPath := filepath.Join(t.TempDir(), "demo.pid")
	if err := pidfile.WriteRecord(pidPath, pidfile.NewRecord(cmd.Process.Pid), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	app, err := New(
		WithName("stop-cleanup-test"),
		WithPIDFile(pidPath),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := app.stopPIDFile(context.Background(), pidPath, "", true, 300*time.Millisecond); err != nil {
		t.Fatalf("stop with force: %v", err)
	}
	if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
		t.Fatalf("pid file survived SIGKILL: %v", statErr)
	}
}

// TestStopHelperProcess is the child used by the SIGKILL case. It ignores
// SIGTERM so stop has to escalate, announces readiness, and then blocks until
// the test kills it.
// TestStopHelperProcess 是 SIGKILL 用例的子进程：忽略 SIGTERM 迫使 stop 升级，宣告
// 就绪后一直阻塞，直到测试把它杀掉。
func TestStopHelperProcess(t *testing.T) {
	if os.Getenv("GOFY_STOP_HELPER") != "1" {
		t.Skip("helper process")
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("ready")
	select {}
}

// TestStopRejectsWorkerGroupThatEscapesPIDPath covers the programmatic entry
// point: App.Stop with a crafted Worker value must be refused before the pid
// file path is built. The CLI validates the same value earlier, but a caller
// using StopOptions directly must not be able to reach filepath.Clean with
// "../../etc/cron".
// TestStopRejectsWorkerGroupThatEscapesPIDPath 覆盖程序化入口：带构造值的
// App.Stop 必须在拼出 pid 文件路径之前被拒绝。CLI 会更早校验同一个值，但直接使用
// StopOptions 的调用方也不能把 "../../etc/cron" 交到 filepath.Clean 手上。
func TestStopRejectsWorkerGroupThatEscapesPIDPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pid based stop is not supported on windows")
	}
	app, err := New(
		WithName("stop-group-test"),
		WithPIDFile(filepath.Join(t.TempDir(), "app.pid")),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.stop(context.Background(), commandline.StopOptions{Worker: "../../etc/cron"})
	if err == nil || !strings.Contains(err.Error(), "invalid group") {
		t.Fatalf("error = %v, want invalid group", err)
	}
}
