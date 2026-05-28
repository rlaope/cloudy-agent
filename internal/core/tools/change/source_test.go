package change

import (
	"testing"
	"time"
)

func TestMergeSorted(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	a := []ChangeEvent{
		{Time: base, Kind: "image", Source: "k8s"},
		{Time: base.Add(-2 * time.Hour), Kind: "scale", Source: "k8s"},
	}
	b := []ChangeEvent{
		{Time: base.Add(-1 * time.Hour), Kind: "container_restart", Source: "docker"},
	}

	t.Run("sorts newest first across groups", func(t *testing.T) {
		got := MergeSorted(0, a, b)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		wantKinds := []string{"image", "container_restart", "scale"}
		for i, k := range wantKinds {
			if got[i].Kind != k {
				t.Errorf("got[%d].Kind = %q, want %q", i, got[i].Kind, k)
			}
		}
	})

	t.Run("applies positive limit", func(t *testing.T) {
		got := MergeSorted(2, a, b)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Kind != "image" || got[1].Kind != "container_restart" {
			t.Errorf("limited result = %q/%q, want image/container_restart", got[0].Kind, got[1].Kind)
		}
	})

	t.Run("zero limit means no cap", func(t *testing.T) {
		if got := MergeSorted(0, a, b); len(got) != 3 {
			t.Errorf("len = %d, want 3 (no cap)", len(got))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := MergeSorted(0); got != nil {
			t.Errorf("MergeSorted() = %v, want nil", got)
		}
		if got := MergeSorted(5, nil, nil); got != nil {
			t.Errorf("MergeSorted(nil, nil) = %v, want nil", got)
		}
	})
}
