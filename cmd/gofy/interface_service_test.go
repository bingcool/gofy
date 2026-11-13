package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeLocalRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, repo := range []string{"gofy-interface-api"} {
		dir := filepath.Join(root, repo)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/"+repo+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testContractScaffold(dir, root string) contractScaffold {
	return contractScaffold{
		Module:    "gitlab.acme.com/platform/contracts",
		Dir:       dir,
		LocalRoot: root,
	}
}

func TestValidateContractModule(t *testing.T) {
	for _, bad := range []string{"", "contracts", "has space/path", "github.com/yourorg/contracts"} {
		if err := validateContractModule(bad); err == nil {
			t.Errorf("validateContractModule(%q) = nil, want error", bad)
		}
	}
	if err := validateContractModule("gitlab.acme.com/platform/contracts"); err != nil {
		t.Fatalf("valid module rejected: %v", err)
	}
}

func TestGenerateContractModuleRewritesEverything(t *testing.T) {
	root := fakeLocalRoot(t)
	dir := filepath.Join(t.TempDir(), "acme-contracts")
	if err := testContractScaffold(dir, root).generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}

	want := []string{
		"go.mod", "go.sum", "Makefile", "README.md",
		"cmd/openapi/main.go", "cmd/openapi/main_test.go",
		"appinterface/demoservice/orderroute/order_route.go",
		"openapi/all.yaml", "openapi/demoservice.yaml", "openapi/embed.go",
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		count++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "yourorg") {
			t.Errorf("%s still contains the placeholder module path", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := 0
	if err := fs.WalkDir(contractTemplateFS, contractTemplateRoot, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			expected++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("generated %d files, want the full %d-file template", count, expected)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal/integration")); !os.IsNotExist(err) {
		t.Fatal("contract module must not carry gofy-dependent integration tests")
	}

	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(goMod)
	if !strings.Contains(text, "module gitlab.acme.com/platform/contracts") {
		t.Fatalf("go.mod module line: %s", text[:min(len(text), 120)])
	}
	absDir, _ := filepath.Abs(dir)
	if strings.Contains(text, contractGofyModule+" => ") {
		t.Fatal("contract module must not carry a gofy replace")
	}
	if strings.Contains(text, "require github.com/bingcool/gofy v0.0.0") {
		t.Fatal("contract module must not require the gofy framework")
	}
	for _, stale := range []string{
		contractGofyModule + " => ../..",
		contractAPIModule + " => ../../../gofy-interface-api",
	} {
		if strings.Contains(text, stale) {
			t.Fatalf("go.mod still contains the template replace %q", stale)
		}
	}
	wantAPIRel, _ := filepath.Rel(absDir, filepath.Join(root, "gofy-interface-api"))
	if !strings.Contains(text, "replace "+contractAPIModule+" => "+filepath.ToSlash(wantAPIRel)) {
		t.Fatalf("api replace not rewritten with %q:\n%s", wantAPIRel, text[len(text)-220:])
	}

	generator, err := os.ReadFile(filepath.Join(dir, "cmd/openapi/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generator), "gitlab.acme.com/platform/contracts/appinterface") {
		t.Fatal("cmd/openapi still imports the placeholder module")
	}

	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "本模块由 `gofy new interface-service` 生成") ||
		strings.Contains(string(readme), "占位 module path") {
		t.Fatalf("README banner not replaced: %s", string(readme)[:min(len(readme), 240)])
	}
}

func TestGenerateContractModuleRequiresSiblingRepos(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme-contracts")
	s := testContractScaffold(dir, t.TempDir())
	if err := s.generate(); err == nil || !strings.Contains(err.Error(), "local replace is required") {
		t.Fatalf("generate error = %v, want missing sibling repos", err)
	}
}

func TestGenerateContractModuleRejectsNonEmptyDir(t *testing.T) {
	root := fakeLocalRoot(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testContractScaffold(dir, root).generate(); err == nil {
		t.Fatal("generate must refuse a non-empty directory without --force")
	}
	s := testContractScaffold(dir, root)
	s.Force = true
	if err := s.generate(); err != nil {
		t.Fatalf("generate with --force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("--force must keep unrelated files")
	}
}

func TestApplyContractReplacesAndBannerAreIdempotent(t *testing.T) {
	replaces := map[string]string{
		contractAPIModule: "../../gofy-interface-api",
	}
	once := applyContractReplaces(
		"replace "+contractAPIModule+" => ../../../gofy-interface-api\n", replaces)
	twice := applyContractReplaces(once, replaces)
	if once != twice {
		t.Fatalf("second apply changed the file:\n%s\n---\n%s", once, twice)
	}
	banner := "> **复制本目录到新仓库后，第一步执行改写。**\n> `old` 只是不发布模板的占位 module path。\n# Title\n"
	got := replaceTemplateBanner(banner, "example.com/new")
	if strings.Contains(got, "占位 module path") || !strings.Contains(got, "example.com/new") || !strings.Contains(got, "# Title") {
		t.Fatalf("banner = %q", got)
	}
}
