package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newAWSCostTools builds the AWS read-only FinOps / cost tools.
func newAWSCostTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSCostAndUsageTool(accts),
	}
}

// newAWSCostAndUsageTool wraps `aws ce get-cost-and-usage` — the Cost Explorer
// read that drives cost-anomaly inquiry. The agent supplies a date window,
// granularity, metric(s), and an optional group-by dimension (e.g. SERVICE).
func newAWSCostAndUsageTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account     string   `json:"account"`
		Start       string   `json:"start"`
		End         string   `json:"end"`
		Granularity string   `json:"granularity"`
		Metrics     []string `json:"metrics"`
		GroupBy     string   `json:"group_by"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":     awsAccountSchema,
			"start":       map[string]any{"type": "string", "description": "Inclusive start date \"YYYY-MM-DD\", e.g. \"2026-05-01\"."},
			"end":         map[string]any{"type": "string", "description": "Exclusive end date \"YYYY-MM-DD\", e.g. \"2026-06-01\"."},
			"granularity": map[string]any{"type": "string", "description": "DAILY | MONTHLY | HOURLY (default MONTHLY)."},
			"metrics":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Cost metrics, e.g. [\"UnblendedCost\"]. Default [\"UnblendedCost\"]."},
			"group_by":    map[string]any{"type": "string", "description": "Optional GROUP BY dimension key, e.g. \"SERVICE\", \"REGION\", \"USAGE_TYPE\"."},
		},
		"required": []any{"start", "end"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_ce_cost_and_usage",
		Description: "Fetch AWS cost and usage over a date window from Cost Explorer (read-only `aws ce get-cost-and-usage`). Provide start/end dates, a granularity, and an optional group_by dimension (e.g. SERVICE) for cost-anomaly inquiry. The table shows the first metric; any additional metrics are in the raw result.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Start == "" || a.End == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_ce_cost_and_usage: start and end are required")
			}
			if a.Granularity == "" {
				a.Granularity = "MONTHLY"
			}
			if len(a.Metrics) == 0 {
				a.Metrics = []string{"UnblendedCost"}
			}
			for field, v := range map[string]string{"start": a.Start, "end": a.End, "granularity": a.Granularity} {
				if err := safeArg(field, v); err != nil {
					return tools.Observation{}, err
				}
			}
			// --time-period takes a single "Start=…,End=…" argv token; argv-only
			// exec means the embedded '=' and ',' never reach a shell.
			cmd := append([]string{"ce", "get-cost-and-usage"}, acct.baseArgs()...)
			cmd = append(cmd,
				"--time-period", fmt.Sprintf("Start=%s,End=%s", a.Start, a.End),
				"--granularity", a.Granularity,
				"--metrics",
			)
			for _, m := range a.Metrics {
				if err := safeArg("metrics", m); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, m)
			}
			if a.GroupBy != "" {
				if err := safeArg("group_by", a.GroupBy); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--group-by", fmt.Sprintf("Type=DIMENSION,Key=%s", a.GroupBy))
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_ce_cost_and_usage: %w", err)
			}
			type amount struct {
				Amount string `json:"Amount"`
				Unit   string `json:"Unit"`
			}
			var parsed struct {
				ResultsByTime []struct {
					TimePeriod struct {
						Start string `json:"Start"`
						End   string `json:"End"`
					} `json:"TimePeriod"`
					Total  map[string]amount `json:"Total"`
					Groups []struct {
						Keys    []string          `json:"Keys"`
						Metrics map[string]amount `json:"Metrics"`
					} `json:"Groups"`
				} `json:"ResultsByTime"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_ce_cost_and_usage: decode: %w", err)
			}
			// Report the first metric in the row; it is the one the agent most
			// commonly asks for and keeps the table single-valued.
			metric := a.Metrics[0]
			tbl := &render.Table{
				Headers: []string{"PERIOD", "GROUP", "METRIC", "AMOUNT", "UNIT"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignLeft},
			}
			rows := 0
			for _, r := range parsed.ResultsByTime {
				if len(r.Groups) > 0 {
					for _, g := range r.Groups {
						amt := g.Metrics[metric]
						tbl.Rows = append(tbl.Rows, []string{r.TimePeriod.Start, joinKeys(g.Keys), metric, amt.Amount, amt.Unit})
						rows++
					}
					continue
				}
				amt := r.Total[metric]
				tbl.Rows = append(tbl.Rows, []string{r.TimePeriod.Start, "(total)", metric, amt.Amount, amt.Unit})
				rows++
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d cost row(s) over [%s, %s) granularity=%s for account %q.", rows, a.Start, a.End, a.Granularity, acct.name),
				Table: tbl,
				Raw:   parsed.ResultsByTime,
			}, nil
		},
	}.Build()
}

// joinKeys renders a Cost Explorer group's key tuple compactly.
func joinKeys(keys []string) string {
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return keys[0]
	default:
		out := keys[0]
		for _, k := range keys[1:] {
			out += "|" + k
		}
		return out
	}
}
