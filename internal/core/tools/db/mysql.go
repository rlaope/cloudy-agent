package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

const mysqlQueryTimeout = 10 * time.Second

func pickMySQL(m map[string]*MySQLClient, name string) (*MySQLClient, error) {
	return tools.PickEndpoint(m, name, "db", "mysql endpoint")
}

// runMySQLQuery executes sql with args and renders the result as a table.
// Each row is also returned as a map so the agent can post-process.
func runMySQLQuery(ctx context.Context, mc *MySQLClient, query string, args ...any) (*render.Table, []map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, mysqlQueryTimeout)
	defer cancel()

	rows, err := mc.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("columns: %w", err)
	}
	tbl := &render.Table{Headers: cols}

	var raw []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("scan: %w", err)
		}
		row := make([]string, len(cols))
		m := make(map[string]any, len(cols))
		for i, v := range vals {
			row[i] = formatMySQLValue(v)
			m[cols[i]] = decodeMySQLValue(v)
		}
		tbl.Rows = append(tbl.Rows, row)
		raw = append(raw, m)
	}
	if rows.Err() != nil {
		return nil, nil, fmt.Errorf("rows: %w", rows.Err())
	}
	return tbl, raw, nil
}

// formatMySQLValue renders a driver value as a string. []byte values (which
// the mysql driver uses for most string-ish types) are converted to UTF-8.
func formatMySQLValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// decodeMySQLValue mirrors formatMySQLValue but returns Raw-friendly Go
// types ([]byte → string) without losing nil-ness.
func decodeMySQLValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

var mysqlEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the mysql endpoint configured under databases. Optional if exactly one is configured.",
}

func newMySQLVersionTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "db.mysql_version",
		Description: "Return the MySQL/MariaDB server VERSION() string.",
		Schema:      mustJSON(map[string]any{"type": "object", "properties": map[string]any{"name": mysqlEndpointSchema}}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runMySQLQuery(ctx, mc, "SELECT VERSION() AS version")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.mysql_version: %w", err)
			}
			text := ""
			if len(raw) > 0 {
				text = formatMySQLValue(raw[0]["version"])
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}

func newMySQLProcesslistTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name        string `json:"name"`
		OnlyActive  bool   `json:"only_active"`
		MinDuration int    `json:"min_duration_seconds"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":                 mysqlEndpointSchema,
			"only_active":          map[string]any{"type": "boolean", "description": "Hide rows in Sleep state.", "default": true},
			"min_duration_seconds": map[string]any{"type": "integer", "description": "Minimum TIME to include (default 0 = no floor).", "default": 0, "minimum": 0},
		},
	})
	const baseSQL = `SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, LEFT(INFO, 200) AS INFO
  FROM information_schema.PROCESSLIST
 WHERE 1=1`
	return tools.Spec[args]{
		Name:        "db.mysql_processlist",
		Description: "Current sessions from information_schema.PROCESSLIST. Filters: only_active hides Sleep, min_duration_seconds floors TIME.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			q := baseSQL
			var qargs []any
			if a.OnlyActive {
				q += " AND COMMAND <> 'Sleep'"
			}
			if a.MinDuration > 0 {
				q += " AND TIME >= ?"
				qargs = append(qargs, a.MinDuration)
			}
			q += " ORDER BY TIME DESC LIMIT 500"
			tbl, raw, err := runMySQLQuery(ctx, mc, q, qargs...)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.mysql_processlist: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("processlist (%d rows)", len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newMySQLGlobalStatusTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
		Like string `json:"like"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": mysqlEndpointSchema,
			"like": map[string]any{"type": "string", "description": "SHOW GLOBAL STATUS LIKE pattern (e.g. 'Innodb_%'). Empty = all."},
		},
	})
	return tools.Spec[args]{
		Name:        "db.mysql_global_status",
		Description: "SHOW GLOBAL STATUS, optionally filtered with a LIKE pattern.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			q, qargs := buildShowLikeQuery("SHOW GLOBAL STATUS", a.Like)
			tbl, raw, err := runMySQLQuery(ctx, mc, q, qargs...)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.mysql_global_status: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("global status (%d rows, like=%q)", len(raw), a.Like),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

func newMySQLGlobalVariablesTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
		Like string `json:"like"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": mysqlEndpointSchema,
			"like": map[string]any{"type": "string", "description": "SHOW GLOBAL VARIABLES LIKE pattern. Empty = all."},
		},
	})
	return tools.Spec[args]{
		Name:        "db.mysql_global_variables",
		Description: "SHOW GLOBAL VARIABLES, optionally filtered with a LIKE pattern.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			q, qargs := buildShowLikeQuery("SHOW GLOBAL VARIABLES", a.Like)
			tbl, raw, err := runMySQLQuery(ctx, mc, q, qargs...)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.mysql_global_variables: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("global variables (%d rows, like=%q)", len(raw), a.Like),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}

// buildShowLikeQuery appends a LIKE clause when filter is non-empty. The
// pattern is passed as a bound parameter to avoid concat surprises, even
// though SHOW does not honour all placeholder forms — empty filter omits
// the clause entirely.
func buildShowLikeQuery(base, filter string) (string, []any) {
	if filter == "" {
		return base, nil
	}
	// MySQL SHOW does not accept ? placeholders for LIKE, so we escape the
	// pattern conservatively. Allow only printable ASCII plus %, _, .
	safe := sanitiseShowLike(filter)
	return base + " LIKE '" + safe + "'", nil
}

func sanitiseShowLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '%' || r == '_' || r == '.' || r == '-':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newMySQLEngineStatusTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "db.mysql_engine_innodb_status",
		Description: "SHOW ENGINE INNODB STATUS — InnoDB monitor output (transactions, latches, log, buffer pool).",
		Schema:      mustJSON(map[string]any{"type": "object", "properties": map[string]any{"name": mysqlEndpointSchema}}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, mysqlQueryTimeout)
			defer cancel()
			var typ, name, status string
			row := mc.db.QueryRowContext(ctx, "SHOW ENGINE INNODB STATUS")
			if err := row.Scan(&typ, &name, &status); err != nil {
				if err == sql.ErrNoRows {
					return tools.Observation{Text: "(empty)"}, nil
				}
				return tools.Observation{}, fmt.Errorf("db.mysql_engine_innodb_status: %w", err)
			}
			return tools.Observation{Text: status, Raw: status}, nil
		},
	}.Build()
}

func newMySQLTopTableSizeTool(clients map[string]*MySQLClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  mysqlEndpointSchema,
			"limit": map[string]any{"type": "integer", "description": "Top N tables by total size (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
	})
	const sql = `SELECT TABLE_SCHEMA, TABLE_NAME, ENGINE, TABLE_ROWS,
       DATA_LENGTH, INDEX_LENGTH, DATA_LENGTH+INDEX_LENGTH AS TOTAL_BYTES
  FROM information_schema.TABLES
 WHERE TABLE_SCHEMA NOT IN ('mysql','information_schema','performance_schema','sys')
 ORDER BY TOTAL_BYTES DESC
 LIMIT ?`
	return tools.Spec[args]{
		Name:        "db.mysql_top_table_size",
		Description: "Top N user tables by DATA_LENGTH + INDEX_LENGTH from information_schema.TABLES.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 20
			}
			if a.Limit > 200 {
				a.Limit = 200
			}
			mc, err := pickMySQL(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			tbl, raw, err := runMySQLQuery(ctx, mc, sql, a.Limit)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.mysql_top_table_size: %w", err)
			}
			return tools.Observation{
				Text:  fmt.Sprintf("top tables (%d rows)", len(raw)),
				Table: tbl,
				Raw:   raw,
			}, nil
		},
	}.Build()
}
