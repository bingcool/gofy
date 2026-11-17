package main

import "testing"

// TestPlanCommand pins what each command line is allowed to touch before the App
// exists. The interesting rows are the negative ones: stop must not read
// nacos.yaml, worker --list must not read it either, version and help must not
// read config.yaml at all, and worker restart must pull the remote config
// because it starts the next process.
// TestPlanCommand 固定每条命令行在 App 存在之前允许碰什么。要紧的是那些"不"的行：stop
// 不读 nacos.yaml，worker --list 也不读，version 与 help 连 config.yaml 都不读，而
// worker restart 会拉远端配置，因为它要启动下一个进程。
func TestPlanCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want commandPlan
	}{
		{
			name: "no args starts HTTP",
			want: commandPlan{Kind: "http-start", LoadNacos: true, Sync: true, Watch: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "start",
			args: []string{"start"},
			want: commandPlan{Kind: "http-start", LoadNacos: true, Sync: true, Watch: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "start after flags",
			args: []string{"--env=dev", "start"},
			want: commandPlan{Kind: "http-start", LoadNacos: true, Sync: true, Watch: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "worker with group",
			args: []string{"worker", "--group", "group1"},
			want: commandPlan{Kind: "worker-run", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "worker with wildcard",
			args: []string{"worker", "--group=*"},
			want: commandPlan{Kind: "worker-run", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "worker without group still attempts startup",
			args: []string{"worker"},
			want: commandPlan{Kind: "worker-run", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "worker list builds groups but reads no nacos",
			args: []string{"worker", "--list"},
			want: commandPlan{Kind: "worker-list", LoadConfig: true, RegisterGroups: true},
		},
		{
			name: "worker list equals form",
			args: []string{"worker", "--list=true"},
			want: commandPlan{Kind: "worker-list", LoadConfig: true, RegisterGroups: true},
		},
		{
			name: "worker help reads nothing",
			args: []string{"worker", "--help"},
			want: commandPlan{Kind: "help"},
		},
		{
			name: "worker short help reads nothing",
			args: []string{"worker", "-h"},
			want: commandPlan{Kind: "help"},
		},
		{
			name: "worker stop only needs the pid path",
			args: []string{"worker", "stop"},
			want: commandPlan{Kind: "worker-stop", LoadConfig: true},
		},
		{
			name: "worker stop with group",
			args: []string{"worker", "stop", "--group=group1"},
			want: commandPlan{Kind: "worker-stop", LoadConfig: true},
		},
		{
			name: "worker status only needs the pid path",
			args: []string{"worker", "status", "--group=group1"},
			want: commandPlan{Kind: "worker-status", LoadConfig: true},
		},
		{
			name: "status reads the config it reports on",
			args: []string{"status"},
			want: commandPlan{Kind: "status", LoadConfig: true},
		},
		{
			name: "worker restart pulls the config it will start on",
			args: []string{"worker", "restart", "--group=group1"},
			want: commandPlan{Kind: "worker-restart", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "group named stop still starts",
			args: []string{"worker", "--group", "stop"},
			want: commandPlan{Kind: "worker-run", LoadNacos: true, Sync: true, LoadConfig: true, RegisterGroups: true, BuildServices: true},
		},
		{
			name: "script",
			args: []string{"script", "gen-uuid"},
			want: commandPlan{Kind: "script", LoadConfig: true, BuildServices: true},
		},
		{
			name: "stop",
			args: []string{"stop", "--worker=group1"},
			want: commandPlan{Kind: "stop", LoadConfig: true},
		},
		{
			name: "version reads nothing",
			args: []string{"version"},
			want: commandPlan{Kind: "print"},
		},
		{
			name: "http restart pulls before it stops",
			args: []string{"restart"},
			want: commandPlan{Kind: "http-restart", LoadNacos: true, Sync: true, LoadConfig: true, BuildServices: true},
		},
		{
			name: "root help reads nothing",
			args: []string{"--help"},
			want: commandPlan{Kind: "help"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planCommand(tc.args); got != tc.want {
				t.Fatalf("planCommand(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}
