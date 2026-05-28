package correlate

import (
	"fmt"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// changeKinds is the set of state-altering change event kinds used by
// candidateCauseV2 to distinguish a cause from a symptom or a bare event.
var changeKinds = map[string]bool{
	"image":             true,
	"rollout":           true,
	"scale":             true,
	"sync":              true,
	"container_restart": true,
	"container_create":  true,
	"image_pull":        true,
}

// symptomKinds is the set of observable symptom event kinds.
var symptomKinds = map[string]bool{
	"log_error":     true,
	"metric_breach": true,
	"trace_error":   true,
	"trace_slow":    true,
}

// candidateCauseV2 aligns a symptom with the change most likely to have caused
// it. events must be newest-first (MergeSorted output).
//
//  1. Find the earliest symptom event (smallest Time among symptomKinds).
//  2. If a symptom exists: find the most recent changeKinds event strictly
//     before that symptom.
//     - Found → "candidate cause: <change> — preceded symptom <symptom>"
//     - Not found → "candidate cause: none — earliest symptom <symptom> has no preceding change found in window"
//  3. No symptom → fall back to the most recent changeKinds event:
//     "candidate cause: <change>".
//  4. No qualifying change at all → "candidate cause: none — no state-altering change in the window".
//
// Each <change>/<symptom> is rendered by describeEvent.
func candidateCauseV2(events []change.ChangeEvent) string {
	// Find the earliest (oldest) symptom.
	var earliestSymptom *change.ChangeEvent
	for i := range events {
		e := &events[i]
		if !symptomKinds[e.Kind] {
			continue
		}
		if earliestSymptom == nil || e.Time.Before(earliestSymptom.Time) {
			earliestSymptom = e
		}
	}

	if earliestSymptom != nil {
		// events is newest-first, so the first changeKinds entry with
		// Time < symptom.Time is the most recent change before the symptom.
		for i := range events {
			e := &events[i]
			if changeKinds[e.Kind] && e.Time.Before(earliestSymptom.Time) {
				return fmt.Sprintf("candidate cause: %s — preceded symptom %s",
					describeEvent(e), describeEvent(earliestSymptom))
			}
		}
		return fmt.Sprintf(
			"candidate cause: none — earliest symptom %s has no preceding change found in window",
			describeEvent(earliestSymptom),
		)
	}

	// No symptom: fall back to the most recent change event.
	for i := range events {
		e := &events[i]
		if changeKinds[e.Kind] {
			return "candidate cause: " + describeEvent(e)
		}
	}
	return "candidate cause: none — no state-altering change in the window"
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
