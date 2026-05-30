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

func TestCandidateCauses_SymptomWithPriorChange(t *testing.T) {
	// metric_breach at T2, image at T1, scale at T0. image is both the closer
	// and the higher-weighted change, so it must lead the ranking.
	events := merge(
		evt("metric_breach", "error rate spiked", t2),
		evt("image", "deployed v2.1", t1),
		evt("scale", "replicas 2→5", t0),
	)
	got := candidateCauses(events, "")

	if !strings.Contains(got, "deployed v2.1") || !strings.Contains(got, "image") {
		t.Fatalf("expected leading image change in output, got: %s", got)
	}
	if !strings.Contains(got, "error rate spiked") {
		t.Fatalf("expected symptom summary in output, got: %s", got)
	}
	if !strings.Contains(got, "candidate causes for symptom") {
		t.Fatalf("expected ranked header, got: %s", got)
	}
	// image must outrank scale: its line number is lower in the rendered list.
	imgIdx := strings.Index(got, "deployed v2.1")
	scaleIdx := strings.Index(got, "replicas 2→5")
	if imgIdx == -1 || scaleIdx == -1 || imgIdx > scaleIdx {
		t.Fatalf("image should rank above scale; img=%d scale=%d in: %s", imgIdx, scaleIdx, got)
	}
	if !strings.Contains(got, "before symptom") {
		t.Fatalf("expected a 'before symptom' delta annotation, got: %s", got)
	}
}

func TestCandidateCauses_SymptomWithoutPriorChange(t *testing.T) {
	// log_error at T2, only change is at T3 (after the symptom) — disqualified.
	events := merge(
		evt("rollout", "deployed v3", t3),
		evt("log_error", "OOM killed", t2),
	)
	got := candidateCauses(events, "")
	if !strings.Contains(got, "no preceding change found") {
		t.Fatalf("expected no-preceding-change message, got: %s", got)
	}
	if !strings.Contains(got, "OOM killed") {
		t.Fatalf("expected symptom summary in output, got: %s", got)
	}
}

func TestCandidateCauses_NoSymptomFallback(t *testing.T) {
	// Only change events — rank by weight × recency; rollout leads.
	events := merge(
		evt("rollout", "rolled out v1.9", t2),
		evt("scale", "scaled down", t1),
	)
	got := candidateCauses(events, "")
	if !strings.Contains(got, "no symptom in window") {
		t.Fatalf("expected no-symptom ranked header, got: %s", got)
	}
	if !strings.Contains(got, "rolled out v1.9") {
		t.Fatalf("expected most recent change summary, got: %s", got)
	}
	// A no-symptom ranking must not claim a "before symptom" delta.
	if strings.Contains(got, "before symptom") {
		t.Fatalf("no-symptom output must not annotate a symptom delta, got: %s", got)
	}
}

func TestCandidateCauses_Empty(t *testing.T) {
	got := candidateCauses(nil, "")
	if !strings.Contains(got, "candidate cause: none") {
		t.Fatalf("expected 'candidate cause: none' for empty input, got: %s", got)
	}
}

func TestCandidateCauses_EarliestSymptomSelected(t *testing.T) {
	// Two symptoms: trace_error at T2, metric_breach at T1 (earlier). One change
	// at T0 (before both). The ranking anchors on the earliest symptom.
	events := merge(
		evt("trace_error", "slow span", t2),
		evt("metric_breach", "cpu spike", t1),
		evt("image", "deployed v5", t0),
	)
	got := candidateCauses(events, "")
	if !strings.Contains(got, "cpu spike") {
		t.Fatalf("expected earliest symptom 'cpu spike' in output, got: %s", got)
	}
	if !strings.Contains(got, "deployed v5") {
		t.Fatalf("expected change 'deployed v5' in output, got: %s", got)
	}
	if strings.Contains(got, "slow span") {
		t.Fatalf("should not reference later symptom 'slow span', got: %s", got)
	}
}

// TestCandidateCauses_RanksByProximityAndWeight pins the scoring: of two equal
// time-distance changes the higher-weighted kind (image) must beat the lower
// (scale); and of two equal-weight changes the closer one must win.
func TestCandidateCauses_RanksByProximityAndWeight(t *testing.T) {
	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	symptom := base.Add(30 * time.Minute)
	events := merge(
		evt("metric_breach", "p99 up", symptom),
		evt("image", "img close", base.Add(25*time.Minute)),   // 5m before, weight 1.0
		evt("scale", "scale close", base.Add(25*time.Minute)), // 5m before, weight 0.5
		evt("image", "img far", base.Add(2*time.Minute)),      // 28m before, weight 1.0
	)
	got := candidateCauses(events, "")

	// Highest score is the close, high-weight image.
	first := strings.Index(got, "img close")
	if first == -1 {
		t.Fatalf("expected 'img close' to appear, got: %s", got)
	}
	for _, weaker := range []string{"scale close", "img far"} {
		if idx := strings.Index(got, weaker); idx != -1 && idx < first {
			t.Errorf("%q should rank below 'img close'; got order in: %s", weaker, got)
		}
	}

	// Confidence is rendered as a bracketed percentage on the leader.
	if !strings.Contains(got, "[") || !strings.Contains(got, "%]") {
		t.Errorf("expected a [NN%%] confidence marker, got: %s", got)
	}
}

// TestCandidateCauses_EntityMatchBreaksTie pins the entity-match term: with two
// same-kind, same-time changes, the one whose Target names the workload wins.
func TestCandidateCauses_EntityMatchBreaksTie(t *testing.T) {
	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	symptom := base.Add(10 * time.Minute)
	mine := change.ChangeEvent{Kind: "image", Summary: "mine", Target: "deploy/checkout", Time: base.Add(5 * time.Minute)}
	other := change.ChangeEvent{Kind: "image", Summary: "other", Target: "deploy/unrelated", Time: base.Add(5 * time.Minute)}
	events := merge(
		evt("metric_breach", "errors", symptom),
		mine,
		other,
	)
	got := candidateCauses(events, "checkout")
	mineIdx := strings.Index(got, "mine")
	otherIdx := strings.Index(got, "other")
	if mineIdx == -1 || otherIdx == -1 || mineIdx > otherIdx {
		t.Fatalf("entity-matched change should rank first; mine=%d other=%d in: %s", mineIdx, otherIdx, got)
	}
}

// TestCandidateCauses_SingleCandidateLabel pins that a lone candidate is
// labeled "[only candidate]" rather than a misleading "[100%]".
func TestCandidateCauses_SingleCandidateLabel(t *testing.T) {
	events := merge(
		evt("metric_breach", "errors", t2),
		evt("image", "deployed v9", t1),
	)
	got := candidateCauses(events, "")
	if !strings.Contains(got, "[only candidate]") {
		t.Fatalf("expected '[only candidate]' for a lone candidate, got: %s", got)
	}
	if strings.Contains(got, "%]") {
		t.Fatalf("a single candidate must not render a share percentage, got: %s", got)
	}
}

// TestCandidateCauses_TruncationNotedSilently pins the no-silent-cap rule: with
// more than topCandidates qualifying changes, the surplus is announced.
func TestCandidateCauses_TruncationNotedSilently(t *testing.T) {
	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	symptom := base.Add(30 * time.Minute)
	events := merge(
		evt("metric_breach", "errors", symptom),
		evt("image", "c1", base.Add(25*time.Minute)),
		evt("image", "c2", base.Add(20*time.Minute)),
		evt("image", "c3", base.Add(15*time.Minute)),
		evt("image", "c4", base.Add(10*time.Minute)),
		evt("image", "c5", base.Add(5*time.Minute)),
	)
	got := candidateCauses(events, "")
	if !strings.Contains(got, "…and 2 more") {
		t.Fatalf("expected '…and 2 more' surplus note (5 candidates, top 3), got: %s", got)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                         "0s",
		500 * time.Millisecond:    "0s",
		45 * time.Second:          "45s",
		2 * time.Minute:           "2m",
		time.Hour + 3*time.Minute: "1h3m",
		2 * time.Hour:             "2h",
		-5 * time.Second:          "0s",
	}
	for in, want := range cases {
		if got := shortDuration(in); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
