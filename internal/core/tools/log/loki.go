// Package log provides read-only log-search tools wrapping Loki and
// Elasticsearch. Every backend is accessed over HTTP through
// httpapi.Client, so the transport-layer GET-only contract applies.
package log

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// LokiClient wraps an httpapi.Client with the Loki query path layout.
type LokiClient struct {
	*httpapi.Client
}

// pickLoki selects an endpoint by name (or the sole endpoint when unambiguous).
func pickLoki(m map[string]*LokiClient, name string) (*LokiClient, error) {
	return tools.PickEndpoint(m, name, "log", "loki endpoint")
}

var lokiEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the loki endpoint configured under logs. Optional if exactly one is configured.",
}

// newLokiQueryRangeTool wraps GET /loki/api/v1/query_range. The query argument
// is LogQL; cloudy does not parse it but constrains the time window and the
// per-tool result limit so an unbounded query cannot stall the agent.
func newLokiQueryRangeTool(clients map[string]*LokiClient) tools.Tool {
	type args struct {
		Name      string `json:"name"`
		Query     string `json:"query"`
		StartUnix int64  `json:"start_unix"`
		EndUnix   int64  `json:"end_unix"`
		Limit     int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       lokiEndpointSchema,
			"query":      map[string]any{"type": "string", "description": "LogQL query, e.g. '{app=\"api\"} |= \"error\"'."},
			"start_unix": map[string]any{"type": "integer", "description": "Range start (Unix seconds). Default = end - 5m."},
			"end_unix":   map[string]any{"type": "integer", "description": "Range end (Unix seconds). Default = now."},
			"limit":      map[string]any{"type": "integer", "description": "Max log lines (default 100, max 5000).", "default": 100, "minimum": 1, "maximum": 5000},
		},
		"required": []string{"query"},
	})
	return tools.Spec[args]{
		Name:        "log.loki_query_range",
		Description: "Run a Loki LogQL range query and return matched log lines. Time defaults to the last 5 minutes.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if strings.TrimSpace(a.Query) == "" {
				return tools.Observation{}, fmt.Errorf("log.loki_query_range: query is required")
			}
			if a.Limit <= 0 {
				a.Limit = 100
			}
			if a.Limit > 5000 {
				a.Limit = 5000
			}
			end := a.EndUnix
			if end == 0 {
				end = time.Now().Unix()
			}
			start := a.StartUnix
			if start == 0 {
				start = end - 300
			}
			c, err := pickLoki(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{
				"query": {a.Query},
				"start": {strconv.FormatInt(start*int64(time.Second), 10)},
				"end":   {strconv.FormatInt(end*int64(time.Second), 10)},
				"limit": {strconv.Itoa(a.Limit)},
			}
			body, err := c.RawGet(ctx, "/loki/api/v1/query_range", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_query_range: %w", err)
			}
			lines, raw, perr := parseLokiStreams(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_query_range: decode: %w", perr)
			}
			text := strings.Join(lines, "\n")
			if text == "" {
				text = "(no matching log lines)"
			}
			return tools.Observation{Text: text, Raw: raw}, nil
		},
	}.Build()
}

// parseLokiStreams flattens the {data: {result: [{stream, values: [[ts, line]]}]}}
// envelope into a list of pre-formatted "ts  labels  line" strings.
func parseLokiStreams(body []byte) ([]string, any, error) {
	var env struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, err
	}
	var lines []string
	for _, s := range env.Data.Result {
		lbl := formatLabels(s.Stream)
		for _, v := range s.Values {
			ts := v[0]
			if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
				ts = time.Unix(0, n).Format(time.RFC3339Nano)
			}
			lines = append(lines, fmt.Sprintf("%s  %s  %s", ts, lbl, v[1]))
		}
	}
	return lines, env, nil
}

// formatLabels renders a label set as {k1="v1",k2="v2",…}. Keys are sorted
// so identical streams always render the same way — without sort, Go's
// randomised map iteration produces non-stable output that defeats grep/diff
// over agent transcripts.
func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, m[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// newLokiLabelsTool wraps GET /loki/api/v1/labels.
func newLokiLabelsTool(clients map[string]*LokiClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "log.loki_labels",
		Description: "Return the set of label names available in Loki.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": lokiEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickLoki(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/loki/api/v1/labels", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_labels: %w", err)
			}
			var env struct {
				Data []string `json:"data"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_labels: decode: %w", err)
			}
			return tools.Observation{
				Text: strings.Join(env.Data, "\n"),
				Raw:  env.Data,
			}, nil
		},
	}.Build()
}

// newLokiLabelValuesTool wraps GET /loki/api/v1/label/{label}/values.
func newLokiLabelValuesTool(clients map[string]*LokiClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  lokiEndpointSchema,
			"label": map[string]any{"type": "string", "description": "Label name to enumerate values for, e.g. app."},
		},
		"required": []string{"label"},
	})
	return tools.Spec[args]{
		Name:        "log.loki_label_values",
		Description: "Return the values seen for a Loki label.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Label == "" {
				return tools.Observation{}, fmt.Errorf("log.loki_label_values: label is required")
			}
			c, err := pickLoki(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			path := "/loki/api/v1/label/" + url.PathEscape(a.Label) + "/values"
			body, err := c.RawGet(ctx, path, nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_label_values: %w", err)
			}
			var env struct {
				Data []string `json:"data"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_label_values: decode: %w", err)
			}
			tbl := &render.Table{Headers: []string{"VALUE"}}
			for _, v := range env.Data {
				tbl.Rows = append(tbl.Rows, []string{v})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d values for label %s", len(env.Data), a.Label),
				Table: tbl,
				Raw:   env.Data,
			}, nil
		},
	}.Build()
}

// newLokiSeriesTool wraps GET /loki/api/v1/series.
func newLokiSeriesTool(clients map[string]*LokiClient) tools.Tool {
	type args struct {
		Name      string `json:"name"`
		Match     string `json:"match"`
		StartUnix int64  `json:"start_unix"`
		EndUnix   int64  `json:"end_unix"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       lokiEndpointSchema,
			"match":      map[string]any{"type": "string", "description": "Series selector, e.g. {app=\"api\"}."},
			"start_unix": map[string]any{"type": "integer", "description": "Range start (Unix seconds). Default = end - 1h."},
			"end_unix":   map[string]any{"type": "integer", "description": "Range end (Unix seconds). Default = now."},
		},
		"required": []string{"match"},
	})
	return tools.Spec[args]{
		Name:        "log.loki_series",
		Description: "List Loki series matching a selector within a time range.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Match == "" {
				return tools.Observation{}, fmt.Errorf("log.loki_series: match is required")
			}
			end := a.EndUnix
			if end == 0 {
				end = time.Now().Unix()
			}
			start := a.StartUnix
			if start == 0 {
				start = end - 3600
			}
			c, err := pickLoki(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{
				"match[]": {a.Match},
				"start":   {strconv.FormatInt(start*int64(time.Second), 10)},
				"end":     {strconv.FormatInt(end*int64(time.Second), 10)},
			}
			body, err := c.RawGet(ctx, "/loki/api/v1/series", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_series: %w", err)
			}
			var env struct {
				Data []map[string]string `json:"data"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				return tools.Observation{}, fmt.Errorf("log.loki_series: decode: %w", err)
			}
			lines := make([]string, len(env.Data))
			for i, m := range env.Data {
				lines[i] = formatLabels(m)
			}
			return tools.Observation{
				Text: strings.Join(lines, "\n"),
				Raw:  env.Data,
			}, nil
		},
	}.Build()
}

var mustJSON = tools.MustJSON
