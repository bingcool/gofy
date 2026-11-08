package apprepository_test

import (
	"testing"

	"github.com/bingcool/gofy"
	apprepository "github.com/yourorg/demo-service/app/repository"
)

func TestAssembleRegistersTypedRepositories(t *testing.T) {
	reg, err := apprepository.Assemble(&gofy.Runtime{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	names := reg.Names()
	want := []string{apprepository.NameOrderOrders, apprepository.NameProductProducts}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	if _, err := apprepository.OrderOrders(reg); err != nil {
		t.Fatalf("OrderOrders: %v", err)
	}
	if _, err := apprepository.ProductProducts(reg); err != nil {
		t.Fatalf("ProductProducts: %v", err)
	}
}

func TestProviderBuildsOneRegistryPerAssembly(t *testing.T) {
	provider := apprepository.NewProvider()
	rt := &gofy.Runtime{}

	first, err := provider.Repositories(rt)
	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	second, err := provider.Repositories(rt)
	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if first != second {
		t.Fatal("one Provider returned two registries for the same assembly")
	}

	other, err := apprepository.NewProvider().Repositories(rt)
	if err != nil {
		t.Fatalf("fresh Repositories: %v", err)
	}
	if first == other {
		t.Fatal("a fresh Provider reused the previous assembly registry")
	}
}
