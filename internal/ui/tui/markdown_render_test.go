package tui

import (
	"strings"
	"testing"
)

func TestRenderAssistantMarkdown_DefaultRendererFormatsMarkdown(t *testing.T) {
	s := newStreamModel(false)
	rendered := stripANSI(s.RenderAssistantMarkdown("### 원인 후보\n\n| 후보 | 근거 |\n|---|---|\n| timeout | 로그 |\n"))

	if strings.Contains(rendered, "### 원인 후보") {
		t.Fatalf("heading markers should not leak into TUI output; got %q", rendered)
	}
	if strings.Contains(rendered, "|---|") {
		t.Fatalf("markdown table separator should be rendered, not shown raw; got %q", rendered)
	}
	if !strings.Contains(rendered, "원인 후보") || !strings.Contains(rendered, "timeout") {
		t.Fatalf("rendered output should preserve the markdown content; got %q", rendered)
	}
}

func TestCleanAssistantMarkdown_PreservesCodeFences(t *testing.T) {
	raw := "### Summary\n\n```text\n### keep this literal\n```"
	cleaned := cleanAssistantMarkdown(raw)
	if strings.Contains(cleaned, "### Summary") {
		t.Fatalf("heading marker outside fence should be removed; got %q", cleaned)
	}
	if !strings.Contains(cleaned, "### keep this literal") {
		t.Fatalf("heading-like text inside code fence should be preserved; got %q", cleaned)
	}
}
