package render

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// Align controls the horizontal alignment of a table column.
type Align int

const (
	// AlignLeft aligns cell content to the left (default).
	AlignLeft Align = iota
	// AlignRight aligns cell content to the right.
	AlignRight
	// AlignCenter centres cell content.
	AlignCenter
)

// Predicate is an optional per-cell colour override.  Return (style, true) to
// apply the style, or (_, false) to use the default rendering.
type Predicate func(rowIdx, colIdx int, cell string) (lipgloss.Style, bool)

// Table is a width-fitting terminal table.
//
// Column widths are auto-sized to content.  When the total exceeds the
// requested width the widest columns are truncated with an ellipsis (…) until
// every rendered line fits within width.
type Table struct {
	// Headers is the list of column names.
	Headers []string
	// Rows contains the data rows; each inner slice must be len(Headers) long.
	Rows [][]string
	// Aligns specifies per-column alignment.  Entries beyond len(Headers) are
	// ignored; missing entries default to AlignLeft.
	Aligns []Align
	// Colorizer is an optional per-cell style override.
	Colorizer Predicate
}

const ellipsis = "…"

// Render formats the table so that no output line exceeds width bytes.
// It returns a string with a trailing newline.
func (t Table) Render(width int, theme Theme) string {
	cols := len(t.Headers)
	if cols == 0 {
		return ""
	}

	// Compute natural (unconstrained) column widths.
	natural := make([]int, cols)
	for i, h := range t.Headers {
		natural[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i >= cols {
				break
			}
			if n := utf8.RuneCountInString(cell); n > natural[i] {
				natural[i] = n
			}
		}
	}

	// Separator columns: " | " between each pair = 3 chars each gap.
	separatorTotal := 3 * (cols - 1)
	colWidths := make([]int, cols)
	copy(colWidths, natural)

	// Fit into width: repeatedly shrink the widest column.
	for {
		total := separatorTotal
		for _, w := range colWidths {
			total += w
		}
		if total <= width {
			break
		}
		excess := total - width
		// Find the widest column.
		maxW, maxI := 0, 0
		for i, w := range colWidths {
			if w > maxW {
				maxW = w
				maxI = i
			}
		}
		shrink := maxW - excess
		if shrink < 1 {
			shrink = 1 // keep at least 1 rune per column
		}
		colWidths[maxI] = shrink
	}

	align := func(i int) Align {
		if i < len(t.Aligns) {
			return t.Aligns[i]
		}
		return AlignLeft
	}

	fitCell := func(s string, w int) string {
		n := utf8.RuneCountInString(s)
		if n > w {
			// Truncate with ellipsis.
			runes := []rune(s)
			if w <= 1 {
				return string(runes[:w])
			}
			return string(runes[:w-1]) + ellipsis
		}
		return s
	}

	padCell := func(s string, w int, a Align) string {
		n := utf8.RuneCountInString(s)
		pad := w - n
		if pad <= 0 {
			return s
		}
		switch a {
		case AlignRight:
			return strings.Repeat(" ", pad) + s
		case AlignCenter:
			left := pad / 2
			right := pad - left
			return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
		default: // AlignLeft
			return s + strings.Repeat(" ", pad)
		}
	}

	renderCell := func(rowIdx, colIdx int, cell string, w int, theme Theme, isHeader bool) string {
		cell = fitCell(cell, w)
		a := align(colIdx)
		cell = padCell(cell, w, a)

		if t.Colorizer != nil && !isHeader && !theme.NoColor() {
			if style, ok := t.Colorizer(rowIdx, colIdx, cell); ok {
				return style.Render(cell)
			}
		}
		if isHeader && !theme.NoColor() {
			return theme.Hi.Render(cell)
		}
		return cell
	}

	var sb strings.Builder

	// Header row.
	for i, h := range t.Headers {
		if i > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString(renderCell(-1, i, h, colWidths[i], theme, true))
	}
	sb.WriteByte('\n')

	// Separator.
	for i, w := range colWidths {
		if i > 0 {
			sb.WriteString("-+-")
		}
		sb.WriteString(strings.Repeat("-", w))
	}
	sb.WriteByte('\n')

	// Data rows.
	for ri, row := range t.Rows {
		for ci := range t.Headers {
			if ci > 0 {
				sb.WriteString(" | ")
			}
			cell := ""
			if ci < len(row) {
				cell = row[ci]
			}
			sb.WriteString(renderCell(ri, ci, cell, colWidths[ci], theme, false))
		}
		sb.WriteByte('\n')
	}

	return sb.String()
}
