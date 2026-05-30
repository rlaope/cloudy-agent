package prom

import (
	"math"
	"strings"
	"testing"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
)

// evalStart is the fixed split point used by the scoreAnomalies tests: points
// with ts < 1000 are baseline, ts >= 1000 are eval.
var anomalyEvalStart = time.Unix(1000, 0)

// matrixOf builds a single-series Result from (ts,val) pairs.
func matrixOf(labels map[string]string, pts ...[2]float64) *promclient.Result {
	return &promclient.Result{
		ResultType: "matrix",
		Matrix:     []promclient.Series{{Labels: labels, Values: pts}},
	}
}

func TestScoreAnomalies_DetectsSpike(t *testing.T) {
	// Baseline ~10 (tight), eval spikes to 100 → clear anomaly.
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 10}, [2]float64{200, 11}, [2]float64{300, 9}, [2]float64{400, 10},
		[2]float64{1000, 100},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.anomalies != 1 {
		t.Fatalf("want 1 anomaly, got %d", rep.anomalies)
	}
	s := rep.series[0]
	if !s.anomalous {
		t.Errorf("series should be anomalous: %+v", s)
	}
	if s.evalPeak != 100 {
		t.Errorf("eval peak = %g, want 100", s.evalPeak)
	}
	if s.maxAbsZ < 3.0 {
		t.Errorf("max|z| = %g, want >= 3", s.maxAbsZ)
	}
}

func TestScoreAnomalies_NormalWithinBand(t *testing.T) {
	// Eval value sits inside the baseline spread → not anomalous.
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 10}, [2]float64{200, 20}, [2]float64{300, 10}, [2]float64{400, 20},
		[2]float64{1000, 15},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.anomalies != 0 {
		t.Fatalf("want 0 anomalies, got %d: %+v", rep.anomalies, rep.series)
	}
	if rep.series[0].anomalous {
		t.Errorf("series should be normal: %+v", rep.series[0])
	}
}

func TestScoreAnomalies_InsufficientBaseline(t *testing.T) {
	// Only 2 baseline points (< minBaselinePoints) → insufficient, never scored.
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 10}, [2]float64{200, 11},
		[2]float64{1000, 999},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.anomalies != 0 {
		t.Fatalf("insufficient baseline must not count as anomaly, got %d", rep.anomalies)
	}
	if !rep.series[0].insufficient {
		t.Errorf("series should be flagged insufficient: %+v", rep.series[0])
	}
}

func TestScoreAnomalies_FlatBaselineMove(t *testing.T) {
	// Baseline pinned at 5 (stddev 0); eval moves to 6 → infinite z, anomalous.
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 5}, [2]float64{200, 5}, [2]float64{300, 5}, [2]float64{400, 5},
		[2]float64{1000, 6},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.anomalies != 1 {
		t.Fatalf("flat baseline + move should be anomalous, got %d", rep.anomalies)
	}
	if !math.IsInf(rep.series[0].maxAbsZ, 1) {
		t.Errorf("flat-baseline move should yield +Inf z, got %g", rep.series[0].maxAbsZ)
	}
}

func TestScoreAnomalies_FlatBaselineStill(t *testing.T) {
	// Baseline pinned at 5, eval stays at 5 → not anomalous (no movement).
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 5}, [2]float64{200, 5}, [2]float64{300, 5}, [2]float64{400, 5},
		[2]float64{1000, 5},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.anomalies != 0 {
		t.Fatalf("flat baseline with no movement must be normal, got %d", rep.anomalies)
	}
}

func TestScoreAnomalies_RanksAnomalousFirst(t *testing.T) {
	res := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{Labels: map[string]string{"s": "calm"}, Values: [][2]float64{
				{100, 10}, {200, 10}, {300, 10}, {400, 10}, {1000, 10},
			}},
			{Labels: map[string]string{"s": "spike"}, Values: [][2]float64{
				{100, 10}, {200, 11}, {300, 9}, {400, 10}, {1000, 80},
			}},
			{Labels: map[string]string{"s": "thin"}, Values: [][2]float64{
				{100, 1}, {1000, 50},
			}},
		},
	}
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	if rep.series[0].labels != "{s=spike}" {
		t.Errorf("anomalous series should rank first, got %q", rep.series[0].labels)
	}
	// The insufficient-baseline series must sink to the bottom.
	last := rep.series[len(rep.series)-1]
	if !last.insufficient || last.labels != "{s=thin}" {
		t.Errorf("insufficient series should rank last, got %+v", last)
	}
}

func TestMeanStd(t *testing.T) {
	mean, std := meanStd([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if mean != 5 {
		t.Errorf("mean = %g, want 5", mean)
	}
	if math.Abs(std-2) > 1e-9 {
		t.Errorf("std = %g, want 2", std)
	}
}

func TestAnomalyReport_Observation(t *testing.T) {
	res := matrixOf(map[string]string{"job": "x"},
		[2]float64{100, 10}, [2]float64{200, 11}, [2]float64{300, 9}, [2]float64{400, 10},
		[2]float64{1000, 100},
	)
	rep := scoreAnomalies(res, anomalyEvalStart, 3.0)
	tbl, text := rep.observation("up", time.Hour, 5*time.Minute, 3.0)
	if tbl == nil || len(tbl.Rows) != 1 {
		t.Fatalf("expected a 1-row table, got %+v", tbl)
	}
	if !strings.Contains(text, "1/1 series anomalous") {
		t.Errorf("summary should report the anomaly count, got %q", text)
	}
	if tbl.Rows[0][len(tbl.Headers)-1] != "ANOMALY" {
		t.Errorf("verdict cell should be ANOMALY, got %q", tbl.Rows[0][len(tbl.Headers)-1])
	}
}

func TestAnomalyReport_EmptyResult(t *testing.T) {
	rep := scoreAnomalies(&promclient.Result{ResultType: "matrix"}, anomalyEvalStart, 3.0)
	tbl, text := rep.observation("up", time.Hour, 5*time.Minute, 3.0)
	if tbl != nil {
		t.Errorf("empty result should produce no table, got %+v", tbl)
	}
	if !strings.Contains(text, "no series") {
		t.Errorf("expected a no-series message, got %q", text)
	}
}
