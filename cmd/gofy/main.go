// Command gofy scaffolds a new gofy application.
// gofy 命令用于脚手架生成一个新的 gofy 应用。
package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

//go:embed all:templates
var templatesFS embed.FS

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type scaffold struct {
	Name         string
	EnvPrefix    string
	Module       string
	Replace      string
	WithDB       bool
	WithScripts  bool
	WithWorker   bool
	WithDocker   bool
	WithMakefile bool
	GoVersion    string
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gofy:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gofy",
		Short:         "Scaffold gofy applications",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newNewCmd())
	return root
}

func newNewCmd() *cobra.Command {
	var (
		module       string
		dir          string
		withDB       bool
		withScripts  bool
		withWorker   bool
		withDocker   bool
		withMakefile bool
		force        bool
		replace      string
		goVersion    string
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new application",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if dir == "" {
				// `gofy new .` scaffolds the directory it is run in: the name comes
				// from the directory, which is the shape used after `go mod init`.
				// `gofy new .` 表示就地生成：名字取自目录名，正是 `go mod init` 之后的用法。
				if name == "." {
					dir = "."
				} else {
					dir = "./" + name
				}
			}
			if name == "." {
				abs, err := filepath.Abs(dir)
				if err != nil {
					return fmt.Errorf("resolve %s: %w", dir, err)
				}
				name = filepath.Base(abs)
			}
			if !namePattern.MatchString(name) {
				return fmt.Errorf("invalid name %q (want lowercase letters, digits and dashes)", name)
			}

			// The module path comes from go.mod when it is already there: the
			// usual flow is `go mod init` first, then this command. Passing both
			// and having them disagree would generate imports that never compile,
			// so that is rejected instead of silently preferred.
			// 已有的 go.mod 决定模块路径——常见流程是先 `go mod init` 再执行本命令。
			// 两者都给且不一致会生成编译不过的 import，因此直接拒绝而不是默默取一个。
			mod := strings.TrimSpace(module)
			if existing, ok := readModulePath(dir); ok {
				if mod == "" {
					mod = existing
					fmt.Printf("using module %q from %s\n", mod, filepath.Join(dir, "go.mod"))
				} else if existing != mod {
					return fmt.Errorf("--module %q does not match go.mod (%q)", mod, existing)
				}
			}
			if mod == "" {
				return errors.New("--module is required when the target has no go.mod")
			}

			absReplace := replace
			if absReplace == "" {
				// Running from inside this repository is the common way to try the
				// scaffold, so point the replace at it instead of demanding a flag
				// the caller would have to look up first.
				// 在框架仓库内试用脚手架是常见场景，直接指向本仓库，省去查路径的步骤。
				absReplace = enclosingGofyRoot()
				if absReplace != "" {
					fmt.Printf("using local framework at %s\n", absReplace)
				}
			} else {
				abs, err := filepath.Abs(absReplace)
				if err != nil {
					return fmt.Errorf("resolve --replace: %w", err)
				}
				absReplace = abs
			}
			s := scaffold{
				Name:         name,
				EnvPrefix:    strings.ToUpper(strings.ReplaceAll(name, "-", "_")),
				Module:       mod,
				Replace:      absReplace,
				WithDB:       withDB,
				WithScripts:  withScripts,
				WithWorker:   withWorker,
				WithDocker:   withDocker,
				WithMakefile: withMakefile,
				GoVersion:    goVersion,
			}
			return s.Generate(dir, force)
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path (default: read from <dir>/go.mod)")
	cmd.Flags().StringVar(&dir, "dir", "", "output directory (default ./<name>, or . when the name is .)")
	cmd.Flags().BoolVar(&withDB, "with-db", false, "include MySQL/GORM wiring")
	cmd.Flags().BoolVar(&withScripts, "with-scripts", true, "include the script/ directory")
	cmd.Flags().BoolVar(&withWorker, "with-worker", true, "include the worker/ entry for long-running tasks")
	cmd.Flags().BoolVar(&withDocker, "with-docker", false, "include Dockerfile and .dockerignore")
	cmd.Flags().BoolVar(&withMakefile, "with-makefile", true, "include a Makefile")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite generated files and allow a non-empty target directory")
	cmd.Flags().StringVar(&replace, "replace", "", "add a local replace directive for github.com/bingcool/gofy")
	cmd.Flags().StringVar(&goVersion, "go", "1.26", "go directive in go.mod")
	cmd.AddCommand(newInterfaceServiceCmd())
	return cmd
}

// Generate renders the embedded templates into dir.
//
// A directory that already holds a Go module is a normal target, not a
// conflict: the flow this command is built for is `go mod init` first. In that
// case go.mod is always merged (the framework requirement and replace are added,
// the module line, the go directive and the caller's other requirements stay),
// even under force: dropping them is silent data loss, and the flag is about
// overwriting files the scaffold generated, not the caller's module identity.
// Every other file that already exists is left alone unless force is set — the
// scaffold must not overwrite work it did not create.
// Generate 把内嵌模板渲染到 dir。
//
// 已有 Go module 的目录是正常目标而不是冲突：本命令面向的流程就是先 `go mod init`。
// 这种情况下 go.mod 始终走合并（补框架依赖与 replace，module 行、go 指令和调用方
// 其它依赖原样保留），force 也不例外：丢掉它们是静默的数据丢失，而这个开关说的是
// 覆盖脚手架生成的文件，不是调用方的模块身份。
// 其余已存在的文件在未指定 force 时保持不动——脚手架不该覆盖不是它创建的内容。
func (s scaffold) Generate(dir string, force bool) error {
	dir = filepath.Clean(dir)
	files, err := s.files()
	if err != nil {
		return err
	}
	outs := make([]string, 0, len(files))
	for _, f := range files {
		outs = append(outs, f.out)
	}
	// The check accepts every path the templates can produce, not just the ones
	// this run writes: turning --with-scripts / --with-worker off and re-running
	// must not make the directories the previous run created look foreign.
	// 检查接受模板可能产出的全部路径，而不只是本次要写的：关掉
	// --with-scripts / --with-worker 后重跑，不能让上一次生成的目录变成外来内容。
	known, err := templateOutputs()
	if err != nil {
		return err
	}
	if err := checkAppTarget(dir, force, append(outs, known...)); err != nil {
		return err
	}
	var kept, merged []string
	for _, f := range files {
		out := filepath.Join(dir, filepath.FromSlash(f.out))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(out), err)
		}
		if existing, err := os.ReadFile(out); err == nil {
			// go.mod keeps its merged form under --force too: the module path,
			// go directive and the caller's own requirements are not the
			// scaffold's to drop, and losing them prints no warning.
			// 即使指定 --force，go.mod 仍保持合并语义：module 路径、go 指令与调用方
			// 自己的依赖不归脚手架处置，而丢掉它们不会有任何提示。
			if f.out == "go.mod" {
				content, changed := mergeGoMod(existing, s)
				if !changed {
					kept = append(kept, f.out)
					continue
				}
				if err := os.WriteFile(out, content, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", out, err)
				}
				merged = append(merged, f.out)
				continue
			}
			if !force {
				kept = append(kept, f.out)
				continue
			}
		}
		if err := os.WriteFile(out, f.content, filePerm(f.out)); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
	}
	if len(merged) > 0 {
		fmt.Printf("updated %s (framework requirement and replace)\n", strings.Join(merged, ", "))
	}
	if len(kept) > 0 {
		fmt.Printf("kept existing: %s\n", strings.Join(kept, ", "))
	}
	if dir == "." {
		fmt.Print("created the current directory\nnext steps:\n  go mod tidy\n  go run . start\n")
		return nil
	}
	fmt.Printf("created %s\nnext steps:\n  cd %s\n  go mod tidy\n  go run . start\n", dir, dir)
	return nil
}

type generatedFile struct {
	out     string
	content []byte
}

// filePerm keeps shell scripts executable: the import guard is meant to be run
// from CI or a pre-commit hook, and a non-executable file there is a papercut
// somebody has to debug.
// filePerm 让 shell 脚本保持可执行：import 守卫要在 CI 或 pre-commit 里跑，一个不可
// 执行的文件会变成需要有人去排查的小坑。
func filePerm(out string) os.FileMode {
	if strings.HasSuffix(out, ".sh") {
		return 0o755
	}
	return 0o644
}

func (s scaffold) files() ([]generatedFile, error) {
	var files []generatedFile
	err := fs.WalkDir(templatesFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimPrefix(p, "templates/")
		out, ok := outputPath(name)
		if !ok || !s.enabled(out) {
			return nil
		}
		raw, err := templatesFS.ReadFile(p)
		if err != nil {
			return err
		}
		tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, s); err != nil {
			return fmt.Errorf("render template %s: %w", name, err)
		}
		content := buf.Bytes()
		if strings.HasSuffix(out, ".go") {
			formatted, err := format.Source(content)
			if err != nil {
				return fmt.Errorf("format %s: %w", out, err)
			}
			content = formatted
		}
		files = append(files, generatedFile{out: out, content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s scaffold) enabled(out string) bool {
	switch {
	case out == "Dockerfile" || out == ".dockerignore":
		return s.WithDocker
	case out == "Makefile":
		return s.WithMakefile
	case strings.HasPrefix(out, "script/"):
		return s.WithScripts
	case strings.HasPrefix(out, "worker/"):
		return s.WithWorker
	default:
		return true
	}
}

// templateOutputs lists every path the embedded templates can produce,
// regardless of the current flags. Generate writes only the enabled subset, but
// the target check has to accept everything this command itself may have
// generated before: otherwise turning an optional module off turns its
// directory into an intruder.
// templateOutputs 列出全部模板可能产出的路径，与当前开关无关。Generate 只写启用的
// 子集，但目标检查必须接受本命令自己此前可能生成的全部内容：否则关掉一个可选模块，
// 它的目录就变成了入侵者。
func templateOutputs() ([]string, error) {
	var outs []string
	err := fs.WalkDir(templatesFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if out, ok := outputPath(strings.TrimPrefix(p, "templates/")); ok {
			outs = append(outs, out)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outs, nil
}

func outputPath(name string) (string, bool) {
	switch name {
	case "gitignore.tmpl":
		return ".gitignore", true
	case "dockerignore.tmpl":
		return ".dockerignore", true
	}
	if !strings.HasSuffix(name, ".tmpl") {
		return "", false
	}
	return strings.TrimSuffix(name, ".tmpl"), true
}

// moduleFiles are what a freshly initialised module directory may contain
// without the scaffold treating it as occupied.
// moduleFiles 是刚初始化的模块目录允许存在的内容，脚手架不把它们视为占用。
var moduleFiles = map[string]bool{
	"go.mod": true, "go.sum": true,
	".git": true, ".gitignore": true, ".gitattributes": true,
	".gitlab-ci.yml": true, ".github": true,
	".idea": true, ".vscode": true, ".DS_Store": true,
}

// checkTarget refuses a directory that holds anything but a go.mod. It belongs
// to the contract scaffolder, which rewrites every file and therefore needs an
// empty target.
// checkTarget 拒绝除 go.mod 之外还有内容的目录。它属于契约脚手架——那个命令会重写
// 所有文件，因此需要空目标。
func checkTarget(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if force {
		return nil
	}
	for _, entry := range entries {
		if moduleFiles[entry.Name()] {
			continue
		}
		return fmt.Errorf("directory %s already contains %s (use --force to write anyway)", dir, entry.Name())
	}
	return nil
}

// checkAppTarget decides whether dir may receive the application scaffold. A
// fresh module directory and a directory this command already generated both
// qualify: the first is the documented flow, the second is a re-run, and existing
// files are kept rather than rewritten. Anything else means the caller pointed at
// a project that is not ours, which is a mistake worth reporting before files
// appear in it.
// checkAppTarget 判断 dir 是否可以接收应用脚手架。刚初始化的模块目录与"本命令已经
// 生成过的目录"都合格：前者是文档流程，后者是重跑，已存在的文件会保留而不是重写。其它
// 情况说明调用方指到了一个不属于我们的项目，应在文件落地之前报错。
func checkAppTarget(dir string, force bool, outs []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if force {
		return nil
	}
	allowed := make(map[string]bool, len(outs)*2)
	for _, out := range outs {
		parts := strings.Split(out, "/")
		for i := 1; i <= len(parts); i++ {
			allowed[strings.Join(parts[:i], "/")] = true
		}
	}
	for _, entry := range entries {
		if moduleFiles[entry.Name()] || allowed[entry.Name()] {
			continue
		}
		return fmt.Errorf("directory %s already contains %s (use --force to write anyway)", dir, entry.Name())
	}
	return nil
}

// readModulePath reads the module directive of dir/go.mod.
// readModulePath 读取 dir/go.mod 的 module 指令。
func readModulePath(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), true
		}
	}
	return "", false
}

// mergeGoMod adds the framework requirement and, when set, the local replace to
// an existing go.mod. Nothing else in the file is touched: the caller's module
// line, toolchain and requirements stay as they are, and a directive that is
// already present is not duplicated (a second replace for the same module is a
// syntax error, a second require is not but would confuse `go mod tidy`).
// mergeGoMod 把框架依赖与（可选）本地 replace 合并进已有的 go.mod，文件其余内容不动：
// module 行、toolchain 与调用方的依赖原样保留，已存在的指令不重复添加（同一模块的第二条
// replace 是语法错误；第二条 require 虽合法却会让 `go mod tidy` 困惑）。
func mergeGoMod(existing []byte, s scaffold) ([]byte, bool) {
	text := string(existing)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	changed := false
	if !strings.Contains(text, gofyModulePath) {
		text += "\nrequire " + gofyModulePath + " " + gofyRequireVersion + "\n"
		changed = true
	}
	if s.Replace != "" && !hasReplaceDirective(text, gofyModulePath) {
		text += "\nreplace " + gofyModulePath + " => " + s.Replace + "\n"
		changed = true
	}
	return []byte(text), changed
}

// hasReplaceDirective reports whether go.mod already replaces module, reading
// both the single-line form and the block form. Appending a second replace for
// the same module is a syntax error, so the check cannot be a substring match on
// one spelling.
// hasReplaceDirective 报告 go.mod 是否已经 replace 了 module，单行与块两种写法都读。
// 同一模块的第二条 replace 是语法错误，因此不能只对一种写法做子串匹配。
func hasReplaceDirective(text, module string) bool {
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if replaceTarget(line) == module {
				return true
			}
			continue
		}
		rest, ok := strings.CutPrefix(line, "replace ")
		if !ok {
			continue
		}
		if strings.TrimSpace(rest) == "(" {
			inBlock = true
			continue
		}
		if replaceTarget(rest) == module {
			return true
		}
	}
	return false
}

// replaceTarget returns the left-hand module of "path => dir" or
// "path v1.2.3 => dir".
// replaceTarget 返回 "path => dir" 或 "path v1.2.3 => dir" 左侧的模块路径。
func replaceTarget(spec string) string {
	left, _, ok := strings.Cut(spec, "=>")
	if !ok {
		return ""
	}
	if fields := strings.Fields(left); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

const (
	// gofyModulePath is the framework's module path, used both in generated imports and in go.mod.
	// gofyModulePath 是框架的模块路径，生成的 import 与 go.mod 都用它。
	gofyModulePath = "github.com/bingcool/gofy"
	// gofyRequireVersion is the placeholder version written when the caller has not
	// pinned one. It only needs to satisfy `go mod tidy` with the replace in place;
	// without a replace, pin the version your proxy actually serves.
	// gofyRequireVersion 是调用方未指定版本时写入的占位版本。带 replace 时它只用于让
	// `go mod tidy` 通过；没有 replace 时应改成你的代理上真实存在的版本。
	gofyRequireVersion = "v1.0.0"
)

// enclosingGofyRoot reports the directory of this repository when the process
// runs inside it, so a local trial does not need --replace.
// enclosingGofyRoot 在进程位于本仓库内时返回仓库目录，本地试用因此不必传 --replace。
func enclosingGofyRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if module, ok := readModulePath(dir); ok && module == gofyModulePath {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
