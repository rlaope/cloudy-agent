package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptModel_SetValueWrapsLongPastedPrompt(t *testing.T) {
	p := newPromptModel(defaultKeys())
	var cmd tea.Cmd
	p, cmd = p.Update(tea.WindowSizeMsg{Width: 48, Height: 20})
	if cmd != nil {
		t.Fatalf("window resize should not emit command, got %v", cmd)
	}

	prompt := "recommendation-api가 오늘 아침부터 주기적으로 재시작하는 것 같아. " +
		"pod 상태와 restart count, events, resource request/limit을 보고 " +
		"근거와 confidence로 나눠서 정리해줘."
	p.SetValue(prompt)

	if p.ta.Height() <= 1 {
		t.Fatalf("long pasted prompt should grow the textarea; height=%d", p.ta.Height())
	}
	view := stripANSI(p.View())
	if !strings.Contains(view, "recommendation-api") || !strings.Contains(view, "나눠서 정리해줘") {
		t.Fatalf("prompt view should show both the start and end of the pasted prompt; got:\n%s", view)
	}
}
