// Command gofy scaffolds a new gofy application.
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

const gofyModule = "github.com/bingcool/gofy"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type scaffold struct {
	Name         string
	EnvPrefix    string
	Module       string
	Replace      string
	WithDB       bool
	WithScripts  bool
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
			if !namePattern.MatchString(name) {
				return fmt.Errorf("invalid name %q (want lowercase letters, digits and dashes)", name)
			}
			if strings.TrimSpace(module) == "" {
				return errors.New("--module is required")
			}
			if dir == "" {
				dir = "./" + name
			}
			absReplace := replace
			if absReplace != "" {
				abs, err := filepath.Abs(absReplace)
				if err != nil {
					return fmt.Errorf("resolve --replace: %w", err)
				}
				absReplace = abs
			}
			s := scaffold{
				Name:         name,
				EnvPrefix:    strings.ToUpper(strings.ReplaceAll(name, "-", "_")),
				Module:       strings.TrimSpace(module),
				Replace:      absReplace,
				WithDB:       withDB,
				WithScripts:  withScripts,
				WithDocker:   withDocker,
				WithMakefile: withMakefile,
				GoVersion:    goVersion,
			}
			return s.Generate(dir, force)
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path (required)")
	cmd.Flags().StringVar(&dir, "dir", "", "output directory (default ./<name>)")
	cmd.Flags().BoolVar(&withDB, "with-db", false, "include MySQL/GORM wiring")
	cmd.Flags().BoolVar(&withScripts, "with-scripts", true, "include the script/ directory")
	cmd.Flags().BoolVar(&withDocker, "with-docker", false, "include Dockerfile and .dockerignore")
	cmd.Flags().BoolVar(&withMakefile, "with-makefile", true, "include a Makefile")
	cmd.Flags().BoolVar(&force, "force", false, "write into a non-empty directory")
	cmd.Flags().StringVar(&replace, "replace", "", "add a local replace directive for github.com/bingcool/gofy")
	cmd.Flags().StringVar(&goVersion, "go", "1.24", "go directive in go.mod")
	return cmd
}

// Generate renders the embedded templates into dir.
func (s scaffold) Generate(dir string, force bool) error {
	dir = filepath.Clean(dir)
	if err := checkTarget(dir, force); err != nil {
		return err
	}
	files, err := s.files()
	if err != nil {
		return err
	}
	for _, f := range files {
		out := filepath.Join(dir, filepath.FromSlash(f.out))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(out), err)
		}
		if err := os.WriteFile(out, f.content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
	}
	fmt.Printf("created %s\nnext steps:\n  cd %s\n  go mod tidy\n  go run . start\n", dir, dir)
	return nil
}

type generatedFile struct {
	out     string
	content []byte
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
	switch out {
	case "Dockerfile", ".dockerignore":
		return s.WithDocker
	case "Makefile":
		return s.WithMakefile
	case "script/register.go", "script/example.go":
		return s.WithScripts
	default:
		return true
	}
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

func checkTarget(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("directory %s is not empty (use --force to overwrite)", dir)
	}
	return nil
}
