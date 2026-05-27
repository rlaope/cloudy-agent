package tui

import (
	"fmt"
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
	// Hidden marker present AND reports the exact count. The earlier
	// "any number is fine" wording let a trailing-newline off-by-one
	// slip through unnoticed; pin the integer so the regression is
	// caught either direction.
	hidden := totalLines - (foldObsHeadTail * 2)
	wantMarker := fmt.Sprintf("[… %d more lines hidden …]", hidden)
	if !strings.Contains(folded, wantMarker) {
		t.Errorf("folded output should carry exact marker %q; got %q", wantMarker, folded)
	}
}

// TestFoldLongObservation_TrailingNewline pins the contract that
// `text` ending in "\n" (the common case for log dumps / file reads)
// does NOT inflate the hidden-line count. Before the normalisation
// step, strings.Split turned the terminator into a phantom empty
// entry which then bumped the marker by 1 and pushed an extra blank
// row into the rendered tail.
func TestFoldLongObservation_TrailingNewline(t *testing.T) {
	const realLines = 40
	var b strings.Builder
	for i := 0; i < realLines; i++ {
		b.WriteString("row\n")
	}

	folded := foldLongObservation(b.String())
	hidden := realLines - (foldObsHeadTail * 2)
	wantMarker := fmt.Sprintf("[… %d more lines hidden …]", hidden)
	if !strings.Contains(folded, wantMarker) {
		t.Errorf("trailing-newline input must report exact hidden count %d; got %q",
			hidden, folded)
	}
	// And the output should still terminate with a single trailing
	// newline (preserving the input shape).
	if !strings.HasSuffix(folded, "\n") {
		t.Error("trailing-newline input should produce trailing-newline output")
	}
	if strings.HasSuffix(folded, "\n\n") {
		t.Errorf("output must not have a double trailing newline (extra blank row); got tail %q",
			folded[len(folded)-4:])
	}
}

// TestFoldLongObservation_BoundaryExactLimit nails the inclusive/
// exclusive boundary of foldObsLineLimit: exactly the limit must NOT
// fold (last value that passes through), exactly limit+1 MUST fold
// (first value that triggers). Without this boundary test, future
// drift of the constant could silently shift the threshold by one.
func TestFoldLongObservation_BoundaryExactLimit(t *testing.T) {
	at := strings.Repeat("x\n", foldObsLineLimit)
	if got := foldLongObservation(at); strings.Contains(got, "hidden") {
		t.Errorf("input of exactly foldObsLineLimit lines must NOT fold; got %q", got)
	}
	over := strings.Repeat("x\n", foldObsLineLimit+1)
	if got := foldLongObservation(over); !strings.Contains(got, "hidden") {
		t.Errorf("input of foldObsLineLimit+1 lines MUST fold; got %q", got)
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
