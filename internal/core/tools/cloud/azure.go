package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

func pickAzure(m map[string]*azureAccount, name string) (*azureAccount, error) {
	return tools.PickEndpoint(m, name, "cloud", "azure account")
}

// baseArgs returns the per-account flags injected on every az CLI call.
func (a *azureAccount) baseArgs() []string {
	return []string{"--subscription", a.subscriptionID, "--output", "json"}
}

var azureAccountSchema = map[string]any{
	"type":        "string",
	"description": "Name of the Azure account configured under cloud_azure. Optional if exactly one is configured.",
}

// newAzureMetricTools builds the Azure Monitor read-only metric tools.
func newAzureMetricTools(accts map[string]*azureAccount) []tools.Tool {
	return []tools.Tool{
		newAzureMetricDefinitionsTool(accts),
		newAzureMetricsListTool(accts),
	}
}

// newAzureMetricDefinitionsTool wraps `az monitor metrics list-definitions` —
// discovery of which metrics a resource emits.
func newAzureMetricDefinitionsTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account  string `json:"account"`
		Resource string `json:"resource"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":  azureAccountSchema,
			"resource": map[string]any{"type": "string", "description": "Full Azure resource ID, e.g. \"/subscriptions/…/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1\"."},
		},
		"required": []any{"resource"},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_monitor_metric_definitions",
		Description: "List the metric definitions a resource emits in Azure Monitor (read-only `az monitor metrics list-definitions`). Use to discover metric names before querying.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Resource == "" {
				return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metric_definitions: resource is required")
			}
			if err := safeArg("resource", a.Resource); err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"monitor", "metrics", "list-definitions"}, acct.baseArgs()...)
			cmd = append(cmd, "--resource", a.Resource)
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metric_definitions: %w", err)
			}
			var parsed struct {
				Value []struct {
					Name struct {
						Value string `json:"value"`
					} `json:"name"`
					Unit                   string `json:"unit"`
					PrimaryAggregationType string `json:"primaryAggregationType"`
				} `json:"value"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				// list-definitions sometimes returns a bare array; fall back.
				var arr []struct {
					Name struct {
						Value string `json:"value"`
					} `json:"name"`
					Unit                   string `json:"unit"`
					PrimaryAggregationType string `json:"primaryAggregationType"`
				}
				if err2 := json.Unmarshal(body, &arr); err2 != nil {
					return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metric_definitions: decode: %w", err)
				}
				parsed.Value = arr
			}
			tbl := &render.Table{
				Headers: []string{"METRIC", "UNIT", "AGGREGATION"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, d := range parsed.Value {
				tbl.Rows = append(tbl.Rows, []string{d.Name.Value, d.Unit, d.PrimaryAggregationType})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d metric definition(s) for resource in account %q.", len(parsed.Value), acct.name),
				Table: tbl,
				Raw:   parsed.Value,
			}, nil
		},
	}.Build()
}

// newAzureMetricsListTool wraps `az monitor metrics list` — a time-bounded
// metric series for one resource.
func newAzureMetricsListTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account     string   `json:"account"`
		Resource    string   `json:"resource"`
		Metrics     []string `json:"metrics"`
		Aggregation string   `json:"aggregation"`
		Interval    string   `json:"interval"`
		StartTime   string   `json:"start_time"`
		EndTime     string   `json:"end_time"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":     azureAccountSchema,
			"resource":    map[string]any{"type": "string", "description": "Full Azure resource ID."},
			"metrics":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Metric names to fetch, e.g. [\"Percentage CPU\"]."},
			"aggregation": map[string]any{"type": "string", "description": "Aggregation: Average | Minimum | Maximum | Total | Count (optional)."},
			"interval":    map[string]any{"type": "string", "description": "ISO8601 duration, e.g. \"PT5M\" (optional)."},
			"start_time":  map[string]any{"type": "string", "description": "ISO8601 start time (optional)."},
			"end_time":    map[string]any{"type": "string", "description": "ISO8601 end time (optional)."},
		},
		"required": []any{"resource", "metrics"},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_monitor_metrics",
		Description: "Fetch a time-bounded metric series for one Azure resource (read-only `az monitor metrics list`). Provide the resource ID and metric name(s).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Resource == "" || len(a.Metrics) == 0 {
				return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metrics: resource and metrics are required")
			}
			if err := safeArg("resource", a.Resource); err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"monitor", "metrics", "list"}, acct.baseArgs()...)
			cmd = append(cmd, "--resource", a.Resource, "--metrics")
			cmd = append(cmd, a.Metrics...)
			if a.Aggregation != "" {
				if err := safeArg("aggregation", a.Aggregation); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--aggregation", a.Aggregation)
			}
			if a.Interval != "" {
				if err := safeArg("interval", a.Interval); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--interval", a.Interval)
			}
			if a.StartTime != "" {
				if err := safeArg("start_time", a.StartTime); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--start-time", a.StartTime)
			}
			if a.EndTime != "" {
				if err := safeArg("end_time", a.EndTime); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--end-time", a.EndTime)
			}
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metrics: %w", err)
			}
			var parsed struct {
				Value []struct {
					Name struct {
						Value string `json:"value"`
					} `json:"name"`
					Unit       string `json:"unit"`
					Timeseries []struct {
						Data []map[string]any `json:"data"`
					} `json:"timeseries"`
				} `json:"value"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_monitor_metrics: decode: %w", err)
			}
			points := 0
			for _, m := range parsed.Value {
				for _, ts := range m.Timeseries {
					points += len(ts.Data)
				}
			}
			return tools.Observation{
				Text: fmt.Sprintf("%d metric series, %d datapoint(s) for resource in account %q.",
					len(parsed.Value), points, acct.name),
				Raw: parsed,
			}, nil
		},
	}.Build()
}
