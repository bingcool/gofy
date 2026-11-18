package nacos

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newMetadataRegister builds a registrar without starting it — the metadata
// contract is about the value handed out, not about a live registration.
func newMetadataRegister(t *testing.T, meta map[string]string) (*Register, map[string]string) {
	t.Helper()
	fake := &fakeNaming{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	cfg := Config{Addr: server.URL, Port: port, Timeout: time.Second}
	cfg.Register = RegisterConfig{
		Enabled:     true,
		ServiceName: "meta-service",
		IP:          "10.0.0.9",
		Port:        18080,
		Metadata:    meta,
	}
	reg, err := NewRegister(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRegister: %v", err)
	}
	return reg, cfg.Register.Metadata
}

// TestRegisterMetadataIsCopied pins both ends of the metadata boundary: the
// config given to NewRegister and the instance handed out by Instance must not
// share the map the registrar itself keeps for registration and heartbeat.
func TestRegisterMetadataIsCopied(t *testing.T) {
	src := map[string]string{"version": "1.0.0", "zone": "a"}
	reg, cfgMeta := newMetadataRegister(t, src)

	// Mutating the caller's config after construction does not reach the
	// registrar's instance.
	cfgMeta["version"] = "9.9.9"
	delete(cfgMeta, "zone")
	src["extra"] = "leak"
	if got := reg.Instance().Metadata; got["version"] != "1.0.0" || got["zone"] != "a" {
		t.Fatalf("config mutation leaked into instance: %v", got)
	}
	if _, ok := reg.Instance().Metadata["extra"]; ok {
		t.Fatalf("config map alias leaked a new key: %v", reg.Instance().Metadata)
	}

	// Mutating a returned instance does not reach the registrar, nor a later
	// caller's copy.
	first := reg.Instance()
	first.Metadata["version"] = "2.0.0"
	first.Metadata["injected"] = "yes"
	if got := reg.Instance().Metadata; got["version"] != "1.0.0" {
		t.Fatalf("returned metadata mutation reached the registrar: %v", got)
	}
	if _, ok := reg.Instance().Metadata["injected"]; ok {
		t.Fatalf("returned metadata mutation leaked to next caller: %v", reg.Instance().Metadata)
	}

	second := reg.Instance()
	if &first.Metadata == &second.Metadata {
		t.Fatal("Instance returned the same map twice")
	}
}
