package gofy

import (
	"testing"
	"time"

	"github.com/bingcool/gofy/core"
	"go.uber.org/zap"
)

// TestStartupBannerShowsStartTimeInsteadOfVersion pins the banner shape: the
// version row is gone (it is in the structured status document and the startup
// log line) and the start time is there in its place, because that is the one
// fact the running process cannot be asked for later.
// TestStartupBannerShowsStartTimeInsteadOfVersion 固定横幅形状：版本行已去掉（它在
// 结构化状态文档与启动日志行里），取而代之的是启动时间——那是进程运行之后再也问不到的事实。
func TestStartupBannerShowsStartTimeInsteadOfVersion(t *testing.T) {
	app, err := New(
		WithName("banner-test"),
		WithEnv("staging"),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rows := app.startupRows("0.0.0.0:8080")
	fields := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row) != 2 {
			t.Fatalf("row %v, want two cells", row)
		}
		fields[row[0]] = row[1]
	}

	if _, ok := fields["版本"]; ok {
		t.Fatal("banner still prints the version row")
	}
	start, ok := fields["启动时间"]
	if !ok {
		t.Fatal("banner is missing 启动时间")
	}
	if _, err := time.Parse("2006-01-02 15:04:05", start); err != nil {
		t.Fatalf("启动时间 = %q, want 2006-01-02 15:04:05", start)
	}
	if got := fields["运行环境"]; got != "staging" {
		t.Fatalf("运行环境 = %q, want staging", got)
	}

	// The start time is the runtime's, not the moment the banner is printed.
	// 启动时间取运行时的构建时刻，而不是打印横幅的那一刻。
	started := time.Date(2026, 11, 22, 9, 8, 7, 0, time.Local)
	app.rt = &core.Runtime{StartedAt: started}
	if got, want := app.startupRows("0.0.0.0:8080")[2][1], "2026-11-22 09:08:07"; got != want {
		t.Fatalf("启动时间 = %q, want %q", got, want)
	}
}
