package worker

import "testing"

func TestIdentityOwnerCarriesHost(t *testing.T) {
	id := Identity{Group: "group1", Name: "scan", Index: 1, Total: 2, Host: "demo-0"}
	if got, want := id.String(), "group1/scan#1"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := id.Owner(), "group1/scan#1@demo-0"; got != want {
		t.Fatalf("Owner() = %q, want %q", got, want)
	}
	// Without a hostname Owner degrades to String instead of rendering a
	// dangling "@", so a host that cannot resolve its name still gets a usable
	// owner value.
	// 拿不到主机名时 Owner 退化为 String，而不是渲染出孤零零的 "@"：解析不到主机名
	// 的宿主因此仍能拿到可用的 owner 值。
	id.Host = ""
	if got, want := id.Owner(), "group1/scan#1"; got != want {
		t.Fatalf("Owner() without host = %q, want %q", got, want)
	}
}

func TestIdentityOwnerDistinguishesPods(t *testing.T) {
	a := Identity{Group: "g", Name: "n", Index: 0, Host: "pod-a"}
	b := Identity{Group: "g", Name: "n", Index: 0, Host: "pod-b"}
	// String stays host-free on purpose: it is the log and metric label, and
	// keeping it stable means the same worker instance series across pods.
	// String 故意不带主机名：它是日志与指标标签，保持稳定才能让同一个 worker 实例
	// 跨 Pod 落到同一条序列上。
	if a.String() != b.String() {
		t.Fatalf("String() changed with host: %q vs %q", a.String(), b.String())
	}
	if a.Owner() == b.Owner() {
		t.Fatalf("same Owner() for two pods: %q", a.Owner())
	}
}
