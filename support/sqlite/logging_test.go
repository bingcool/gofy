package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

func TestParamsFilterHidesValuesByDefault(t *testing.T) {
	gl := newGormLogger(zap.NewNop(), Config{})
	filter, ok := gl.(gorm.ParamsFilter)
	if !ok {
		t.Fatal("gorm logger does not implement gorm.ParamsFilter")
	}
	sql, params := filter.ParamsFilter(context.Background(), "SELECT * FROM orders WHERE phone = ?", "13800000000")
	if sql != "SELECT * FROM orders WHERE phone = ?" {
		t.Fatalf("sql = %q", sql)
	}
	if params != nil {
		t.Fatalf("params = %v, want nil", params)
	}
}

func TestParamsFilterKeepsValuesWhenEnabled(t *testing.T) {
	gl := newGormLogger(zap.NewNop(), Config{LogParameterValues: true})
	filter := gl.(gorm.ParamsFilter)
	_, params := filter.ParamsFilter(context.Background(), "SELECT * FROM orders WHERE phone = ?", "13800000000")
	if len(params) != 1 || params[0] != "13800000000" {
		t.Fatalf("params = %v, want the original argument", params)
	}
}

func TestTraceLogsTemplateOnError(t *testing.T) {
	core, observed := observer.New(zapcore.ErrorLevel)
	gl := newGormLogger(zap.New(core), Config{})
	// gormLogger.Trace receives the SQL already filtered by ParamsFilter.
	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM orders WHERE phone = ?", 0
	}, errors.New("driver failure"))

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	if sql, _ := entries[0].ContextMap()["sql"].(string); !strings.Contains(sql, "?") {
		t.Fatalf("sql = %q, want placeholders", sql)
	}
}
