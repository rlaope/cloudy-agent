package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// exceptionLineRe matches log lines that are likely the head of a fault — the
// places an SRE looks at first. Case-insensitive, broad on purpose: a single
// false positive costs at most one extra line per match, while a miss can hide
// the actual root cause.
var exceptionLineRe = regexp.MustCompile(`(?i)(exception|caused by:|\bpanic\b|traceback|stack ?trace|\bfatal\b|\berror\b)`)

// LogSummaryHook compresses oversized log-tool observations before they reach
// the LLM. Applied only to tools whose name starts with "log." and only when
// the observation text exceeds maxBytes. Below the threshold the observation
// is passed through unchanged — head/tail clipping with exception extraction
// is more expensive than a token-budget truncate, so it is reserved for the
// cases where it actually pays for itself.
type LogSummaryHook struct {
	NoopHook
	maxBytes int
}

// NewLogSummaryHook returns a LogSummaryHook with the given budget. A
// non-positive budget disables the hook (it becomes a no-op).
func NewLogSummaryHook(maxBytes int) *LogSummaryHook {
	return &LogSummaryHook{maxBytes: maxBytes}
}

// AfterToolCall implements Hook. The error from the upstream tool is
// preserved as-is; only the textual observation is rewritten.
func (h *LogSummaryHook) AfterToolCall(_ context.Context, call llm.ToolCall, obs tools.Observation, err error) (tools.Observation, error) {
	if h.maxBytes <= 0 || err != nil {
		return obs, nil
	}
	if !strings.HasPrefix(call.Name, "log.") {
		return obs, nil
	}
	if len(obs.Text) <= h.maxBytes {
		return obs, nil
	}
	obs.Text = SummarizeLog(obs.Text, h.maxBytes)
	return obs, nil
}

// SummarizeLog returns a compressed view of text that keeps the head, the
// tail, and any Exception / stack-trace neighbourhoods, separated by section
// markers. The returned string is bounded by ~maxBytes; the exact size may
// vary slightly because we cut on line boundaries.
//
// Exported for direct use by callers that want to apply the same compression
// without going through the hook chain (e.g. ad-hoc CLI tooling).
func SummarizeLog(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	lines := strings.Split(text, "\n")

	headBudget := maxBytes / 4
	tailBudget := maxBytes / 2
	excBudget := maxBytes - headBudget - tailBudget
	if excBudget < 0 {
		excBudget = 0
	}

	head := clipHead(text, headBudget)
	tail := clipTail(text, tailBudget)
	excLines := extractExceptionContext(lines, 5)
	exc := clipHead(strings.Join(excLines, "\n"), excBudget)

	var sb strings.Builder
	fmt.Fprintf(&sb, "=== log summary: %d bytes total, head/tail with exceptions ===\n", len(text))
	sb.WriteString("--- head ---\n")
	sb.WriteString(head)
	if exc != "" {
		sb.WriteString("\n--- exceptions / errors ---\n")
		sb.WriteString(exc)
	}
	sb.WriteString("\n--- tail ---\n")
	sb.WriteString(tail)
	return sb.String()
}

// clipHead returns the longest prefix of text that fits within max bytes,
// breaking on the last newline so partial lines are not surfaced.
func clipHead(text string, max int) string {
	if max <= 0 || text == "" {
		return ""
	}
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i]
	}
	return cut
}

// clipTail returns the longest suffix of text that fits within max bytes,
// breaking on the first newline after the cut so partial lines are not
// surfaced.
func clipTail(text string, max int) string {
	if max <= 0 || text == "" {
		return ""
	}
	if len(text) <= max {
		return text
	}
	cut := text[len(text)-max:]
	if i := strings.IndexByte(cut, '\n'); i > 0 && i < len(cut)-1 {
		return cut[i+1:]
	}
	return cut
}

// extractExceptionContext walks lines and returns every line that either
// matches exceptionLineRe or sits within `lookahead` lines after such a match.
// Consecutive duplicate lines are collapsed because chatty stack traces often
// repeat the same frame.
func extractExceptionContext(lines []string, lookahead int) []string {
	keep := make([]bool, len(lines))
	for i, line := range lines {
		if exceptionLineRe.MatchString(line) {
			end := i + lookahead
			if end > len(lines) {
				end = len(lines)
			}
			for k := i; k < end; k++ {
				keep[k] = true
			}
		}
	}
	out := make([]string, 0)
	var prev string
	for i, ok := range keep {
		if !ok {
			continue
		}
		if lines[i] == prev {
			continue
		}
		out = append(out, lines[i])
		prev = lines[i]
	}
	return out
}
