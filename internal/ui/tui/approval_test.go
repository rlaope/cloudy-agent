package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
