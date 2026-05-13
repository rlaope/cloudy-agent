package render

import (
	"errors"
	"strings"
	"testing"
)

func TestStreamWriteTokens(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	for i := 0; i < 100; i++ {
		s.WriteToken("tok ")
	}

	got := buf.String()
	if !strings.Contains(got, "tok ") {
		t.Errorf("expected tokens in output, got: %q", got[:min0(len(got), 80)])
	}
	count := strings.Count(got, "tok ")
	if count != 100 {
		t.Errorf("expected 100 occurrences of 'tok ', got %d", count)
	}
}

func TestStreamToolCallBoundaries(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	s.WriteToken("thinking... ")
	s.BeginToolCall("jvm.jcmd_gc", "pid=42")
	s.EndToolCall("GC triggered successfully", nil)
	s.WriteToken("done.")

	out := buf.String()
	if !strings.Contains(out, "▶ tool: jvm.jcmd_gc(pid=42)") {
		t.Errorf("expected tool header, got:\n%s", out)
	}
	if !strings.Contains(out, "GC triggered successfully") {
		t.Errorf("expected observation in output, got:\n%s", out)
	}
	if !strings.Contains(out, "done.") {
		t.Errorf("expected post-tool token, got:\n%s", out)
	}
}

func TestStreamToolCallWithError(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	s.BeginToolCall("k8s.get_pods", "ns=prod")
	s.EndToolCall("", errors.New("timeout after 30s"))

	out := buf.String()
	if !strings.Contains(out, "error: timeout after 30s") {
		t.Errorf("expected error message in output, got:\n%s", out)
	}
}

func TestStreamReset(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	s.BeginToolCall("tool", "arg=1")
	s.Reset() // reset mid-call

	// After reset, writing tokens should not panic.
	s.WriteToken("after reset")
	if !strings.Contains(buf.String(), "after reset") {
		t.Error("expected output after Reset()")
	}
}

func TestStreamMultipleToolCalls(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	for i := 0; i < 5; i++ {
		s.BeginToolCall("tool.action", "i=1")
		s.EndToolCall("ok", nil)
	}

	out := buf.String()
	count := strings.Count(out, "▶ tool:")
	if count != 5 {
		t.Errorf("expected 5 tool headers, got %d\n%s", count, out)
	}
}

func TestStreamObservationIndented(t *testing.T) {
	var buf strings.Builder
	s := NewStream(&buf, NewTheme(true))

	s.BeginToolCall("k8s.logs", "pod=api-0")
	s.EndToolCall("line one\nline two", nil)

	out := buf.String()
	if !strings.Contains(out, "  line one") {
		t.Errorf("expected indented observation, got:\n%s", out)
	}
	if !strings.Contains(out, "  line two") {
		t.Errorf("expected indented second line, got:\n%s", out)
	}
}
