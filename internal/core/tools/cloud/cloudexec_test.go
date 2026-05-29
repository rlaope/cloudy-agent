package cloud

import (
	"context"
	"errors"
	"testing"
)

// TestCloudExec_RejectsMutatingSubcommand is the core security assertion: a
// mutating CLI verb must be refused before exec even though the binary itself
// is known. This is the read-only boundary for the shell-out path, which the
// HTTP transport guard does not cover.
func TestCloudExec_RejectsMutatingSubcommand(t *testing.T) {
	calls := 0
	cloudExecRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		return nil, nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	mutating := [][]string{
		{"ec2", "terminate-instances", "--instance-ids", "i-0abc"},
		{"cloudwatch", "put-metric-alarm", "--alarm-name", "x"},
		{"cloudwatch", "delete-alarms"},
		{"logs", "delete-log-group", "--log-group-name", "x"},
		{"logs", "put-log-events", "--log-group-name", "x"},
	}
	for _, args := range mutating {
		_, err := CloudExec(context.Background(), "aws", args)
		if !errors.Is(err, ErrSubcommandNotAllowed) {
			t.Errorf("CloudExec(aws %v) = %v, want ErrSubcommandNotAllowed", args, err)
		}
	}
	if calls != 0 {
		t.Errorf("runner was invoked %d time(s); a rejected sub-command must never exec", calls)
	}
}

// TestCloudExec_RejectsUnknownBinary guards against shelling out to anything
// outside the curated aws/az set.
func TestCloudExec_RejectsUnknownBinary(t *testing.T) {
	cloudExecRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		t.Fatal("runner must not be called for an unknown binary")
		return nil, nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	_, err := CloudExec(context.Background(), "kubectl", []string{"get", "pods"})
	if !errors.Is(err, ErrSubcommandNotAllowed) {
		t.Errorf("CloudExec(kubectl …) = %v, want ErrSubcommandNotAllowed", err)
	}
}

// TestCloudExec_AllowsReadOnlySubcommand confirms an allowlisted read verb
// reaches the runner with its argv intact.
func TestCloudExec_AllowsReadOnlySubcommand(t *testing.T) {
	var gotBin string
	var gotArgs []string
	cloudExecRunner = func(_ context.Context, bin string, args []string) ([]byte, error) {
		gotBin, gotArgs = bin, args
		return []byte(`{"Metrics":[]}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	args := []string{"cloudwatch", "list-metrics", "--namespace", "AWS/EC2", "--output", "json"}
	out, err := CloudExec(context.Background(), "aws", args)
	if err != nil {
		t.Fatalf("CloudExec returned error: %v", err)
	}
	if string(out) != `{"Metrics":[]}` {
		t.Errorf("unexpected output: %s", out)
	}
	if gotBin != "aws" || len(gotArgs) != len(args) {
		t.Errorf("runner got (%q, %v), want (aws, %v)", gotBin, gotArgs, args)
	}
}

// TestCloudExec_AllowsLogReadVerbs confirms the Phase-2 read-only log verbs are
// allowlisted across both clouds.
func TestCloudExec_AllowsLogReadVerbs(t *testing.T) {
	cloudExecRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte(`[]`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	allowed := []struct {
		bin  string
		args []string
	}{
		{"aws", []string{"logs", "describe-log-groups", "--limit", "1"}},
		{"aws", []string{"logs", "filter-log-events", "--log-group-name", "x"}},
		{"aws", []string{"logs", "start-query", "--query-string", "fields @message"}},
		{"aws", []string{"logs", "get-query-results", "--query-id", "q1"}},
		{"az", []string{"monitor", "log-analytics", "query", "--workspace", "w", "--analytics-query", "X"}},
	}
	for _, c := range allowed {
		if _, err := CloudExec(context.Background(), c.bin, c.args); err != nil {
			t.Errorf("CloudExec(%s %v) should be allowed, got: %v", c.bin, c.args, err)
		}
	}
}

// TestSubcommandPrefix covers variable-length command paths and the
// stop-at-first-flag rule. A leading flag yields an empty prefix (refused),
// which is the safe default.
func TestSubcommandPrefix(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"cloudwatch", "list-metrics", "--namespace", "AWS/EC2"}, "cloudwatch list-metrics"},
		{[]string{"cloudwatch", "get-metric-statistics", "--region", "us-east-1"}, "cloudwatch get-metric-statistics"},
		{[]string{"monitor", "metrics", "list", "--resource", "x"}, "monitor metrics list"},
		{[]string{"monitor", "metrics", "list-definitions", "--subscription", "s"}, "monitor metrics list-definitions"},
		{[]string{"--region", "us-east-1", "cloudwatch", "list-metrics"}, ""},
	}
	for _, c := range cases {
		if got := subcommandPrefix(c.args); got != c.want {
			t.Errorf("subcommandPrefix(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

// TestCloudExec_AllowsAzureThreeTokenVerb guards the variable-length path: az
// verbs are three tokens, and must still pass the allowlist.
func TestCloudExec_AllowsAzureThreeTokenVerb(t *testing.T) {
	called := false
	cloudExecRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		called = true
		return []byte(`{"value":[]}`), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })

	_, err := CloudExec(context.Background(), "az",
		[]string{"monitor", "metrics", "list", "--resource", "/subscriptions/x/vm1", "--metrics", "Percentage CPU"})
	if err != nil {
		t.Fatalf("az monitor metrics list should be allowed, got: %v", err)
	}
	if !called {
		t.Error("runner must be invoked for an allowlisted az verb")
	}
}
