package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// isoToUnix parses an ISO8601/RFC3339 timestamp and returns Unix time in the
// requested unit ("s" or "ms"). CloudWatch Logs is annoyingly split: Logs
// Insights start-query wants seconds, filter-log-events wants milliseconds.
func isoToUnix(field, s, unit string) (int64, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("cloud: %s %q must be RFC3339 (e.g. 2026-05-29T00:00:00Z): %w", field, s, err)
	}
	if unit == "ms" {
		return t.UnixMilli(), nil
	}
	return t.Unix(), nil
}

// newAWSLogTools builds the AWS CloudWatch Logs read-only tools.
func newAWSLogTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSDescribeLogGroupsTool(accts),
		newAWSFilterLogEventsTool(accts),
		newAWSLogsInsightsQueryTool(accts),
	}
}

// newAWSDescribeLogGroupsTool wraps `aws logs describe-log-groups` — discovery
// of which log groups exist before querying them.
func newAWSDescribeLogGroupsTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account    string `json:"account"`
		NamePrefix string `json:"name_prefix"`
		Limit      int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":     awsAccountSchema,
			"name_prefix": map[string]any{"type": "string", "description": "Only return log groups whose name begins with this prefix (optional)."},
			"limit":       map[string]any{"type": "integer", "description": "Max log groups to return (default 50, max 50).", "default": 50, "minimum": 1, "maximum": 50},
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_logs_describe_groups",
		Description: "List CloudWatch log groups in an AWS account (read-only `aws logs describe-log-groups`). Use to discover log group names before filtering or querying.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Limit <= 0 || a.Limit > 50 {
				a.Limit = 50
			}
			cmd := append([]string{"logs", "describe-log-groups"}, acct.baseArgs()...)
			cmd = append(cmd, "--limit", strconv.Itoa(a.Limit))
			if a.NamePrefix != "" {
				if err := safeArg("name_prefix", a.NamePrefix); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--log-group-name-prefix", a.NamePrefix)
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_describe_groups: %w", err)
			}
			var parsed struct {
				LogGroups []struct {
					LogGroupName    string `json:"logGroupName"`
					StoredBytes     int64  `json:"storedBytes"`
					RetentionInDays int    `json:"retentionInDays"`
				} `json:"logGroups"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_describe_groups: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"LOG GROUP", "STORED BYTES", "RETENTION(d)"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
			}
			for _, g := range parsed.LogGroups {
				ret := ""
				if g.RetentionInDays > 0 {
					ret = strconv.Itoa(g.RetentionInDays)
				}
				tbl.Rows = append(tbl.Rows, []string{g.LogGroupName, strconv.FormatInt(g.StoredBytes, 10), ret})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d log group(s) in account %q.", len(parsed.LogGroups), acct.name),
				Table: tbl,
				Raw:   parsed.LogGroups,
			}, nil
		},
	}.Build()
}

// newAWSFilterLogEventsTool wraps `aws logs filter-log-events` — the simple,
// synchronous workhorse for pulling matching log lines from one group.
func newAWSFilterLogEventsTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account       string `json:"account"`
		LogGroupName  string `json:"log_group_name"`
		FilterPattern string `json:"filter_pattern"`
		StartTime     string `json:"start_time"`
		EndTime       string `json:"end_time"`
		Limit         int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":        awsAccountSchema,
			"log_group_name": map[string]any{"type": "string", "description": "CloudWatch log group name, e.g. \"/aws/lambda/my-fn\"."},
			"filter_pattern": map[string]any{"type": "string", "description": "CloudWatch Logs filter pattern, e.g. \"ERROR\" or \"{ $.level = \\\"error\\\" }\" (optional)."},
			"start_time":     map[string]any{"type": "string", "description": "RFC3339 start time (optional)."},
			"end_time":       map[string]any{"type": "string", "description": "RFC3339 end time (optional)."},
			"limit":          map[string]any{"type": "integer", "description": "Max events to return (default 100, max 1000).", "default": 100, "minimum": 1, "maximum": 1000},
		},
		"required": []any{"log_group_name"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_logs_filter_events",
		Description: "Fetch matching log events from one CloudWatch log group (read-only `aws logs filter-log-events`). Provide log_group_name and an optional filter_pattern + time window.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.LogGroupName == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_filter_events: log_group_name is required")
			}
			if err := safeArg("log_group_name", a.LogGroupName); err != nil {
				return tools.Observation{}, err
			}
			if a.Limit <= 0 || a.Limit > 1000 {
				a.Limit = 100
			}
			cmd := append([]string{"logs", "filter-log-events"}, acct.baseArgs()...)
			cmd = append(cmd, "--log-group-name", a.LogGroupName, "--limit", strconv.Itoa(a.Limit))
			if a.FilterPattern != "" {
				// filter_pattern may legitimately start with "{"; it is passed as a
				// single argv value so no shell parsing applies. Do not safeArg it
				// (patterns can contain characters safeArg would reject is fine —
				// only leading "-" matters, which a pattern never starts with).
				cmd = append(cmd, "--filter-pattern", a.FilterPattern)
			}
			if a.StartTime != "" {
				ms, err := isoToUnix("start_time", a.StartTime, "ms")
				if err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--start-time", strconv.FormatInt(ms, 10))
			}
			if a.EndTime != "" {
				ms, err := isoToUnix("end_time", a.EndTime, "ms")
				if err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--end-time", strconv.FormatInt(ms, 10))
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_filter_events: %w", err)
			}
			var parsed struct {
				Events []struct {
					Timestamp     int64  `json:"timestamp"`
					Message       string `json:"message"`
					LogStreamName string `json:"logStreamName"`
				} `json:"events"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_filter_events: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"TIME", "STREAM", "MESSAGE"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, e := range parsed.Events {
				ts := time.UnixMilli(e.Timestamp).UTC().Format(time.RFC3339)
				tbl.Rows = append(tbl.Rows, []string{ts, e.LogStreamName, e.Message})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d event(s) from %q in account %q.", len(parsed.Events), a.LogGroupName, acct.name),
				Table: tbl,
				Raw:   parsed.Events,
			}, nil
		},
	}.Build()
}

// insightsPollInterval and insightsMaxPolls bound the Logs Insights wait. The
// agent-loop ctx deadline is the hard cap; these keep a fast query from being
// throttled and a slow one from spinning forever.
var (
	insightsPollInterval = 1 * time.Second
	insightsMaxPolls     = 30
)

// newAWSLogsInsightsQueryTool wraps the two-step Logs Insights flow
// (`aws logs start-query` then poll `aws logs get-query-results`) behind one
// synchronous tool call. Logs Insights is the powerful query path; the agent
// supplies a CloudWatch Logs Insights query string.
func newAWSLogsInsightsQueryTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account       string   `json:"account"`
		LogGroupNames []string `json:"log_group_names"`
		QueryString   string   `json:"query_string"`
		StartTime     string   `json:"start_time"`
		EndTime       string   `json:"end_time"`
		Limit         int      `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":         awsAccountSchema,
			"log_group_names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "One or more log group names to query."},
			"query_string":    map[string]any{"type": "string", "description": "CloudWatch Logs Insights query, e.g. \"fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc\"."},
			"start_time":      map[string]any{"type": "string", "description": "RFC3339 start time."},
			"end_time":        map[string]any{"type": "string", "description": "RFC3339 end time."},
			"limit":           map[string]any{"type": "integer", "description": "Max result rows (default 100, max 1000).", "default": 100, "minimum": 1, "maximum": 1000},
		},
		"required": []any{"log_group_names", "query_string", "start_time", "end_time"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_logs_insights_query",
		Description: "Run a CloudWatch Logs Insights query and wait for the result (read-only `aws logs start-query` + `get-query-results`). Provide log_group_names, an Insights query_string, and a time window.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if len(a.LogGroupNames) == 0 || a.QueryString == "" || a.StartTime == "" || a.EndTime == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: log_group_names, query_string, start_time, end_time are required")
			}
			if a.Limit <= 0 || a.Limit > 1000 {
				a.Limit = 100
			}
			startSec, err := isoToUnix("start_time", a.StartTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			endSec, err := isoToUnix("end_time", a.EndTime, "s")
			if err != nil {
				return tools.Observation{}, err
			}
			startCmd := append([]string{"logs", "start-query"}, acct.baseArgs()...)
			startCmd = append(startCmd,
				"--start-time", strconv.FormatInt(startSec, 10),
				"--end-time", strconv.FormatInt(endSec, 10),
				"--query-string", a.QueryString,
				"--limit", strconv.Itoa(a.Limit),
				"--log-group-names",
			)
			for _, g := range a.LogGroupNames {
				if err := safeArg("log_group_names", g); err != nil {
					return tools.Observation{}, err
				}
				startCmd = append(startCmd, g)
			}
			startOut, err := CloudExec(ctx, "aws", startCmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: start: %w", err)
			}
			var started struct {
				QueryID string `json:"queryId"`
			}
			if err := json.Unmarshal(startOut, &started); err != nil || started.QueryID == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: could not read queryId from start-query output")
			}

			// Poll get-query-results until the query leaves the Running/Scheduled
			// state or we exhaust the bounded budget.
			for i := 0; i < insightsMaxPolls; i++ {
				select {
				case <-ctx.Done():
					return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: %w", ctx.Err())
				case <-time.After(insightsPollInterval):
				}
				resCmd := append([]string{"logs", "get-query-results"}, acct.baseArgs()...)
				resCmd = append(resCmd, "--query-id", started.QueryID)
				resOut, err := CloudExec(ctx, "aws", resCmd)
				if err != nil {
					return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: results: %w", err)
				}
				var res struct {
					Status  string             `json:"status"`
					Results [][]map[string]any `json:"results"`
				}
				if err := json.Unmarshal(resOut, &res); err != nil {
					return tools.Observation{}, fmt.Errorf("cloud.aws_logs_insights_query: decode results: %w", err)
				}
				switch res.Status {
				case "Running", "Scheduled", "Unknown", "":
					continue
				case "Complete":
					return tools.Observation{
						Text: fmt.Sprintf("Logs Insights query %s: %d row(s) from account %q.", started.QueryID, len(res.Results), acct.name),
						Raw:  res.Results,
					}, nil
				default: // Failed, Cancelled, Timeout
					return tools.Observation{
						Text: fmt.Sprintf("Logs Insights query %s ended with status %q.", started.QueryID, res.Status),
						Raw:  res,
					}, nil
				}
			}
			return tools.Observation{
				Text: fmt.Sprintf("Logs Insights query %s still running after %d polls; try a narrower time window.", started.QueryID, insightsMaxPolls),
			}, nil
		},
	}.Build()
}
