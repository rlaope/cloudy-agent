package render

import (
	"strings"
	"testing"
)

func TestRenderMarkdownNonEmpty(t *testing.T) {
	md := "# Hello\n\nThis is a **paragraph** with some text.\n\n```go\nfmt.Println(\"hello\")\n```\n"

	for _, noColor := range []bool{false, true} {
		t.Run(map[bool]string{false: "color", true: "nocolor"}[noColor], func(t *testing.T) {
			out, err := RenderMarkdown(md, NewTheme(noColor), 80)
			if err != nil {
				t.Fatalf("RenderMarkdown returned error: %v", err)
			}
			if strings.TrimSpace(out) == "" {
				t.Error("expected non-empty output")
			}
			// Must contain the heading text somewhere.
			if !strings.Contains(out, "Hello") {
				t.Errorf("output missing 'Hello': %q", out)
			}
		})
	}
}

func TestRenderMarkdownCodeFence(t *testing.T) {
	md := "```python\nprint('hello')\n```\n"
	out, err := RenderMarkdown(md, NewTheme(true), 80)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("expected non-empty output for code fence")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	out, err := RenderMarkdown("", NewTheme(true), 80)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// glamour may return whitespace for empty input; just ensure no panic.
	_ = out
}
