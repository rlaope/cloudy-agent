package tui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{5 * time.Second, "00:05"},
		{65 * time.Second, "01:05"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{61 * time.Minute, "1:01:00"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestStreamModel_ToolBeginEmitsTick(t *testing.T) {
	s := newStreamModel(true) // noColor=true for predictable text
	out, cmd := s.Update(streamToolBeginMsg{name: "jvm.async_profile", args: "{}"})
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd to schedule first tick")
	}
	if out.pendingTool == nil {
		t.Fatal("pendingTool should be set after streamToolBeginMsg")
	}
	if !strings.Contains(out.content.String(), "(00:00)") {
		t.Errorf("initial header should contain (00:00), got %q", out.content.String())
	}
}

func TestStreamModel_TickUpdatesElapsedHeader(t *testing.T) {
	s := newStreamModel(true)
	s2, _ := s.Update(streamToolBeginMsg{name: "tool.x", args: "a"})
	// Simulate that the tool began 5 seconds ago.
	s2.pendingStart = time.Now().Add(-5 * time.Second)

	s3, cmd := s2.Update(streamToolTickMsg{})
	if cmd == nil {
		t.Fatal("tick while tool in flight should re-issue tickCmd")
	}
	body := s3.content.String()
	if strings.Contains(body, "(00:00)") {
		t.Errorf("old (00:00) header should have been replaced: %q", body)
	}
	if !strings.Contains(body, "(00:05)") && !strings.Contains(body, "(00:06)") {
		t.Errorf("header should reflect ~5s elapsed: %q", body)
	}
}

func TestStreamModel_TickAfterEndIsNoop(t *testing.T) {
	s := newStreamModel(true)
	s2, _ := s.Update(streamToolBeginMsg{name: "tool.x", args: ""})
	s3, _ := s2.Update(streamToolEndMsg{observation: "ok", err: nil})

	// A late tick that races past end should not re-issue a tick cmd and
	// should not panic on the nil pendingTool.
	s4, cmd := s3.Update(streamToolTickMsg{})
	if cmd != nil {
		t.Errorf("tick after end should not schedule another tick, got %v", cmd)
	}
	if s4.pendingTool != nil {
		t.Error("pendingTool must remain nil after end+late-tick")
	}
}
