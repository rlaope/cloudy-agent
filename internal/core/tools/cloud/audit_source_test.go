package cloud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
)

func TestNewAuditChangeSource_NilWhenEmpty(t *testing.T) {
	if src := NewAuditChangeSource(Clients{}); src != nil {
		t.Errorf("expected nil source with no provider, got %T", src)
	}
	if src := NewAuditChangeSource(Clients{AWS: oneAWS()}); src == nil {
		t.Error("expected non-nil source when a provider is configured")
	}
}

func TestAuditSource_AWSCloudTrail(t *testing.T) {
	epoch := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC).Unix()
	var args []string
	stubRunner(t, nil, &args,
		`{"Events":[{"EventName":"ModifyDBInstance","Username":"alice","EventTime":`+itoa(epoch)+`}]}`)

	src := NewAuditChangeSource(Clients{AWS: oneAWS()})
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "mydb", Since: time.Hour})
	if err != nil {
		t.Fatalf("RecentChanges error: %v", err)
	}
	if args[0] != "cloudtrail" || args[1] != "lookup-events" {
		t.Errorf("command path = %v, want cloudtrail lookup-events", args[:2])
	}
	if !hasFlag(args, "--lookup-attributes", "AttributeKey=ResourceName,AttributeValue=mydb") {
		t.Errorf("ResourceName lookup attribute missing/wrong: %v", args)
	}
	if !hasToken(args, "--start-time") || !hasToken(args, "--end-time") {
		t.Errorf("time window flags missing: %v", args)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %+v", evs)
	}
	e := evs[0]
	if e.Kind != "cloud_audit" || e.Source != "cloud_audit_aws" || e.Target != "mydb" {
		t.Errorf("unexpected event fields: %+v", e)
	}
	if !e.Time.Equal(time.Unix(epoch, 0).UTC()) {
		t.Errorf("EventTime epoch decode wrong: got %v", e.Time)
	}
	if !strings.Contains(e.Summary, "ModifyDBInstance by alice") {
		t.Errorf("summary should name event + user: %q", e.Summary)
	}
}

func TestAuditSource_GCPReusesLoggingRead(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"timestamp":"2026-05-29T00:00:00Z","protoPayload":{"methodName":"v1.compute.instances.insert","authenticationInfo":{"principalEmail":"bob@example.com"}}}]`)

	src := NewAuditChangeSource(Clients{GCP: oneGCP()})
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "web", Since: 2 * time.Hour})
	if err != nil {
		t.Fatalf("RecentChanges error: %v", err)
	}
	// Reuses the allowlisted logging-read path; the filter is the trailing
	// positional so the allowlist prefix must stay exactly "logging read".
	if got := subcommandPrefix(args); got != "logging read" {
		t.Errorf("allowlist prefix = %q, want %q", got, "logging read")
	}
	filter := args[len(args)-1]
	if !strings.Contains(filter, "cloudaudit.googleapis.com") || !strings.Contains(filter, `protoPayload.resourceName:"web"`) {
		t.Errorf("audit filter malformed: %q", filter)
	}
	if !hasFlag(args, "--freshness", "7200s") {
		t.Errorf("freshness from 2h window wrong: %v", args)
	}
	if len(evs) != 1 || evs[0].Source != "cloud_audit_gcp" {
		t.Fatalf("expected 1 gcp audit event, got %+v", evs)
	}
	if !strings.Contains(evs[0].Summary, "v1.compute.instances.insert by bob@example.com") {
		t.Errorf("unexpected summary: %q", evs[0].Summary)
	}
}

func TestAuditSource_AzureActivityLogClientSideFilter(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `[
		{"eventTimestamp":"2026-05-29T00:00:00Z","resourceId":"/subscriptions/x/resourceGroups/rg/providers/Microsoft.Web/sites/checkout","caller":"carol","operationName":{"localizedValue":"Restart Web App","value":"Microsoft.Web/sites/restart/action"}},
		{"eventTimestamp":"2026-05-29T00:05:00Z","resourceId":"/subscriptions/x/resourceGroups/rg/providers/Microsoft.Web/sites/other","caller":"dave","operationName":{"localizedValue":"Restart Web App","value":"Microsoft.Web/sites/restart/action"}}
	]`)

	src := NewAuditChangeSource(Clients{Azure: oneAzure()})
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout", Since: time.Hour})
	if err != nil {
		t.Fatalf("RecentChanges error: %v", err)
	}
	if got := subcommandPrefix(args); got != "monitor activity-log list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "monitor activity-log list")
	}
	// Only the record whose resourceId contains the workload survives the
	// client-side filter.
	if len(evs) != 1 {
		t.Fatalf("expected 1 matching event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Source != "cloud_audit_azure" || !strings.Contains(evs[0].Summary, "Restart Web App by carol") {
		t.Errorf("unexpected event: %+v", evs[0])
	}
}

func TestParseEpochOrRFC3339(t *testing.T) {
	want := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	// epoch number (CloudTrail JSON form).
	if got := parseEpochOrRFC3339([]byte(itoa(want.Unix()))); !got.Equal(want) {
		t.Errorf("epoch decode = %v, want %v", got, want)
	}
	// RFC3339 string form.
	if got := parseEpochOrRFC3339([]byte(`"2026-05-29T00:00:00Z"`)); !got.Equal(want) {
		t.Errorf("rfc3339 decode = %v, want %v", got, want)
	}
	// garbage / null → zero.
	for _, bad := range []string{``, `null`, `"notatime"`, `{}`} {
		if got := parseEpochOrRFC3339([]byte(bad)); !got.IsZero() {
			t.Errorf("parseEpochOrRFC3339(%q) = %v, want zero", bad, got)
		}
	}
}

func TestAuditSource_RejectsFlagInjectionWorkload(t *testing.T) {
	stubRunner(t, nil, nil, `{"Events":[]}`)
	src := NewAuditChangeSource(Clients{AWS: oneAWS()})
	_, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "-rf"})
	if err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Errorf("want safeArg rejection, got %v", err)
	}
}

// TestAuditAllowlist_RefusesMutatingVerbs proves the two new audit allowlist
// entries did not open an adjacent mutating verb on either binary.
func TestAuditAllowlist_RefusesMutatingVerbs(t *testing.T) {
	cases := []struct {
		bin  string
		args []string
	}{
		{"aws", []string{"cloudtrail", "delete-trail", "--name", "t1"}},
		{"az", []string{"monitor", "activity-log", "alert", "create", "--name", "a1"}},
	}
	for _, c := range cases {
		if _, err := CloudExec(context.Background(), c.bin, c.args); err == nil {
			t.Errorf("%s %v: expected refusal, got nil", c.bin, c.args)
		} else if !strings.Contains(err.Error(), "not a read-only allowlisted sub-command") {
			t.Errorf("%s %v: want allowlist refusal, got %v", c.bin, c.args, err)
		}
	}
}
