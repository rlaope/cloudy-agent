package render

import (
	"strings"
	"testing"
)

func TestDiffStringsIdentical(t *testing.T) {
	out := DiffStrings("a\nb\nc", "a\nb\nc", NewTheme(true))
	// No changes → no hunks → empty output.
	if strings.TrimSpace(out) != "" {
		t.Errorf("identical strings should produce no diff, got:\n%s", out)
	}
}

func TestDiffStrings3vs4Lines(t *testing.T) {
	a := "line1\nline2\nline3"
	b := "line1\nline2\nline3\nline4"
	out := DiffStrings(a, b, NewTheme(true))
	if !strings.Contains(out, "@@") {
		t.Errorf("expected unified hunk header, got:\n%s", out)
	}
	if !strings.Contains(out, "+ line4") {
		t.Errorf("expected '+ line4' in diff output, got:\n%s", out)
	}
}

func TestDiffStringsRemoval(t *testing.T) {
	a := "alpha\nbeta\ngamma"
	b := "alpha\ngamma"
	out := DiffStrings(a, b, NewTheme(true))
	if !strings.Contains(out, "- beta") {
		t.Errorf("expected '- beta' in diff output, got:\n%s", out)
	}
}

func TestDiffStringsReplacement(t *testing.T) {
	a := "hello\nworld"
	b := "hello\nearth"
	out := DiffStrings(a, b, NewTheme(true))
	if !strings.Contains(out, "- world") {
		t.Errorf("expected '- world' in diff output, got:\n%s", out)
	}
	if !strings.Contains(out, "+ earth") {
		t.Errorf("expected '+ earth' in diff output, got:\n%s", out)
	}
}

func TestDiffStringsEmpty(t *testing.T) {
	// Both empty: no output, no panic.
	out := DiffStrings("", "", NewTheme(true))
	_ = out
}

func TestDiffStringsColorOutput(t *testing.T) {
	// With colour enabled, ANSI codes will be present; just verify no panic
	// and the content is there.
	a := "x\ny"
	b := "x\nz"
	out := DiffStrings(a, b, NewTheme(false))
	if !strings.Contains(out, "y") || !strings.Contains(out, "z") {
		t.Errorf("colour diff missing expected content, got:\n%s", out)
	}
}
