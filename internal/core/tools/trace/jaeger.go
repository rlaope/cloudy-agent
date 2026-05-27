package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// JaegerClient wraps an httpapi.Client with the Jaeger query-API layout.
type JaegerClient struct {
	*httpapi.Client
}

func pickJaeger(m map[string]*JaegerClient, name string) (*JaegerClient, error) {
	return tools.PickEndpoint(m, name, "trace", "jaeger endpoint")
}

var jaegerEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the jaeger endpoint configured under tracing. Optional if exactly one is configured.",
}

func newJaegerServicesTool(clients map[string]*JaegerClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "trace.jaeger_services",
		Description: "Return the list of service names known to Jaeger (GET /api/services).",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": jaegerEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickJaeger(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/services", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_services: %w", err)
			}
			var env struct {
				Data []string `json:"data"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_services: decode: %w", err)
			}
			return tools.Observation{
				Text: strings.Join(env.Data, "\n"),
				Raw:  env.Data,
			}, nil
		},
	}.Build()
}

func newJaegerOperationsTool(clients map[string]*JaegerClient) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		Service string `json:"service"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    jaegerEndpointSchema,
			"service": map[string]any{"type": "string", "description": "Service name."},
		},
		"required": []string{"service"},
	})
	return tools.Spec[args]{
		Name:        "trace.jaeger_operations",
		Description: "List the operation names recorded for a Jaeger service.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Service == "" {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_operations: service is required")
			}
			c, err := pickJaeger(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/services/"+url.PathEscape(a.Service)+"/operations", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_operations: %w", err)
			}
			var env struct {
				Data []string `json:"data"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_operations: decode: %w", err)
			}
			return tools.Observation{
				Text: strings.Join(env.Data, "\n"),
				Raw:  env.Data,
			}, nil
		},
	}.Build()
}

func newJaegerSearchTracesTool(clients map[string]*JaegerClient) tools.Tool {
	type args struct {
		Name        string `json:"name"`
		Service     string `json:"service"`
		Operation   string `json:"operation"`
		Tags        string `json:"tags"`
		MinDuration string `json:"min_duration"`
		StartUnix   int64  `json:"start_unix"`
		EndUnix     int64  `json:"end_unix"`
		Limit       int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":         jaegerEndpointSchema,
			"service":      map[string]any{"type": "string", "description": "Service name (required)."},
			"operation":    map[string]any{"type": "string", "description": "Operation name (optional)."},
			"tags":         map[string]any{"type": "string", "description": `Tag filter as JSON object, e.g. '{"error":"true"}'.`},
			"min_duration": map[string]any{"type": "string", "description": "Minimum span duration, e.g. '200ms', '2s'."},
			"start_unix":   map[string]any{"type": "integer", "description": "Range start (Unix seconds). Default = end - 1h."},
			"end_unix":     map[string]any{"type": "integer", "description": "Range end (Unix seconds). Default = now."},
			"limit":        map[string]any{"type": "integer", "description": "Max traces (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
		"required": []string{"service"},
	})
	return tools.Spec[args]{
		Name:        "trace.jaeger_search_traces",
		Description: "Search Jaeger for traces by service + optional operation/tags/duration within a time range.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Service == "" {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_search_traces: service is required")
			}
			if a.Limit <= 0 {
				a.Limit = 20
			}
			if a.Limit > 200 {
				a.Limit = 200
			}
			end := a.EndUnix
			if end == 0 {
				end = time.Now().Unix()
			}
			start := a.StartUnix
			if start == 0 {
				start = end - 3600
			}
			c, err := pickJaeger(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{
				"service": {a.Service},
				// Jaeger expects microseconds for start/end.
				"start": {strconv.FormatInt(start*1_000_000, 10)},
				"end":   {strconv.FormatInt(end*1_000_000, 10)},
				"limit": {strconv.Itoa(a.Limit)},
			}
			if a.Operation != "" {
				params.Set("operation", a.Operation)
			}
			if a.Tags != "" {
				params.Set("tags", a.Tags)
			}
			if a.MinDuration != "" {
				params.Set("minDuration", a.MinDuration)
			}
			body, err := c.RawGet(ctx, "/api/traces", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_search_traces: %w", err)
			}
			text, tbl, raw, perr := parseJaegerSearch(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("trace.jaeger_search_traces: decode: %w", perr)
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}

// parseJaegerSearch produces a one-line summary per matched trace
// (traceID, span count, duration µs, root service/operation).
func parseJaegerSearch(body []byte) (string, *render.Table, any, error) {
	var env struct {
		Data []struct {
			TraceID string `json:"traceID"`
			Spans   []struct {
				OperationName string `json:"operationName"`
				Duration      int64  `json:"duration"`
				ProcessID     string `json:"processID"`
				References    []struct {
					RefType string `json:"refType"`
				} `json:"references"`
			} `json:"spans"`
			Processes map[string]struct {
				ServiceName string `json:"serviceName"`
			} `json:"processes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, nil, err
	}
	tbl := &render.Table{Headers: []string{"TRACE_ID", "SPANS", "ROOT_SERVICE", "ROOT_OP", "DUR_MS"}}
	for _, t := range env.Data {
		rootService, rootOp, rootDurMs := "", "", int64(0)
		for _, sp := range t.Spans {
			isRoot := true
			for _, r := range sp.References {
				if r.RefType != "" {
					isRoot = false
					break
				}
			}
			if isRoot {
				if p, ok := t.Processes[sp.ProcessID]; ok {
					rootService = p.ServiceName
				}
				rootOp = sp.OperationName
				rootDurMs = sp.Duration / 1000
				break
			}
		}
		tbl.Rows = append(tbl.Rows, []string{
			t.TraceID,
			strconv.Itoa(len(t.Spans)),
			rootService,
			rootOp,
			strconv.FormatInt(rootDurMs, 10),
		})
	}
	return fmt.Sprintf("%d traces", len(env.Data)), tbl, env, nil
}
