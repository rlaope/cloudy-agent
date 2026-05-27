package db

import (
	"context"
	"fmt"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

const pgQueryTimeout = 10 * time.Second

func pickPostgres(m map[string]*PostgresClient, name string) (*PostgresClient, error) {
	return tools.PickEndpoint(m, name, "db", "postgres endpoint")
}

// runPGQuery executes a fixed query under a deadline-bounded context and
// renders the result as a render.Table plus a row-of-maps Raw payload.
func runPGQuery(ctx context.Context, pc *PostgresClient, sql string, args ...any) (*render.Table, []map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()
	conn, err := pc.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	headers := make([]string, len(fields))
	for i, f := range fields {
		headers[i] = string(f.Name)
	}
	tbl := &render.Table{Headers: headers}

	var raw []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, nil, fmt.Errorf("scan: %w", err)
		}
		row := make([]string, len(vals))
		m := make(map[string]any, len(vals))
		for i, v := range vals {
			row[i] = formatPGValue(v)
			m[headers[i]] = v
		}
		tbl.Rows = append(tbl.Rows, row)
		raw = append(raw, m)
	}
	if rows.Err() != nil {
		return nil, nil, fmt.Errorf("rows: %w", rows.Err())
	}
	return tbl, raw, nil
}

func formatPGValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case time.Time:
		return t.Format(time.RFC3339)
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

var pgEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the postgres endpoint configured under databases. Optional if exactly one is configured.",
}

func newPGVersionTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "db.pg_version",
		Description: "Return the Postgres server version() string.",
		Schema:      mustJSON(map[string]any{"type": "object", "properties": map[string]any{"name": pgEndpointSchema}}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, "SELECT version()")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_version: %w", err)
			}
			text := ""
			if len(raw) > 0 {
				text = formatPGValue(raw[0]["version"])
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}

func newPGStatActivityTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  pgEndpointSchema,
			"limit": map[string]any{"type": "integer", "description": "Rows to return, ordered by query duration desc (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	const sql = `SELECT pid, usename, application_name, client_addr::text, state,
       EXTRACT(EPOCH FROM (now() - xact_start))::int AS xact_secs,
       EXTRACT(EPOCH FROM (now() - query_start))::int AS query_secs,
       wait_event_type, wait_event,
       LEFT(query, 200) AS query
  FROM pg_stat_activity
 WHERE state IS NOT NULL AND pid <> pg_backend_pid()
 ORDER BY query_secs DESC NULLS LAST
 LIMIT $1`
	return tools.Spec[args]{
		Name:        "db.pg_stat_activity",
		Description: "Return the top N connections from pg_stat_activity ordered by query duration.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, sql, a.Limit)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_stat_activity: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("pg_stat_activity top %d (returned %d)", a.Limit, len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newPGStatDatabaseTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	const sql = `SELECT datname, numbackends, xact_commit, xact_rollback,
       blks_read, blks_hit,
       tup_returned, tup_fetched, tup_inserted, tup_updated, tup_deleted,
       deadlocks, conflicts, temp_files, temp_bytes
  FROM pg_stat_database
 WHERE datname IS NOT NULL
 ORDER BY datname`
	return tools.Spec[args]{
		Name:        "db.pg_stat_database",
		Description: "Per-database commit/rollback/blks_read/tup_* counters from pg_stat_database.",
		Schema:      mustJSON(map[string]any{"type": "object", "properties": map[string]any{"name": pgEndpointSchema}}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, sql)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_stat_database: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("pg_stat_database (%d rows)", len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newPGStatReplicationTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	const sql = `SELECT application_name, client_addr::text, state, sync_state,
       pg_wal_lsn_diff(pg_current_wal_lsn(), sent_lsn)   AS sent_lag_bytes,
       pg_wal_lsn_diff(pg_current_wal_lsn(), write_lsn)  AS write_lag_bytes,
       pg_wal_lsn_diff(pg_current_wal_lsn(), flush_lsn)  AS flush_lag_bytes,
       pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS replay_lag_bytes
  FROM pg_stat_replication
 ORDER BY application_name`
	return tools.Spec[args]{
		Name:        "db.pg_stat_replication",
		Description: "Replication state and per-stage lag (bytes) for each connected standby.",
		Schema:      mustJSON(map[string]any{"type": "object", "properties": map[string]any{"name": pgEndpointSchema}}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, sql)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_stat_replication: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("pg_stat_replication (%d standbys)", len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newPGLocksTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name           string `json:"name"`
		OnlyNotGranted bool   `json:"only_not_granted"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":             pgEndpointSchema,
			"only_not_granted": map[string]any{"type": "boolean", "description": "Restrict to locks that are waiting (granted = false).", "default": false},
		},
	})
	const sql = `SELECT l.pid, l.locktype, l.mode, l.relation::regclass::text AS relation,
       l.granted, a.usename, a.application_name,
       EXTRACT(EPOCH FROM (now() - a.query_start))::int AS query_secs,
       LEFT(a.query, 200) AS query
  FROM pg_locks l
  LEFT JOIN pg_stat_activity a ON a.pid = l.pid
 WHERE ($1::bool IS FALSE) OR (l.granted IS FALSE)
 ORDER BY l.granted, query_secs DESC NULLS LAST
 LIMIT 500`
	return tools.Spec[args]{
		Name:        "db.pg_locks",
		Description: "Current locks (pg_locks ⨝ pg_stat_activity). Use only_not_granted=true to surface waiters.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, sql, a.OnlyNotGranted)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_locks: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("pg_locks (%d rows, only_not_granted=%v)", len(raw), a.OnlyNotGranted),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newPGTableSizeTool(clients map[string]*PostgresClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  pgEndpointSchema,
			"limit": map[string]any{"type": "integer", "description": "Top N tables by total size (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
	})
	const sql = `SELECT schemaname, relname,
       pg_size_pretty(pg_total_relation_size(format('%I.%I', schemaname, relname)::regclass)) AS total_size,
       pg_size_pretty(pg_relation_size(format('%I.%I', schemaname, relname)::regclass))        AS heap_size,
       n_live_tup, n_dead_tup
  FROM pg_stat_user_tables
 ORDER BY pg_total_relation_size(format('%I.%I', schemaname, relname)::regclass) DESC NULLS LAST
 LIMIT $1`
	return tools.Spec[args]{
		Name:        "db.pg_top_table_size",
		Description: "Top N user tables by pg_total_relation_size with live/dead tuple counts.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 20
			}
			if a.Limit > 200 {
				a.Limit = 200
			}
			pc, err := pickPostgres(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runPGQuery(ctx, pc, sql, a.Limit)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.pg_top_table_size: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("pg top tables (%d rows)", len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}
