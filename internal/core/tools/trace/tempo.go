// Package trace provides read-only distributed-tracing tools wrapping Tempo
// and Jaeger. All access is over HTTP through httpapi.Client, so the
// transport-layer GET-only contract applies.
package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// TempoClient wraps an httpapi.Client with the Tempo endpoint layout.
type TempoClient struct {
	*httpapi.Client
}

func pickTempo(m map[string]*TempoClient, name string) (*TempoClient, error) {
	return tools.PickEndpoint(m, name, "trace", "tempo endpoint")
}

var tempoEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the tempo endpoint configured under tracing. Optional if exactly one is configured.",
}

// newTempoGetTraceTool wraps GET /api/traces/{traceID}.
func newTempoGetTraceTool(clients map[string]*TempoClient) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		TraceID string `json:"trace_id"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     tempoEndpointSchema,
			"trace_id": map[string]any{"type": "string", "description": "Trace ID (hex)."},
		},
		"required": []string{"trace_id"},
	})
	return tools.Spec[args]{
		Name:        "trace.tempo_get_trace",
		Description: "Fetch a single trace from Tempo by trace ID. Returns the full span tree as JSON.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.TraceID == "" {
				return tools.Observation{}, fmt.Errorf("trace.tempo_get_trace: trace_id is required")
			}
			c, err := pickTempo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/traces/"+url.PathEscape(a.TraceID), nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.tempo_get_trace: %w", err)
			}
			return tools.Observation{Text: string(body), Raw: json.RawMessage(body)}, nil
		},
	}.Build()
}

// newTempoSearchTool wraps GET /api/search using TraceQL or tag filters.
func newTempoSearchTool(clients map[string]*TempoClient) tools.Tool {
	type args struct {
		Name      string `json:"name"`
		Query     string `json:"query"`
		Tags      string `json:"tags"`
		StartUnix int64  `json:"start_unix"`
		EndUnix   int64  `json:"end_unix"`
		Limit     int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       tempoEndpointSchema,
			"query":      map[string]any{"type": "string", "description": "TraceQL query, e.g. '{ resource.service.name=\"api\" && status=error }'."},
			"tags":       map[string]any{"type": "string", "description": "Logfmt tag filter, e.g. 'service.name=api error=true'. Used when query is empty."},
			"start_unix": map[string]any{"type": "integer", "description": "Range start (Unix seconds). Default = end - 1h."},
			"end_unix":   map[string]any{"type": "integer", "description": "Range end (Unix seconds). Default = now."},
			"limit":      map[string]any{"type": "integer", "description": "Max traces (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
	})
	return tools.Spec[args]{
		Name:        "trace.tempo_search",
		Description: "Search Tempo for traces matching a TraceQL query or tag filter within a time range.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Query == "" && a.Tags == "" {
				return tools.Observation{}, fmt.Errorf("trace.tempo_search: one of query or tags is required")
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
			c, err := pickTempo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{
				"start": {strconv.FormatInt(start, 10)},
				"end":   {strconv.FormatInt(end, 10)},
				"limit": {strconv.Itoa(a.Limit)},
			}
			if a.Query != "" {
				params.Set("q", a.Query)
			}
			if a.Tags != "" {
				params.Set("tags", a.Tags)
			}
			body, err := c.RawGet(ctx, "/api/search", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.tempo_search: %w", err)
			}
			return tools.Observation{Text: string(body), Raw: json.RawMessage(body)}, nil
		},
	}.Build()
}

// TempoTraceSummary is the minimal, read-only view of a single Tempo trace
// needed by correlation: when it started, how long it ran, and the root
// service/operation that produced it. Tempo's /api/search summary already
// carries startTimeUnixNano + durationMs per matched trace, so folding trace
// symptoms onto a change timeline needs no full-trace OTLP fetch (the v2 doc's
// pessimistic estimate); the search summary is enough.
type TempoTraceSummary struct {
	StartTime   time.Time
	Duration    time.Duration
	RootService string
	RootName    string
}

// SearchTraces queries Tempo's /api/search with a TraceQL query over
// [start, end] and returns one TempoTraceSummary per matched trace. limit caps
// matched traces. This is a search read only — it mutates nothing.
func (c *TempoClient) SearchTraces(ctx context.Context, traceQL string, start, end time.Time, limit int) ([]TempoTraceSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	params := url.Values{
		"q":     {traceQL},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"limit": {strconv.Itoa(limit)},
	}
	body, err := c.RawGet(ctx, "/api/search", params)
	if err != nil {
		return nil, fmt.Errorf("trace.tempo search: %w", err)
	}
	return parseTempoSearch(body)
}

// parseTempoSearch decodes Tempo's /api/search envelope into trace summaries,
// reading startTimeUnixNano (string, nanoseconds since epoch) and durationMs
// (milliseconds). Traces whose start time cannot be parsed are skipped.
func parseTempoSearch(body []byte) ([]TempoTraceSummary, error) {
	var env struct {
		Traces []struct {
			RootServiceName   string `json:"rootServiceName"`
			RootTraceName     string `json:"rootTraceName"`
			StartTimeUnixNano string `json:"startTimeUnixNano"`
			DurationMs        int64  `json:"durationMs"`
		} `json:"traces"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]TempoTraceSummary, 0, len(env.Traces))
	for _, t := range env.Traces {
		ns, err := strconv.ParseInt(t.StartTimeUnixNano, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, TempoTraceSummary{
			StartTime:   time.Unix(0, ns),
			Duration:    time.Duration(t.DurationMs) * time.Millisecond,
			RootService: t.RootServiceName,
			RootName:    t.RootTraceName,
		})
	}
	return out, nil
}

var mustJSON = tools.MustJSON
