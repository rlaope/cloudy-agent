package log

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// ESClient wraps an httpapi.Client with the Elasticsearch endpoint layout.
type ESClient struct {
	*httpapi.Client
}

func pickES(m map[string]*ESClient, name string) (*ESClient, error) {
	if name == "" {
		if len(m) == 1 {
			for _, c := range m {
				return c, nil
			}
		}
		return nil, fmt.Errorf("log: elasticsearch endpoint name required (configured: %s)", strings.Join(keys(m), ", "))
	}
	c, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("log: unknown elasticsearch endpoint %q (configured: %s)", name, strings.Join(keys(m), ", "))
	}
	return c, nil
}

var esEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the elasticsearch endpoint configured under logs. Optional if exactly one is configured.",
}

// newESSearchTool wraps GET /<index>/_search using URI query parameters.
// Free-form request bodies are intentionally not supported — only the URI
// search subset is exposed, which keeps the surface read-only at the
// transport level (GET-only) and avoids leaking aggregations / scripted
// fields that can mutate cluster state in some plugin configurations.
func newESSearchTool(clients map[string]*ESClient) tools.Tool {
	type args struct {
		Name   string `json:"name"`
		Index  string `json:"index"`
		Query  string `json:"query"`
		Size   int    `json:"size"`
		Source string `json:"source"`
		Sort   string `json:"sort"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":   esEndpointSchema,
			"index":  map[string]any{"type": "string", "description": "Index or pattern, e.g. logs-* (default _all)."},
			"query":  map[string]any{"type": "string", "description": "Query-string DSL (URI search), e.g. 'level:ERROR AND service:api'."},
			"size":   map[string]any{"type": "integer", "description": "Max documents to return (default 25, max 500).", "default": 25, "minimum": 1, "maximum": 500},
			"source": map[string]any{"type": "string", "description": "Comma-separated _source fields, e.g. '@timestamp,message,level'. Empty = all."},
			"sort":   map[string]any{"type": "string", "description": "Sort spec, e.g. '@timestamp:desc'."},
		},
		"required": []string{"query"},
	})
	return tools.Spec[args]{
		Name:        "log.es_search",
		Description: "Run an Elasticsearch URI search and return top-N hits. Uses the Query-String DSL (no request body).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if strings.TrimSpace(a.Query) == "" {
				return tools.Observation{}, fmt.Errorf("log.es_search: query is required")
			}
			if a.Size <= 0 {
				a.Size = 25
			}
			if a.Size > 500 {
				a.Size = 500
			}
			idx := a.Index
			if idx == "" {
				idx = "_all"
			}
			c, err := pickES(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{
				"q":    {a.Query},
				"size": {strconv.Itoa(a.Size)},
			}
			if a.Source != "" {
				params.Set("_source", a.Source)
			}
			if a.Sort != "" {
				params.Set("sort", a.Sort)
			}
			path := "/" + url.PathEscape(idx) + "/_search"
			body, err := c.RawGet(ctx, path, params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.es_search: %w", err)
			}
			text, raw, perr := parseESHits(body, a.Size)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("log.es_search: decode: %w", perr)
			}
			return tools.Observation{Text: text, Raw: raw}, nil
		},
	}.Build()
}

func parseESHits(body []byte, _ int) (string, any, error) {
	var env struct {
		Took int `json:"took"`
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
			Hits []struct {
				Index  string          `json:"_index"`
				ID     string          `json:"_id"`
				Score  float64         `json:"_score"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "total=%d took=%dms\n", env.Hits.Total.Value, env.Took)
	for _, h := range env.Hits.Hits {
		fmt.Fprintf(&b, "[%s/%s] %s\n", h.Index, h.ID, string(h.Source))
	}
	return b.String(), env, nil
}

func newESIndicesTool(clients map[string]*ESClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "log.es_indices",
		Description: "List indices via /_cat/indices?format=json with health, primary/replica shard counts, doc count, store size.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": esEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickES(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/_cat/indices", url.Values{"format": {"json"}})
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.es_indices: %w", err)
			}
			var arr []map[string]any
			if err := json.Unmarshal(body, &arr); err != nil {
				return tools.Observation{}, fmt.Errorf("log.es_indices: decode: %w", err)
			}
			tbl := &render.Table{Headers: []string{"HEALTH", "STATUS", "INDEX", "DOCS_COUNT", "STORE_SIZE", "PRI", "REP"}}
			for _, m := range arr {
				tbl.Rows = append(tbl.Rows, []string{
					asString(m["health"]),
					asString(m["status"]),
					asString(m["index"]),
					asString(m["docs.count"]),
					asString(m["store.size"]),
					asString(m["pri"]),
					asString(m["rep"]),
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d indices", len(arr)),
				Table: tbl,
				Raw:   arr,
			}, nil
		},
	}.Build()
}

func newESClusterHealthTool(clients map[string]*ESClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "log.es_cluster_health",
		Description: "GET /_cluster/health — cluster name, status (green/yellow/red), shard counts, pending tasks.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": esEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickES(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/_cluster/health", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.es_cluster_health: %w", err)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				return tools.Observation{}, fmt.Errorf("log.es_cluster_health: decode: %w", err)
			}
			return tools.Observation{
				Text: fmt.Sprintf("cluster=%v status=%v nodes=%v active_shards=%v",
					m["cluster_name"], m["status"], m["number_of_nodes"], m["active_shards"]),
				Raw: m,
			}, nil
		},
	}.Build()
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
