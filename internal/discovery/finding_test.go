package discovery

import "testing"

func TestGroupConstants(t *testing.T) {
	cases := []struct {
		got  Group
		want string
	}{
		{GroupProm, "prom"},
		{GroupLog, "log"},
		{GroupTrace, "trace"},
		{GroupDB, "db"},
		{GroupPerf, "perf"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("Group constant: got %q, want %q", string(c.got), c.want)
		}
	}
}

func TestAuthKindConstants(t *testing.T) {
	cases := []struct {
		got  AuthKind
		want string
	}{
		{AuthNone, "none"},
		{AuthBasic, "basic"},
		{AuthBearer, "bearer"},
		{AuthPassword, "password"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("AuthKind constant: got %q, want %q", string(c.got), c.want)
		}
	}
}

func TestFinding_ZeroValueIsUsable(t *testing.T) {
	// A zero-value Finding must be safe to construct and compare against
	// individual fields, since detectors populate Findings incrementally.
	var f Finding
	if f.Confidence != 0 {
		t.Errorf("zero Finding.Confidence: got %v, want 0", f.Confidence)
	}
	if f.Group != "" {
		t.Errorf("zero Finding.Group: got %q, want \"\"", string(f.Group))
	}
	if f.AuthHint.Kind != "" {
		t.Errorf("zero AuthHint.Kind: got %q, want \"\"", string(f.AuthHint.Kind))
	}
	if f.Source.External {
		t.Error("zero Source.External: got true, want false")
	}
}
