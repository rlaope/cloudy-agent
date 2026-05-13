package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTableRenderWidths(t *testing.T) {
	tbl := Table{
		Headers: []string{"Namespace", "Pod", "Status", "Restarts", "Age"},
		Rows: [][]string{
			{"production", "api-server-7d9f8b-xkcd2", "Running", "0", "14d"},
			{"staging", "worker-abc123", "CrashLoopBackOff", "42", "2h"},
			{"default", "db-primary-0", "Running", "1", "30d"},
		},
		Aligns: []Align{AlignLeft, AlignLeft, AlignLeft, AlignRight, AlignRight},
	}

	for _, width := range []int{60, 80, 120} {
		t.Run(strings.ReplaceAll(t.Name(), "/", "_")+string(rune('0'+width/10)), func(t *testing.T) {
			out := tbl.Render(width, NewTheme(true)) // no-color for easy length check
			for i, line := range strings.Split(out, "\n") {
				if line == "" {
					continue
				}
				n := utf8.RuneCountInString(line)
				if n > width {
					t.Errorf("line %d exceeds width %d (got %d): %q", i, width, n, line)
				}
			}
		})
	}
}

func TestTableWidths(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		tbl := Table{
			Headers: []string{"Namespace", "Pod", "Status", "Restarts", "Age"},
			Rows: [][]string{
				{"production", "api-server-7d9f8b-xkcd2", "Running", "0", "14d"},
				{"staging", "worker-abc123", "CrashLoopBackOff", "42", "2h"},
				{"default", "db-primary-0", "Running", "1", "30d"},
			},
		}
		out := tbl.Render(width, NewTheme(true))
		for i, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			n := utf8.RuneCountInString(line)
			if n > width {
				t.Errorf("width=%d line %d too long (%d): %q", width, i, n, line)
			}
		}
	}
}

func TestTableEllipsis(t *testing.T) {
	tbl := Table{
		Headers: []string{"A", "B"},
		Rows:    [][]string{{"hello world", "foo"}},
	}
	// Very narrow: forces truncation.
	out := tbl.Render(10, NewTheme(true))
	for i, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if n := utf8.RuneCountInString(line); n > 10 {
			t.Errorf("line %d exceeds width 10 (got %d): %q", i, n, line)
		}
	}
	// Ellipsis should appear somewhere.
	if !strings.Contains(out, ellipsis) {
		t.Errorf("expected ellipsis in truncated output, got:\n%s", out)
	}
}

func TestTableEmpty(t *testing.T) {
	tbl := Table{}
	if got := tbl.Render(80, NewTheme(true)); got != "" {
		t.Errorf("expected empty output for empty table, got %q", got)
	}
}
