package correlate

import (
	"context"
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
