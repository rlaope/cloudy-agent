package cloud

import (
	"strings"
	"testing"
)

func TestAzureAppInsightsQuery_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"tables":[{"name":"PrimaryResult","columns":[{"name":"a"}],"rows":[[1],[2]]}]}`)

	tool := newAzureAppInsightsQueryTool(oneAzure())
	obs := runTool(t, tool, `{"app":"guid-123","query":"requests | take 10","offset":"1h","resource_group":"rg"}`)

	if args[0] != "monitor" || args[1] != "app-insights" || args[2] != "query" {
		t.Errorf("command path = %v, want monitor app-insights query", args[:3])
	}
	if !hasFlag(args, "--subscription", "sub-123") || !hasFlag(args, "--output", "json") {
		t.Errorf("per-account flags missing: %v", args)
	}
	if !hasFlag(args, "--apps", "guid-123") || !hasFlag(args, "--analytics-query", "requests | take 10") {
		t.Errorf("app/query flags missing: %v", args)
	}
	if !hasFlag(args, "--offset", "1h") || !hasFlag(args, "--resource-group", "rg") {
		t.Errorf("optional flags missing: %v", args)
	}
	if !strings.Contains(obs.Text, "2 row(s) across 1 table") {
		t.Errorf("expected 2 rows / 1 table, got %q", obs.Text)
	}
}

func TestAzureAppInsightsQuery_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `{"tables":[]}`)
	tool := newAzureAppInsightsQueryTool(oneAzure())
	err := runToolErr(t, tool, `{"app":"guid-123"}`) // no query
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}
