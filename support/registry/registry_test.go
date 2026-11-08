package registry_test

import (
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bingcool/gofy/support/registry"
)

type greeter interface {
	Greet() string
}

type greeterImpl struct {
	prefix string
}

func (g greeterImpl) Greet() string { return g.prefix + " world" }

func TestRegistryRegisterAndLookup(t *testing.T) {
	reg := registry.NewRegistry()
	want := greeterImpl{prefix: "hello"}
	if err := reg.Register("demo.greeter", want); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := reg.Lookup("demo.greeter")
	if !ok {
		t.Fatalf("Lookup reported missing after Register")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup = %#v, want %#v", got, want)
	}
	if _, ok := reg.Lookup("absent"); ok {
		t.Fatalf("Lookup of an unregistered name reported success")
	}
	if _, ok := reg.Lookup("  "); ok {
		t.Fatalf("Lookup of a blank name reported success")
	}
}

func TestGetReturnsTypedInstance(t *testing.T) {
	reg := registry.NewRegistry()
	reg.MustRegister("demo.greeter", greeterImpl{prefix: "hi"})

	got, err := registry.Get[greeter](reg, "demo.greeter")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Greet() != "hi world" {
		t.Fatalf("Get returned %q, want %q", got.Greet(), "hi world")
	}
}

func TestGetReportsMissingAndWrongType(t *testing.T) {
	reg := registry.NewRegistry()
	reg.MustRegister("demo.greeter", greeterImpl{})

	if _, err := registry.Get[greeter](reg, "absent"); err == nil {
		t.Fatalf("Get of an unregistered name returned no error")
	} else if !strings.Contains(err.Error(), "absent") || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("Get missing error = %q, want name and reason", err)
	}

	if _, err := registry.Get[string](reg, "demo.greeter"); err == nil {
		t.Fatalf("Get with a mismatched type returned no error")
	} else {
		if !strings.Contains(err.Error(), "string") || !strings.Contains(err.Error(), "greeterImpl") {
			t.Fatalf("Get type error = %q, want both types", err)
		}
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	var typedNil *greeterImpl
	var nilMap map[string]string

	tests := []struct {
		name     string
		alias    string
		instance any
		wantErr  string
	}{
		{name: "empty name", alias: "", instance: greeterImpl{}, wantErr: "must not be empty"},
		{name: "blank name", alias: "   ", instance: greeterImpl{}, wantErr: "must not be empty"},
		{name: "nil instance", alias: "demo.greeter", instance: nil, wantErr: "must not be nil"},
		{name: "typed nil pointer", alias: "demo.greeter", instance: typedNil, wantErr: "must not be nil"},
		{name: "nil map", alias: "demo.greeter", instance: nilMap, wantErr: "must not be nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewRegistry()
			err := reg.Register(tt.alias, tt.instance)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Register(%q) error = %v, want containing %q", tt.alias, err, tt.wantErr)
			}
			if names := reg.Names(); len(names) != 0 {
				t.Fatalf("invalid Register mutated the registry: %v", names)
			}
		})
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	reg := registry.NewRegistry()
	reg.MustRegister("demo.greeter", greeterImpl{prefix: "first"})

	err := reg.Register("demo.greeter", greeterImpl{prefix: "second"})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate Register error = %v, want already registered", err)
	}

	got, ok := reg.Lookup("demo.greeter")
	if !ok || got.(greeterImpl).prefix != "first" {
		t.Fatalf("duplicate Register overwrote the original instance: %#v, %v", got, ok)
	}
}

func TestRegisterTrimsName(t *testing.T) {
	reg := registry.NewRegistry()
	reg.MustRegister("  demo.greeter  ", greeterImpl{})

	if _, ok := reg.Lookup("demo.greeter"); !ok {
		t.Fatalf("Register did not trim the name; Names = %v", reg.Names())
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "demo.greeter" {
		t.Fatalf("Names = %v, want [demo.greeter]", names)
	}
}

func TestNamesReturnsSortedCopy(t *testing.T) {
	reg := registry.NewRegistry()
	reg.MustRegister("b.second", greeterImpl{})
	reg.MustRegister("a.first", greeterImpl{})

	want := []string{"a.first", "b.second"}
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}

	names := reg.Names()
	names[0] = "mutated"
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names returned a mutable view: %v", got)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var reg *registry.Registry

	if _, ok := reg.Lookup("demo.greeter"); ok {
		t.Fatalf("nil Registry.Lookup reported success")
	}
	if names := reg.Names(); names != nil {
		t.Fatalf("nil Registry.Names = %v, want nil", names)
	}
	if _, err := registry.Get[greeter](reg, "demo.greeter"); err == nil {
		t.Fatalf("Get on a nil Registry returned no error")
	}
}

func TestRegistrySupportsConcurrentUse(t *testing.T) {
	reg := registry.NewRegistry()
	const workers = 32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.MustRegister(nameFor(i), greeterImpl{prefix: nameFor(i)})
			if _, err := registry.Get[greeter](reg, nameFor(i)); err != nil {
				t.Errorf("Get(%q): %v", nameFor(i), err)
			}
			_ = reg.Names()
		}()
	}
	wg.Wait()

	if got := len(reg.Names()); got != workers {
		t.Fatalf("registered %d entries, want %d", got, workers)
	}
}

func nameFor(i int) string {
	return "demo.greeter." + strconv.Itoa(i)
}
