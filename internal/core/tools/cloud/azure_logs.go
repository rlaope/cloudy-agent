package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// newAzureLogTools builds the Azure Log Analytics read-only tools.
func newAzureLogTools(accts map[string]*azureAccount) []tools.Tool {
	return []tools.Tool{
		newAzureLogAnalyticsQueryTool(accts),
	}
}

// newAzureLogAnalyticsQueryTool wraps `az monitor log-analytics query` — run a
// KQL query against a Log Analytics workspace and return the rows.
func newAzureLogAnalyticsQueryTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account   string `json:"account"`
		Workspace string `json:"workspace"`
		Query     string `json:"query"`
		Timespan  string `json:"timespan"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":   azureAccountSchema,
			"workspace": map[string]any{"type": "string", "description": "Log Analytics workspace ID (customer/GUID), e.g. \"12345678-1234-1234-1234-123456789abc\"."},
			"query":     map[string]any{"type": "string", "description": "KQL query, e.g. \"AzureDiagnostics | where Level == 'Error' | take 50\"."},
			"timespan":  map[string]any{"type": "string", "description": "ISO8601 duration or interval, e.g. \"PT1H\" or \"2026-05-29T00:00:00Z/2026-05-29T01:00:00Z\" (optional)."},
		},
		"required": []any{"workspace", "query"},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_log_analytics_query",
		Description: "Run a KQL query against an Azure Log Analytics workspace and return the rows (read-only `az monitor log-analytics query`). Provide the workspace ID and a KQL query.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Workspace == "" || a.Query == "" {
				return tools.Observation{}, fmt.Errorf("cloud.azure_log_analytics_query: workspace and query are required")
			}
			if err := safeArg("workspace", a.Workspace); err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"monitor", "log-analytics", "query"}, acct.baseArgs()...)
			cmd = append(cmd, "--workspace", a.Workspace, "--analytics-query", a.Query)
			if a.Timespan != "" {
				if err := safeArg("timespan", a.Timespan); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--timespan", a.Timespan)
			}
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_log_analytics_query: %w", err)
			}
			// az returns a JSON array of row objects with --output json.
			var rows []map[string]any
			if err := json.Unmarshal(body, &rows); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_log_analytics_query: decode: %w", err)
			}
			return tools.Observation{
				Text: fmt.Sprintf("%d row(s) from Log Analytics workspace in account %q.", len(rows), acct.name),
				Raw:  rows,
			}, nil
		},
	}.Build()
}
