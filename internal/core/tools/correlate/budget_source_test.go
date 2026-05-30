package correlate

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// promReturning serves a constant scalar value for every instant query, so the
// 5m and 1h burn-window queries both see the same ratio.
func promReturning(t *testing.T, value string) (*promclient.Client, *httptest.Server) {
	t.Helper()
	body := `{"status":"success","data":{"resultType":"scalar","result":[1000,"` + value + `"]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	c, err := promclient.NewClient(srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

func TestBudgetSource_FastBurnEmitsSymptom(t *testing.T) {
	// ratio 0.02 with target 0.999 (budget 0.001) → burn 20× ≥ 14.4 on both
	// windows → a budget_burn symptom is folded.
	c, srv := promReturning(t, "0.02")
	defer srv.Close()

	src := newBudgetSource(map[string]*promclient.Client{"p": c}, "rate(err[$window])/rate(total[$window])", 0.999)
	if src == nil {
		t.Fatal("newBudgetSource returned nil for valid args")
	}
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 budget_burn event, got %d", len(evs))
	}
	e := evs[0]
	if e.Kind != "budget_burn" || e.Source != "slo" {
		t.Errorf("event kind/source = %s/%s, want budget_burn/slo", e.Kind, e.Source)
	}
	if !strings.Contains(e.Summary, "burning fast") {
		t.Errorf("summary = %q, want a fast-burn description", e.Summary)
	}
}

func TestBudgetSource_BelowThresholdNoSymptom(t *testing.T) {
	// ratio 0.0005 → burn 0.5× < 14.4 → no symptom, no error.
	c, srv := promReturning(t, "0.0005")
	defer srv.Close()

	src := newBudgetSource(map[string]*promclient.Client{"p": c}, "q[$window]", 0.999)
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("a quiet budget must emit no symptom, got %d", len(evs))
	}
}

func TestNewBudgetSource_NilWhenUnusable(t *testing.T) {
	c, srv := promReturning(t, "0.02")
	defer srv.Close()
	clients := map[string]*promclient.Client{"p": c}

	cases := []struct {
		name   string
		query  string
		target float64
		prom   map[string]*promclient.Client
	}{
		{"empty query", "", 0.999, clients},
		{"target 0", "q[$window]", 0, clients},
		{"target 1", "q[$window]", 1, clients},
		{"no prom", "q[$window]", 0.999, nil},
	}
	for _, tc := range cases {
		if src := newBudgetSource(tc.prom, tc.query, tc.target); src != nil {
			t.Errorf("%s: expected nil source", tc.name)
		}
	}
}

// promPerWindow serves a different scalar value per substituted window so a
// test can express divergent 5m vs 1h burn — the multi-window anti-flap case
// the constant-value helper can't reach.
func promPerWindow(t *testing.T, byWindow map[string]string) (*promclient.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		val := "0"
		for win, v := range byWindow {
			if strings.Contains(q, "["+win+"]") {
				val = v
			}
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[1000,"` + val + `"]}}`))
	}))
	c, err := promclient.NewClient(srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// TestBudgetSource_ShortHotLongCalmDoesNotFire is the multi-window anti-flap
// case: the 5m window spikes (burn 20×) but the 1h window is calm (0.5×), so
// the budget is NOT acutely burning and no symptom is folded.
func TestBudgetSource_ShortHotLongCalmDoesNotFire(t *testing.T) {
	c, srv := promPerWindow(t, map[string]string{"5m": "0.02", "1h": "0.0005"})
	defer srv.Close()

	src := newBudgetSource(map[string]*promclient.Client{"p": c}, "rate(e[$window])", 0.999)
	evs, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("a hot short window with a calm long window must not fire, got %d", len(evs))
	}
}

// TestBudgetSource_QueryErrorPropagates pins that a real query failure surfaces
// as an error (correlate notes the failed source) instead of a silent clean
// timeline during the incident this is meant to catch.
func TestBudgetSource_QueryErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c, err := promclient.NewClient(srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	src := newBudgetSource(map[string]*promclient.Client{"p": c}, "q[$window]", 0.999)
	if _, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "x"}); err == nil {
		t.Error("a query failure must propagate as an error, not collapse to no-data")
	}
}

func TestInstantScalar(t *testing.T) {
	if _, ok := instantScalar(nil); ok {
		t.Error("nil result must be no-data")
	}
	if v, ok := instantScalar(&promclient.Result{Scalar: &promclient.Sample{Value: 0.3}}); !ok || v != 0.3 {
		t.Errorf("scalar should yield its value, got %g,%v", v, ok)
	}
	one := &promclient.Result{Vector: []promclient.Sample{{Value: 0.4}}}
	if v, ok := instantScalar(one); !ok || v != 0.4 {
		t.Errorf("single-series vector should yield its value, got %g,%v", v, ok)
	}
	multi := &promclient.Result{Vector: []promclient.Sample{{Value: 0.1}, {Value: 0.2}}}
	if _, ok := instantScalar(multi); ok {
		t.Error("multi-series vector must be no-data (forgot to aggregate)")
	}
	nan := &promclient.Result{Scalar: &promclient.Sample{Value: math.NaN()}}
	if _, ok := instantScalar(nan); ok {
		t.Error("NaN must be no-data")
	}
}

// TestCandidateCauses_BudgetBurnIsSymptom pins that budget_burn anchors the
// causal ranking as a symptom: a preceding deploy is named its candidate cause.
func TestCandidateCauses_BudgetBurnIsSymptom(t *testing.T) {
	events := merge(
		evt("budget_burn", "error budget burning fast", t2),
		evt("image", "deployed v3.1", t1),
	)
	got := candidateCauses(events, "checkout")
	if !strings.Contains(got, "deployed v3.1") {
		t.Errorf("a deploy before the budget_burn symptom should be the candidate cause, got: %s", got)
	}
	if !strings.Contains(got, "candidate causes for symptom") {
		t.Errorf("budget_burn must be treated as a symptom, got: %s", got)
	}
}
