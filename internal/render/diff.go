package render

import (
	"fmt"
	"strings"
)

// DiffStrings produces a human-readable unified-style diff between strings a
// and b, with added lines highlighted in the Ok colour and removed lines in
// the Err colour.  The diff is computed with a simple LCS-based diff kept
// under ~200 LoC; it is intended for human review in a terminal, not for
// patch application.
//
// Each hunk is preceded by a context header showing the approximate line
// numbers.  Three lines of context surround changed regions (standard unified
// diff behaviour).
func DiffStrings(a, b string, theme Theme) string {
	aLines := splitLines(a)
	bLines := splitLines(b)

	edits := lcsDiff(aLines, bLines)
	return renderDiff(edits, aLines, bLines, theme)
}

// splitLines splits s into lines.  A trailing newline does not produce a
// phantom empty final line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ---- LCS-based diff ----

type editKind int

const (
	editKeep editKind = iota
	editInsert
	editDelete
)

type edit struct {
	kind editKind
	aIdx int // valid for editKeep and editDelete
	bIdx int // valid for editKeep and editInsert
}

// lcsLen computes the LCS length table for a[0:n] and b[0:m].
func lcsLen(a, b []string) [][]int {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	return dp
}

// lcsDiff returns the edit script transforming a into b using the LCS table.
func lcsDiff(a, b []string) []edit {
	dp := lcsLen(a, b)
	var edits []edit
	backtrack(dp, a, b, len(a), len(b), &edits)
	return edits
}

func backtrack(dp [][]int, a, b []string, i, j int, edits *[]edit) {
	if i == 0 && j == 0 {
		return
	}
	if i == 0 {
		backtrack(dp, a, b, i, j-1, edits)
		*edits = append(*edits, edit{editInsert, i, j - 1})
		return
	}
	if j == 0 {
		backtrack(dp, a, b, i-1, j, edits)
		*edits = append(*edits, edit{editDelete, i - 1, j})
		return
	}
	if a[i-1] == b[j-1] {
		backtrack(dp, a, b, i-1, j-1, edits)
		*edits = append(*edits, edit{editKeep, i - 1, j - 1})
		return
	}
	if dp[i-1][j] >= dp[i][j-1] {
		backtrack(dp, a, b, i-1, j, edits)
		*edits = append(*edits, edit{editDelete, i - 1, j})
	} else {
		backtrack(dp, a, b, i, j-1, edits)
		*edits = append(*edits, edit{editInsert, i, j - 1})
	}
}

// ---- Unified-diff renderer ----

const contextLines = 3

// hunk represents a contiguous region of edit indices to render.
type hunk struct{ start, end int }

func renderDiff(edits []edit, a, b []string, theme Theme) string {
	if len(edits) == 0 {
		return ""
	}

	// Check whether there are any changes at all.
	hasChanges := false
	for _, e := range edits {
		if e.kind != editKeep {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		return ""
	}

	// Build hunks: windows of contextLines around each change.
	var hunks []hunk
	var cur *hunk
	for i, e := range edits {
		if e.kind != editKeep {
			if cur == nil {
				s := i - contextLines
				if s < 0 {
					s = 0
				}
				cur = &hunk{start: s}
			}
			end := i + contextLines + 1
			if end > len(edits) {
				end = len(edits)
			}
			cur.end = end
		} else if cur != nil && i >= cur.end {
			hunks = append(hunks, *cur)
			cur = nil
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}

	merged := mergeHunks(hunks)

	var sb strings.Builder
	for _, h := range merged {
		// Compute a/b line ranges for the @@ header.
		aStart, aCount, bStart, bCount := 0, 0, 0, 0
		first := true
		for _, e := range edits[h.start:h.end] {
			switch e.kind {
			case editKeep:
				if first {
					aStart = e.aIdx + 1
					bStart = e.bIdx + 1
					first = false
				}
				aCount++
				bCount++
			case editDelete:
				if first {
					aStart = e.aIdx + 1
					bStart = e.bIdx + 1 // approximate: next b position
					first = false
				}
				aCount++
			case editInsert:
				if first {
					aStart = e.aIdx + 1 // approximate: current a position
					bStart = e.bIdx + 1
					first = false
				}
				bCount++
			}
		}
		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount))

		for _, e := range edits[h.start:h.end] {
			switch e.kind {
			case editKeep:
				sb.WriteString("  ")
				sb.WriteString(a[e.aIdx])
				sb.WriteByte('\n')
			case editDelete:
				line := "- " + a[e.aIdx]
				if !theme.NoColor() {
					line = theme.Err.Render(line)
				}
				sb.WriteString(line)
				sb.WriteByte('\n')
			case editInsert:
				line := "+ " + b[e.bIdx]
				if !theme.NoColor() {
					line = theme.Ok.Render(line)
				}
				sb.WriteString(line)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

func mergeHunks(hunks []hunk) []hunk {
	if len(hunks) == 0 {
		return nil
	}
	merged := []hunk{hunks[0]}
	for _, h := range hunks[1:] {
		last := &merged[len(merged)-1]
		if h.start <= last.end {
			if h.end > last.end {
				last.end = h.end
			}
		} else {
			merged = append(merged, h)
		}
	}
	return merged
}

func max0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min0(a, b int) int {
	if a < b {
		return a
	}
	return b
}
