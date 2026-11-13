// Command interface-service scaffolds a project contract module.
// gofy new interface-service 生成项目契约模块（appinterface + OpenAPI 生成器）。
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// contractTemplateFS is a byte-for-byte snapshot of examples/interface-service.
// Files carry a .tmpl suffix so the Go toolchain never treats the snapshot as a
// compilable package; regenerate it with `make sync-contract-template`.
// contractTemplateFS 是 examples/interface-service 的逐字节快照。文件带 .tmpl
// 后缀，避免 Go 工具链把快照当成可编译包；用 `make sync-contract-template` 重新生成。
//
//go:embed all:contract-template
var contractTemplateFS embed.FS

const (
	contractTemplateModule = "github.com/yourorg/interface-service"
	contractAPIModule      = "github.com/bingcool/gofy-interface-api"
	contractGofyModule     = "github.com/bingcool/gofy"
	contractTemplateRoot   = "contract-template"
)

type contractScaffold struct {
	Module    string
	Dir       string
	LocalRoot string
	Force     bool
	WithGit   bool
}

func newInterfaceServiceCmd() *cobra.Command {
	var (
		module    string
		dir       string
		localRoot string
		force     bool
		noGit     bool
	)
	cmd := &cobra.Command{
		Use:   "interface-service",
		Short: "Create a project contract module (appinterface + OpenAPI generator)",
		Long: "Create a project contract module from the embedded interface-service template.\n" +
			"The module path and every import are rewritten, the api library replace is\n" +
			"pointed at the sibling gofy-interface-api checkout, and go mod tidy is run so the\n" +
			"first command in the generated project can be `make check`.\n" +
			"从内嵌的 interface-service 模板创建项目契约模块：改写 module path 与全部 import，\n" +
			"把本地 replace 指向并排的 gofy-interface-api 仓库，并执行 go mod tidy，\n" +
			"使生成项目的第一条命令即可执行 `make check`。",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			module = strings.TrimSpace(module)
			if err := validateContractModule(module); err != nil {
				return err
			}
			if dir == "" {
				dir = "./interface-service"
			}
			if localRoot == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				localRoot = cwd
			}
			s := contractScaffold{
				Module:    module,
				Dir:       dir,
				LocalRoot: localRoot,
				Force:     force,
				WithGit:   !noGit,
			}
			if err := s.generate(); err != nil {
				return err
			}
			if err := s.tidy(); err != nil {
				return err
			}
			if s.WithGit {
				if err := s.initGit(); err != nil {
					return err
				}
			}
			fmt.Printf("created contract module %s at %s\nrun: cd %s && make check\n", module, dir, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path of the new contract project (required)")
	cmd.Flags().StringVar(&dir, "dir", "", "output directory (default ./interface-service)")
	cmd.Flags().StringVar(&localRoot, "local-root", "", "directory holding the sibling gofy-interface-api checkout (default cwd)")
	cmd.Flags().BoolVar(&force, "force", false, "write into a non-empty directory")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "skip git init/add (needed by the golden check)")
	return cmd
}

func validateContractModule(module string) error {
	switch {
	case module == "":
		return errors.New("--module is required")
	case strings.ContainsAny(module, " \t\n"):
		return fmt.Errorf("invalid module path %q: whitespace is not allowed", module)
	case !strings.Contains(module, "/"):
		return fmt.Errorf("invalid module path %q: want a full path such as gitlab.company.com/team/contracts", module)
	case strings.Contains(module, "yourorg"):
		return fmt.Errorf("invalid module path %q: the placeholder path must be replaced", module)
	default:
		return nil
	}
}

// generate writes every template file into Dir, rewriting the module path and
// the api library replace directive. It performs no external commands so unit
// tests can exercise the file layout without a Go toolchain.
// generate 把模板文件写入 Dir，改写 module path 与 api 库的本地 replace。它不执行外部
// 命令，便于单元测试在不依赖 Go 工具链的情况下检查文件布局。
func (s contractScaffold) generate() error {
	dir := filepath.Clean(s.Dir)
	if err := checkTarget(dir, s.Force); err != nil {
		return err
	}
	replaces, err := s.localReplaces(dir)
	if err != nil {
		return err
	}
	return fs.WalkDir(contractTemplateFS, contractTemplateRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), contractTemplateRoot+"/")
		name := strings.TrimSuffix(rel, ".tmpl")
		raw, err := contractTemplateFS.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(raw), contractTemplateModule, s.Module)
		switch name {
		case "go.mod":
			text = applyContractReplaces(text, replaces)
		case "README.md":
			text = replaceTemplateBanner(text, s.Module)
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, []byte(text), 0o644)
	})
}

func (s contractScaffold) localReplaces(dir string) (map[string]string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(s.LocalRoot)
	if err != nil {
		return nil, err
	}
	// The contract module no longer needs gofy at all: only the api library
	// requires a local checkout until it is published with a fixed version.
	// 契约模块已完全不需要 gofy；在 api 库发布并固定版本前，只有它需要本地 checkout。
	checks := []struct{ module, repo string }{
		{contractAPIModule, "gofy-interface-api"},
	}
	out := make(map[string]string, len(checks))
	for _, c := range checks {
		target := filepath.Join(root, c.repo)
		if _, err := os.Stat(filepath.Join(target, "go.mod")); err != nil {
			return nil, fmt.Errorf("--local-root %q: %s/go.mod not found; the api library is not published yet, so a local replace is required", root, c.repo)
		}
		rel, err := filepath.Rel(absDir, target)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		out[c.module] = rel
	}
	return out, nil
}

// applyContractReplaces rewrites replace directives line by line. A substring
// replacement would match a previously generated relative path on a second run
// and append to it, so the whole line is replaced instead.
// applyContractReplaces 按行重写 replace 指令。若用子串替换，第二次调用会命中
// 上次生成的相对路径并继续拼接，因此这里整行替换以保证幂等。
func applyContractReplaces(goMod string, replaces map[string]string) string {
	lines := strings.Split(goMod, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		// go.mod allows both a replace block and a standalone "replace X => Y"
		// line; keep whichever form the template uses.
		// go.mod 既支持 replace 块也支持单行 "replace X => Y"，保留模板原有写法。
		prefix := ""
		if strings.HasPrefix(trimmed, "replace ") {
			prefix = "replace "
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "replace "))
		}
		for module, rel := range replaces {
			if strings.HasPrefix(trimmed, module+" => ") {
				lines[i] = indent + prefix + module + " => " + rel
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func replaceTemplateBanner(text, module string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "> **复制本目录") {
			out = append(out, "> 本模块由 `gofy new interface-service` 生成，module path 为 `"+module+"`。")
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "> `") {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

func (s contractScaffold) tidy() error {
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = filepath.Clean(s.Dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// initGit makes the golden target usable: `git diff --exit-code -- openapi`
// needs a repository with the generated files tracked. Existing repositories
// are left untouched.
// initGit 让 golden 目标可用：`git diff --exit-code -- openapi` 需要一个已跟踪
// 生成物的仓库；若目录已属于某个仓库则不做改动。
func (s contractScaffold) initGit() error {
	dir := filepath.Clean(s.Dir)
	if err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Run(); err == nil {
		return nil
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
