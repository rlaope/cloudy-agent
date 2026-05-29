package tools_test

import (
	"testing"

	"github.com/rlaope/cloudy/internal/core/tools"
)

func TestPickDefaultEndpoint(t *testing.T) {
	t.Run("empty map errors", func(t *testing.T) {
		key, v, err := tools.PickDefaultEndpoint(map[string]int{}, "correlate", "prometheus endpoint")
		if err == nil {
			t.Fatalf("expected error for empty map, got key=%q v=%d", key, v)
		}
		want := "correlate: no prometheus endpoint configured"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("single entry returns it", func(t *testing.T) {
		key, v, err := tools.PickDefaultEndpoint(map[string]int{"only": 7}, "correlate", "loki endpoint")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "only" || v != 7 {
			t.Errorf("got (%q, %d), want (%q, %d)", key, v, "only", 7)
		}
	})

	t.Run("multiple entries pick deterministic sorted-first", func(t *testing.T) {
		m := map[string]int{"prom-2": 2, "prom-1": 1, "prom-3": 3}
		key, v, err := tools.PickDefaultEndpoint(m, "correlate", "prometheus endpoint")
		if err != nil {
			t.Fatalf("unexpected error for multi-endpoint map: %v", err)
		}
		if key != "prom-1" || v != 1 {
			t.Errorf("got (%q, %d), want (%q, %d)", key, v, "prom-1", 1)
		}
	})
}
