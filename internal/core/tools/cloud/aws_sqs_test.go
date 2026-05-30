package cloud

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sqsStub installs a cloudExecRunner that answers list-queues with the given
// URLs and get-queue-attributes from a per-queue-name attribute table. A name
// present in failQueues returns an exec error for that queue.
func sqsStub(t *testing.T, urls []string, attrs map[string]map[string]string, failQueues map[string]bool) {
	t.Helper()
	cloudExecRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "sqs" && args[1] == "list-queues":
			body := `{"QueueUrls":["` + strings.Join(urls, `","`) + `"]}`
			if len(urls) == 0 {
				body = `{}`
			}
			return []byte(body), nil
		case len(args) >= 2 && args[0] == "sqs" && args[1] == "get-queue-attributes":
			name := queueNameFromURL(flagValue(args, "--queue-url"))
			if failQueues[name] {
				return nil, errors.New("AccessDenied")
			}
			a := attrs[name]
			parts := make([]string, 0, len(a))
			for k, v := range a {
				parts = append(parts, `"`+k+`":"`+v+`"`)
			}
			return []byte(`{"Attributes":{` + strings.Join(parts, ",") + `}}`), nil
		default:
			t.Fatalf("unexpected exec args: %v", args)
			return nil, nil
		}
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })
}

func flagValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func u(name string) string {
	return "https://sqs.us-east-1.amazonaws.com/123456789012/" + name
}

func TestSQSQueueDepth_RanksAndFlags(t *testing.T) {
	sqsStub(t,
		[]string{u("hot"), u("busy")},
		map[string]map[string]string{
			"hot":  {"ApproximateNumberOfMessages": "500", "ApproximateNumberOfMessagesNotVisible": "0", "ApproximateNumberOfMessagesDelayed": "0"},
			"busy": {"ApproximateNumberOfMessages": "100", "ApproximateNumberOfMessagesNotVisible": "50", "ApproximateNumberOfMessagesDelayed": "0"},
		}, nil)

	tool := newAWSSQSQueueDepthTool(oneAWS())
	obs := runTool(t, tool, `{}`)

	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", obs.Table)
	}
	// hot (500) outranks busy (100).
	if obs.Table.Rows[0][0] != "hot" {
		t.Errorf("highest backlog should rank first, got: %v", obs.Table.Rows[0])
	}
	if obs.Table.Rows[0][4] != "NO IN-FLIGHT" {
		t.Errorf("a backlog with zero in-flight must flag NO IN-FLIGHT, got: %v", obs.Table.Rows[0])
	}
	if obs.Table.Rows[1][4] != "" {
		t.Errorf("a queue with in-flight messages should not be flagged, got: %v", obs.Table.Rows[1])
	}
	if !strings.Contains(obs.Text, "⚠ 1 queue(s) with a backlog and nothing in flight") {
		t.Errorf("summary should count the stuck queue, got: %s", obs.Text)
	}
}

func TestSQSQueueDepth_LimitTruncates(t *testing.T) {
	sqsStub(t,
		[]string{u("a"), u("b"), u("c")},
		map[string]map[string]string{
			"a": {"ApproximateNumberOfMessages": "1"},
			"b": {"ApproximateNumberOfMessages": "2"},
		}, nil)

	tool := newAWSSQSQueueDepthTool(oneAWS())
	obs := runTool(t, tool, `{"limit":2}`)

	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("limit=2 should attribute 2 queues, got %+v", obs.Table)
	}
	if !strings.Contains(obs.Text, "showing the first 2") {
		t.Errorf("a truncated listing should say so, got: %s", obs.Text)
	}
}

func TestSQSQueueDepth_PerQueueErrorKept(t *testing.T) {
	sqsStub(t,
		[]string{u("ok"), u("denied")},
		map[string]map[string]string{
			"ok": {"ApproximateNumberOfMessages": "10", "ApproximateNumberOfMessagesNotVisible": "5"},
		},
		map[string]bool{"denied": true})

	tool := newAWSSQSQueueDepthTool(oneAWS())
	obs := runTool(t, tool, `{}`)

	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("an unreadable queue must still appear as a row, got %+v", obs.Table)
	}
	var sawErr bool
	for _, row := range obs.Table.Rows {
		if row[0] == "denied" && strings.HasPrefix(row[4], "ERROR") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("the denied queue should carry an ERROR flag, got: %+v", obs.Table.Rows)
	}
}

func TestSQSQueueDepth_Argv(t *testing.T) {
	var lastList []string
	cloudExecRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "list-queues" {
			lastList = args
			return []byte(`{"QueueUrls":[]}`), nil
		}
		return []byte(`{}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	tool := newAWSSQSQueueDepthTool(oneAWS())
	_ = runTool(t, tool, `{"prefix":"orders"}`)

	if lastList[0] != "sqs" || lastList[1] != "list-queues" {
		t.Errorf("command path = %v, want sqs list-queues", lastList[:2])
	}
	if flagValue(lastList, "--queue-name-prefix") != "orders" {
		t.Errorf("prefix should reach the CLI, got: %v", lastList)
	}
	if !hasFlag(lastList, "--output", "json") {
		t.Errorf("per-account flags missing: %v", lastList)
	}
}

func TestSQSQueueDepth_RejectsFlagPrefix(t *testing.T) {
	tool := newAWSSQSQueueDepthTool(oneAWS())
	if err := runToolErr(t, tool, `{"prefix":"--debug"}`); err == nil {
		t.Error("a prefix that looks like a flag must be rejected")
	}
}
