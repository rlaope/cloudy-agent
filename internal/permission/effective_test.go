package permission

import "testing"

func TestEffectiveInt(t *testing.T) {
	cases := []struct {
		profile, fallback, want int
		name                    string
	}{
		{0, 5000, 5000, "zero-profile uses fallback"},
		{100, 5000, 100, "profile narrower than fallback wins"},
		{5000, 100, 100, "fallback narrower than profile wins"},
		{0, 0, 0, "both zero stays zero"},
		{500, 0, 500, "fallback zero uses profile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveInt(c.profile, c.fallback); got != c.want {
				t.Errorf("EffectiveInt(%d, %d) = %d, want %d", c.profile, c.fallback, got, c.want)
			}
		})
	}
}

func TestEffectiveLogLines_NilProfileReturnsFallback(t *testing.T) {
	if got := EffectiveLogLines(nil, 5000); got != 5000 {
		t.Errorf("nil profile should yield fallback, got %d", got)
	}
}

func TestEffectiveLogLines_ProfileWithLimitWins(t *testing.T) {
	p := &Profile{Limits: Limits{MaxLogLines: 200}}
	if got := EffectiveLogLines(p, 5000); got != 200 {
		t.Errorf("profile limit should win, got %d", got)
	}
}

func TestEffectiveProfileSeconds_NilProfile(t *testing.T) {
	if got := EffectiveProfileSeconds(nil, 60); got != 60 {
		t.Errorf("nil profile should yield fallback, got %d", got)
	}
}
