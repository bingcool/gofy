package consumer

import (
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestLeasePredicateOnMySQL runs the lease predicate against a real MySQL, the
// dialect the tests cannot cover with SQLite. It is skipped unless
// GOFY_TEST_MYSQL_DSN points at a throwaway database: the predicate only needs a
// temporary table, but connecting somewhere shared by default is not a test's
// decision to make.
// TestLeasePredicateOnMySQL 在真实 MySQL 上跑租约表达式——SQLite 覆盖不到的方言。只有
// GOFY_TEST_MYSQL_DSN 指向一个可丢弃的库时才运行：表达式只需要一张临时表，但默认连到共享
// 库不该由测试替人决定。
func TestLeasePredicateOnMySQL(t *testing.T) {
	dsn := os.Getenv("GOFY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOFY_TEST_MYSQL_DSN to run this against a real MySQL")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}

	deduper := &deduper{db: db, consumer: "lease-probe", maxAttempts: 3, lease: time.Minute}
	predicate, ok := deduper.leaseExpiredPredicate()
	if !ok {
		t.Fatal("mysql is not recognised, so the lease would silently fall back to the node clock")
	}

	if err := db.Exec(`CREATE TEMPORARY TABLE lease_probe (
  id BIGINT PRIMARY KEY,
  updated_at DATETIME(3)
)`).Error; err != nil {
		t.Fatalf("create temporary table: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DROP TEMPORARY TABLE lease_probe`).Error })
	if err := db.Exec(`REPLACE INTO lease_probe (id, updated_at) VALUES
  (1, CURRENT_TIMESTAMP(3)),
  (2, CURRENT_TIMESTAMP(3) - INTERVAL 120 SECOND)`).Error; err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	cases := map[int64]bool{1: false, 2: true}
	for id, want := range cases {
		var expired bool
		if err := db.Raw(`SELECT `+predicate+` FROM lease_probe WHERE id = ?`, id).Scan(&expired).Error; err != nil {
			t.Fatalf("evaluate predicate for row %d: %v", id, err)
		}
		if expired != want {
			t.Fatalf("row %d expired = %v, want %v (predicate: %s)", id, expired, want, predicate)
		}
	}
}
