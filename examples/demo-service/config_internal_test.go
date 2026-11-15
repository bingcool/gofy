package main

import "testing"

func TestConfigInvocation(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantSync  bool
		wantWatch bool
	}{
		{name: "no args starts HTTP", args: nil, wantSync: true, wantWatch: true},
		{name: "start", args: []string{"start"}, wantSync: true, wantWatch: true},
		{name: "start after flags", args: []string{"--env=dev", "start"}, wantSync: true, wantWatch: true},
		{name: "worker with group", args: []string{"worker", "--group", "group1"}, wantSync: true, wantWatch: false},
		{name: "worker with group equals form", args: []string{"worker", "--group=*"}, wantSync: true, wantWatch: false},
		{name: "worker with name filter", args: []string{"worker", "--group=group1", "--name=x"}, wantSync: true, wantWatch: false},
		{name: "worker without group still attempts startup", args: []string{"worker"}, wantSync: true, wantWatch: false},
		{name: "worker list", args: []string{"worker", "--list"}, wantSync: false, wantWatch: false},
		{name: "worker list equals form", args: []string{"worker", "--list=true"}, wantSync: false, wantWatch: false},
		{name: "worker help", args: []string{"worker", "--help"}, wantSync: false, wantWatch: false},
		{name: "worker short help", args: []string{"worker", "-h"}, wantSync: false, wantWatch: false},
		{name: "worker daemon starts and syncs", args: []string{"worker", "--group=all", "--daemon=1"}, wantSync: true, wantWatch: false},
		{name: "worker stop", args: []string{"worker", "stop"}, wantSync: false, wantWatch: false},
		{name: "worker stop with group", args: []string{"worker", "stop", "--group=group1"}, wantSync: false, wantWatch: false},
		{name: "worker restart keeps Nacos out", args: []string{"worker", "restart", "--group=group1"}, wantSync: false, wantWatch: false},
		{name: "group named stop still starts", args: []string{"worker", "--group", "stop"}, wantSync: true, wantWatch: false},
		{name: "script", args: []string{"script", "gen-uuid"}, wantSync: false, wantWatch: false},
		{name: "stop", args: []string{"stop", "--worker=group1"}, wantSync: false, wantWatch: false},
		{name: "version", args: []string{"version"}, wantSync: false, wantWatch: false},
		{name: "restart", args: []string{"restart"}, wantSync: false, wantWatch: false},
		{name: "root help", args: []string{"--help"}, wantSync: false, wantWatch: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sync, watch := configInvocation(tc.args)
			if sync != tc.wantSync || watch != tc.wantWatch {
				t.Fatalf("configInvocation(%v) = sync:%v watch:%v, want sync:%v watch:%v",
					tc.args, sync, watch, tc.wantSync, tc.wantWatch)
			}
		})
	}
}
