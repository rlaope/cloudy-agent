package tui

import (
	"strings"
	"testing"
)

// TestPendingUserEcho_StacksMultipleSubmits pins the Claude-Code-style
// "you can keep typing follow-ups while a reply is in flight" behaviour:
// two submitMsg events back-to-back must produce a single
// pendingUserEcho string that contains BOTH chips, not just the second.
// Earlier versions overwrote the field on every submit and the first
// queued question silently vanished.
func TestPendingUserEcho_StacksMultipleSubmits(t *testing.T) {
	deps := makeDeps()
	deps.Model = "claude-test"
	// AgentRunner that never emits anything so the chip stays queued.
	deps.AgentRunner = func(_ <-chan struct{}, _ string, _ func(AgentEvent)) {}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, _ = m.Update(submitMsg("first question"))
	m = next.(Model)
	if !strings.Contains(m.pendingUserEcho, "first question") {
		t.Fatalf("first submit did not queue an echo; pendingUserEcho=%q", m.pendingUserEcho)
	}

	next, _ = m.Update(submitMsg("second question"))
	m = next.(Model)
	if !strings.Contains(m.pendingUserEcho, "first question") {
		t.Errorf("second submit clobbered the first chip; pendingUserEcho=%q", m.pendingUserEcho)
	}
	if !strings.Contains(m.pendingUserEcho, "second question") {
		t.Errorf("second submit did not append; pendingUserEcho=%q", m.pendingUserEcho)
	}
	// First must appear before second so the transcript reads chronologically.
	firstIdx := strings.Index(m.pendingUserEcho, "first question")
	secondIdx := strings.Index(m.pendingUserEcho, "second question")
	if firstIdx == -1 || secondIdx == -1 || firstIdx >= secondIdx {
		t.Errorf("chips should be in submission order; first@%d second@%d", firstIdx, secondIdx)
	}
}

func TestFormatUserEcho_WrapsLongPrompt(t *testing.T) {
	long := "checkout-api가 최근 배포 이후로 느려지고 일부 요청이 실패하는 것 같아. kind-cloudy-test 클러스터의 cloudy-shop 네임스페이스를 중심으로 확인해줘."
	echo := formatUserEcho(long, 56)
	plain := stripANSI(echo)
	if !strings.Contains(plain, "checkout-api") {
		t.Fatalf("echo should keep the submitted prompt text; got %q", plain)
	}
	if strings.Count(plain, "\n") == 0 {
		t.Fatalf("long prompt echo should wrap instead of clipping to one row; got %q", plain)
	}
}
