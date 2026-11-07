package config_test

import (
	"context"
	"strings"
	"testing"
	"time"

	gofyconfig "github.com/bingcool/gofy/config"
)

const documentYAML = `
db:
  default:
    dsn: user:pass@tcp(db:3306)/app
    max_open_conns: 50
    prepare_stmt: false
    ratio: 1.5
    conn_max_lifetime: 1h
    tags:
      - primary
      - reporting
    empty: null
servers:
  - host: one
  - host: two
`

func loadDocument(t *testing.T, contents string, opts ...gofyconfig.Option) *gofyconfig.Document {
	t.Helper()
	doc, err := gofyconfig.LoadDocument(context.Background(), writeFile(t, contents), opts...)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	return doc
}

func TestDocumentReadsValuesByPath(t *testing.T) {
	doc := loadDocument(t, documentYAML, gofyconfig.WithStrict(true))

	if got, ok := doc.GetString("db.default.dsn"); !ok || got != "user:pass@tcp(db:3306)/app" {
		t.Fatalf("GetString(dsn) = (%q, %v)", got, ok)
	}
	if got, ok := doc.GetInt("db.default.max_open_conns"); !ok || got != 50 {
		t.Fatalf("GetInt(max_open_conns) = (%d, %v)", got, ok)
	}
	if got, ok := doc.GetInt64("db.default.max_open_conns"); !ok || got != 50 {
		t.Fatalf("GetInt64(max_open_conns) = (%d, %v)", got, ok)
	}
	if got, ok := doc.GetBool("db.default.prepare_stmt"); !ok || got {
		t.Fatalf("GetBool(prepare_stmt) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetFloat64("db.default.ratio"); !ok || got != 1.5 {
		t.Fatalf("GetFloat64(ratio) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetDuration("db.default.conn_max_lifetime"); !ok || got != time.Hour {
		t.Fatalf("GetDuration(conn_max_lifetime) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetStringSlice("db.default.tags"); !ok || len(got) != 2 || got[0] != "primary" || got[1] != "reporting" {
		t.Fatalf("GetStringSlice(tags) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetString("servers.1.host"); !ok || got != "two" {
		t.Fatalf("GetString(servers.1.host) = (%q, %v)", got, ok)
	}
	if !doc.Has("db.default.dsn") || doc.Has("db.missing") {
		t.Fatal("Has returned an unexpected result")
	}
}

func TestDocumentCoercesScalarsToString(t *testing.T) {
	doc := loadDocument(t, documentYAML)

	cases := map[string]string{
		"db.default.max_open_conns": "50",
		"db.default.prepare_stmt":   "false",
		"db.default.ratio":          "1.5",
	}
	for path, want := range cases {
		got, ok := doc.GetString(path)
		if !ok || got != want {
			t.Fatalf("GetString(%s) = (%q, %v), want %q", path, got, ok, want)
		}
	}
}

func TestDocumentReturnsFalseForMismatchedValues(t *testing.T) {
	doc := loadDocument(t, documentYAML)

	if got, ok := doc.Get("db.default.empty"); !ok || got != nil {
		t.Fatalf("Get(empty) = (%v, %v), want (nil, true)", got, ok)
	}
	if _, ok := doc.GetString("db.default.empty"); ok {
		t.Fatal("GetString accepted a null value")
	}
	if _, ok := doc.GetInt("db.default.dsn"); ok {
		t.Fatal("GetInt accepted a non-numeric string")
	}
	if _, ok := doc.GetInt("db.default.ratio"); ok {
		t.Fatal("GetInt accepted a float")
	}
	if _, ok := doc.GetBool("db.default.max_open_conns"); ok {
		t.Fatal("GetBool accepted an integer")
	}
	if _, ok := doc.GetDuration("db.default.max_open_conns"); ok {
		t.Fatal("GetDuration accepted a bare number")
	}
	if _, ok := doc.GetStringSlice("db.default.dsn"); ok {
		t.Fatal("GetStringSlice accepted a scalar")
	}

	badPaths := []string{
		"",
		"db.default.dsn.extra",
		"db..dsn",
		"db.missing",
		"servers.2.host",
		"servers.x.host",
		"servers.-1.host",
	}
	for _, path := range badPaths {
		if _, ok := doc.Get(path); ok {
			t.Fatalf("Get(%q) = ok, want false", path)
		}
	}
}

func TestDocumentParsesStringScalars(t *testing.T) {
	doc := loadDocument(t, `
port: "8080"
enabled: "true"
timeout: "30s"
ratio: "2.25"
`)

	if got, ok := doc.GetInt("port"); !ok || got != 8080 {
		t.Fatalf("GetInt(port) = (%d, %v)", got, ok)
	}
	if got, ok := doc.GetBool("enabled"); !ok || !got {
		t.Fatalf("GetBool(enabled) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetDuration("timeout"); !ok || got != 30*time.Second {
		t.Fatalf("GetDuration(timeout) = (%v, %v)", got, ok)
	}
	if got, ok := doc.GetFloat64("ratio"); !ok || got != 2.25 {
		t.Fatalf("GetFloat64(ratio) = (%v, %v)", got, ok)
	}
}

func TestDocumentExpandsEnvironmentVariables(t *testing.T) {
	t.Setenv("GOFY_TEST_DOC_DSN", "user:secret@tcp(db:3306)/app")
	contents := "db:\n  dsn: ${GOFY_TEST_DOC_DSN}\n"

	doc := loadDocument(t, contents)
	if got, _ := doc.GetString("db.dsn"); got != "user:secret@tcp(db:3306)/app" {
		t.Fatalf("expanded dsn = %q", got)
	}

	doc = loadDocument(t, contents, gofyconfig.WithExpandEnv(false))
	if got, _ := doc.GetString("db.dsn"); got != "${GOFY_TEST_DOC_DSN}" {
		t.Fatalf("unescaped dsn = %q", got)
	}
}

func TestDocumentRequiresEnvironmentVariables(t *testing.T) {
	path := writeFile(t, "db:\n  dsn: \"${GOFY_TEST_DOC_UNSET}\"\n")

	_, err := gofyconfig.LoadDocument(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "GOFY_TEST_DOC_UNSET") {
		t.Fatalf("LoadDocument error = %v, want missing variable", err)
	}

	doc, err := gofyconfig.LoadDocument(context.Background(), path, gofyconfig.WithAllowMissingEnv(true))
	if err != nil {
		t.Fatalf("LoadDocument with allow missing: %v", err)
	}
	if got, ok := doc.GetString("db.dsn"); !ok || got != "" {
		t.Fatalf("missing dsn = (%q, %v), want empty string", got, ok)
	}
}

func TestDocumentHandlesEmptyAndInvalidFiles(t *testing.T) {
	doc := loadDocument(t, "")
	if _, ok := doc.Get("db"); ok {
		t.Fatal("empty document returned a value")
	}

	path := writeFile(t, "db: [unclosed\n")
	if _, err := gofyconfig.LoadDocument(context.Background(), path); err == nil {
		t.Fatal("LoadDocument accepted invalid YAML")
	}

	if _, err := gofyconfig.LoadDocument(context.Background(), "  "); err == nil {
		t.Fatal("LoadDocument accepted an empty path")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gofyconfig.LoadDocument(ctx, writeFile(t, "db: {}\n")); err == nil {
		t.Fatal("LoadDocument ignored a cancelled context")
	}
}

func TestNilDocumentIsSafe(t *testing.T) {
	var doc *gofyconfig.Document
	if _, ok := doc.GetString("db.dsn"); ok {
		t.Fatal("nil Document returned a value")
	}
	if doc.Has("db.dsn") {
		t.Fatal("nil Document reported a key")
	}
}
