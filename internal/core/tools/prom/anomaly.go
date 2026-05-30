package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

const (
	defaultAnomalyBaseline = time.Hour
	defaultAnomalyEval     = 5 * time.Minute
	defaultZThreshold      = 3.0
	// minBaselinePoints is the fewest baseline samples needed for a stddev to
	// mean anything; below this a series is reported as "insufficient baseline"
	// rather than producing a noisy z-score off one or two points.
	minBaselinePoints = 4
	// anomalyMaxPoints mirrors maxRangePoints intent — keep the matrix small.
	anomalyMaxPoints = 1000
)

// AnomalyTool implements prom.anomaly: it learns a per-series baseline (mean +
// stddev) over a reference window and flags the recent eval window when its
// values deviate by at least z_threshold standard deviations. This turns
// Prometheus from point-in-time threshold checks into baseline-relative
// anomaly detection — a metric can breach "normal" without crossing any fixed
// line, which a static threshold misses.
type AnomalyTool struct {
	clients    map[string]*promclient.Client
	defaultKey string
}

// NewAnomalyTool constructs an AnomalyTool over the shared Prometheus clients.
func NewAnomalyTool(clients map[string]*promclient.Client) *AnomalyTool {
	return &AnomalyTool{clients: clients, defaultKey: firstKey(clients)}
}

func (t *AnomalyTool) Name() string { return "prom.anomaly" }

func (t *AnomalyTool) Description() string {
	return "Detect baseline-relative anomalies in a PromQL series: learn a per-series mean and standard deviation over a baseline window, then flag the recent eval window when its values deviate by at least z_threshold standard deviations (default 3). Catches a metric that left its normal band without crossing any fixed threshold — the limitation of point-in-time checks. Read-only."
}

func (t *AnomalyTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint":    strProp("Named Prometheus endpoint (empty = default)."),
		"query":       strProp("PromQL expression to evaluate; each returned series is scored independently."),
		"baseline":    strProp("Reference window to learn normal from, as a Go duration (e.g. \"1h\", \"6h\"); default \"1h\"."),
		"eval":        strProp("Recent window tested for anomaly, as a Go duration (e.g. \"5m\", \"15m\"); default \"5m\"."),
		"z_threshold": map[string]any{"type": "number", "description": "Standard deviations of deviation at/above which the eval window is anomalous; default 3."},
	}, []string{"query"})
}

// Risk implements tools.RiskRated: prom.anomaly is a read-only range query plus
// arithmetic.
func (t *AnomalyTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *AnomalyTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint   string  `json:"endpoint"`
		Query      string  `json:"query"`
		Baseline   string  `json:"baseline"`
		Eval       string  `json:"eval"`
		ZThreshold float64 `json:"z_threshold"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("prom.anomaly: parse args: %w", err)
		}
	}
	if a.Query == "" {
		return tools.Observation{}, fmt.Errorf("prom.anomaly: query is required")
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("prom.anomaly: unknown endpoint %q", a.Endpoint)
	}

	baseline := parseDurationOr(a.Baseline, defaultAnomalyBaseline)
	eval := parseDurationOr(a.Eval, defaultAnomalyEval)
	z := a.ZThreshold
	if z <= 0 {
		z = defaultZThreshold
	}

	end := time.Now()
	evalStart := end.Add(-eval)
	start := evalStart.Add(-baseline)

	step := (baseline + eval) / 200
	if step < 15*time.Second {
		step = 15 * time.Second
	}
	if int((baseline+eval)/step) > anomalyMaxPoints {
		return tools.Observation{}, fmt.Errorf("prom.anomaly: window too large for the step; shrink baseline/eval")
	}

	res, err := c.QueryRange(ctx, a.Query, start, end, step)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.anomaly: %w", err)
	}

	report := scoreAnomalies(res, evalStart, z)
	tbl, text := report.observation(a.Query, baseline, eval, z)
	return tools.Observation{Text: text, Table: tbl, Raw: res}, nil
}

// seriesAnomaly is the per-series anomaly verdict.
type seriesAnomaly struct {
	labels        string
	baselineMean  float64
	baselineStd   float64
	baselineCount int
	evalPeak      float64
	maxAbsZ       float64
	peakAt        time.Time
	anomalous     bool
	insufficient  bool
}

// anomalyReport is the full set of per-series verdicts, anomalous first.
type anomalyReport struct {
	series    []seriesAnomaly
	anomalies int
}

// scoreAnomalies splits each series at evalStart, computes the baseline
// mean/stddev from the pre-evalStart points, and scores the eval points by
// z-score. A flat baseline (stddev 0) is treated as anomalous when any eval
// value differs from the mean at all — a metric that was pinned and then moved.
func scoreAnomalies(res *promclient.Result, evalStart time.Time, z float64) anomalyReport {
	var rep anomalyReport
	if res == nil {
		return rep
	}
	evalStartSec := float64(evalStart.Unix())

	for _, s := range res.Matrix {
		var base []float64
		var sa seriesAnomaly
		sa.labels = labelsString(s.Labels)

		// First pass: collect baseline values.
		for _, pt := range s.Values {
			if pt[0] < evalStartSec {
				base = append(base, pt[1])
			}
		}
		sa.baselineCount = len(base)
		if len(base) < minBaselinePoints {
			sa.insufficient = true
			rep.series = append(rep.series, sa)
			continue
		}
		mean, std := meanStd(base)
		sa.baselineMean = mean
		sa.baselineStd = std

		// Second pass: score eval points.
		haveEval := false
		for _, pt := range s.Values {
			if pt[0] < evalStartSec {
				continue
			}
			val := pt[1]
			var absZ float64
			if std > 0 {
				absZ = math.Abs((val - mean) / std)
			} else if val != mean {
				// Flat baseline, value moved: treat as maximally anomalous so
				// it is never silently ranked below a finite z.
				absZ = math.Inf(1)
			}
			if !haveEval || absZ > sa.maxAbsZ || (math.IsInf(absZ, 1) && !math.IsInf(sa.maxAbsZ, 1)) {
				sa.maxAbsZ = absZ
				sa.evalPeak = val
				sa.peakAt = unixToTime(pt[0])
				haveEval = true
			}
		}
		if !haveEval {
			sa.insufficient = true
			rep.series = append(rep.series, sa)
			continue
		}
		sa.anomalous = sa.maxAbsZ >= z
		if sa.anomalous {
			rep.anomalies++
		}
		rep.series = append(rep.series, sa)
	}

	// Anomalous first, then by descending |z|; insufficient series sink last.
	sort.SliceStable(rep.series, func(i, j int) bool {
		a, b := rep.series[i], rep.series[j]
		if a.insufficient != b.insufficient {
			return !a.insufficient
		}
		if a.anomalous != b.anomalous {
			return a.anomalous
		}
		return a.maxAbsZ > b.maxAbsZ
	})
	return rep
}

// observation renders the report into a table + summary text.
func (rep anomalyReport) observation(query string, baseline, eval time.Duration, z float64) (*render.Table, string) {
	if len(rep.series) == 0 {
		return nil, fmt.Sprintf("prom.anomaly: query=%q returned no series over the baseline+eval window", query)
	}
	tbl := &render.Table{
		Headers: []string{"SERIES", "BASELINE_MEAN", "BASELINE_STD", "EVAL_PEAK", "MAX_Z", "VERDICT"},
		Aligns: []render.Align{
			render.AlignLeft, render.AlignRight, render.AlignRight,
			render.AlignRight, render.AlignRight, render.AlignLeft,
		},
	}
	for _, s := range rep.series {
		verdict := "normal"
		switch {
		case s.insufficient:
			verdict = "insufficient baseline"
		case s.anomalous:
			verdict = "ANOMALY"
		}
		tbl.Rows = append(tbl.Rows, []string{
			s.labels,
			fmtMaybe(s.insufficient, s.baselineMean),
			fmtMaybe(s.insufficient, s.baselineStd),
			fmtMaybe(s.insufficient, s.evalPeak),
			zString(s),
			verdict,
		})
	}
	text := fmt.Sprintf("prom.anomaly: query=%q baseline=%s eval=%s z>=%.4g → %d/%d series anomalous",
		query, baseline, eval, z, rep.anomalies, len(rep.series))
	return tbl, text
}

// meanStd returns the mean and population standard deviation of xs. Caller
// guarantees len(xs) >= 1.
func meanStd(xs []float64) (mean, std float64) {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean = sum / float64(len(xs))
	var sq float64
	for _, x := range xs {
		d := x - mean
		sq += d * d
	}
	std = math.Sqrt(sq / float64(len(xs)))
	return mean, std
}

func parseDurationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

func unixToTime(tsSec float64) time.Time {
	return time.Unix(int64(tsSec), int64((tsSec-float64(int64(tsSec)))*1e9))
}

func fmtMaybe(insufficient bool, v float64) string {
	if insufficient {
		return "—"
	}
	return fmt.Sprintf("%.4g", v)
}

func zString(s seriesAnomaly) string {
	if s.insufficient {
		return "—"
	}
	if math.IsInf(s.maxAbsZ, 1) {
		return "∞"
	}
	return fmt.Sprintf("%.2f", s.maxAbsZ)
}
