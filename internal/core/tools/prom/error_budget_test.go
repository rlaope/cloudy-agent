package prom

import (
	"strings"
	"testing"
	"time"
)

// ratioFn builds a window→ratio lookup from a map; a missing window is "no data".
func ratioFn(m map[string]float64) func(string) (float64, bool) {
	return func(w string) (float64, bool) {
		v, ok := m[w]
		return v, ok
	}
}

const target999 = 0.999 // budget = 0.001

func TestComputeBudget_FastBurnPagesWithBudgetLeft(t *testing.T) {
	// burn = ratio/0.001. 0.02 → 20× everywhere short-term; 30d barely touched.
	rep := computeBudget(ratioFn(map[string]float64{
		"5m": 0.02, "1h": 0.02, "30m": 0.02, "6h": 0.02, "24h": 0.02, "30d": 0.0003,
	}), target999, "30d")

	if !strings.HasPrefix(rep.Verdict, "PAGE") {
		t.Fatalf("verdict = %q, want PAGE", rep.Verdict)
	}
	if rep.Tiers[0].Pair != "5m/1h" || !rep.Tiers[0].Fires {
		t.Errorf("fast tier should fire: %+v", rep.Tiers[0])
	}
	if !rep.HaveConsumed || rep.RemainingPct < 60 || rep.RemainingPct > 80 {
		t.Errorf("remaining = %.1f%%, want ~70%%", rep.RemainingPct)
	}
	if !strings.Contains(rep.Exhaustion, "day") {
		t.Errorf("exhaustion = %q, want a days estimate", rep.Exhaustion)
	}
}

func TestComputeBudget_SlowBurnTicketsOnly(t *testing.T) {
	// burn 4× everywhere: below 14.4 and 6, but >= 3 → only the 6h/24h tier fires.
	rep := computeBudget(ratioFn(map[string]float64{
		"5m": 0.004, "1h": 0.004, "30m": 0.004, "6h": 0.004, "24h": 0.004, "30d": 0.0002,
	}), target999, "30d")

	if !strings.HasPrefix(rep.Verdict, "TICKET") {
		t.Fatalf("verdict = %q, want TICKET", rep.Verdict)
	}
	for _, tr := range rep.Tiers {
		if tr.Pair == "6h/24h" && !tr.Fires {
			t.Error("6h/24h tier should fire at burn 4× (threshold 3×)")
		}
		if tr.Pair != "6h/24h" && tr.Fires {
			t.Errorf("tier %s must not fire at burn 4×", tr.Pair)
		}
	}
}

func TestComputeBudget_OKWithinLimits(t *testing.T) {
	rep := computeBudget(ratioFn(map[string]float64{
		"5m": 0.0005, "1h": 0.0005, "30m": 0.0005, "6h": 0.0005, "24h": 0.0005, "30d": 0.0001,
	}), target999, "30d")
	if !strings.HasPrefix(rep.Verdict, "OK") {
		t.Fatalf("verdict = %q, want OK", rep.Verdict)
	}
	if rep.Exhaustion != "not on track to breach (burn < 1×)" {
		t.Errorf("exhaustion = %q, want the not-on-track message (burn 0.5×)", rep.Exhaustion)
	}
}

func TestComputeBudget_ShortWindowOnlyDoesNotFire(t *testing.T) {
	// 5m hot (20×) but 1h cold (0.5×): the fast tier must NOT fire — the AND of
	// both windows is the whole point of multi-window detection.
	rep := computeBudget(ratioFn(map[string]float64{
		"5m": 0.02, "1h": 0.0005, "30m": 0.0005, "6h": 0.0005, "24h": 0.0005, "30d": 0.0001,
	}), target999, "30d")
	if rep.Tiers[0].Fires {
		t.Errorf("5m/1h must not fire when only the short window is hot: %+v", rep.Tiers[0])
	}
	if !strings.HasPrefix(rep.Verdict, "OK") {
		t.Errorf("verdict = %q, want OK (short-only blip)", rep.Verdict)
	}
}

func TestComputeBudget_ExhaustedBudget(t *testing.T) {
	rep := computeBudget(ratioFn(map[string]float64{
		"1h": 0.02, "30d": 0.0015, // consumed 150% of budget
	}), target999, "30d")
	if rep.RemainingPct != 0 {
		t.Errorf("remaining = %.1f%%, want 0", rep.RemainingPct)
	}
	if rep.Exhaustion != "budget already exhausted" {
		t.Errorf("exhaustion = %q, want already-exhausted", rep.Exhaustion)
	}
}

func TestComputeBudget_NoData(t *testing.T) {
	rep := computeBudget(ratioFn(map[string]float64{}), target999, "30d")
	if !strings.HasPrefix(rep.Verdict, "OK") {
		t.Errorf("verdict = %q, want OK with no data", rep.Verdict)
	}
	if rep.HaveConsumed {
		t.Error("HaveConsumed must be false with no data")
	}
	for _, tr := range rep.Tiers {
		if tr.HaveData {
			t.Errorf("tier %s should have no data", tr.Pair)
		}
	}
}

func TestParseWindowDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"30d": 30 * 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"6h":  6 * time.Hour,
		"90m": 90 * time.Minute,
	}
	for in, want := range cases {
		got, err := parseWindowDuration(in)
		if err != nil || got != want {
			t.Errorf("parseWindowDuration(%q) = %v,%v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "abc", "-5h", "10"} {
		if _, err := parseWindowDuration(bad); err == nil {
			t.Errorf("parseWindowDuration(%q) should error", bad)
		}
	}
}
