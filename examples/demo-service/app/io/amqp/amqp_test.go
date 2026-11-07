package amqp

import (
	"context"
	"testing"
)

func TestOpenSkipsDisabledInstances(t *testing.T) {
	set, err := Open(context.Background(), Config{
		DefaultInstance: {Enabled: false},
		"order":         {Enabled: false},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	if Default != nil {
		t.Fatal("disabled default instance was installed")
	}
	if client := Get(DefaultInstance); client != nil {
		t.Fatal("disabled instance is reachable through Get")
	}
	if names := Names(); len(names) != 0 {
		t.Fatalf("names = %v", names)
	}
}

func TestNilSetIsSafe(t *testing.T) {
	var set *Set
	set.Install()
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
