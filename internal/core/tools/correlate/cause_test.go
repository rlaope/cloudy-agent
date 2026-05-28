package correlate

import (
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
)

var (
	t0 = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
	t3 = t0.Add(3 * time.Minute)
)

func evt(kind, summary string, at time.Time) change.ChangeEvent {
	return change.ChangeEvent{Kind: kind, Summary: summary, Time: at}
}

// merge wraps MergeSorted with no limit, producing newest-first order.
func merge(events ...change.ChangeEvent) []change.ChangeEvent {
	return change.MergeSorted(0, events)
}

func TestCandidateCauseV2_SymptomWithPriorChange(t *testing.T) {
	// metric_breach at T2, image at T1, scale at T0 — image is the most
	// recent change before the symptom.
	events := merge(
		evt("metric_breach", "error rate spiked", t2),
		evt("image", "deployed v2.1", t1),
		evt("scale", "replicas 2→5", t0),
	)
	got := candidateCauseV2(events)
	if !strings.Contains(got, "deployed v2.1") {
		t.Fatalf("expected image change summary in output, got: %s", got)
	}
	if !strings.Contains(got, "image") {
		t.Fatalf("expected kind 'image' in output, got: %s", got)
	}
	if !strings.Contains(got, "error rate spiked") {
		t.Fatalf("expected symptom summary in output, got: %s", got)
	}
	if !strings.HasPrefix(got, "candidate cause:") {
		t.Fatalf("expected 'candidate cause:' prefix, got: %s", got)
	}
}

func TestCandidateCauseV2_SymptomWithoutPriorChange(t *testing.T) {
	// log_error at T2, only change is at T3 (after the symptom).
	events := merge(
		evt("rollout", "deployed v3", t3),
		evt("log_error", "OOM killed", t2),
	)
	got := candidateCauseV2(events)
	if !strings.Contains(got, "no preceding change found") {
		t.Fatalf("expected no-preceding-change message, got: %s", got)
	}
	if !strings.Contains(got, "OOM killed") {
		t.Fatalf("expected symptom summary in output, got: %s", got)
	}
}

func TestCandidateCauseV2_NoSymptomFallback(t *testing.T) {
	// Only change events — falls back to v1: most recent change.
	events := merge(
		evt("rollout", "rolled out v1.9", t2),
		evt("scale", "scaled down", t1),
	)
	got := candidateCauseV2(events)
	if !strings.HasPrefix(got, "candidate cause:") {
		t.Fatalf("expected 'candidate cause:' prefix, got: %s", got)
	}
	if !strings.Contains(got, "rolled out v1.9") {
		t.Fatalf("expected most recent change summary, got: %s", got)
	}
}

func TestCandidateCauseV2_Empty(t *testing.T) {
	got := candidateCauseV2(nil)
	if !strings.Contains(got, "candidate cause: none") {
		t.Fatalf("expected 'candidate cause: none' for empty input, got: %s", got)
	}
}

func TestCandidateCauseV2_EarliestSymptomSelected(t *testing.T) {
	// Two symptoms: trace_error at T2, metric_breach at T1 (earlier).
	// One change at T0 (before both). Should align with the earliest symptom
	// (metric_breach at T1) and the change at T0.
	events := merge(
		evt("trace_error", "slow span", t2),
		evt("metric_breach", "cpu spike", t1),
		evt("image", "deployed v5", t0),
	)
	got := candidateCauseV2(events)
	if !strings.Contains(got, "cpu spike") {
		t.Fatalf("expected earliest symptom 'cpu spike' in output, got: %s", got)
	}
	if !strings.Contains(got, "deployed v5") {
		t.Fatalf("expected change 'deployed v5' in output, got: %s", got)
	}
	// The change immediately before the earliest symptom (T0 < T1) is picked,
	// not the one before the later symptom.
	if strings.Contains(got, "slow span") {
		t.Fatalf("should not reference later symptom 'slow span', got: %s", got)
	}
}
