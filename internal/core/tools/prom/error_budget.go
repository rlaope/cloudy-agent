package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// windowToken is the placeholder the caller embeds in error_query; the tool
// substitutes each evaluation window (e.g. "[5m]", "[1h]") for it.
const windowToken = "$window"

const defaultSLOWindow = "30d"

// budgetTier is one multi-window multi-burn-rate detector. A tier fires only
// when the bad-event ratio exceeds the threshold over BOTH its short and long
// window — the short window confirms the burn is still happening, the long
// window suppresses flapping (Google SRE Workbook). Thresholds match the
// slo-burn / incident-triage skills so the tool and the playbooks agree.
type budgetTier struct {
	short     string
	long      string
	threshold float64
	severity  string // "page" or "ticket"
}

var budgetTiers = []budgetTier{
	{short: "5m", long: "1h", threshold: 14.4, severity: "page"},
	{short: "30m", long: "6h", threshold: 6, severity: "page"},
	{short: "6h", long: "24h", threshold: 3, severity: "ticket"},
}

// ErrorBudgetTool implements prom.error_budget.
type ErrorBudgetTool struct {
	clients    map[string]*promclient.Client
	defaultKey string
}

// NewErrorBudgetTool constructs an ErrorBudgetTool over the shared clients.
func NewErrorBudgetTool(clients map[string]*promclient.Client) *ErrorBudgetTool {
	return &ErrorBudgetTool{clients: clients, defaultKey: firstKey(clients)}
}

func (t *ErrorBudgetTool) Name() string { return "prom.error_budget" }

func (t *ErrorBudgetTool) Description() string {
	return "Compute SLO error-budget burn from a bad-event ratio query: multi-window multi-burn-rate analysis (5m/1h, 30m/6h, 6h/24h), budget consumed/remaining, current burn rate, and time-to-exhaustion, with a page-now / ticket / ok verdict. error_query must be a PromQL expression returning the bad-event ratio (0..1) with the literal token $window where the range goes, e.g. sum(rate(http_5xx[$window]))/sum(rate(http_total[$window])). Read-only."
}

func (t *ErrorBudgetTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint":    strProp("Named Prometheus endpoint (empty = default)."),
		"error_query": strProp("PromQL returning the bad-event ratio (0..1) with the literal token $window where the range selector goes, e.g. \"sum(rate(http_5xx[$window]))/sum(rate(http_total[$window]))\"."),
		"slo_target":  map[string]any{"type": "number", "description": "SLO target as a fraction in (0,1), e.g. 0.999 for 99.9%."},
		"slo_window":  strProp("SLO budget window as a duration (e.g. \"30d\", \"7d\"); default \"30d\". Carries the time dimension for time-to-exhaustion."),
	}, []string{"error_query", "slo_target"})
}

// Risk implements tools.RiskRated: read-only instant queries plus arithmetic.
func (t *ErrorBudgetTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *ErrorBudgetTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint   string  `json:"endpoint"`
		ErrorQuery string  `json:"error_query"`
		SLOTarget  float64 `json:"slo_target"`
		SLOWindow  string  `json:"slo_window"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("prom.error_budget: parse args: %w", err)
		}
	}
	if a.ErrorQuery == "" {
		return tools.Observation{}, fmt.Errorf("prom.error_budget: error_query is required")
	}
	if !strings.Contains(a.ErrorQuery, windowToken) {
		return tools.Observation{}, fmt.Errorf("prom.error_budget: error_query must contain the %s token where the range selector goes", windowToken)
	}
	if a.SLOTarget <= 0 || a.SLOTarget >= 1 {
		return tools.Observation{}, fmt.Errorf("prom.error_budget: slo_target must be a fraction in (0,1), e.g. 0.999")
	}
	sloWindow := a.SLOWindow
	if sloWindow == "" {
		sloWindow = defaultSLOWindow
	}
	if _, err := parseWindowDuration(sloWindow); err != nil {
		return tools.Observation{}, fmt.Errorf("prom.error_budget: slo_window %q: %w", sloWindow, err)
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("prom.error_budget: unknown endpoint %q", a.Endpoint)
	}

	now := time.Now()
	// ratioAt evaluates error_query with the given window substituted. A query
	// error or a non-finite result is treated as "no data" for that window so a
	// single empty window doesn't sink the whole budget readout; computeBudget
	// reports which windows were missing.
	ratioAt := func(window string) (float64, bool) {
		q := strings.ReplaceAll(a.ErrorQuery, windowToken, window)
		res, err := c.Query(ctx, q, now)
		if err != nil {
			return 0, false
		}
		v, ok := scalarOrFirst(res)
		if !ok || !isFinite(v) {
			return 0, false
		}
		return v, true
	}

	rep := computeBudget(ratioAt, a.SLOTarget, sloWindow)
	tbl, text := rep.observation(a.SLOTarget, sloWindow)
	return tools.Observation{Text: text, Table: tbl, Raw: rep}, nil
}

// tierResult is one tier's evaluated burn.
type tierResult struct {
	Pair      string  `json:"pair"`
	Threshold float64 `json:"threshold"`
	Severity  string  `json:"severity"`
	BurnShort float64 `json:"burn_short"`
	BurnLong  float64 `json:"burn_long"`
	HaveData  bool    `json:"have_data"`
	Fires     bool    `json:"fires"`
}

// budgetReport is the full error-budget readout.
type budgetReport struct {
	Tiers        []tierResult `json:"tiers"`
	Verdict      string       `json:"verdict"`
	ConsumedPct  float64      `json:"consumed_pct"`
	RemainingPct float64      `json:"remaining_pct"`
	HaveConsumed bool         `json:"have_consumed"`
	HeadlineBurn float64      `json:"headline_burn"`
	HaveHeadline bool         `json:"have_headline"`
	Exhaustion   string       `json:"time_to_exhaustion"`
}

// computeBudget is the pure core: given a window→ratio lookup, the SLO target,
// and the budget window, it evaluates every tier, the consumed budget over the
// full window, the headline burn rate, and the time-to-exhaustion. ratioAt
// returns (ratio, ok); ok=false means no data for that window.
func computeBudget(ratioAt func(window string) (float64, bool), target float64, sloWindow string) budgetReport {
	budget := 1 - target // fraction of events we're allowed to fail

	// Memoize so a window shared across tiers is queried once.
	cache := map[string]struct {
		v  float64
		ok bool
	}{}
	ratio := func(w string) (float64, bool) {
		if c, seen := cache[w]; seen {
			return c.v, c.ok
		}
		v, ok := ratioAt(w)
		cache[w] = struct {
			v  float64
			ok bool
		}{v, ok}
		return v, ok
	}
	burnAt := func(w string) (float64, bool) {
		r, ok := ratio(w)
		if !ok {
			return 0, false
		}
		return r / budget, true
	}

	var rep budgetReport
	worst := "ok"
	for _, t := range budgetTiers {
		bs, okS := burnAt(t.short)
		bl, okL := burnAt(t.long)
		tr := tierResult{
			Pair:      t.short + "/" + t.long,
			Threshold: t.threshold,
			Severity:  t.severity,
			BurnShort: bs,
			BurnLong:  bl,
			HaveData:  okS && okL,
		}
		tr.Fires = okS && okL && bs >= t.threshold && bl >= t.threshold
		if tr.Fires {
			worst = escalate(worst, t.severity)
		}
		rep.Tiers = append(rep.Tiers, tr)
	}
	rep.Verdict = verdictLabel(worst)

	// Headline burn = the conventional "current burn rate", the 1h-window burn
	// (the fast tier's long window). Fall back to progressively longer windows
	// when 1h has no data (e.g. a momentarily-stale recording rule) so a
	// healthy 6h/24h signal still yields a time-to-exhaustion.
	for _, w := range []string{"1h", "6h", "24h"} {
		if b, ok := burnAt(w); ok {
			rep.HeadlineBurn = b
			rep.HaveHeadline = true
			break
		}
	}

	// Consumed budget over the full SLO window.
	if r, ok := ratio(sloWindow); ok {
		consumedFrac := r / budget // fraction of the budget used
		rep.ConsumedPct = clampPct(consumedFrac * 100)
		rep.RemainingPct = clampPct((1 - consumedFrac) * 100)
		rep.HaveConsumed = true
		rep.Exhaustion = exhaustion(1-consumedFrac, rep.HeadlineBurn, rep.HaveHeadline, sloWindow)
	}
	return rep
}

// escalate returns the higher-severity of two verdict levels.
func escalate(cur, next string) string {
	rank := map[string]int{"ok": 0, "ticket": 1, "page": 2}
	if rank[next] > rank[cur] {
		return next
	}
	return cur
}

func verdictLabel(worst string) string {
	switch worst {
	case "page":
		return "PAGE — budget burning fast"
	case "ticket":
		return "TICKET — budget burning slowly"
	default:
		return "OK — within budget burn limits"
	}
}

// exhaustion renders time-to-exhaustion = W × remaining_fraction / burn. A burn
// below 1 is not draining the budget within the window.
func exhaustion(remainingFrac, burn float64, haveBurn bool, sloWindow string) string {
	if !haveBurn || remainingFrac <= 0 {
		if remainingFrac <= 0 {
			return "budget already exhausted"
		}
		return "n/a (no current burn signal)"
	}
	if burn < 1 {
		return "not on track to breach (burn < 1×)"
	}
	w, err := parseWindowDuration(sloWindow)
	if err != nil {
		return "n/a"
	}
	ttx := time.Duration(float64(w) * remainingFrac / burn)
	return shortBudgetDuration(ttx)
}

func (rep budgetReport) observation(target float64, sloWindow string) (*render.Table, string) {
	tbl := &render.Table{
		Headers: []string{"WINDOW", "BURN_SHORT", "BURN_LONG", "THRESHOLD", "FIRES"},
		Aligns: []render.Align{
			render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignLeft,
		},
	}
	for _, t := range rep.Tiers {
		fires := "no"
		if !t.HaveData {
			fires = "no data"
		} else if t.Fires {
			fires = "YES (" + t.Severity + ")"
		}
		tbl.Rows = append(tbl.Rows, []string{
			t.Pair,
			burnCell(t.BurnShort, t.HaveData),
			burnCell(t.BurnLong, t.HaveData),
			fmt.Sprintf("%.1f×", t.Threshold),
			fires,
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SLO target=%.4g%% budget=%.4g%% window=%s → %s\n",
		target*100, (1-target)*100, sloWindow, rep.Verdict)
	if rep.HaveConsumed {
		fmt.Fprintf(&b, "budget consumed=%.1f%% remaining=%.1f%%", rep.ConsumedPct, rep.RemainingPct)
	} else {
		b.WriteString("budget consumed=n/a (no data over the SLO window)")
	}
	if rep.HaveHeadline {
		fmt.Fprintf(&b, ", current burn=%.2g×", rep.HeadlineBurn)
	}
	if rep.Exhaustion != "" {
		fmt.Fprintf(&b, ", time-to-exhaustion=%s", rep.Exhaustion)
	}
	return tbl, b.String()
}

func burnCell(v float64, have bool) string {
	if !have {
		return "—"
	}
	return fmt.Sprintf("%.2g×", v)
}

// scalarOrFirst extracts the single float an error-budget ratio query must
// return — a scalar, or a one-element vector. A multi-series vector means the
// author forgot to aggregate (e.g. no sum()), so picking an arbitrary series'
// ratio would silently mislead the budget verdict; that is reported as no-data
// instead, surfacing as "no data" in the per-window row so the operator fixes
// the query rather than trusting a wrong burn rate.
func scalarOrFirst(res *promclient.Result) (float64, bool) {
	if res == nil {
		return 0, false
	}
	if res.Scalar != nil {
		return res.Scalar.Value, true
	}
	if len(res.Vector) == 1 {
		return res.Vector[0].Value, true
	}
	return 0, false
}

func clampPct(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// parseWindowDuration parses a Prometheus-style window ("30d", "6h", "90m").
// time.ParseDuration cannot handle the day unit, so days are expanded first.
func parseWindowDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid day window")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid window")
	}
	return d, nil
}

// shortBudgetDuration renders a duration as days/hours for the exhaustion line.
func shortBudgetDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		days := d.Hours() / 24
		return fmt.Sprintf("%.1f days", days)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}
