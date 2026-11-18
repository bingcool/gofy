package nacos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeNaming struct {
	mu sync.Mutex

	registers   []url.Values
	deregisters []url.Values
	beats       []url.Values
	listQueries []url.Values
	hosts       []Instance

	beatReply string
	listFail  bool

	// registerGate, when non-nil, holds POST /instance until it is closed, and
	// registerStarted, when non-nil, is closed once that request arrives. Together
	// they let a test drive "Stop arrives while Start is still registering".
	// registerGate 非 nil 时，POST /instance 会一直等到它被关闭；registerStarted
	// 非 nil 时，该请求到达后立即关闭。两者配合可以构造"Start 还在注册、Stop 已经
	// 进来"的时序。
	registerGate    chan struct{}
	registerStarted chan struct{}
	registerOnce    sync.Once
}

func (f *fakeNaming) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	switch r.URL.Path {
	case instancePath:
		if r.Method == http.MethodPost {
			if f.registerStarted != nil {
				f.registerOnce.Do(func() { close(f.registerStarted) })
			}
			if f.registerGate != nil {
				<-f.registerGate
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			f.registers = append(f.registers, query)
		case http.MethodDelete:
			f.deregisters = append(f.deregisters, query)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprint(w, "ok")

	case instanceBeatPath:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.beats = append(f.beats, query)
		if f.beatReply != "" {
			fmt.Fprint(w, f.beatReply)
			return
		}
		fmt.Fprint(w, `{"clientBeatInterval":3000,"lightBeatEnabled":true,"code":10200}`)

	case instanceListPath:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listQueries = append(f.listQueries, query)
		if f.listFail {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		payload, err := json.Marshal(map[string]any{"hosts": f.hosts})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(payload)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeNaming) snapshot() (registers, deregisters, beats, listQueries []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.registers...),
		append([]url.Values(nil), f.deregisters...),
		append([]url.Values(nil), f.beats...),
		append([]url.Values(nil), f.listQueries...)
}

func newFakeNaming(t *testing.T) (*fakeNaming, *Client) {
	t.Helper()
	fake := &fakeNaming{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := NewNamingClient(Config{Addr: server.URL, Namespace: "dev"})
	if err != nil {
		t.Fatalf("NewNamingClient: %v", err)
	}
	return fake, client
}

func testInstance() Instance {
	return Instance{
		IP:          "10.0.0.5",
		Port:        8080,
		Weight:      2,
		Ephemeral:   true,
		ClusterName: "DEFAULT",
		ServiceName: "demo-service",
		Group:       "gofy-group",
		Namespace:   "dev",
		Metadata:    map[string]string{"version": "1.0.0"},
	}
}

func TestRegisterInstanceSendsInstanceForm(t *testing.T) {
	fake, client := newFakeNaming(t)
	if err := client.RegisterInstance(context.Background(), testInstance()); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	registers, _, _, _ := fake.snapshot()
	if len(registers) != 1 {
		t.Fatalf("registers = %d, want 1", len(registers))
	}
	form := registers[0]
	want := map[string]string{
		"serviceName": "demo-service",
		"groupName":   "gofy-group",
		"namespaceId": "dev",
		"ip":          "10.0.0.5",
		"port":        "8080",
		"weight":      "2",
		"ephemeral":   "true",
		"enabled":     "true",
		"healthy":     "true",
		"clusterName": "DEFAULT",
	}
	for key, value := range want {
		if got := form.Get(key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(form.Get("metadata")), &metadata); err != nil {
		t.Fatalf("metadata = %q: %v", form.Get("metadata"), err)
	}
	if metadata["version"] != "1.0.0" {
		t.Fatalf("metadata = %v", metadata)
	}
}

func TestDeregisterInstanceSendsDelete(t *testing.T) {
	fake, client := newFakeNaming(t)
	if err := client.DeregisterInstance(context.Background(), testInstance()); err != nil {
		t.Fatalf("DeregisterInstance: %v", err)
	}
	_, deregisters, _, _ := fake.snapshot()
	if len(deregisters) != 1 {
		t.Fatalf("deregisters = %d, want 1", len(deregisters))
	}
	if got := deregisters[0].Get("serviceName"); got != "demo-service" {
		t.Fatalf("serviceName = %q", got)
	}
	if got := deregisters[0].Get("ip"); got != "10.0.0.5" {
		t.Fatalf("ip = %q", got)
	}
}

func TestBeatInstanceLightAndHeavy(t *testing.T) {
	fake, client := newFakeNaming(t)
	inst := testInstance()

	result, err := client.BeatInstance(context.Background(), inst, true)
	if err != nil {
		t.Fatalf("light BeatInstance: %v", err)
	}
	if result.ClientBeatInterval != 3*time.Second || !result.LightBeatEnabled {
		t.Fatalf("result = %+v", result)
	}

	if _, err := client.BeatInstance(context.Background(), inst, false); err != nil {
		t.Fatalf("heavy BeatInstance: %v", err)
	}
	_, _, beats, _ := fake.snapshot()
	if len(beats) != 2 {
		t.Fatalf("beats = %d, want 2", len(beats))
	}
	if beats[0].Get("beat") != "" {
		t.Fatalf("light beat carried a body: %q", beats[0].Get("beat"))
	}
	var beat map[string]any
	if err := json.Unmarshal([]byte(beats[1].Get("beat")), &beat); err != nil {
		t.Fatalf("heavy beat body = %q: %v", beats[1].Get("beat"), err)
	}
	if beat["serviceName"] != "demo-service" || beat["ip"] != "10.0.0.5" {
		t.Fatalf("beat = %v", beat)
	}
}

func TestBeatPlainOKReply(t *testing.T) {
	fake, client := newFakeNaming(t)
	fake.beatReply = "ok"
	result, err := client.BeatInstance(context.Background(), testInstance(), true)
	if err != nil {
		t.Fatalf("BeatInstance: %v", err)
	}
	if result != (BeatResult{}) {
		t.Fatalf("result = %+v, want zero", result)
	}
}

func TestListInstancesSendsQueryAndParsesHosts(t *testing.T) {
	fake, client := newFakeNaming(t)
	fake.hosts = []Instance{
		{IP: "10.0.0.5", Port: 8080, Healthy: true, Enabled: true, ServiceName: "demo-service"},
		{IP: "10.0.0.6", Port: 8081, Healthy: false, Enabled: true, ServiceName: "demo-service"},
	}

	hosts, err := client.ListInstances(context.Background(), InstanceQuery{
		ServiceName: "demo-service",
		Group:       "",
		Namespace:   "dev",
		Clusters:    []string{"DEFAULT", "OTHER"},
		HealthyOnly: true,
	})
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(hosts))
	}
	_, _, _, lists := fake.snapshot()
	query := lists[0]
	if got := query.Get("serviceName"); got != "demo-service" {
		t.Fatalf("serviceName = %q", got)
	}
	if got := query.Get("groupName"); got != DefaultGroup {
		t.Fatalf("groupName = %q, want %q", got, DefaultGroup)
	}
	if got := query.Get("namespaceId"); got != "dev" {
		t.Fatalf("namespaceId = %q", got)
	}
	if got := query.Get("clusters"); got != "DEFAULT,OTHER" {
		t.Fatalf("clusters = %q", got)
	}
	if got := query.Get("healthyOnly"); got != "true" {
		t.Fatalf("healthyOnly = %q", got)
	}

	available := AvailableInstances(hosts)
	if len(available) != 1 || available[0].IP != "10.0.0.5" {
		t.Fatalf("available = %+v", available)
	}
}

func TestNewNamingClientRequiresValidPort(t *testing.T) {
	if _, err := NewNamingClient(Config{Port: 70000}); err == nil {
		t.Fatal("NewNamingClient accepted an invalid port")
	}
}

func TestRegisterRegistersAndDeregisters(t *testing.T) {
	fake := &fakeNaming{}
	server := httptest.NewServer(fake)
	defer server.Close()

	cfg := Config{
		Addr:      server.URL,
		Namespace: "dev",
		Register: RegisterConfig{
			Enabled:     true,
			ServiceName: "demo-service",
			IP:          "10.0.0.5",
			Port:        8080,
			Group:       "gofy-group",
			Weight:      1,
			Metadata:    map[string]string{"version": "1.0.0"},
		},
	}
	registrar, err := NewRegister(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegister: %v", err)
	}
	inst := registrar.Instance()
	if !inst.Ephemeral || !inst.Enabled || !inst.Healthy {
		t.Fatalf("instance flags = %+v", inst)
	}
	if inst.ServiceName != "demo-service" || inst.IP != "10.0.0.5" || inst.Port != 8080 {
		t.Fatalf("instance = %+v", inst)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := registrar.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := registrar.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	registers, _, _, _ := fake.snapshot()
	if len(registers) != 1 {
		t.Fatalf("registers = %d, want 1 after idempotent Start", len(registers))
	}

	if err := registrar.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_, deregisters, _, _ := fake.snapshot()
	if len(deregisters) != 1 {
		t.Fatalf("deregisters = %d, want 1", len(deregisters))
	}
	if err := registrar.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestRegisterRejectsMissingServiceName(t *testing.T) {
	cfg := Config{Register: RegisterConfig{Enabled: true, Port: 8080}}
	if _, err := NewRegister(cfg, nil); err == nil {
		t.Fatal("NewRegister accepted a missing service name")
	}
}

func TestResolveRegisterIPPrecedence(t *testing.T) {
	t.Setenv("NACOS_SERVICE_REGISTER_HOST", "10.1.2.3")
	t.Setenv("POD_IP", "10.9.8.7")
	if got := ResolveRegisterIP("192.168.1.1", "127.0.0.1"); got != "10.1.2.3" {
		t.Fatalf("got %q, want NACOS_SERVICE_REGISTER_HOST", got)
	}

	t.Setenv("NACOS_SERVICE_REGISTER_HOST", "")
	if got := ResolveRegisterIP("192.168.1.1", "127.0.0.1"); got != "10.9.8.7" {
		t.Fatalf("got %q, want POD_IP", got)
	}

	t.Setenv("POD_IP", "")
	if got := ResolveRegisterIP("192.168.1.1", "127.0.0.1"); got != "192.168.1.1" {
		t.Fatalf("got %q, want configured IP", got)
	}
}

func TestRegisterConfigNormalize(t *testing.T) {
	t.Setenv("POD_IP", "10.9.8.7")
	t.Setenv("NACOS_SERVICE_GROUP_NAME", "")
	cfg := RegisterConfig{ServiceName: " demo-service ", IP: "${POD_IP}"}.normalize("dev")
	if cfg.ServiceName != "demo-service" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if cfg.IP != "10.9.8.7" {
		t.Fatalf("IP = %q, want POD_IP expansion", cfg.IP)
	}
	if cfg.Namespace != "dev" || cfg.Group != DefaultGroup {
		t.Fatalf("namespace/group = %q/%q", cfg.Namespace, cfg.Group)
	}
	if cfg.Weight != 1 || cfg.HeartbeatInterval != 5*time.Second {
		t.Fatalf("weight/interval = %v/%s", cfg.Weight, cfg.HeartbeatInterval)
	}
	if cfg.Metadata == nil {
		t.Fatal("Metadata = nil")
	}
}

func TestGroupOrDefaultTrimsEmpty(t *testing.T) {
	if groupOrDefault("  ") != DefaultGroup {
		t.Fatalf("groupOrDefault = %q", groupOrDefault("  "))
	}
	if groupOrDefault(" g ") != "g" {
		t.Fatalf("groupOrDefault = %q", groupOrDefault(" g "))
	}
}

func TestNormalizeNamespacePublic(t *testing.T) {
	if got := normalizeNamespace(" Public "); got != "" {
		t.Fatalf("normalizeNamespace = %q, want empty", got)
	}
}

func TestParseBeatRejectsErrorCode(t *testing.T) {
	_, err := parseBeat([]byte(`{"code":500}`))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}
