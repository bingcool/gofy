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

func TestWorkerNames(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"group only", []string{"worker", "--group=order"}, nil},
		{"equals form", []string{"worker", "--group=order", "--name=order-consumer"}, []string{"order-consumer"}},
		{"space form", []string{"worker", "--name", "order-consumer", "--group=order"}, []string{"order-consumer"}},
		{"repeated", []string{"worker", "--name=order-scan", "--name=order-consumer"}, []string{"order-scan", "order-consumer"}},
		{"comma separated", []string{"worker", "--name=order-scan,order-consumer"}, []string{"order-scan", "order-consumer"}},
		{"duplicates dropped", []string{"worker", "--name=a", "--name=a"}, []string{"a"}},
		{"path characters removed", []string{"worker", "--name=../../etc/passwd"}, []string{"etcpasswd"}},
		{"other subcommand", []string{"script", "--name=order-consumer"}, nil},
		{"help", []string{"worker", "--help"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = append([]string{"demo-service"}, tc.args...)
			got := systemenv.WorkerNames()
			if len(got) != len(tc.want) {
				t.Fatalf("WorkerNames() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("WorkerNames() = %v, want %v", got, tc.want)
				}
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
