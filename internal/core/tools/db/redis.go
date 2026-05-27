package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// redisCallTimeout caps a single Redis call so a slow node can't stall the
// agent loop. Per-call ctx already carries a deadline from the agent; this
// is a fallback floor.
const redisCallTimeout = 5 * time.Second

// pickRedis is the redis-flavour alias around tools.PickEndpoint — kept as a
// named wrapper so the call sites in this file read with their domain noun.
func pickRedis(m map[string]*redis.Client, name string) (*redis.Client, error) {
	return tools.PickEndpoint(m, name, "db", "redis endpoint")
}

// redisEndpointSchema is the shared "name" property included in every tool.
var redisEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the redis endpoint configured under databases. Optional if exactly one is configured.",
}

// newRedisInfoTool wraps INFO [section]. Returns the raw text plus a
// parsed key-value table for the most common metrics.
func newRedisInfoTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		Section string `json:"section"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": redisEndpointSchema,
			"section": map[string]any{
				"type":        "string",
				"description": "INFO section (server, clients, memory, replication, stats, cpu, commandstats, keyspace). Empty = all.",
			},
		},
	})
	return tools.Spec[args]{
		Name:        "db.redis_info",
		Description: "Run Redis INFO [section] for server, memory, replication, and keyspace metrics.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			var out string
			if a.Section == "" {
				out, err = c.Info(ctx).Result()
			} else {
				out, err = c.Info(ctx, a.Section).Result()
			}
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.redis_info: %w", err)
			}
			return tools.Observation{
				Text:  out,
				Table: parseRedisInfoKV(out),
				Raw:   out,
			}, nil
		},
	}.Build()
}

// parseRedisInfoKV turns INFO output into a 2-column key=value table.
// Section headers ("# Server") become standalone "—" rows so the user can
// still read structure in the rendered table.
func parseRedisInfoKV(s string) *render.Table {
	tbl := &render.Table{
		Headers: []string{"KEY", "VALUE"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignLeft},
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			tbl.Rows = append(tbl.Rows, []string{strings.TrimSpace(strings.TrimPrefix(line, "#")), ""})
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			tbl.Rows = append(tbl.Rows, []string{k, v})
		}
	}
	return tbl
}

// newRedisDBSizeTool returns the keyspace size for the selected DB.
func newRedisDBSizeTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	schema := mustJSON(map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": redisEndpointSchema},
	})
	return tools.Spec[args]{
		Name:        "db.redis_dbsize",
		Description: "Return the number of keys in the selected Redis DB (DBSIZE).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			n, err := c.DBSize(ctx).Result()
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.redis_dbsize: %w", err)
			}
			return tools.Observation{Text: fmt.Sprintf("dbsize=%d", n), Raw: n}, nil
		},
	}.Build()
}

// newRedisScanTool wraps SCAN with MATCH/COUNT. Free-form KEYS * is *not*
// supported — SCAN is non-blocking and bounded by the count cap.
func newRedisScanTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		Match   string `json:"match"`
		Count   int64  `json:"count"`
		MaxKeys int64  `json:"max_keys"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     redisEndpointSchema,
			"match":    map[string]any{"type": "string", "description": "Key pattern (glob), e.g. user:* — required to avoid scanning all keys."},
			"count":    map[string]any{"type": "integer", "description": "SCAN COUNT hint per round (default 100, max 1000).", "default": 100, "minimum": 1, "maximum": 1000},
			"max_keys": map[string]any{"type": "integer", "description": "Total keys to collect across SCAN rounds (default 200, max 2000).", "default": 200, "minimum": 1, "maximum": 2000},
		},
		"required": []string{"match"},
	})
	return tools.Spec[args]{
		Name:        "db.redis_scan",
		Description: "Non-blocking key enumeration via SCAN MATCH. Caps key count to avoid full scans on huge instances.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Match == "" {
				return tools.Observation{}, fmt.Errorf("db.redis_scan: match is required")
			}
			if a.Count <= 0 {
				a.Count = 100
			}
			if a.Count > 1000 {
				a.Count = 1000
			}
			if a.MaxKeys <= 0 {
				a.MaxKeys = 200
			}
			if a.MaxKeys > 2000 {
				a.MaxKeys = 2000
			}
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			var keys []string
			var cursor uint64
			for {
				ks, next, err := c.Scan(ctx, cursor, a.Match, a.Count).Result()
				if err != nil {
					return tools.Observation{}, fmt.Errorf("db.redis_scan: %w", err)
				}
				keys = append(keys, ks...)
				if next == 0 || int64(len(keys)) >= a.MaxKeys {
					break
				}
				cursor = next
			}
			if int64(len(keys)) > a.MaxKeys {
				keys = keys[:a.MaxKeys]
			}
			return tools.Observation{
				Text: fmt.Sprintf("scanned match=%q found=%d\n%s", a.Match, len(keys), strings.Join(keys, "\n")),
				Raw:  keys,
			}, nil
		},
	}.Build()
}

// newRedisInspectKeyTool reports TYPE + TTL + MEMORY USAGE for a single key.
func newRedisInspectKeyTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": redisEndpointSchema,
			"key":  map[string]any{"type": "string", "description": "Redis key to inspect."},
		},
		"required": []string{"key"},
	})
	return tools.Spec[args]{
		Name:        "db.redis_inspect_key",
		Description: "Return TYPE, TTL (seconds), and MEMORY USAGE (bytes) for a single Redis key.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Key == "" {
				return tools.Observation{}, fmt.Errorf("db.redis_inspect_key: key is required")
			}
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			t, err := c.Type(ctx, a.Key).Result()
			if err != nil {
				return tools.Observation{}, fmt.Errorf("type: %w", err)
			}
			ttl, err := c.TTL(ctx, a.Key).Result()
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ttl: %w", err)
			}
			mu, err := c.MemoryUsage(ctx, a.Key).Result()
			if err != nil && err != redis.Nil {
				return tools.Observation{}, fmt.Errorf("memory usage: %w", err)
			}
			out := map[string]any{
				"key":          a.Key,
				"type":         t,
				"ttl_seconds":  int64(ttl.Seconds()),
				"memory_bytes": mu,
			}
			text := fmt.Sprintf("key=%s type=%s ttl=%s memory=%d bytes", a.Key, t, ttl, mu)
			return tools.Observation{Text: text, Raw: out}, nil
		},
	}.Build()
}

// newRedisSlowlogTool returns the most recent slowlog entries.
func newRedisSlowlogTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  redisEndpointSchema,
			"count": map[string]any{"type": "integer", "description": "Number of slowlog entries to fetch (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
	})
	return tools.Spec[args]{
		Name:        "db.redis_slowlog",
		Description: "Return the N most recent Redis slowlog entries (SLOWLOG GET n).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Count <= 0 {
				a.Count = 20
			}
			if a.Count > 200 {
				a.Count = 200
			}
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			entries, err := c.SlowLogGet(ctx, a.Count).Result()
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.redis_slowlog: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"ID", "TIMESTAMP", "DURATION_US", "COMMAND"},
				Aligns:  []render.Align{render.AlignRight, render.AlignLeft, render.AlignRight, render.AlignLeft},
			}
			for _, e := range entries {
				tbl.Rows = append(tbl.Rows, []string{
					fmt.Sprintf("%d", e.ID),
					e.Time.Format(time.RFC3339),
					fmt.Sprintf("%d", e.Duration.Microseconds()),
					strings.Join(e.Args, " "),
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("redis slowlog get %d (%d entries)", a.Count, len(entries)),
				Table: tbl,
				Raw:   entries,
			}, nil
		},
	}.Build()
}

// newRedisClientListTool returns currently-connected clients.
func newRedisClientListTool(clients map[string]*redis.Client) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	schema := mustJSON(map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": redisEndpointSchema},
	})
	return tools.Spec[args]{
		Name:        "db.redis_client_list",
		Description: "Return the connected clients (CLIENT LIST): id, addr, age, idle, db, last cmd.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickRedis(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, redisCallTimeout)
			defer cancel()
			out, err := c.ClientList(ctx).Result()
			if err != nil {
				return tools.Observation{}, fmt.Errorf("db.redis_client_list: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

var mustJSON = tools.MustJSON
