package middleware_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
)

// marker returns an identifiable middleware: on a hit it writes name into a
// response header, which lets the test confirm Lookup returned the very instance
// that was registered.
// marker 返回一个可识别的中间件：命中时把 name 写进响应头，
// 测试据此确认 Lookup 拿到的就是注册进去的那个实例。
func marker(name string) support.Middleware {
	return func(c *gin.Context) {
		c.Header("X-Middleware", name)
		c.Next()
	}
}

func serveWith(t *testing.T, mw support.Middleware) string {
	t.Helper()
	engine := newGin()
	engine.GET("/", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	return rr.Header().Get("X-Middleware")
}

func TestRegistryLookupReturnsRegisteredInstance(t *testing.T) {
	reg := middleware.NewRegistry()
	if err := reg.Register(middleware.NameCORS, marker("cors")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	mw, ok := reg.Lookup(middleware.NameCORS)
	if !ok || mw == nil {
		t.Fatalf("Lookup(%q) = (%v, %v), want the registered middleware", middleware.NameCORS, mw, ok)
	}
	if got := serveWith(t, mw); got != "cors" {
		t.Fatalf("resolved middleware ran as %q, want %q", got, "cors")
	}
	if _, ok := reg.Lookup("absent"); ok {
		t.Fatalf("Lookup of an unregistered alias reported success")
	}
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	tests := []struct {
		name    string
		alias   string
		mw      support.Middleware
		wantErr string
	}{
		{name: "empty alias", alias: "", mw: marker("x"), wantErr: "must not be empty"},
		{name: "blank alias", alias: "   ", mw: marker("x"), wantErr: "must not be empty"},
		{name: "nil middleware", alias: "audit", mw: nil, wantErr: `middleware "audit" must not be nil`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := middleware.NewRegistry()
			err := reg.Register(tt.alias, tt.mw)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Register(%q) error = %v, want it to contain %q", tt.alias, err, tt.wantErr)
			}
			if len(reg.Names()) != 0 {
				t.Fatalf("failed registration left entries behind: %v", reg.Names())
			}
		})
	}
}

func TestRegistryRejectsDuplicateAlias(t *testing.T) {
	reg := middleware.NewRegistry()
	if err := reg.Register("audit", marker("first")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := reg.Register("audit", marker("second"))
	if err == nil || !strings.Contains(err.Error(), `alias "audit" is already registered`) {
		t.Fatalf("duplicate Register error = %v, want an already-registered error", err)
	}
	mw, ok := reg.Lookup("audit")
	if !ok {
		t.Fatalf("duplicate registration dropped the original entry")
	}
	if got := serveWith(t, mw); got != "first" {
		t.Fatalf("alias resolved to %q, want the first registered instance %q", got, "first")
	}
}

func TestRegistryMustRegisterPanicsOnInvalidAlias(t *testing.T) {
	reg := middleware.NewRegistry()
	reg.MustRegister("audit", marker("audit"))

	defer func() {
		if recover() == nil {
			t.Fatalf("MustRegister with a duplicate alias did not panic")
		}
	}()
	reg.MustRegister("audit", marker("again"))
}

func TestRegistryTrimsAliasOnRegisterAndLookup(t *testing.T) {
	reg := middleware.NewRegistry()
	if err := reg.Register("  audit  ", marker("audit")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := reg.Lookup("audit"); !ok {
		t.Fatalf("Lookup with the trimmed alias failed")
	}
	if _, ok := reg.Lookup(" audit "); !ok {
		t.Fatalf("Lookup with a padded alias failed")
	}
	if names := reg.Names(); !reflect.DeepEqual(names, []string{"audit"}) {
		t.Fatalf("Names = %v, want [audit]", names)
	}
}

func TestRegistryNamesAreSortedAndDetached(t *testing.T) {
	reg := middleware.NewRegistry()
	for _, alias := range []string{"security-headers", "audit", "cors"} {
		if err := reg.Register(alias, marker(alias)); err != nil {
			t.Fatalf("Register(%q): %v", alias, err)
		}
	}

	names := reg.Names()
	want := []string{"audit", "cors", "security-headers"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	names[0] = "mutated"
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating the returned slice changed the registry: %v", got)
	}
}

func TestRegistryNilIsSafeToRead(t *testing.T) {
	var reg *middleware.Registry
	if mw, ok := reg.Lookup("audit"); ok || mw != nil {
		t.Fatalf("nil registry Lookup = (%v, %v), want (nil, false)", mw, ok)
	}
	if names := reg.Names(); names != nil {
		t.Fatalf("nil registry Names = %v, want nil", names)
	}
}

func TestRegistryConcurrentRegisterAndLookup(t *testing.T) {
	reg := middleware.NewRegistry()
	const workers = 16

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			alias := fmt.Sprintf("mw-%02d", i)
			if err := reg.Register(alias, marker(alias)); err != nil {
				t.Errorf("Register(%q): %v", alias, err)
			}
			for j := 0; j < workers; j++ {
				reg.Lookup(fmt.Sprintf("mw-%02d", j))
				reg.Names()
			}
		}(i)
	}
	wg.Wait()

	if got := len(reg.Names()); got != workers {
		t.Fatalf("registered %d aliases, want %d", got, workers)
	}
}

func TestBuiltinAliasesAreUnique(t *testing.T) {
	aliases := []string{
		middleware.NameAccessLog,
		middleware.NameBodyLimit,
		middleware.NameCORS,
		middleware.NameHeaderPropagation,
		middleware.NameJWTAuth,
		middleware.NameRateLimit,
		middleware.NameRecovery,
		middleware.NameRequestID,
		middleware.NameSecurityHeaders,
	}
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if strings.TrimSpace(alias) == "" {
			t.Fatalf("built-in alias must not be empty")
		}
		if _, dup := seen[alias]; dup {
			t.Fatalf("built-in alias %q is used twice", alias)
		}
		seen[alias] = struct{}{}
	}
}
