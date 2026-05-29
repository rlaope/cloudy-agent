package cloud

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAWSListMetrics_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"Metrics":[{"Namespace":"AWS/EC2","MetricName":"CPUUtilization","Dimensions":[{"Name":"InstanceId","Value":"i-1"}]}]}`)

	tool := newAWSListMetricsTool(oneAWS())
	obs := runTool(t, tool, `{"namespace":"AWS/EC2","metric_name":"CPUUtilization"}`)

	// Per-account flags are always injected.
	if !hasFlag(args, "--region", "us-east-1") || !hasFlag(args, "--profile", "p1") || !hasFlag(args, "--output", "json") {
		t.Errorf("missing per-account flags in argv: %v", args)
	}
	if !hasFlag(args, "--namespace", "AWS/EC2") || !hasFlag(args, "--metric-name", "CPUUtilization") {
		t.Errorf("missing query flags in argv: %v", args)
	}
	if args[0] != "cloudwatch" || args[1] != "list-metrics" {
		t.Errorf("command path = %v, want cloudwatch list-metrics", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 table row, got %+v", obs.Table)
	}
	if got := obs.Table.Rows[0]; got[0] != "AWS/EC2" || got[1] != "CPUUtilization" || got[2] != "InstanceId=i-1" {
		t.Errorf("row = %v", got)
	}
}

func TestAWSGetMetricStatistics_ArgvDefaultsAndDimensions(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `{"Label":"CPUUtilization","Datapoints":[{"Average":12.5}]}`)

	tool := newAWSGetMetricStatisticsTool(oneAWS())
	obs := runTool(t, tool, `{
		"namespace":"AWS/EC2","metric_name":"CPUUtilization",
		"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z",
		"dimensions":["InstanceId=i-1"]
	}`)

	// period defaults to 300, statistics defaults to Average.
	if !hasFlag(args, "--period", "300") {
		t.Errorf("default period 300 missing: %v", args)
	}
	if !hasFlag(args, "--statistics", "Average") {
		t.Errorf("default statistic Average missing: %v", args)
	}
	// dimension "k=v" is converted to CloudWatch "Name=k,Value=v".
	if !hasToken(args, "Name=InstanceId,Value=i-1") {
		t.Errorf("dimension not converted to Name=,Value= form: %v", args)
	}
	if !strings.Contains(obs.Text, "CPUUtilization") {
		t.Errorf("summary should mention the metric label, got %q", obs.Text)
	}
}

func TestAWSGetMetricStatistics_RequiredFields(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAWSGetMetricStatisticsTool(oneAWS())
	// missing metric_name / times
	err := runToolErr(t, tool, `{"namespace":"AWS/EC2"}`)
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("want required-field error, got %v", err)
	}
}

func TestAWSGetMetricStatistics_BadDimension(t *testing.T) {
	stubRunner(t, nil, nil, `{}`)
	tool := newAWSGetMetricStatisticsTool(oneAWS())
	err := runToolErr(t, tool, `{
		"namespace":"AWS/EC2","metric_name":"X",
		"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z",
		"dimensions":["no-equals-sign"]
	}`)
	if !strings.Contains(err.Error(), "Name=Value") {
		t.Errorf("want dimension format error, got %v", err)
	}
}

func TestAWSDescribeLogGroups_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `{"logGroups":[{"logGroupName":"/aws/lambda/fn","storedBytes":1024,"retentionInDays":7}]}`)

	tool := newAWSDescribeLogGroupsTool(oneAWS())
	obs := runTool(t, tool, `{"name_prefix":"/aws/lambda"}`)

	if args[0] != "logs" || args[1] != "describe-log-groups" {
		t.Errorf("command path = %v", args[:2])
	}
	if !hasFlag(args, "--log-group-name-prefix", "/aws/lambda") {
		t.Errorf("name prefix flag missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][0] != "/aws/lambda/fn" {
		t.Errorf("unexpected table: %+v", obs.Table)
	}
}

func TestAWSFilterLogEvents_TimeConvertedToMillis(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `{"events":[{"timestamp":1748476800000,"message":"boom","logStreamName":"s1"}]}`)

	tool := newAWSFilterLogEventsTool(oneAWS())
	obs := runTool(t, tool, `{"log_group_name":"/aws/lambda/fn","filter_pattern":"ERROR","start_time":"2026-05-29T00:00:00Z"}`)

	// 2026-05-29T00:00:00Z == 1779667200 s == 1779667200000 ms.
	wantMs := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC).UnixMilli()
	if !hasFlag(args, "--start-time", itoa(wantMs)) {
		t.Errorf("start-time not converted to millis (want %d): %v", wantMs, args)
	}
	if !hasFlag(args, "--filter-pattern", "ERROR") {
		t.Errorf("filter pattern missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][2] != "boom" {
		t.Errorf("unexpected events table: %+v", obs.Table)
	}
}

func TestAWSLogsInsights_StartThenPollComplete(t *testing.T) {
	// Speed the poll loop up for the test.
	oldInt, oldMax := insightsPollInterval, insightsMaxPolls
	insightsPollInterval = time.Millisecond
	insightsMaxPolls = 10
	t.Cleanup(func() { insightsPollInterval, insightsMaxPolls = oldInt, oldMax })

	call := 0
	cloudExecRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		call++
		switch {
		case hasToken(args, "start-query"):
			return []byte(`{"queryId":"q-1"}`), nil
		case hasToken(args, "get-query-results"):
			if call < 3 {
				return []byte(`{"status":"Running","results":[]}`), nil
			}
			return []byte(`{"status":"Complete","results":[[{"field":"@message","value":"hi"}]]}`), nil
		}
		return []byte(`{}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	tool := newAWSLogsInsightsQueryTool(oneAWS())
	obs := runTool(t, tool, `{
		"log_group_names":["/aws/lambda/fn"],"query_string":"fields @message",
		"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z"
	}`)
	if !strings.Contains(obs.Text, "1 row") {
		t.Errorf("expected 1 row on Complete, got %q", obs.Text)
	}
}

func TestAWSLogsInsights_FailedStatus(t *testing.T) {
	oldInt := insightsPollInterval
	insightsPollInterval = time.Millisecond
	t.Cleanup(func() { insightsPollInterval = oldInt })

	cloudExecRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		if hasToken(args, "start-query") {
			return []byte(`{"queryId":"q-2"}`), nil
		}
		return []byte(`{"status":"Failed"}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	tool := newAWSLogsInsightsQueryTool(oneAWS())
	obs := runTool(t, tool, `{
		"log_group_names":["g"],"query_string":"q",
		"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z"
	}`)
	if !strings.Contains(obs.Text, "Failed") {
		t.Errorf("expected Failed status surfaced, got %q", obs.Text)
	}
}

func TestAWSLogsInsights_CtxCancelled(t *testing.T) {
	insightsPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { insightsPollInterval = time.Second })
	cloudExecRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		if hasToken(args, "start-query") {
			return []byte(`{"queryId":"q-3"}`), nil
		}
		return []byte(`{"status":"Running"}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	tool := newAWSLogsInsightsQueryTool(oneAWS())
	_, err := tool.Run(ctx, json.RawMessage(`{
		"log_group_names":["g"],"query_string":"q",
		"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-29T01:00:00Z"
	}`))
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
