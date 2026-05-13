// Package db provides read-only diagnostic tools for the cloudy SRE agent
// against Postgres, MySQL, and Redis. Every tool wraps a fixed query or
// command — there is no free-form SQL or arbitrary command surface. Use a
// read-only database user for the connection; cloudy enforces read-only at
// the query layer (only SELECT/SHOW/EXPLAIN-style queries are issued) and at
// the Redis command layer (only read commands are wrapped).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/rlaope/cloudy/internal/config"
)

// Clients holds connected handles keyed by the endpoint Name from
// cloudy.yaml. Each backend has its own map; tools look up by name.
type Clients struct {
	Postgres map[string]*PostgresClient
	MySQL    map[string]*MySQLClient
	Redis    map[string]*redis.Client
}

// PostgresClient is a thin wrapper around pgx that hands out short-lived
// connections per call. We use pgx.ConnectConfig over a pool to keep the
// surface small and avoid long-lived idle connections to production DBs.
type PostgresClient struct {
	cfg *pgx.ConnConfig
}

// Acquire opens a single connection. Callers MUST defer Close on the
// returned connection.
func (c *PostgresClient) Acquire(ctx context.Context) (*pgx.Conn, error) {
	return pgx.ConnectConfig(ctx, c.cfg)
}

// MySQLClient holds a database/sql handle. database/sql pools connections
// internally; we set conservative limits to avoid hammering a prod replica.
type MySQLClient struct {
	db *sql.DB
}

// DB returns the underlying *sql.DB. Use QueryContext / QueryRowContext with
// ctx-bounded deadlines.
func (c *MySQLClient) DB() *sql.DB { return c.db }

// connectTimeout caps initial-connect / ping time for probes. Per-tool query
// timeouts are configured via context at call sites.
const connectTimeout = 3 * time.Second

// BuildClients constructs the per-backend client maps from cfg. An entry
// whose connection cannot be established is dropped with its error recorded
// in skipReasons; the caller (wiring) propagates these to the Registry's
// skipped-group surface.
//
// Endpoints fall into three groups by Kind; an empty Kind or unknown Kind
// is treated as "unknown" and skipped with a clear reason.
func BuildClients(ctx context.Context, eps []config.DatabaseEndpoint) (Clients, []string) {
	cs := Clients{
		Postgres: map[string]*PostgresClient{},
		MySQL:    map[string]*MySQLClient{},
		Redis:    map[string]*redis.Client{},
	}
	var skips []string

	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	for _, ep := range eps {
		if ep.Name == "" || ep.DSN == "" {
			skips = append(skips, fmt.Sprintf("entry %q: missing name or dsn", ep.Name))
			continue
		}
		kind := strings.ToLower(ep.Kind)
		switch kind {
		case "postgres", "postgresql", "pg":
			pc, err := dialPostgres(cctx, ep)
			if err != nil {
				skips = append(skips, fmt.Sprintf("postgres %q: %v", ep.Name, err))
				continue
			}
			cs.Postgres[ep.Name] = pc
		case "mysql", "mariadb":
			mc, err := dialMySQL(cctx, ep)
			if err != nil {
				skips = append(skips, fmt.Sprintf("mysql %q: %v", ep.Name, err))
				continue
			}
			cs.MySQL[ep.Name] = mc
		case "redis", "valkey":
			rc, err := dialRedis(cctx, ep)
			if err != nil {
				skips = append(skips, fmt.Sprintf("redis %q: %v", ep.Name, err))
				continue
			}
			cs.Redis[ep.Name] = rc
		default:
			skips = append(skips, fmt.Sprintf("entry %q: unknown kind %q", ep.Name, ep.Kind))
		}
	}
	return cs, skips
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool {
	return len(c.Postgres) == 0 && len(c.MySQL) == 0 && len(c.Redis) == 0
}

func dialPostgres(ctx context.Context, ep config.DatabaseEndpoint) (*PostgresClient, error) {
	cfg, err := pgx.ParseConfig(ep.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if ep.PasswordEnv != "" {
		if pw := os.Getenv(ep.PasswordEnv); pw != "" {
			cfg.Password = pw
		}
	}
	// Force read-only at the session level — belt-and-suspenders alongside
	// the expectation that the configured user is itself read-only.
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	_ = conn.Close(ctx)
	return &PostgresClient{cfg: cfg}, nil
}

func dialMySQL(ctx context.Context, ep config.DatabaseEndpoint) (*MySQLClient, error) {
	dsn := ep.DSN
	if ep.PasswordEnv != "" {
		if pw := os.Getenv(ep.PasswordEnv); pw != "" {
			dsn = injectMySQLPassword(dsn, pw)
		}
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &MySQLClient{db: db}, nil
}

// injectMySQLPassword injects a password into a Go-MySQL DSN of the form
// "user@tcp(host:port)/db" or "user:pw@tcp(...)/db". If a password is
// already present it is preserved.
func injectMySQLPassword(dsn, password string) string {
	at := strings.Index(dsn, "@")
	if at < 0 {
		return dsn
	}
	cred := dsn[:at]
	if strings.Contains(cred, ":") {
		return dsn
	}
	return cred + ":" + password + dsn[at:]
}

func dialRedis(ctx context.Context, ep config.DatabaseEndpoint) (*redis.Client, error) {
	opts, err := parseRedisDSN(ep.DSN)
	if err != nil {
		return nil, err
	}
	if ep.PasswordEnv != "" {
		if pw := os.Getenv(ep.PasswordEnv); pw != "" {
			opts.Password = pw
		}
	}
	rc := redis.NewClient(opts)
	if err := rc.Ping(ctx).Err(); err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return rc, nil
}

// parseRedisDSN accepts either "host:port" (with optional ?db=N) or a
// "redis://" URL. We avoid the full url.Parse round-trip when the simpler
// form is used, since most operators write "redis-prod:6379".
func parseRedisDSN(dsn string) (*redis.Options, error) {
	if strings.HasPrefix(dsn, "redis://") || strings.HasPrefix(dsn, "rediss://") {
		return redis.ParseURL(dsn)
	}
	addr := dsn
	db := 0
	if i := strings.Index(dsn, "?"); i >= 0 {
		addr = dsn[:i]
		if v := paramValue(dsn[i+1:], "db"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &db); err != nil {
				return nil, fmt.Errorf("db param: %w", err)
			}
		}
	}
	if addr == "" {
		return nil, errors.New("empty addr")
	}
	return &redis.Options{Addr: addr, DB: db}, nil
}

func paramValue(query, key string) string {
	for _, kv := range strings.Split(query, "&") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}
