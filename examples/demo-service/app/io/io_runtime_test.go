package io

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	gofyredis "github.com/bingcool/gofy/support/redis"
	appmysql "github.com/yourorg/demo-service/app/io/mysql"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	"gorm.io/gorm"
)

func TestStartInstallsAndStopClearsHandles(t *testing.T) {
	mr := miniredis.RunT(t)
	rt := core.NewRuntime(core.RuntimeConfig{DB: &gorm.DB{}})
	m := NewModule(rt, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, appredis.Config{
		appredis.InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		appmysql.Db, appmysql.OrderDb, appmysql.ProductDb = nil, nil, nil
	})

	if appmysql.Db != rt.DB {
		t.Fatal("mysql.Db does not point at the runtime connection")
	}
	if appredis.RateLimit == nil {
		t.Fatal("redis.RateLimit was not installed")
	}
	if err := appredis.RateLimit.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping ratelimit: %v", err)
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appredis.RateLimit != nil {
		t.Fatal("redis.RateLimit was not cleared")
	}
}
