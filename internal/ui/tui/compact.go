package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
)

// compactDoneMsg carries the result of an async /compact run.
type compactDoneMsg struct {
	summary string
	err     error
}

// compactCmd runs CompactHistory in a tea.Cmd goroutine so the TUI stays
// responsive during the summarizer's LLM round-trip, delivering the result
// as a compactDoneMsg.
func compactCmd(fn func(context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		summary, err := fn(context.Background())
		return compactDoneMsg{summary: summary, err: err}
	}
}

const (
	// compactKeepMessages is how many of the most recent history messages
	// stay verbatim after a /compact; everything older folds into one
	// summary message.
	compactKeepMessages = 6
	// summaryPrefix tags the synthetic summary message so a re-compact can
	// detect the previous summary (it sits in the older slice and is folded
	// into the new one), keeping /compact idempotent.
	summaryPrefix = "[conversation-summary]\n"
	// compactSummaryInstruction primes the model to compress an SRE
	// investigation transcript without dropping load-bearing identifiers.
	compactSummaryInstruction = "You are compressing a read-only SRE incident-investigation transcript. " +
		"Produce a dense factual summary that preserves every cluster, namespace, pod, " +
		"service, and node name mentioned, the findings and metrics observed, the " +
		"hypotheses considered, and the open questions still unresolved. Be specific; " +
		"do not generalise away resource names. Keep it under 400 words."
	// compactSummaryMaxTokens caps the summarizer's output.
	compactSummaryMaxTokens = 1024
)

// makeCompactHistory returns the Deps.CompactHistory closure. It folds the
// older portion of the conversation into one summary message via a single
// LLM call while keeping the most recent messages verbatim (the hybrid
// strategy). Idempotent: a prior summary sits in the older slice and is
// folded into the new one. On ANY error the live history is left untouched.
func makeCompactHistory(ref *providerRef, state *convoState) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		provider, modelID := ref.get()
		if provider == nil || modelID == "" {
			return "", fmt.Errorf("compact: no LLM provider configured")
		}

		state.mu.Lock()
		history := state.history
		startVer := state.version
		state.mu.Unlock()

		if len(history) <= compactKeepMessages+1 {
			return "", fmt.Errorf("conversation is already short (%d messages); nothing to compact", len(history))
		}

		older := history[:len(history)-compactKeepMessages]
		keep := history[len(history)-compactKeepMessages:]

		summaryText, err := summarizeMessages(ctx, provider, modelID, older)
		if err != nil {
			return "", err
		}

		newHistory := make([]llm.Message, 0, 1+len(keep))
		newHistory = append(newHistory, llm.Message{Role: llm.RoleUser, Content: summaryPrefix + summaryText})
		newHistory = append(newHistory, keep...)

		state.mu.Lock()
		// The summarizer round-trip is slow; if the conversation changed in
		// the meantime (an agent turn finished, /new, /resume), abort rather
		// than overwrite their work with this stale compaction.
		if state.version != startVer {
			state.mu.Unlock()
			return "", fmt.Errorf("conversation changed during compaction; aborted — run /compact again")
		}
		state.history = newHistory
		state.version++
		sess := state.sess
		state.mu.Unlock()

		// Re-persist the compacted history so a restart resumes the smaller
		// conversation, not the pre-compact one. Best-effort.
		if sess != nil {
			activeProfile, _ := permission.LoadActive()
			masked := permission.MaskHistory(activeProfile, newHistory)
			_ = session.SaveHistory(sess.ID, modelID, masked)
		}
		return summaryText, nil
	}
}

// makeResetHistory returns the Deps.ResetHistory closure: it clears the
// conversation and rolls a fresh session file so /new starts truly blank,
// returning the new session id.
func makeResetHistory(state *convoState) func() (string, error) {
	return func() (string, error) {
		return swapSession(state, "", nil)
	}
}

// makeSeedHistory returns the Deps.SeedHistory closure used by /resume to
// load a past conversation into the live history. It rolls the session to
// the resumed id so follow-up turns append to — and re-snapshot — the SAME
// conversation, matching `cloudy ask --resume` rather than diverging from it.
func makeSeedHistory(state *convoState) func(string, []llm.Message) error {
	return func(id string, msgs []llm.Message) error {
		_, err := swapSession(state, id, msgs)
		return err
	}
}

// swapSession opens session id (empty → fresh id), installs it plus history
// onto state, bumps the version, and closes the prior session. Shared by
// /new (nil history) and /resume (loaded history).
func swapSession(state *convoState, id string, history []llm.Message) (string, error) {
	newSess, err := session.New(id)
	if err != nil {
		return "", err
	}
	state.mu.Lock()
	old := state.sess
	state.sess = newSess
	state.history = history
	state.version++
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return newSess.ID, nil
}

// summarizeMessages renders the older slice to text and asks the active
// model for one dense summary, draining the stream. Pure w.r.t. convoState
// (no shared mutation) so it is unit-testable with a fake provider.
func summarizeMessages(ctx context.Context, provider llm.Provider, modelID string, msgs []llm.Message) (string, error) {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			b.WriteString("USER: ")
			b.WriteString(m.Content)
		case llm.RoleAssistant:
			b.WriteString("ASSISTANT: ")
			b.WriteString(m.Content)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "\n  [tool-call %s %s]", tc.Name, string(tc.Arguments))
			}
		case llm.RoleTool:
			b.WriteString("TOOL-RESULT: ")
			b.WriteString(m.Content)
		default:
			b.WriteString(m.Content)
		}
		b.WriteString("\n\n")
	}

	ch, err := provider.Stream(ctx, llm.Request{
		Model: modelID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactSummaryInstruction},
			{Role: llm.RoleUser, Content: "Summarize the following transcript:\n\n" + b.String()},
		},
		MaxTokens: compactSummaryMaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("compact: summarizer stream: %w", err)
	}

	var out strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", fmt.Errorf("compact: summarizer: %w", chunk.Err)
		}
		out.WriteString(chunk.DeltaText)
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", fmt.Errorf("compact: summarizer returned empty output")
	}
	return summary, nil
}
