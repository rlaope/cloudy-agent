package correlate

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// budgetWindowToken is the placeholder slo_query embeds where the range goes;
// it matches the prom.error_budget tool's contract so an operator can reuse the
// same query expression for both.
const budgetWindowToken = "$window"

// fastBurnThreshold is the fast-burn page threshold (Google SRE Workbook): a
// burn rate this high over BOTH a short and long window means the error budget
// is draining fast enough to warrant paging. It matches the prom.error_budget
// tool's top tier and the slo-burn / incident-triage skills.
const fastBurnThreshold = 14.4

const burnShortWindow = "5m"
const burnLongWindow = "1h"

// budgetSource folds an acute SLO error-budget burn onto the change timeline as
// a symptom: a ChangeEvent whose Kind is "budget_burn" and Source is "slo",
// emitted only when the bad-event ratio burns above fastBurnThreshold over BOTH
// the 5m and 1h windows (the multi-window confirmation that suppresses
// flapping). The query and target are per-call args, so this source is built at
// Run time like the metric source.
//
// Only the fast-burn tier is folded — a slow burn is a ticket, not an acute
// symptom worth anchoring a correlation timeline. The full multi-tier verdict
// (ticket/ok, consumed/remaining, time-to-exhaustion) lives in the
// prom.error_budget tool; this source answers only "is the budget acutely
// burning right now".
type budgetSource struct {
	clients map[string]*promclient.Client
	query   string
	target  float64
}

// newBudgetSource builds a budgetSource. It returns nil — so callers can omit
// the source — when there is no query, no usable target, or no Prometheus
// backend.
func newBudgetSource(prom map[string]*promclient.Client, query string, target float64) change.ChangeSource {
	if query == "" || target <= 0 || target >= 1 || len(prom) == 0 {
		return nil
	}
	return &budgetSource{clients: prom, query: query, target: target}
}

func (s *budgetSource) Name() string { return "slo" }

// RecentChanges evaluates the bad-event ratio at the short and long burn
// windows and emits one "budget_burn" symptom when both windows exceed the
// fast-burn threshold. The short window is evaluated first and the long-window
// query is skipped when the short window can't fire.
//
// A genuine query failure (auth, Prometheus outage, malformed expression) is
// propagated as an error — correlate records it as a failed-source note rather
// than showing a clean timeline during the exact incident this is meant to
// catch. A quiet budget (ratio present but burn below threshold) or empty/
// non-finite data yields no event and no error, matching the metric source's
// partial tolerance. The Prometheus backend is the deterministic default.
func (s *budgetSource) RecentChanges(ctx context.Context, _ change.ChangeQuery) ([]change.ChangeEvent, error) {
	_, client, err := tools.PickDefaultEndpoint(s.clients, "correlate", "prometheus endpoint")
	if err != nil {
		return nil, err
	}

	now := time.Now()
	budget := 1 - s.target
	// burnAt returns (burn, hasData, error): error = the query failed and must
	// be surfaced; hasData=false = empty/non-finite result (a quiet "no data").
	burnAt := func(window string) (float64, bool, error) {
		q := strings.ReplaceAll(s.query, budgetWindowToken, window)
		res, qerr := client.Query(ctx, q, now)
		if qerr != nil {
			return 0, false, qerr
		}
		v, ok := instantScalar(res)
		if !ok {
			return 0, false, nil
		}
		return v / budget, true, nil
	}

	burnShort, okS, err := burnAt(burnShortWindow)
	if err != nil {
		return nil, err
	}
	if !okS || burnShort < fastBurnThreshold {
		return nil, nil
	}
	burnLong, okL, err := burnAt(burnLongWindow)
	if err != nil {
		return nil, err
	}
	if !okL || burnLong < fastBurnThreshold {
		return nil, nil
	}

	// The symptom is anchored at detection time (now), NOT at the burn's true
	// onset — unlike metric_breach, which uses the actual breach timestamp. The
	// onset is not recoverable from two instant burn-rate samples (finding it
	// would need a range scan, which is the metric source's job). One
	// consequence: when budget_burn is the ONLY symptom, every change in the
	// window precedes it, so the cause ranking leans coarser/recency-biased.
	// When a timestamped symptom (metric/log/trace) co-exists, the cause engine
	// anchors on that earlier symptom instead, and this just rides the timeline.
	return []change.ChangeEvent{{
		Time:    now,
		Kind:    "budget_burn",
		Target:  s.query,
		Summary: fmt.Sprintf("error budget burning fast (burn 5m=%.1f× 1h=%.1f×, threshold %.1f×)", burnShort, burnLong, fastBurnThreshold),
		After:   fmt.Sprintf("%.1f×", burnLong),
		Source:  "slo",
	}}, nil
}

// instantScalar extracts the single ratio from an instant query — a scalar or a
// one-element vector. A multi-series vector (the query forgot to aggregate) is
// rejected as no-data rather than picking an arbitrary series; non-finite
// values (a PromQL divide-by-zero) are rejected too.
func instantScalar(res *promclient.Result) (float64, bool) {
	if res == nil {
		return 0, false
	}
	var v float64
	switch {
	case res.Scalar != nil:
		v = res.Scalar.Value
	case len(res.Vector) == 1:
		v = res.Vector[0].Value
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
