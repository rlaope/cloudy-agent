package cloud

import (
	"strings"
	"testing"
	"time"
)

func TestAWSXRayTraceSummaries_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"TraceSummaries":[{"Id":"1-abc","Duration":0.5,"ResponseTime":0.4,"HasFault":true,"Http":{"HttpStatus":500,"HttpURL":"http://x"}}]}`)

	tool := newAWSXRayTraceSummariesTool(oneAWS())
	obs := runTool(t, tool, `{"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z","filter_expression":"error"}`)

	if args[0] != "xray" || args[1] != "get-trace-summaries" {
		t.Errorf("command path = %v, want xray get-trace-summaries", args[:2])
	}
	// times convert to epoch seconds.
	wantStart := itoa(time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC).Unix())
	wantEnd := itoa(time.Date(2026, 5, 29, 1, 0, 0, 0, time.UTC).Unix())
	if !hasFlag(args, "--start-time", wantStart) || !hasFlag(args, "--end-time", wantEnd) {
		t.Errorf("times not converted to epoch seconds: %v", args)
	}
	if !hasFlag(args, "--filter-expression", "error") {
		t.Errorf("filter expression missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "1-abc" || row[1] != "0.500" || row[3] != "FAULT" || row[4] != "500" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestAWSXRayTraceSummaries_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAWSXRayTraceSummariesTool(oneAWS())
	err := runToolErr(t, tool, `{"start_time":"2026-05-29T00:00:00Z"}`) // no end_time
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}

func TestAWSXRayBatchGetTraces_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"Traces":[{"Id":"1-abc","Duration":0.5,"Segments":[{"Id":"s1"},{"Id":"s2"}]}],"UnprocessedTraceIds":["1-xyz"]}`)

	tool := newAWSXRayBatchGetTracesTool(oneAWS())
	obs := runTool(t, tool, `{"trace_ids":["1-abc","1-xyz"]}`)

	if args[0] != "xray" || args[1] != "batch-get-traces" {
		t.Errorf("command path = %v, want xray batch-get-traces", args[:2])
	}
	if !hasToken(args, "1-abc") || !hasToken(args, "1-xyz") {
		t.Errorf("trace ids missing from argv: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][2] != "2" {
		t.Errorf("unexpected segments count: %+v", obs.Table)
	}
	if !strings.Contains(obs.Text, "unprocessed") {
		t.Errorf("expected unprocessed note, got %q", obs.Text)
	}
}

func TestAWSXRayBatchGetTraces_TooMany(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAWSXRayBatchGetTracesTool(oneAWS())
	err := runToolErr(t, tool, `{"trace_ids":["1","2","3","4","5","6"]}`)
	if !strings.Contains(err.Error(), "at most 5") {
		t.Errorf("want 5-id cap error, got %v", err)
	}
}

func TestAWSXRayServiceGraph_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"Services":[{"Name":"api","Type":"AWS::EC2::Instance","SummaryStatistics":{"OkCount":10,"TotalCount":12,"ErrorStatistics":{"TotalCount":1},"FaultStatistics":{"TotalCount":1}}}]}`)

	tool := newAWSXRayServiceGraphTool(oneAWS())
	obs := runTool(t, tool, `{"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z"}`)

	if args[0] != "xray" || args[1] != "get-service-graph" {
		t.Errorf("command path = %v, want xray get-service-graph", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	// SERVICE, TYPE, OK, ERROR, FAULT, TOTAL
	if row[0] != "api" || row[2] != "10" || row[3] != "1" || row[4] != "1" || row[5] != "12" {
		t.Errorf("unexpected row: %v", row)
	}
}
