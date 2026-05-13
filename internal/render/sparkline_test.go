package render

import (
	"math"
	"strings"
	"testing"
)

func TestSparklineKnownInputs(t *testing.T) {
	cases := []struct {
		name   string
		series []float64
		width  int
		want   string
	}{
		{
			name:   "ascending",
			series: []float64{0, 1, 2, 3, 4, 5, 6, 7},
			width:  8,
			want:   "▁▂▃▄▅▆▇█",
		},
		{
			name:   "flat zero",
			series: []float64{0, 0, 0},
			width:  3,
			want:   "▁▁▁",
		},
		{
			name:   "single value",
			series: []float64{5},
			width:  1,
			want:   "▁",
		},
		{
			name:   "all same non-zero",
			series: []float64{3, 3, 3},
			width:  3,
			want:   "▁▁▁",
		},
		{
			name:   "negative clipped to zero",
			series: []float64{-5, 0, 5},
			width:  3,
			// -5 clipped to 0; range is [0,5]; indices: 0,0,7 → ▁▁█
			want: "▁▁█",
		},
		{
			name:   "NaN renders as space",
			series: []float64{math.NaN(), 1},
			width:  2,
			// Only one non-NaN value (1); min==max so flat → ▁
			want: " ▁",
		},
		{
			name:   "width limits output",
			series: []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
			width:  4,
			// Only the last 4 values: 6,7,8,9
			// range [6,9]: scaled → ▁▃▆█
			want: "▁▃▆█",
		},
		{
			name:   "empty",
			series: []float64{},
			width:  5,
			want:   "",
		},
		{
			name:   "zero width",
			series: []float64{1, 2, 3},
			width:  0,
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.series, tc.width)
			if got != tc.want {
				t.Errorf("Render(%v, %d)\n  got:  %q\n  want: %q", tc.series, tc.width, got, tc.want)
			}
		})
	}
}

func TestSparklineAllNaN(t *testing.T) {
	series := []float64{math.NaN(), math.NaN(), math.NaN()}
	got := Render(series, 3)
	if got != "   " {
		t.Errorf("all-NaN: got %q, want three spaces", got)
	}
}

func TestSparklineNoExceedWidth(t *testing.T) {
	series := make([]float64, 200)
	for i := range series {
		series[i] = float64(i)
	}
	for _, w := range []int{10, 50, 100} {
		got := Render(series, w)
		if strings.Count(got, "") > w+1 { // rune count check
			t.Errorf("output exceeds width %d: %q", w, got)
		}
	}
}
