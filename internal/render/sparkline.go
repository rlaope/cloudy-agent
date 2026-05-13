package render

import (
	"math"
	"strings"
)

// sparkBlocks is the standard 8-level Unicode block sequence.
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Render produces a Unicode sparkline for series.
//
// Rules:
//   - The output is exactly min(len(series), width) runes wide.
//   - NaN values are rendered as a space.
//   - Negative values are clipped to zero before scaling (documented
//     behaviour: a sub-zero reading is treated as "no signal").
//   - An all-zero or single-point series renders as all ▁ characters.
func Render(series []float64, width int) string {
	if len(series) == 0 || width <= 0 {
		return ""
	}

	// Trim to width from the right (show the most recent data).
	if len(series) > width {
		series = series[len(series)-width:]
	}

	// Clip negatives and find min/max.
	clipped := make([]float64, len(series))
	minVal := math.MaxFloat64
	maxVal := -math.MaxFloat64
	for i, v := range series {
		if math.IsNaN(v) {
			clipped[i] = math.NaN()
			continue
		}
		if v < 0 {
			v = 0
		}
		clipped[i] = v
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	if minVal == math.MaxFloat64 {
		// All NaN.
		return strings.Repeat(" ", len(series))
	}

	rng := maxVal - minVal

	var sb strings.Builder
	for _, v := range clipped {
		if math.IsNaN(v) {
			sb.WriteRune(' ')
			continue
		}
		var idx int
		if rng == 0 {
			idx = 0 // flat line → lowest block
		} else {
			scaled := (v - minVal) / rng
			idx = int(math.Round(scaled * float64(len(sparkBlocks)-1)))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkBlocks) {
				idx = len(sparkBlocks) - 1
			}
		}
		sb.WriteRune(sparkBlocks[idx])
	}
	return sb.String()
}
