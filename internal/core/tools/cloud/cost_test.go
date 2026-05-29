package cloud

import (
	"context"
	"strings"
	"testing"
)

func TestAWSCostAndUsage_ArgvGroupedAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-05-01","End":"2026-06-01"},"Total":{},"Groups":[{"Keys":["Amazon EC2"],"Metrics":{"UnblendedCost":{"Amount":"50.00","Unit":"USD"}}},{"Keys":["Amazon RDS"],"Metrics":{"UnblendedCost":{"Amount":"12.34","Unit":"USD"}}}]}]}`)

	tool := newAWSCostAndUsageTool(oneAWS())
	obs := runTool(t, tool, `{"start":"2026-05-01","end":"2026-06-01","group_by":"SERVICE"}`)

	if args[0] != "ce" || args[1] != "get-cost-and-usage" {
		t.Errorf("command path = %v, want ce get-cost-and-usage", args[:2])
	}
	// --time-period is a single Start=…,End=… token.
	if !hasFlag(args, "--time-period", "Start=2026-05-01,End=2026-06-01") {
		t.Errorf("time-period token malformed: %v", args)
	}
	// default granularity MONTHLY and default metric UnblendedCost.
	if !hasFlag(args, "--granularity", "MONTHLY") || !hasToken(args, "UnblendedCost") {
		t.Errorf("defaults missing: %v", args)
	}
	if !hasFlag(args, "--group-by", "Type=DIMENSION,Key=SERVICE") {
		t.Errorf("group-by token malformed: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 group rows, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[1] != "Amazon EC2" || row[3] != "50.00" || row[4] != "USD" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestAWSCostAndUsage_UngroupedTotal(t *testing.T) {
	stubRunner(t, nil, nil,
		`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-05-01","End":"2026-06-01"},"Total":{"UnblendedCost":{"Amount":"62.34","Unit":"USD"}},"Groups":[]}]}`)
	tool := newAWSCostAndUsageTool(oneAWS())
	obs := runTool(t, tool, `{"start":"2026-05-01","end":"2026-06-01"}`)
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 total row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[1] != "(total)" || row[3] != "62.34" {
		t.Errorf("unexpected total row: %v", row)
	}
}

func TestAWSCostAndUsage_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAWSCostAndUsageTool(oneAWS())
	err := runToolErr(t, tool, `{"start":"2026-05-01"}`) // no end
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}

func TestAzureConsumptionUsage_ArgvAndTotal(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"instanceName":"vm1","pretaxCost":"10.50","currency":"USD","usageStart":"2026-05-01T00:00:00Z","meterDetails":{"meterName":"D2s v3","meterCategory":"Virtual Machines"}},{"instanceName":"db1","pretaxCost":"4.25","currency":"USD","usageStart":"2026-05-01T00:00:00Z","meterDetails":{"meterName":"vCore","meterCategory":"SQL Database"}}]`)

	tool := newAzureConsumptionUsageTool(oneAzure())
	obs := runTool(t, tool, `{"start_date":"2026-05-01","end_date":"2026-05-31","top":100}`)

	if got := subcommandPrefix(args); got != "consumption usage list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "consumption usage list")
	}
	if !hasFlag(args, "--top", "100") || !hasFlag(args, "--start-date", "2026-05-01") || !hasFlag(args, "--end-date", "2026-05-31") {
		t.Errorf("query flags missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", obs.Table)
	}
	// pre-tax total summed: 10.50 + 4.25 = 14.75.
	if !strings.Contains(obs.Text, "14.75 USD") {
		t.Errorf("expected summed total in text, got %q", obs.Text)
	}
	if obs.Table.Rows[0][1] != "Virtual Machines/D2s v3" {
		t.Errorf("meter category/name join wrong: %v", obs.Table.Rows[0])
	}
}

func TestAzureConsumptionUsage_DatePairRequired(t *testing.T) {
	stubRunner(t, nil, nil, `[]`)
	tool := newAzureConsumptionUsageTool(oneAzure())
	err := runToolErr(t, tool, `{"start_date":"2026-05-01"}`) // end_date missing
	if !strings.Contains(err.Error(), "together") {
		t.Errorf("want date-pair error, got %v", err)
	}
}

// TestCostAllowlist_RefusesNonCostVerbs proves the FinOps allowlist entries did
// not open any adjacent mutating/budget-write verb on the two binaries.
func TestCostAllowlist_RefusesNonCostVerbs(t *testing.T) {
	cases := []struct {
		bin  string
		args []string
	}{
		{"aws", []string{"ce", "create-anomaly-monitor", "--anomaly-monitor", "x"}},
		{"az", []string{"consumption", "budget", "create", "--budget-name", "b1"}},
	}
	for _, c := range cases {
		if _, err := CloudExec(context.Background(), c.bin, c.args); err == nil {
			t.Errorf("%s %v: expected refusal, got nil", c.bin, c.args)
		} else if !strings.Contains(err.Error(), "not a read-only allowlisted sub-command") {
			t.Errorf("%s %v: want allowlist refusal, got %v", c.bin, c.args, err)
		}
	}
}
