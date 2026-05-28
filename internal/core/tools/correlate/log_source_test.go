package correlate

import (
	"testing"
	"time"
)

func TestIsErrorLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"all good", false},
		{"level=error msg=something", true},
		{"", false},
		{"Fatal: out of memory", true},
		{"panic: runtime error", true},
		{"ERROR: connection refused", true},
		{"   ", false},
		{"info: starting server", false},
	}
	for _, c := range cases {
		got := isErrorLine(c.line)
		if got != c.want {
			t.Errorf("isErrorLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestLogErrorEvents_WithErrors(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0) // later
	t2 := time.Unix(500, 0)  // earliest

	lines := []TimestampedLine{
		{Time: t0, Text: "info: all good"},
		{Time: t1, Text: "ERROR x"},
		{Time: t2, Text: "panic y"},
		{Time: t0, Text: "debug: nothing here"},
	}

	events := logErrorEvents(lines, "myapp")

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Kind != "log_error" {
		t.Errorf("Kind = %q, want %q", e.Kind, "log_error")
	}
	if e.Source != "log" {
		t.Errorf("Source = %q, want %q", e.Source, "log")
	}
	if e.Target != "myapp" {
		t.Errorf("Target = %q, want %q", e.Target, "myapp")
	}
	if !e.Time.Equal(t2) {
		t.Errorf("Time = %v, want earliest error time %v", e.Time, t2)
	}
	// Summary must mention the count (2 error lines: "ERROR x" and "panic y")
	if e.Summary != "2 error line(s) in window" {
		t.Errorf("Summary = %q, want %q", e.Summary, "2 error line(s) in window")
	}
}

func TestLogErrorEvents_NoErrors(t *testing.T) {
	lines := []TimestampedLine{
		{Time: time.Unix(1000, 0), Text: "info: server started"},
		{Time: time.Unix(2000, 0), Text: "debug: request received"},
	}
	events := logErrorEvents(lines, "myapp")
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestLogErrorEvents_Empty(t *testing.T) {
	events := logErrorEvents(nil, "myapp")
	if len(events) != 0 {
		t.Errorf("expected 0 events for nil input, got %d", len(events))
	}
}
