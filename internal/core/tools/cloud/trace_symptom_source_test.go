package cloud

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// xrayID builds a synthetic X-Ray trace Id whose embedded epoch is t.
func xrayID(t time.Time, suffix string) string {
	return fmt.Sprintf("1-%x-%s", t.Unix(), suffix)
}

func TestNewTraceSymptomSource_NilWithoutAWS(t *testing.T) {
	if src := NewTraceSymptomSource(Clients{}); src != nil {
		t.Errorf("expected nil source with no AWS account, got %T", src)
	}
	if src := NewTraceSymptomSource(Clients{AWS: oneAWS()}); src == nil {
		t.Error("expected non-nil source when an AWS account is configured")
	}
}

func TestXRayTraceStartTime(t *testing.T) {
	want := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	if got := xrayTraceStartTime(xrayID(want, "abc123")); !got.Equal(want) {
		t.Errorf("decoded %v, want %v", got, want)
	}
	// Malformed Ids yield the zero time, not a panic.
	for _, bad := range []string{"", "garbage", "1-nothex-x", "1-2-3-4"} {
		if got := xrayTraceStartTime(bad); !got.IsZero() {
			t.Errorf("xrayTraceStartTime(%q) = %v, want zero", bad, got)
		}
	}
}

func TestCloudTraceSymptomEvents_EarliestErrorAndSlow(t *testing.T) {
	t0 := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	summaries := []xrayTraceSummary{
		{ID: xrayID(t0.Add(2*time.Minute), "e2"), Duration: 0.3, HasFault: true}, // later fault
		{ID: xrayID(t0, "e1"), Duration: 0.2, HasError: true},                    // earliest error
		{ID: xrayID(t0.Add(time.Minute), "s1"), Duration: 3.0},                   // slow, healthy
		{ID: xrayID(t0.Add(3*time.Minute), "ok"), Duration: 0.1},                 // fast, healthy
	}
	out := cloudTraceSymptomEvents(summaries, "api")
	if len(out) != 2 {
		t.Fatalf("expected 1 trace_error + 1 trace_slow, got %d: %+v", len(out), out)
	}
	var errEv, slowEv *change.ChangeEvent
	for i := range out {
		switch out[i].Kind {
		case "trace_error":
			errEv = &out[i]
		case "trace_slow":
			slowEv = &out[i]
		}
	}
	if errEv == nil || slowEv == nil {
		t.Fatalf("missing a symptom kind: %+v", out)
	}
	// earliest error is e1 at t0, and the count of failing traces is 2 (e1+e2).
	if !errEv.Time.Equal(t0) || errEv.Source != "cloud_trace" || errEv.Target != "api" {
		t.Errorf("unexpected trace_error event: %+v", errEv)
	}
	if !strings.Contains(errEv.Summary, "2 failing trace") {
		t.Errorf("trace_error summary should report 2 failing traces: %q", errEv.Summary)
	}
	// slow event is s1, healthy and over the 1s threshold.
	if !slowEv.Time.Equal(t0.Add(time.Minute)) {
		t.Errorf("unexpected trace_slow time: %+v", slowEv)
	}
}

func TestCloudTraceSymptomEvents_NoneWhenHealthy(t *testing.T) {
	t0 := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	summaries := []xrayTraceSummary{
		{ID: xrayID(t0, "ok1"), Duration: 0.2},
		{ID: xrayID(t0, "ok2"), Duration: 0.9}, // under 1s threshold
	}
	if out := cloudTraceSymptomEvents(summaries, "api"); len(out) != 0 {
		t.Errorf("expected no symptoms for healthy fast traces, got %+v", out)
	}
}

func TestTraceSymptomSource_RecentChangesArgv(t *testing.T) {
	t0 := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	var args []string
	stubRunner(t, nil, &args,
		fmt.Sprintf(`{"TraceSummaries":[{"Id":"%s","Duration":0.5,"HasFault":true}]}`, xrayID(t0, "abc")))

	src := NewTraceSymptomSource(Clients{AWS: oneAWS()})
	events, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout", Since: time.Hour})
	if err != nil {
		t.Fatalf("RecentChanges error: %v", err)
	}
	if args[0] != "xray" || args[1] != "get-trace-summaries" {
		t.Errorf("command path = %v, want xray get-trace-summaries", args[:2])
	}
	if !hasFlag(args, "--filter-expression", `service("checkout")`) {
		t.Errorf("service filter expression missing/wrong: %v", args)
	}
	if !hasToken(args, "--start-time") || !hasToken(args, "--end-time") {
		t.Errorf("time window flags missing: %v", args)
	}
	if len(events) != 1 || events[0].Kind != "trace_error" || events[0].Source != "cloud_trace" {
		t.Errorf("expected one cloud_trace trace_error event, got %+v", events)
	}
}

func TestTraceSymptomSource_RejectsFlagInjectionWorkload(t *testing.T) {
	stubRunner(t, nil, nil, `{"TraceSummaries":[]}`)
	src := NewTraceSymptomSource(Clients{AWS: oneAWS()})
	_, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "--profile=evil"})
	if err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Errorf("want safeArg rejection of flag-like workload, got %v", err)
	}
}
