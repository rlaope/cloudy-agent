package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptModel_SetValueWrapsLongPastedPrompt(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p, _ = p.Update(tea.WindowSizeMsg{Width: 48, Height: 20})

	prompt := qaOOMPrompt()
	p.SetValue(prompt)

	assertPromptVisible(t, p)
}

func TestPromptModel_BracketedPasteWrapsLongPrompt(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p, _ = p.Update(tea.WindowSizeMsg{Width: 48, Height: 20})

	prompt := qaOOMPrompt()
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(prompt), Paste: true})

	if got := p.Value(); got != prompt {
		t.Fatalf("paste should keep the full prompt in the textarea value; got %q", got)
	}
	assertPromptVisible(t, p)
}

func qaOOMPrompt() string {
	return "recommendation-api가 오늘 아침부터 주기적으로 재시작하는 것 같아. " +
		"kind-cloudy-test 클러스터의 cloudy-shop 네임스페이스에서 pod 상태, restart count, events, resource request/limit, 로그를 확인해서 OOMKilled인지 봐줘. " +
		"단순히 limit이 낮은 건지, 최근 배포 이후 메모리 누수/사용량 증가 가능성이 있는지도 근거와 confidence로 나눠서 정리해줘."
}

func assertPromptVisible(t *testing.T, p PromptModel) {
	t.Helper()
	if p.ta.Height() <= 1 {
		t.Fatalf("long pasted prompt should grow the textarea; height=%d", p.ta.Height())
	}
	view := stripANSI(p.View())
	if !strings.Contains(view, "recommendation-api") || !strings.Contains(view, "나눠서 정리해줘") {
		t.Fatalf("prompt view should show both the start and end of the pasted prompt; got:\n%s", view)
	}
}
