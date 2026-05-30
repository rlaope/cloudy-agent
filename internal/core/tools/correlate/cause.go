package correlate

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// changeKinds is the set of state-altering change event kinds considered as
// candidate causes. The value is the kind's causal weight — a deploy (image /
// rollout / argo sync) is a far stronger prior for a fresh symptom than a
// scale or a routine image pull, so it earns a higher base score before
// time-proximity and entity-match are folded in.
var changeKinds = map[string]float64{
	"image":             1.0,
	"rollout":           1.0,
	"sync":              0.9,
	"container_create":  0.7,
	"image_pull":        0.6,
	"container_restart": 0.6,
	"scale":             0.5,
}

// otherChangeWeight is the causal weight for a state-altering kind not in
// changeKinds — kept low so an unrecognised change never outranks a deploy.
const otherChangeWeight = 0.3

// proximityTauSeconds is the e-folding time of the proximity term: a change
// one τ before the symptom keeps ~37% of its score, two τ before ~14%. Ten
// minutes matches the typical deploy→symptom lag in a rolling update.
const proximityTauSeconds = 600.0

// topCandidates bounds how many ranked causes are rendered. Three is enough to
// show the leader plus the runners-up an operator should rule out.
const topCandidates = 3

// symptomKinds is the set of observable symptom event kinds.
var symptomKinds = map[string]bool{
	"log_error":     true,
	"metric_breach": true,
	"trace_error":   true,
	"trace_slow":    true,
}

// scoredCause is a candidate change paired with its causal score.
type scoredCause struct {
	event *change.ChangeEvent
	score float64
}

// candidateCauses ranks the state-altering changes most likely to have caused
// the workload's earliest symptom, replacing the v2 single-pick heuristic with
// a weighted score: kind weight × time-proximity (exponential decay) × entity
// match. events must be newest-first (MergeSorted output); workload is used for
// the entity-match term.
//
//   - Symptom present, ≥1 preceding change → a ranked block with each
//     candidate's relative confidence (its share of the total score).
//   - Symptom present, no preceding change → the v2 "none — …no preceding
//     change" line, so an operator still sees the unexplained symptom.
//   - No symptom → rank the most significant recent changes instead.
//   - Nothing state-altering → "candidate cause: none …".
func candidateCauses(events []change.ChangeEvent, workload string) string {
	earliestSymptom := earliestSymptom(events)

	if earliestSymptom != nil {
		ranked := scoreCauses(events, workload, earliestSymptom)
		if len(ranked) == 0 {
			return fmt.Sprintf(
				"candidate cause: none — earliest symptom %s has no preceding change found in window",
				describeEvent(earliestSymptom),
			)
		}
		return renderRanked(
			fmt.Sprintf("candidate causes for symptom %s (relative confidence)", describeEvent(earliestSymptom)),
			ranked, earliestSymptom)
	}

	// No symptom: rank the recent changes by weight × recency so the operator
	// still sees what moved, without claiming any of it caused anything.
	ranked := scoreCauses(events, workload, nil)
	if len(ranked) == 0 {
		return "candidate cause: none — no state-altering change in the window"
	}
	return renderRanked("candidate causes (no symptom in window; most significant recent changes)", ranked, nil)
}

// earliestSymptom returns the oldest symptom event, or nil when none is present.
func earliestSymptom(events []change.ChangeEvent) *change.ChangeEvent {
	var earliest *change.ChangeEvent
	for i := range events {
		e := &events[i]
		if !symptomKinds[e.Kind] {
			continue
		}
		if earliest == nil || e.Time.Before(earliest.Time) {
			earliest = e
		}
	}
	return earliest
}

// scoreCauses scores every state-altering change against the anchor and returns
// them sorted by score descending (ties broken by recency). When anchor is a
// symptom, only changes strictly before it qualify (a change cannot cause an
// earlier symptom); the proximity term measures anchor−change. When anchor is
// nil (no symptom), every change qualifies and proximity is measured against
// the newest event so recent changes lead.
func scoreCauses(events []change.ChangeEvent, workload string, anchor *change.ChangeEvent) []scoredCause {
	ref := anchor
	if ref == nil {
		ref = newestEvent(events)
	}
	if ref == nil {
		return nil
	}

	var out []scoredCause
	for i := range events {
		e := &events[i]
		weight, ok := causeWeight(e.Kind)
		if !ok {
			continue
		}
		// A cause must not be newer than the symptom it explains. With no
		// symptom anchor (ref = newest event) this never trips.
		if anchor != nil && !e.Time.Before(anchor.Time) {
			continue
		}
		dt := ref.Time.Sub(e.Time).Seconds()
		if dt < 0 {
			dt = -dt
		}
		score := weight * math.Exp(-dt/proximityTauSeconds) * entityMatch(workload, e.Target)
		out = append(out, scoredCause{event: e, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].event.Time.After(out[j].event.Time)
	})
	return out
}

// causeWeight returns the causal weight of a change kind and whether the kind
// is state-altering at all (symptoms and bare events return false).
func causeWeight(kind string) (float64, bool) {
	if w, ok := changeKinds[kind]; ok {
		return w, true
	}
	if symptomKinds[kind] {
		return 0, false
	}
	// An unrecognised kind that is not a symptom is treated as a weak change
	// only when it carries no symptom semantics — otherwise it is noise. We
	// deliberately exclude unknown kinds from candidacy to avoid ranking a
	// log line as a cause; return false so it is skipped.
	return 0, false
}

// entityMatch boosts a change whose Target names the workload under
// investigation. Everything is workload-scoped already, so this only
// discriminates the occasional cross-target event (e.g. a shared ConfigMap
// rollout vs the deployment's own image bump). 1.0 on match, 0.6 otherwise;
// an empty workload cannot discriminate and scores 1.0.
func entityMatch(workload, target string) float64 {
	if workload == "" {
		return 1.0
	}
	if strings.Contains(strings.ToLower(target), strings.ToLower(workload)) {
		return 1.0
	}
	return 0.6
}

// newestEvent returns the newest event (events are newest-first, so index 0),
// or nil for an empty slice.
func newestEvent(events []change.ChangeEvent) *change.ChangeEvent {
	if len(events) == 0 {
		return nil
	}
	return &events[0]
}

// renderRanked formats up to topCandidates scored causes with their relative
// confidence (share of the total score). When symptom is non-nil each line
// notes how long before the symptom the change landed.
func renderRanked(header string, ranked []scoredCause, symptom *change.ChangeEvent) string {
	var total float64
	for _, c := range ranked {
		total += c.score
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(":\n")
	n := len(ranked)
	if n > topCandidates {
		n = topCandidates
	}
	for i := 0; i < n; i++ {
		c := ranked[i]
		pct := 0
		if total > 0 && c.score > 0 {
			// Clamp to ≥1% so a genuine (if distant) candidate never renders
			// as a misleading [0%] after rounding.
			pct = int(math.Round(100 * c.score / total))
			if pct < 1 {
				pct = 1
			}
		}
		fmt.Fprintf(&b, "  %d. [%d%%] %s", i+1, pct, describeEvent(c.event))
		if symptom != nil {
			fmt.Fprintf(&b, " — %s before symptom", shortDuration(symptom.Time.Sub(c.event.Time)))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// shortDuration renders a non-negative duration compactly: "45s", "2m", "1h3m".
// Sub-second and negative inputs clamp to "0s".
func shortDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// describeEvent renders a compact human description of an event:
// "<source> <kind> on <target>", plus its quoted summary and a before→after
// segment when present, suffixed with the UTC RFC3339 timestamp. Empty fields
// are omitted so there are no dangling separators.
func describeEvent(e *change.ChangeEvent) string {
	var b strings.Builder
	if e.Source != "" {
		b.WriteString(e.Source)
		b.WriteByte(' ')
	}
	b.WriteString(e.Kind)
	if e.Target != "" {
		b.WriteString(" on ")
		b.WriteString(e.Target)
	}
	if e.Summary != "" {
		fmt.Fprintf(&b, " %q", e.Summary)
	}
	if e.Before != "" || e.After != "" {
		fmt.Fprintf(&b, " (%s→%s)", e.Before, e.After)
	}
	fmt.Fprintf(&b, " @ %s", e.Time.UTC().Format(time.RFC3339))
	return b.String()
}
