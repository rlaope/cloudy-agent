package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/incidentmemory"
)

// approvalEvent constructs an agentEventMsg carrying a pending approval
// request with a buffered Reply channel the test can drain.
func approvalEvent(tool string) (agentEventMsg, chan bool) {
	reply := make(chan bool, 1)
	return agentEventMsg{Approval: &ApprovalRequest{
		Tool:  tool,
		Args:  `{"pid":1}`,
		Reply: reply,
	}}, reply
}

func memoryReviewEvent() (agentEventMsg, chan bool) {
	reply := make(chan bool, 1)
	return agentEventMsg{MemoryReview: &MemoryReviewRequest{
		Card: incidentmemory.Card{
			ID:              "case-1",
			Symptoms:        []string{"latency spike"},
			AffectedService: "payments-api",
			CauseStatus:     incidentmemory.CauseSuspected,
			Source:          incidentmemory.Source{Type: "postmortem", ID: "INC-142"},
			Confidence:      0.7,
		},
		Reply: reply,
	}}, reply
}

func TestModel_ApprovalEvent_SetsPending(t *testing.T) {
	m := NewModel(Deps{})
	evt, _ := approvalEvent("jvm.async_profile")
	next, _ := m.Update(evt)
	nm := next.(Model)
	if nm.pendingApproval == nil {
		t.Fatalf("ApprovalRequest did not set pendingApproval")
	}
	if nm.pendingApproval.Tool != "jvm.async_profile" {
		t.Fatalf("wrong tool name: %q", nm.pendingApproval.Tool)
	}
}

func TestModel_Y_ApprovesPending(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := approvalEvent("jvm.async_profile")
	next, _ := m.Update(evt)
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	select {
	case ok := <-reply:
		if !ok {
			t.Fatalf("'y' produced deny on reply channel")
		}
	default:
		t.Fatalf("reply channel never received a decision")
	}
	if next.(Model).pendingApproval != nil {
		t.Fatalf("pendingApproval not cleared after approval")
	}
}

func TestModel_N_DeniesPending(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := approvalEvent("perf.linux_perf_record")
	next, _ := m.Update(evt)
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	select {
	case ok := <-reply:
		if ok {
			t.Fatalf("'n' produced approve on reply channel")
		}
	default:
		t.Fatalf("reply channel never received a decision")
	}
	if next.(Model).pendingApproval != nil {
		t.Fatalf("pendingApproval not cleared after deny")
	}
}

func TestModel_Esc_DeniesPending(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := approvalEvent("ebpf.execsnoop")
	next, _ := m.Update(evt)
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})

	select {
	case ok := <-reply:
		if ok {
			t.Fatalf("Esc produced approve on reply channel")
		}
	default:
		t.Fatalf("Esc did not send a decision")
	}
	if next.(Model).pendingApproval != nil {
		t.Fatalf("pendingApproval not cleared after Esc deny")
	}
}

func TestModel_OtherKeyDuringApproval_IsSwallowed(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := approvalEvent("jvm.jcmd_gc")
	next, _ := m.Update(evt)
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if next.(Model).pendingApproval == nil {
		t.Fatalf("non y/n/Esc key cleared pendingApproval")
	}
	select {
	case <-reply:
		t.Fatalf("non y/n/Esc key sent a decision on reply")
	default:
	}
}

func TestModel_MemoryReview_YApprovesPending(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := memoryReviewEvent()
	next, _ := m.Update(evt)
	if next.(Model).pendingMemoryReview == nil {
		t.Fatalf("MemoryReview event did not set pendingMemoryReview")
	}
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	select {
	case ok := <-reply:
		if !ok {
			t.Fatalf("'y' produced reject on memory review")
		}
	default:
		t.Fatalf("reply channel never received a decision")
	}
	if next.(Model).pendingMemoryReview != nil {
		t.Fatalf("pendingMemoryReview not cleared after approval")
	}
}

func TestModel_MemoryReview_OtherKeyIsSwallowed(t *testing.T) {
	m := NewModel(Deps{})
	evt, reply := memoryReviewEvent()
	next, _ := m.Update(evt)
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if next.(Model).pendingMemoryReview == nil {
		t.Fatalf("non y/n/Esc key cleared pendingMemoryReview")
	}
	select {
	case got := <-reply:
		t.Fatalf("unexpected reply for swallowed key: %v", got)
	default:
	}
}

func TestModel_MemoryReviewCommand_ApprovesPersistedCandidate(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	cardJSON := `{"symptoms":["latency spike"],"affected_service":"payments-api","signals":["redis errors"],"cause_status":"suspected","cause":"possible pool exhaustion","fix_or_mitigation":"check redis clients","what_was_different":"deploy state unknown","source":{"type":"postmortem","id":"INC-142"},"confidence":0.7}`
	m := NewModel(Deps{})
	next, _ := m.Update(submitMsg("/memory-review " + cardJSON))
	nm := next.(Model)
	if nm.pendingMemoryReview == nil {
		t.Fatalf("/memory-review did not install pending review")
	}
	id := nm.pendingMemoryReview.Card.ID
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if next.(Model).pendingMemoryReview != nil {
		t.Fatalf("pendingMemoryReview not cleared after approval")
	}
	store := incidentmemory.NewDefaultStore()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get approved card: %v", err)
	}
	if got.Status != incidentmemory.StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
}
