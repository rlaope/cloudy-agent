package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/db"
)

func TestBuildClients_NoEndpoints(t *testing.T) {
	t.Parallel()
	cs, skips := db.BuildClients(context.Background(), nil)
	if !cs.Empty() {
		t.Errorf("expected empty clients, got %+v", cs)
	}
	if len(skips) != 0 {
		t.Errorf("expected no skip reasons, got %v", skips)
	}
}

func TestBuildClients_UnknownKindRecordsSkip(t *testing.T) {
	t.Parallel()
	cs, skips := db.BuildClients(context.Background(), []config.DatabaseEndpoint{
		{Name: "weird", Kind: "supabase", DSN: "anything"},
	})
	if !cs.Empty() {
		t.Errorf("expected empty clients for unknown kind, got %+v", cs)
	}
	if len(skips) == 0 || !strings.Contains(skips[0], "unknown kind") {
		t.Errorf("expected skip reason about unknown kind, got %v", skips)
	}
}

func TestBuildClients_MissingFieldsRecordsSkip(t *testing.T) {
	t.Parallel()
	_, skips := db.BuildClients(context.Background(), []config.DatabaseEndpoint{
		{Name: "", Kind: "postgres", DSN: "postgres://localhost"},
	})
	if len(skips) == 0 || !strings.Contains(skips[0], "missing name or dsn") {
		t.Errorf("expected skip about missing name, got %v", skips)
	}
}

func TestBuildClients_BadPostgresDSNRecordsSkip(t *testing.T) {
	t.Parallel()
	_, skips := db.BuildClients(context.Background(), []config.DatabaseEndpoint{
		{Name: "bad-pg", Kind: "postgres", DSN: "::::not-a-uri::::"},
	})
	if len(skips) == 0 || !strings.Contains(skips[0], "bad-pg") {
		t.Errorf("expected skip for bad-pg, got %v", skips)
	}
}

func TestRegisterAll_EmptyMarksGroupSkipped(t *testing.T) {
	t.Parallel()
	reg := tools.New()
	db.RegisterAll(reg, db.Clients{}, []string{"redis \"cache\": dial: connection refused"})

	skipped := reg.Skipped()
	reason, ok := skipped["db"]
	if !ok {
		t.Fatalf("expected group db to be skipped, got %+v", skipped)
	}
	if !strings.Contains(reason, "no usable database endpoints") {
		t.Errorf("expected composed reason, got %q", reason)
	}
	if len(reg.List()) != 0 {
		t.Errorf("expected no tools registered, got %d", len(reg.List()))
	}
}

func TestRegisterAll_RedisOnlyRegistersRedisTools(t *testing.T) {
	t.Parallel()
	cs := db.Clients{
		Redis: map[string]*redis.Client{
			"cache": redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}),
		},
		Postgres: map[string]*db.PostgresClient{},
		MySQL:    map[string]*db.MySQLClient{},
	}
	reg := tools.New()
	db.RegisterAll(reg, cs, nil)

	wantTools := []string{
		"db.redis_info",
		"db.redis_dbsize",
		"db.redis_scan",
		"db.redis_inspect_key",
		"db.redis_slowlog",
		"db.redis_client_list",
	}
	for _, name := range wantTools {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("expected %q registered", name)
		}
	}
	// No postgres/mysql tools should be registered.
	if _, ok := reg.Get("db.pg_version"); ok {
		t.Error("did not expect db.pg_version when no postgres clients")
	}
	if _, ok := reg.Get("db.mysql_version"); ok {
		t.Error("did not expect db.mysql_version when no mysql clients")
	}

	inv := reg.Inventory()
	hasDB := false
	for _, g := range inv.Groups {
		if g.Name == "db" {
			hasDB = true
			if g.Skipped {
				t.Errorf("group db should not be skipped when redis clients exist: %+v", g)
			}
		}
	}
	if !hasDB {
		t.Error("expected group db in inventory")
	}
}
