package gofy

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/worker"
	"go.uber.org/zap"
)

// failingConsumer models the case that matters: a worker which must connect
// before it can accept work, never gets there, and exits. A plain Func reports
// readiness as soon as Run starts, so it would answer "ready" before failing
// and the one-line pipe could not carry the reason any more.
// failingConsumer 模拟真正需要通知的场景：必须先建连才能接活的 worker 没连上就退出了。
// 普通 Func 在 Run 一开始就算就绪，因此会先答 "ready"，这条只写一行的管道就再也带不出
// 失败原因。
type failingConsumer struct{}

func (failingConsumer) Name() string  { return "boom" }
func (failingConsumer) ReportsReady() {}
func (failingConsumer) Run(context.Context) error {
	return errors.New("broker unreachable")
}

// TestWorkerReportsStartupFailureToTheDaemonParent covers the worker half of
// the startup handshake: a group that dies before it ever reports ready must
// write "error: ..." to the pipe, because a detached child's stdout and stderr
// are /dev/null and the parent would otherwise only see an exit code. The HTTP
// path has this covered in start_internal_test.go; runWorker did not.
// TestWorkerReportsStartupFailureToTheDaemonParent 覆盖启动握手的 worker 一侧：在报告
// 就绪之前就退出的分组必须把 "error: ..." 写进管道，因为脱离的子进程 stdout 与 stderr
// 都是 /dev/null，父进程否则只能拿到退出码。HTTP 路径在 start_internal_test.go 里已有
// 覆盖，runWorker 此前没有。
func TestWorkerReportsStartupFailureToTheDaemonParent(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()

	group, err := worker.NewGroup("order", failingConsumer{})
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}

	app, err := New(
		WithName("worker-daemon-failure-test"),
		WithWorkerGroups(group),
		// ExitOnError keeps the test fast: a restart policy would retry with
		// backoff before the failure ever surfaced.
		// ExitOnError 让测试保持快速：重启策略会先按退避重试，失败迟迟不浮出。
		WithWorkerPolicy(worker.Policy{Mode: worker.ExitOnError}),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.notifier = &startupNotifier{file: writer}

	if err := app.ExecuteArgs(context.Background(), []string{"worker", "--group=order"}); err == nil {
		t.Fatal("worker command succeeded although the worker failed")
	}

	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("read startup report: %v", err)
	}
	if !strings.HasPrefix(line, "error:") {
		t.Fatalf("startup report = %q, want an error line", line)
	}
	if !strings.Contains(line, "broker unreachable") {
		t.Fatalf("startup report = %q, want the failure reason", line)
	}
}
