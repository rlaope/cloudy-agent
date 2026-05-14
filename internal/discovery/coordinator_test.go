package discovery

import (
	"context"
	"testing"
	"time"
)

// fakeDetector is a configurable Detector used only by these tests.
type fakeDetector struct {
	name     string
	findings []Finding
	panicMsg string
	sleep    time.Duration
}

func (f *fakeDetector) Name() string { return f.name }

func (f *fakeDetector) Detect(ctx context.Context, _ Env) []Finding {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return nil
		}
	}
	return f.findings
}

func TestRun_AggregatesAndStableSorts(t *testing.T) {
	Reset()
	defer Reset()

	// Two detectors with findings that exercise the multi-key sort order.
	Register(&fakeDetector{
		name: "tools.alpha",
		findings: []Finding{
			{
				Group:       GroupLog,
				Kind:        "loki",
				Source:      Source{Context: "prod", Namespace: "obs", ServiceName: "loki"},
				EndpointURL: "https://example/loki",
				Confidence:  0.9,
			},
		},
	})
	Register(&fakeDetector{
		name: "tools.beta",
		findings: []Finding{
			{
				Group:       GroupProm,
				Kind:        "prometheus",
				Source:      Source{Context: "prod", Namespace: "obs", ServiceName: "prom"},
				EndpointURL: "https://example/prom",
				Confidence:  0.95,
			},
		},
	})

	got := Run(context.Background(), Env{})
	if len(got) != 2 {
		t.Fatalf("Run: got %d findings, want 2", len(got))
	}
	// GroupLog ("log") sorts before GroupProm ("prom") alphabetically.
	if got[0].Group != GroupLog {
		t.Errorf("Run[0].Group: got %q, want %q", got[0].Group, GroupLog)
	}
	if got[1].Group != GroupProm {
		t.Errorf("Run[1].Group: got %q, want %q", got[1].Group, GroupProm)
	}
}

func TestRun_StableSortByMultipleKeys(t *testing.T) {
	Reset()
	defer Reset()

	// One detector emits multiple findings in the same Group; sort must fall
	// back to Kind, then Source fields.
	Register(&fakeDetector{
		name: "tools.multi",
		findings: []Finding{
			{Group: GroupDB, Kind: "redis", Source: Source{Context: "b", Namespace: "x"}},
			{Group: GroupDB, Kind: "postgres", Source: Source{Context: "a", Namespace: "x"}},
			{Group: GroupDB, Kind: "postgres", Source: Source{Context: "a", Namespace: "y"}},
			{Group: GroupDB, Kind: "mysql", Source: Source{Context: "a", Namespace: "x"}},
		},
	})

	got := Run(context.Background(), Env{})
	if len(got) != 4 {
		t.Fatalf("Run: got %d findings, want 4", len(got))
	}
	wantKinds := []string{"mysql", "postgres", "postgres", "redis"}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("Run[%d].Kind: got %q, want %q", i, got[i].Kind, k)
		}
	}
	// Within the two "postgres" findings, Namespace ascends after Context tie.
	if got[1].Source.Namespace != "x" || got[2].Source.Namespace != "y" {
		t.Errorf("postgres ordering by namespace: got [%q,%q], want [x,y]",
			got[1].Source.Namespace, got[2].Source.Namespace)
	}
}

func TestRun_RecoversFromPanic(t *testing.T) {
	Reset()
	defer Reset()

	Register(&fakeDetector{name: "tools.boom", panicMsg: "boom"})
	Register(&fakeDetector{
		name: "tools.ok",
		findings: []Finding{
			{Group: GroupTrace, Kind: "tempo", Source: Source{Context: "c", Namespace: "n"}},
		},
	})

	got := Run(context.Background(), Env{})
	if len(got) != 1 {
		t.Fatalf("Run: got %d findings, want 1 (panicking detector skipped)", len(got))
	}
	if got[0].Kind != "tempo" {
		t.Errorf("Run[0].Kind: got %q, want %q", got[0].Kind, "tempo")
	}
}

func TestRun_RespectsCallerDeadline(t *testing.T) {
	Reset()
	defer Reset()

	// Sleeper outlasts the caller's deadline; it returns nil and other
	// detectors still report their findings. This exercises the "ctx has
	// its own deadline" branch without waiting 30s for the default.
	Register(&fakeDetector{name: "tools.sleeper", sleep: 5 * time.Second})
	Register(&fakeDetector{
		name: "tools.fast",
		findings: []Finding{
			{Group: GroupPerf, Kind: "pprof", Source: Source{Context: "c", Namespace: "n"}},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := Run(ctx, Env{})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run took %v; expected to return shortly after 50ms deadline", elapsed)
	}
	if len(got) != 1 {
		t.Fatalf("Run: got %d findings, want 1 (sleeper should yield nil)", len(got))
	}
	if got[0].Kind != "pprof" {
		t.Errorf("Run[0].Kind: got %q, want %q", got[0].Kind, "pprof")
	}
}

func TestRegister_DuplicateNamePanics(t *testing.T) {
	Reset()
	defer Reset()

	Register(&fakeDetector{name: "tools.dup"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register: expected panic on duplicate Name, got none")
		}
	}()
	Register(&fakeDetector{name: "tools.dup"})
}

func TestRegister_NilAndEmptyNamePanic(t *testing.T) {
	Reset()
	defer Reset()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Register(nil): expected panic, got none")
			}
		}()
		Register(nil)
	}()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Register(empty name): expected panic, got none")
			}
		}()
		Register(&fakeDetector{name: ""})
	}()
}

func TestAll_SortedByName(t *testing.T) {
	Reset()
	defer Reset()

	Register(&fakeDetector{name: "tools.c"})
	Register(&fakeDetector{name: "tools.a"})
	Register(&fakeDetector{name: "tools.b"})

	got := All()
	wantOrder := []string{"tools.a", "tools.b", "tools.c"}
	if len(got) != len(wantOrder) {
		t.Fatalf("All: got %d, want %d", len(got), len(wantOrder))
	}
	for i, name := range wantOrder {
		if got[i].Name() != name {
			t.Errorf("All[%d]: got %q, want %q", i, got[i].Name(), name)
		}
	}
}

func TestRun_EmptyRegistryReturnsNil(t *testing.T) {
	Reset()
	defer Reset()

	if got := Run(context.Background(), Env{}); got != nil {
		t.Errorf("Run with empty registry: got %v, want nil", got)
	}
}
