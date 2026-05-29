package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newAWSTraceTools builds the AWS X-Ray read-only trace tools.
func newAWSTraceTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSXRayTraceSummariesTool(accts),
		newAWSXRayBatchGetTracesTool(accts),
		newAWSXRayServiceGraphTool(accts),
	}
}

// xrayFlags renders the boolean error/fault/throttle annotations as a compact
// "E/F/T" string so a trace summary row is scannable at a glance.
func xrayFlags(hasErr, hasFault, hasThrottle bool) string {
	var f []string
	if hasErr {
		f = append(f, "ERROR")
	}
	if hasFault {
		f = append(f, "FAULT")
	}
	if hasThrottle {
		f = append(f, "THROTTLE")
	}
	if len(f) == 0 {
		return "ok"
	}
	return strings.Join(f, "+")
}

// newAWSXRayTraceSummariesTool wraps `aws xray get-trace-summaries` — discovery
// of trace IDs + latency/error annotations in a window. The trace IDs feed
// cloud.aws_xray_batch_get_traces for full segment documents.
func newAWSXRayTraceSummariesTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account          string `json:"account"`
		StartTime        string `json:"start_time"`
		EndTime          string `json:"end_time"`
		FilterExpression string `json:"filter_expression"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":           awsAccountSchema,
			"start_time":        map[string]any{"type": "string", "description": "RFC3339 start time, e.g. \"2026-05-29T00:00:00Z\"."},
			"end_time":          map[string]any{"type": "string", "description": "RFC3339 end time."},
			"filter_expression": map[string]any{"type": "string", "description": "X-Ray filter expression, e.g. \"error\" or \"service(\\\"api\\\")\" (optional)."},
		},
		"required": []any{"start_time", "end_time"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_xray_trace_summaries",
		Description: "List X-Ray trace summaries (IDs, latency, error/fault annotations) for a time window (read-only `aws xray get-trace-summaries`). Feed the returned trace IDs to cloud.aws_xray_batch_get_traces for full segments.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.StartTime == "" || a.EndTime == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_trace_summaries: start_time and end_time are required")
			}
			startSec, err := isoToUnix("start_time", a.StartTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			endSec, err := isoToUnix("end_time", a.EndTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"xray", "get-trace-summaries"}, acct.baseArgs()...)
			cmd = append(cmd,
				"--start-time", strconv.FormatInt(startSec, 10),
				"--end-time", strconv.FormatInt(endSec, 10),
			)
			if a.FilterExpression != "" {
				if err := safeArg("filter_expression", a.FilterExpression); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--filter-expression", a.FilterExpression)
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_trace_summaries: %w", err)
			}
			var parsed struct {
				TraceSummaries []struct {
					ID           string  `json:"Id"`
					Duration     float64 `json:"Duration"`
					ResponseTime float64 `json:"ResponseTime"`
					HasError     bool    `json:"HasError"`
					HasFault     bool    `json:"HasFault"`
					HasThrottle  bool    `json:"HasThrottle"`
					HTTP         struct {
						HTTPStatus int    `json:"HttpStatus"`
						HTTPURL    string `json:"HttpURL"`
					} `json:"Http"`
				} `json:"TraceSummaries"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_trace_summaries: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"TRACE ID", "DURATION(s)", "RESP(s)", "STATUS", "HTTP", "URL"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignLeft, render.AlignRight, render.AlignLeft},
			}
			for _, s := range parsed.TraceSummaries {
				httpStatus := ""
				if s.HTTP.HTTPStatus != 0 {
					httpStatus = strconv.Itoa(s.HTTP.HTTPStatus)
				}
				tbl.Rows = append(tbl.Rows, []string{
					s.ID,
					strconv.FormatFloat(s.Duration, 'f', 3, 64),
					strconv.FormatFloat(s.ResponseTime, 'f', 3, 64),
					xrayFlags(s.HasError, s.HasFault, s.HasThrottle),
					httpStatus,
					s.HTTP.HTTPURL,
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d trace summary(ies) in account %q over [%s, %s].", len(parsed.TraceSummaries), acct.name, a.StartTime, a.EndTime),
				Table: tbl,
				Raw:   parsed.TraceSummaries,
			}, nil
		},
	}.Build()
}

// newAWSXRayBatchGetTracesTool wraps `aws xray batch-get-traces` — full segment
// documents for up to 5 trace IDs surfaced by the summaries tool.
func newAWSXRayBatchGetTracesTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account  string   `json:"account"`
		TraceIDs []string `json:"trace_ids"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":   awsAccountSchema,
			"trace_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Trace IDs to fetch (max 5), e.g. [\"1-581cf771-a006649127e371903a2de979\"]."},
		},
		"required": []any{"trace_ids"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_xray_batch_get_traces",
		Description: "Fetch full X-Ray segment documents for up to 5 trace IDs (read-only `aws xray batch-get-traces`). Use cloud.aws_xray_trace_summaries first to find trace IDs.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if len(a.TraceIDs) == 0 {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_batch_get_traces: trace_ids is required")
			}
			if len(a.TraceIDs) > 5 {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_batch_get_traces: at most 5 trace_ids per call (got %d)", len(a.TraceIDs))
			}
			cmd := append([]string{"xray", "batch-get-traces"}, acct.baseArgs()...)
			cmd = append(cmd, "--trace-ids")
			for _, id := range a.TraceIDs {
				if err := safeArg("trace_ids", id); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, id)
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_batch_get_traces: %w", err)
			}
			var parsed struct {
				Traces []struct {
					ID       string  `json:"Id"`
					Duration float64 `json:"Duration"`
					Segments []struct {
						ID string `json:"Id"`
					} `json:"Segments"`
				} `json:"Traces"`
				UnprocessedTraceIDs []string `json:"UnprocessedTraceIds"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_batch_get_traces: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"TRACE ID", "DURATION(s)", "SEGMENTS"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
			}
			for _, t := range parsed.Traces {
				tbl.Rows = append(tbl.Rows, []string{
					t.ID,
					strconv.FormatFloat(t.Duration, 'f', 3, 64),
					strconv.Itoa(len(t.Segments)),
				})
			}
			text := fmt.Sprintf("%d trace(s) in account %q.", len(parsed.Traces), acct.name)
			if len(parsed.UnprocessedTraceIDs) > 0 {
				text += fmt.Sprintf(" %d unprocessed.", len(parsed.UnprocessedTraceIDs))
			}
			return tools.Observation{Text: text, Table: tbl, Raw: parsed}, nil
		},
	}.Build()
}

// newAWSXRayServiceGraphTool wraps `aws xray get-service-graph` — the
// service-dependency topology plus per-service health for a window, a strong
// correlate input.
func newAWSXRayServiceGraphTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account   string `json:"account"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":    awsAccountSchema,
			"start_time": map[string]any{"type": "string", "description": "RFC3339 start time."},
			"end_time":   map[string]any{"type": "string", "description": "RFC3339 end time."},
		},
		"required": []any{"start_time", "end_time"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_xray_service_graph",
		Description: "Fetch the X-Ray service-dependency graph with per-service health for a window (read-only `aws xray get-service-graph`). Use to see the cloud service topology and which node is erroring/faulting.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.StartTime == "" || a.EndTime == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_service_graph: start_time and end_time are required")
			}
			startSec, err := isoToUnix("start_time", a.StartTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			endSec, err := isoToUnix("end_time", a.EndTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"xray", "get-service-graph"}, acct.baseArgs()...)
			cmd = append(cmd,
				"--start-time", strconv.FormatInt(startSec, 10),
				"--end-time", strconv.FormatInt(endSec, 10),
			)
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_service_graph: %w", err)
			}
			var parsed struct {
				Services []struct {
					Name              string `json:"Name"`
					Type              string `json:"Type"`
					SummaryStatistics struct {
						OkCount         int64 `json:"OkCount"`
						TotalCount      int64 `json:"TotalCount"`
						ErrorStatistics struct {
							TotalCount int64 `json:"TotalCount"`
						} `json:"ErrorStatistics"`
						FaultStatistics struct {
							TotalCount int64 `json:"TotalCount"`
						} `json:"FaultStatistics"`
					} `json:"SummaryStatistics"`
				} `json:"Services"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_xray_service_graph: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"SERVICE", "TYPE", "OK", "ERROR", "FAULT", "TOTAL"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignRight},
			}
			for _, s := range parsed.Services {
				ss := s.SummaryStatistics
				tbl.Rows = append(tbl.Rows, []string{
					s.Name, s.Type,
					strconv.FormatInt(ss.OkCount, 10),
					strconv.FormatInt(ss.ErrorStatistics.TotalCount, 10),
					strconv.FormatInt(ss.FaultStatistics.TotalCount, 10),
					strconv.FormatInt(ss.TotalCount, 10),
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d service(s) in the X-Ray graph for account %q.", len(parsed.Services), acct.name),
				Table: tbl,
				Raw:   parsed.Services,
			}, nil
		},
	}.Build()
}
