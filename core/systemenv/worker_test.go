package systemenv_test

import (
	"os"
	"testing"

	"github.com/bingcool/gofy/core/systemenv"
)

func TestWorkerMode(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()

	cases := []struct {
		name   string
		args   []string
		want   bool
		group  string
		reason string
	}{
		{"group flag", []string{"worker", "--group=order"}, true, "order", ""},
		{"group value", []string{"worker", "--group", "order"}, true, "order", ""},
		{"list", []string{"worker", "--list"}, true, "", ""},
		{"start is http", []string{"start"}, false, "", ""},
		{"script is not worker", []string{"script", "fix-order"}, false, "", ""},
		{"no args is http", nil, false, "", ""},
		{"sanitised", []string{"worker", "--group=../../etc/passwd"}, true, "etcpasswd", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = append([]string{"demo-service"}, tc.args...)
			if got := systemenv.IsWorkerService(); got != tc.want {
				t.Fatalf("IsWorkerService() = %v, want %v", got, tc.want)
			}
			if got := systemenv.WorkerGroup(); got != tc.group {
				t.Fatalf("WorkerGroup() = %q, want %q", got, tc.group)
			}
		})
	}
}

func TestWorkerModeIsNotHTTPService(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()

	os.Args = []string{"demo-service", "worker", "--group=order"}
	if systemenv.IsHTTPService() {
		t.Fatalf("IsHTTPService() must be false for the worker subcommand")
	}
}
