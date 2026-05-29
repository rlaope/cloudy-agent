package cloud

import (
	"strings"
	"testing"
)

func TestAzureMonitorMetrics_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"value":[{"name":{"value":"Percentage CPU"},"unit":"Percent","timeseries":[{"data":[{"average":1.2},{"average":3.4}]}]}]}`)

	tool := newAzureMetricsListTool(oneAzure())
	obs := runTool(t, tool, `{"resource":"/subscriptions/x/vm1","metrics":["Percentage CPU"],"aggregation":"Average","interval":"PT5M"}`)

	if args[0] != "monitor" || args[1] != "metrics" || args[2] != "list" {
		t.Errorf("command path = %v, want monitor metrics list", args[:3])
	}
	if !hasFlag(args, "--subscription", "sub-123") || !hasFlag(args, "--output", "json") {
		t.Errorf("per-account flags missing: %v", args)
	}
	if !hasFlag(args, "--resource", "/subscriptions/x/vm1") {
		t.Errorf("resource flag missing: %v", args)
	}
	// metric name with a space must survive as a single argv token.
	if !hasToken(args, "Percentage CPU") {
		t.Errorf("metric name token missing/split: %v", args)
	}
	if !hasFlag(args, "--aggregation", "Average") || !hasFlag(args, "--interval", "PT5M") {
		t.Errorf("optional flags missing: %v", args)
	}
	if !strings.Contains(obs.Text, "2 datapoint") {
		t.Errorf("expected 2 datapoints counted, got %q", obs.Text)
	}
}

func TestAzureMonitorMetrics_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAzureMetricsListTool(oneAzure())
	err := runToolErr(t, tool, `{"resource":"/subscriptions/x/vm1"}`) // no metrics
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}

func TestAzureMetricDefinitions_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `{"value":[{"name":{"value":"Percentage CPU"},"unit":"Percent","primaryAggregationType":"Average"}]}`)

	tool := newAzureMetricDefinitionsTool(oneAzure())
	obs := runTool(t, tool, `{"resource":"/subscriptions/x/vm1"}`)

	if args[2] != "list-definitions" {
		t.Errorf("command path = %v, want monitor metrics list-definitions", args[:3])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][0] != "Percentage CPU" {
		t.Errorf("unexpected definitions table: %+v", obs.Table)
	}
}

func TestAzureMetricDefinitions_BareArrayFallback(t *testing.T) {
	// list-definitions sometimes returns a bare array instead of {value:[...]}.
	stubRunner(t, nil, nil, `[{"name":{"value":"Disk Reads"},"unit":"Count","primaryAggregationType":"Total"}]`)
	tool := newAzureMetricDefinitionsTool(oneAzure())
	obs := runTool(t, tool, `{"resource":"/subscriptions/x/vm1"}`)
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][0] != "Disk Reads" {
		t.Errorf("bare-array fallback failed: %+v", obs.Table)
	}
}

func TestAzureLogAnalyticsQuery_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `[{"Level":"Error","Count":3},{"Level":"Warning","Count":7}]`)

	tool := newAzureLogAnalyticsQueryTool(oneAzure())
	obs := runTool(t, tool, `{"workspace":"ws-guid","query":"AzureDiagnostics | take 50","timespan":"PT1H"}`)

	if args[0] != "monitor" || args[1] != "log-analytics" || args[2] != "query" {
		t.Errorf("command path = %v, want monitor log-analytics query", args[:3])
	}
	if !hasFlag(args, "--workspace", "ws-guid") || !hasFlag(args, "--analytics-query", "AzureDiagnostics | take 50") {
		t.Errorf("workspace/query flags missing: %v", args)
	}
	if !hasFlag(args, "--timespan", "PT1H") {
		t.Errorf("timespan flag missing: %v", args)
	}
	if !strings.Contains(obs.Text, "2 row") {
		t.Errorf("expected 2 rows counted, got %q", obs.Text)
	}
}

func TestAzureLogAnalyticsQuery_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `[]`)
	tool := newAzureLogAnalyticsQueryTool(oneAzure())
	err := runToolErr(t, tool, `{"workspace":"ws-guid"}`) // no query
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}
