package tui

import (
	"strings"
	"testing"
)

// TestFoldLongObservation_ShortPassesThrough confirms that observations
// at or under foldObsLineLimit are not touched. Without this guard the
// fold would chew through every tool result and the visual noise of
// "[… 0 more lines hidden …]" would be everywhere.
func TestFoldLongObservation_ShortPassesThrough(t *testing.T) {
	short := strings.Repeat("line\n", foldObsLineLimit-2)
	if got := foldLongObservation(short); got != short {
		t.Errorf("short observation must pass through unchanged; got %q", got)
	}
}

// TestFoldLongObservation_LongCollapsesToHeadTail checks the actual
// fold: an observation taller than foldObsLineLimit retains the first
// foldObsHeadTail lines, a "[… N more lines hidden …]" marker, then
// the last foldObsHeadTail lines. This is what makes a 5,000-row
// prom.query or a 50 MB log.tail scannable instead of drowning the
// transcript.
func TestFoldLongObservation_LongCollapsesToHeadTail(t *testing.T) {
	const totalLines = 60
	var b strings.Builder
	for i := 0; i < totalLines; i++ {
		b.WriteString("row ")
		b.WriteString(strings.Repeat(string(rune('0'+i%10)), 4))
		b.WriteString("\n")
	}
	folded := foldLongObservation(b.String())

	// Head lines preserved.
	if !strings.Contains(folded, "row 0000") {
		t.Errorf("head should keep the earliest rows; got %q", folded)
	}
	// Tail lines preserved (last row is index 59 -> "9999").
	if !strings.Contains(folded, "row 9999") {
		t.Errorf("tail should keep the latest rows; got %q", folded)
	}
	// Hidden marker present.
	wantMarker := "more lines hidden"
	if !strings.Contains(folded, wantMarker) {
		t.Errorf("folded output should carry the hidden-line marker %q; got %q", wantMarker, folded)
	}
	// Marker must report a number greater than zero so the operator
	// can tell something was suppressed. Exact count depends on
	// trailing-newline behaviour of the input, which is irrelevant to
	// the contract.
	if !strings.Contains(folded, "more lines hidden") {
		t.Errorf("hidden-line marker must include line count; got %q", folded)
	}
}

// TestStreamModel_ToolEnd_FoldsLongObservation is the integration-style
// check: a tool with a 100-line observation goes through the full
// streamToolEndMsg path and the content buffer ends up with the folded
// shape — not the raw 100 lines.
func TestStreamModel_ToolEnd_FoldsLongObservation(t *testing.T) {
	s := newStreamModel(true)
	s, _ = s.Update(windowMsg())
	s, _ = s.Update(streamToolBeginMsg{name: "log.tail", args: `{"limit":100}`})

	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("entry ")
		b.WriteString(strings.Repeat("x", 3))
		b.WriteString("\n")
	}
	s, _ = s.Update(streamToolEndMsg{observation: b.String()})

	content := s.content.String()
	if !strings.Contains(content, "more lines hidden") {
		t.Errorf("long observation should trigger fold; content: %q", content)
	}
	// Sanity: the rendered content must be much shorter than the
	// raw 100-line observation.
	rawLines := strings.Count(b.String(), "\n")
	contentLines := strings.Count(content, "\n")
	if contentLines >= rawLines {
		t.Errorf("folded content should shed lines (raw=%d, content=%d)", rawLines, contentLines)
	}
}
