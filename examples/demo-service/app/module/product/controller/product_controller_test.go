package controller

import (
	"context"
	"errors"
	"testing"
)

func TestProductControllerGetAndList(t *testing.T) {
	c := NewProductController(nil)
	ctx := context.Background()

	got, err := c.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	if got.ID != 1 || got.Name == "" {
		t.Fatalf("unexpected product: %+v", got)
	}
	if _, err := c.Get(ctx, 999); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("Get(999) error = %v, want ErrProductNotFound", err)
	}

	items := c.List(ctx)
	if len(items) == 0 {
		t.Fatal("List returned no products")
	}
	items[0].Name = "mutated"
	if again := c.List(ctx); again[0].Name == "mutated" {
		t.Fatal("List returned the internal slice")
	}
}
