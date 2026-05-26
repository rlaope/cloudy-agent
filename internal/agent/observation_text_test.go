package agent

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// TestObservationText pins the new rule that obs.Table is appended to
// the LLM/operator-facing text as a GitHub-flavored markdown table.
// Before this change every k8s.list_* tool surfaced only "3 node(s)" to
// the model — the actual rows were stranded on obs.Table and the model
// had nothing concrete to reason from when asked "what's in the
// cluster".
func TestObservationText(t *testing.T) {
	t.Run("text_only", func(t *testing.T) {
		obs := tools.Observation{Text: "hello"}
		if got := observationText(obs); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("empty_table_returns_text", func(t *testing.T) {
		obs := tools.Observation{
			Text:  "3 node(s)",
			Table: &render.Table{Headers: []string{"NAME"}},
		}
		if got := observationText(obs); got != "3 node(s)" {
			t.Errorf("empty Rows should not produce a table; got %q", got)
		}
	})

	t.Run("renders_table_with_alignment", func(t *testing.T) {
		obs := tools.Observation{
			Text: "3 node(s)",
			Table: &render.Table{
				Headers: []string{"NAME", "CPU (m)", "MEMORY (Mi)"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
				Rows: [][]string{
					{"node-a", "250", "512"},
					{"node-b", "1200", "3072"},
				},
			},
		}
		got := observationText(obs)
		for _, want := range []string{
			"3 node(s)",
			"| NAME | CPU (m) | MEMORY (Mi) |",
			"| --- | ---: | ---: |",
			"| node-a | 250 | 512 |",
			"| node-b | 1200 | 3072 |",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("escapes_pipes_and_newlines", func(t *testing.T) {
		obs := tools.Observation{
			Table: &render.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows: [][]string{
					{"path", "a|b\nc"},
				},
			},
		}
		got := observationText(obs)
		if !strings.Contains(got, `a\|b c`) {
			t.Errorf("cell contents should escape pipe and replace newline with space; got:\n%s", got)
		}
	})

	t.Run("table_only_no_text", func(t *testing.T) {
		obs := tools.Observation{
			Table: &render.Table{
				Headers: []string{"K"},
				Rows:    [][]string{{"v"}},
			},
		}
		got := observationText(obs)
		if strings.HasPrefix(got, "\n") {
			t.Errorf("table-only obs should not start with blank line; got:\n%s", got)
		}
		if !strings.Contains(got, "| K |") {
			t.Errorf("table header missing; got:\n%s", got)
		}
	})

	// Review follow-up: rows with fewer cells than headers used to emit a
	// short markdown row that downstream renderers misaligned. Now we pad
	// to len(Headers) with empty cells.
	t.Run("pads_short_rows", func(t *testing.T) {
		obs := tools.Observation{
			Table: &render.Table{
				Headers: []string{"A", "B", "C"},
				Rows: [][]string{
					{"a", "b"}, // 2 cells under a 3-column header
					{},         // 0 cells
				},
			},
		}
		got := observationText(obs)
		for _, want := range []string{
			"| a | b |  |",
			"|  |  |  |",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing padded row %q in:\n%s", want, got)
			}
		}
	})

	// Review follow-up: a pre-existing `\|` in cell content used to
	// double-escape into `\\|`, splitting the cell at the resulting
	// terminator. Backslash is now escaped before the pipe.
	t.Run("backslash_pipe_not_double_escaped", func(t *testing.T) {
		obs := tools.Observation{
			Table: &render.Table{
				Headers: []string{"X"},
				Rows:    [][]string{{`a\|b`}},
			},
		}
		got := observationText(obs)
		// Expected on-disk form: `\\` then `\|` → 4 characters `\\\|`
		// surrounding `a` and `b`.
		want := `a\\\|b`
		if !strings.Contains(got, want) {
			t.Errorf("expected the cell to escape backslash before pipe (%q), got:\n%s", want, got)
		}
	})

	// Review follow-up: obs.Text ending in `\n` joined with `\n\n` + table
	// previously yielded three consecutive newlines and rendered a blank
	// paragraph above the table. Trailing newlines are trimmed.
	t.Run("trims_trailing_newlines", func(t *testing.T) {
		obs := tools.Observation{
			Text: "3 node(s)\n\n",
			Table: &render.Table{
				Headers: []string{"NAME"},
				Rows:    [][]string{{"node-a"}},
			},
		}
		got := observationText(obs)
		if strings.Contains(got, "\n\n\n") {
			t.Errorf("expected at most two consecutive newlines after trim; got:\n%s", got)
		}
	})
}
