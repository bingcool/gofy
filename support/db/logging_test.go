package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	sensitivePhone = "13800000000"
	sensitiveEmail = "secret@example.com"
	sensitiveToken = "test-sensitive-token"
)

func TestSlowSQLLogKeepsTemplateWithoutValues(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	gdb := dryRunGorm(t, newGormLogger(zap.New(core), Config{SlowThreshold: time.Nanosecond}))

	var rows []struct{ ID int64 }
	err := gdb.Table("orders").
		Where("phone = ? OR email = ? OR token = ?", sensitivePhone, sensitiveEmail, sensitiveToken).
		Find(&rows).Error
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	entry := singleEntry(t, observed)
	if entry.Message != "slow sql" {
		t.Fatalf("message = %q, want slow sql", entry.Message)
	}
	fields := entry.ContextMap()
	if _, ok := fields["elapsed"]; !ok {
		t.Fatal("slow sql entry has no elapsed field")
	}
	sql, _ := fields["sql"].(string)
	if sql == "" {
		t.Fatal("slow sql entry has no statement template")
	}
	if !strings.Contains(sql, "?") {
		t.Fatalf("sql = %q, want placeholders", sql)
	}
	for _, secret := range []string{sensitivePhone, sensitiveEmail, sensitiveToken} {
		if strings.Contains(sql, secret) {
			t.Fatalf("sql leaked parameter value %q: %s", secret, sql)
		}
	}
}

func TestSQLErrorLogKeepsTemplateWithoutValues(t *testing.T) {
	core, observed := observer.New(zapcore.ErrorLevel)
	gl := newGormLogger(zap.New(core), Config{})

	template := "SELECT * FROM orders WHERE phone = ?"
	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return template, 0
	}, errors.New("driver failure"))

	entry := singleEntry(t, observed)
	if entry.Message != "sql error" {
		t.Fatalf("message = %q, want sql error", entry.Message)
	}
	if got := entry.ContextMap()["sql"]; got != template {
		t.Fatalf("sql = %v, want %q", got, template)
	}
	if got := fmt.Sprint(errorField(entry)); !strings.Contains(got, "driver failure") {
		t.Fatalf("error field = %s", got)
	}
}

func TestLogParameterValuesOptsIntoExpansion(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	gdb := dryRunGorm(t, newGormLogger(zap.New(core), Config{
		SlowThreshold:      time.Nanosecond,
		LogParameterValues: true,
	}))

	var rows []struct{ ID int64 }
	if err := gdb.Table("orders").Where("phone = ?", sensitivePhone).Find(&rows).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}

	sql, _ := singleEntry(t, observed).ContextMap()["sql"].(string)
	if !strings.Contains(sql, sensitivePhone) {
		t.Fatalf("sql = %q, want the explicit switch to expand values", sql)
	}
}

func dryRunGorm(t *testing.T, logger gormlogger.Interface) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "app:secret@tcp(127.0.0.1:3306)/app",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		Logger:               logger,
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return gdb
}

func singleEntry(t *testing.T, observed *observer.ObservedLogs) observer.LoggedEntry {
	t.Helper()
	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1: %v", len(entries), entries)
	}
	return entries[0]
}

func errorField(entry observer.LoggedEntry) interface{} {
	for _, field := range entry.Context {
		if field.Key == "error" {
			return field.Interface
		}
	}
	return nil
}
