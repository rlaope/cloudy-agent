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
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools/k8s"
	"github.com/rlaope/cloudy/internal/transport"
)

// Clients holds connected handles keyed by the endpoint Name from
// cloudy.yaml. Each backend has its own map; tools look up by name.
type Clients struct {
	Postgres map[string]*PostgresClient
	MySQL    map[string]*MySQLClient
	Redis    map[string]*redis.Client
	// redisForwarders holds port-forward tunnels for k8s-backed Redis clients.
	// redis.Client has no extension point for a forwarder, so we track them
	// here keyed by the same endpoint name. Close via CloseForwarders.
	redisForwarders map[string]*transport.Forwarder
}

// CloseForwarders closes all port-forward tunnels owned by this Clients set.
// Call this when the registry is being replaced or the process is shutting
// down. PostgresClient.Close and MySQLClient.Close handle their own tunnels;
// this covers Redis.
func (c *Clients) CloseForwarders() {
	for _, fwd := range c.redisForwarders {
		_ = fwd.Close()
	}
}

// PostgresClient is a thin wrapper around pgx that hands out short-lived
// connections per call. We use pgx.ConnectConfig over a pool to keep the
// surface small and avoid long-lived idle connections to production DBs.
type PostgresClient struct {
	cfg *pgx.ConnConfig
	// fwd is non-nil when the connection is tunnelled through a k8s port-forward.
	// Close() shuts it down; nil fwd means the connection is direct.
	fwd *transport.Forwarder
}

// Acquire opens a single connection. Callers MUST defer Close on the
// returned connection.
func (c *PostgresClient) Acquire(ctx context.Context) (*pgx.Conn, error) {
	return pgx.ConnectConfig(ctx, c.cfg)
}

// Close releases the underlying port-forward tunnel (if any). Safe to call
// on a direct (non-k8s) client — it is a no-op in that case.
func (c *PostgresClient) Close() error {
	if c.fwd != nil {
		return c.fwd.Close()
	}
	return nil
}

// MySQLClient holds a database/sql handle. database/sql pools connections
// internally; we set conservative limits to avoid hammering a prod replica.
type MySQLClient struct {
	db *sql.DB
	// fwd is non-nil when the connection is tunnelled through a k8s port-forward.
	fwd *transport.Forwarder
}

// DB returns the underlying *sql.DB. Use QueryContext / QueryRowContext with
// ctx-bounded deadlines.
func (c *MySQLClient) DB() *sql.DB { return c.db }

// Close closes the underlying *sql.DB and releases the port-forward tunnel
// (if any). Safe to call on a direct client.
func (c *MySQLClient) Close() error {
	var dbErr error
	if c.db != nil {
		dbErr = c.db.Close()
	}
	if c.fwd != nil {
		if err := c.fwd.Close(); err != nil && dbErr == nil {
			return err
		}
	}
	return dbErr
}

// connectTimeout caps initial-connect / ping time for probes. Per-tool query
// timeouts are configured via context at call sites.
const connectTimeout = 3 * time.Second

// BuildClients constructs the per-backend client maps from eps.
//
// hub is required only for k8s:// DSNs. When hub is nil and a k8s:// DSN is
// encountered, a skip reason is recorded and that endpoint is omitted; all
// direct (postgres://, mysql://, redis://…) DSNs are unaffected.
//
// An entry whose connection cannot be established is dropped with its error
// recorded in skipReasons; the caller (wiring) propagates these to the
// Registry's skipped-group surface.
func BuildClients(ctx context.Context, hub *k8s.Hub, eps []config.DatabaseEndpoint) (Clients, []string) {
	cs := Clients{
		Postgres:        map[string]*PostgresClient{},
		MySQL:           map[string]*MySQLClient{},
		Redis:           map[string]*redis.Client{},
		redisForwarders: map[string]*transport.Forwarder{},
	}
	var (
		mu    sync.Mutex
		skips []string
		wg    sync.WaitGroup
	)

	addSkip := func(s string) { mu.Lock(); skips = append(skips, s); mu.Unlock() }

	for _, ep := range eps {
		ep := ep
		if ep.Name == "" || ep.DSN == "" {
			addSkip(fmt.Sprintf("entry %q: missing name or dsn", ep.Name))
			continue
		}

		// k8s:// DSNs require a live port-forward before the driver can dial.
		if strings.HasPrefix(ep.DSN, "k8s://") {
			if hub == nil {
				addSkip(fmt.Sprintf("db: %s: k8s DSN requires a Kubernetes hub", ep.Name))
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, connectTimeout)
				defer cancel()
				if err := buildK8sClient(cctx, hub, ep, &cs, &mu); err != nil {
					addSkip(fmt.Sprintf("db: %s (k8s): %v", ep.Name, err))
				}
			}()
			continue
		}

		// Direct dial path — unchanged from original behaviour.
		kind := strings.ToLower(ep.Kind)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each endpoint gets its own connectTimeout budget so a slow
			// failing endpoint cannot starve the rest. Dials happen in
			// parallel — startup latency is bounded by the slowest single
			// endpoint, not the sum.
			cctx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			switch kind {
			case "postgres", "postgresql", "pg":
				pc, err := dialPostgres(cctx, ep)
				if err != nil {
					addSkip(fmt.Sprintf("postgres %q: %v", ep.Name, err))
					return
				}
				mu.Lock()
				cs.Postgres[ep.Name] = pc
				mu.Unlock()
			case "mysql", "mariadb":
				mc, err := dialMySQL(cctx, ep)
				if err != nil {
					addSkip(fmt.Sprintf("mysql %q: %v", ep.Name, err))
					return
				}
				mu.Lock()
				cs.MySQL[ep.Name] = mc
				mu.Unlock()
			case "redis", "valkey":
				rc, err := dialRedis(cctx, ep)
				if err != nil {
					addSkip(fmt.Sprintf("redis %q: %v", ep.Name, err))
					return
				}
				mu.Lock()
				cs.Redis[ep.Name] = rc
				mu.Unlock()
			default:
				addSkip(fmt.Sprintf("entry %q: unknown kind %q", ep.Name, ep.Kind))
			}
		}()
	}
	wg.Wait()
	return cs, skips
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool {
	return len(c.Postgres) == 0 && len(c.MySQL) == 0 && len(c.Redis) == 0
}

// parseK8sDSN parses a k8s:// DSN into its components.
//
// Expected forms:
//
//	k8s://<ctx>/<namespace>/<service>:<port>
//	k8s:///<namespace>/<service>:<port>   — empty ctx means "default context"
//
// v0 deliberate compromise: user/db are defaulted per-driver
// (postgres→"postgres"/"postgres", mysql→"root"/"mysql"). A future
// "k8s+postgres://user@ctx/ns/svc:port/dbname" extended scheme can lift these.
func parseK8sDSN(dsn string) (ctxName, namespace, svcName string, port int, ok bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "k8s" {
		return
	}
	ctxName = u.Host // "" is valid — means "default context"

	// u.Path is "/<ns>/<svc>:<port>"
	path := strings.TrimPrefix(u.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return
	}
	namespace = parts[0]
	svcPort := parts[1]

	// Split "svcName:port"
	lastColon := strings.LastIndex(svcPort, ":")
	if lastColon < 0 {
		return
	}
	svcName = svcPort[:lastColon]
	portStr := svcPort[lastColon+1:]
	if namespace == "" || svcName == "" || portStr == "" {
		return
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return
	}
	port = p
	ok = true
	return
}

// buildK8sClient resolves a k8s:// endpoint into a SPDY port-forward then
// dials the appropriate driver against 127.0.0.1:<local-port>, storing the
// result (with the Forwarder attached) in cs under mu.
func buildK8sClient(ctx context.Context, hub *k8s.Hub, ep config.DatabaseEndpoint, cs *Clients, mu *sync.Mutex) error {
	ctxName, namespace, svcName, port, ok := parseK8sDSN(ep.DSN)
	if !ok {
		return fmt.Errorf("malformed k8s:// DSN %q", ep.DSN)
	}

	kClient, err := hub.Get(ctxName)
	if err != nil {
		return fmt.Errorf("get k8s client for context %q: %w", ctxName, err)
	}

	restCfg := kClient.RESTConfig()
	if restCfg == nil {
		return fmt.Errorf("k8s client for context %q has no REST config", ctxName)
	}

	podName, err := transport.SelectPod(ctx, kClient.Core(), namespace, svcName)
	if err != nil {
		return fmt.Errorf("select pod for %s/%s: %w", namespace, svcName, err)
	}

	fwd, err := transport.OpenPortForward(ctx, restCfg, namespace, podName, port, nil)
	if err != nil {
		return fmt.Errorf("open port-forward to %s/%s:%d: %w", namespace, podName, port, err)
	}

	localAddr := fwd.Local()
	kind := strings.ToLower(ep.Kind)

	switch kind {
	case "postgres", "postgresql", "pg":
		rewrittenEP := config.DatabaseEndpoint{
			Name: ep.Name,
			Kind: ep.Kind,
			DSN:  composePostgresDSN(localAddr, ep.PasswordEnv),
			// PasswordEnv baked into DSN by composePostgresDSN; leave empty
			// so dialPostgres does not attempt a second os.Getenv substitution.
		}
		pc, err := dialPostgres(ctx, rewrittenEP)
		if err != nil {
			_ = fwd.Close()
			return fmt.Errorf("dial postgres via port-forward: %w", err)
		}
		pc.fwd = fwd
		mu.Lock()
		cs.Postgres[ep.Name] = pc
		mu.Unlock()

	case "mysql", "mariadb":
		rewrittenEP := config.DatabaseEndpoint{
			Name: ep.Name,
			Kind: ep.Kind,
			DSN:  composeMySQLDSN(localAddr, ep.PasswordEnv),
		}
		mc, err := dialMySQL(ctx, rewrittenEP)
		if err != nil {
			_ = fwd.Close()
			return fmt.Errorf("dial mysql via port-forward: %w", err)
		}
		mc.fwd = fwd
		mu.Lock()
		cs.MySQL[ep.Name] = mc
		mu.Unlock()

	case "redis", "valkey":
		rewrittenEP := config.DatabaseEndpoint{
			Name:        ep.Name,
			Kind:        ep.Kind,
			DSN:         localAddr,
			PasswordEnv: ep.PasswordEnv,
		}
		rc, err := dialRedis(ctx, rewrittenEP)
		if err != nil {
			_ = fwd.Close()
			return fmt.Errorf("dial redis via port-forward: %w", err)
		}
		mu.Lock()
		cs.Redis[ep.Name] = rc
		cs.redisForwarders[ep.Name] = fwd
		mu.Unlock()

	default:
		_ = fwd.Close()
		return fmt.Errorf("unknown kind %q for k8s:// endpoint", ep.Kind)
	}

	return nil
}

// composePostgresDSN builds a postgres:// DSN pointing at localAddr.
//
// v0 defaults: user="postgres", db="postgres", sslmode=disable.
// Password is read from passwordEnv and URL-encoded into the DSN so the
// caller can pass an empty PasswordEnv to dialPostgres (no double-lookup).
func composePostgresDSN(localAddr, passwordEnv string) string {
	pw := ""
	if passwordEnv != "" {
		pw = os.Getenv(passwordEnv)
	}
	if pw != "" {
		return fmt.Sprintf("postgres://postgres:%s@%s/postgres?sslmode=disable",
			url.QueryEscape(pw), localAddr)
	}
	return fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", localAddr)
}

// composeMySQLDSN builds a Go-MySQL DSN pointing at localAddr.
//
// v0 defaults: user="root", db="mysql".
//
// The password is URL-escaped — a literal '@', ':', '/' or '?' in the
// operator-supplied password would otherwise split the DSN at the wrong
// boundary and the connection would silently authenticate as a different
// user or fail to parse. The Postgres composer already does this; missing
// here was the L-1 finding from the v0.5 security review.
func composeMySQLDSN(localAddr, passwordEnv string) string {
	pw := ""
	if passwordEnv != "" {
		pw = os.Getenv(passwordEnv)
	}
	if pw != "" {
		return fmt.Sprintf("root:%s@tcp(%s)/mysql", url.QueryEscape(pw), localAddr)
	}
	return fmt.Sprintf("root@tcp(%s)/mysql", localAddr)
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
