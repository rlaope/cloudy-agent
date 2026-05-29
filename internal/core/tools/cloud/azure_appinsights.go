package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// newAzureTraceTools builds the Azure Application Insights read-only tools.
func newAzureTraceTools(accts map[string]*azureAccount) []tools.Tool {
	return []tools.Tool{
		newAzureAppInsightsQueryTool(accts),
	}
}

// newAzureAppInsightsQueryTool wraps `az monitor app-insights query` — run a
// KQL query against an Application Insights app (requests / dependencies /
// traces tables) and return the result rows. KQL is read-only by construction.
func newAzureAppInsightsQueryTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account       string `json:"account"`
		App           string `json:"app"`
		Query         string `json:"query"`
		Offset        string `json:"offset"`
		ResourceGroup string `json:"resource_group"`
		StartTime     string `json:"start_time"`
		EndTime       string `json:"end_time"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":        azureAccountSchema,
			"app":            map[string]any{"type": "string", "description": "Application Insights app GUID, name, or full resource ID. If a name is given, also set resource_group."},
			"query":          map[string]any{"type": "string", "description": "KQL query, e.g. \"dependencies | where success == false | take 50\" over the requests/dependencies/traces tables."},
			"offset":         map[string]any{"type": "string", "description": "Query range as ##d##h, e.g. \"1h30m\" (optional; defaults to 1h). Ignored if start_time and end_time are both set."},
			"resource_group": map[string]any{"type": "string", "description": "Resource group of the app (required only when app is a name, not a GUID)."},
			"start_time":     map[string]any{"type": "string", "description": "Start time \"yyyy-mm-dd hh:mm:ss\" (optional)."},
			"end_time":       map[string]any{"type": "string", "description": "End time \"yyyy-mm-dd hh:mm:ss\" (optional)."},
		},
		"required": []any{"app", "query"},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_appinsights_query",
		Description: "Run a KQL query against an Azure Application Insights app and return rows (read-only `az monitor app-insights query`). Provide the app (GUID or name+resource_group) and a KQL query over requests/dependencies/traces.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.App == "" || a.Query == "" {
				return tools.Observation{}, fmt.Errorf("cloud.azure_appinsights_query: app and query are required")
			}
			if err := safeArg("app", a.App); err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"monitor", "app-insights", "query"}, acct.baseArgs()...)
			cmd = append(cmd, "--apps", a.App, "--analytics-query", a.Query)
			if a.ResourceGroup != "" {
				if err := safeArg("resource_group", a.ResourceGroup); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--resource-group", a.ResourceGroup)
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
			if a.Offset != "" {
				if err := safeArg("offset", a.Offset); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--offset", a.Offset)
			}
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_appinsights_query: %w", err)
			}
			// app-insights query returns the raw Application Insights response:
			// {"tables":[{"name":...,"columns":[...],"rows":[[...]]}]}.
			var parsed struct {
				Tables []struct {
					Name string  `json:"name"`
					Rows [][]any `json:"rows"`
				} `json:"tables"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_appinsights_query: decode: %w", err)
			}
			rows := 0
			for _, t := range parsed.Tables {
				rows += len(t.Rows)
			}
			return tools.Observation{
				Text: fmt.Sprintf("%d row(s) across %d table(s) from Application Insights app in account %q.", rows, len(parsed.Tables), acct.name),
				Raw:  parsed.Tables,
			}, nil
		},
	}.Build()
}
