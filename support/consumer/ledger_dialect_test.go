package consumer

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TestLedgerTimestampColumnsHaveNoHardcodedType pins the dialect-neutral model:
// GORM already maps time.Time to datetime(3) on MySQL, timestamptz on
// PostgreSQL and datetime on SQLite. An explicit type tag overrides all three
// and makes AutoMigrate fail on PostgreSQL.
// TestLedgerTimestampColumnsHaveNoHardcodedType 固定模型不写死列类型：GORM 本来就会把
// time.Time 映射成 MySQL 的 datetime(3)、PostgreSQL 的 timestamptz 与 SQLite 的
// datetime。显式 type tag 会覆盖这三者，并让 PostgreSQL 上的 AutoMigrate 失败。
func TestLedgerTimestampColumnsHaveNoHardcodedType(t *testing.T) {
	parsed, err := schema.Parse(&consumedMessage{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse consumedMessage: %v", err)
	}
	for _, name := range []string{"CreatedAt", "UpdatedAt"} {
		field := parsed.LookUpField(name)
		if field == nil {
			t.Fatalf("field %s is missing", name)
		}
		if got := field.TagSettings["TYPE"]; got != "" {
			t.Fatalf("%s carries type %q; leave the type to the dialector", name, got)
		}
	}
}

// TestLedgerDbNowKeepsMySQLMilliseconds pins the write side of the lease to the
// precision the MySQL predicate compares with. PostgreSQL and SQLite already
// have enough precision in CURRENT_TIMESTAMP.
// TestLedgerDbNowKeepsMySQLMilliseconds 固定租约写入侧与 MySQL 判断侧的精度一致。
// PostgreSQL 与 SQLite 的 CURRENT_TIMESTAMP 精度已经足够。
func TestLedgerDbNowKeepsMySQLMilliseconds(t *testing.T) {
	cases := map[string]string{
		"mysql":    "CURRENT_TIMESTAMP(3)",
		"postgres": "CURRENT_TIMESTAMP",
		"sqlite":   "CURRENT_TIMESTAMP",
	}
	for dialect, want := range cases {
		if got := dbNowExpr(dialect).SQL; got != want {
			t.Fatalf("dbNowExpr(%q) = %q, want %q", dialect, got, want)
		}
	}
}
