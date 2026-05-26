package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// thinkingVerbs cycle as the agent works so the screen never looks
// frozen during a long generation. Trimmed from the original nine-verb
// pool (which included "Catapulting" / "Spelunking" — fun once, off-
// brand on a tenth incident response) to four neutral, evergreen verbs
// that read as serious-but-alive.
var thinkingVerbs = []string{
	"Thinking",
	"Working",
	"Synthesizing",
	"Pondering",
}

// thinkingTickInterval and thinkingVerbRotateTicks together pace the
// in-flight animation: 250ms per tick, verb changes every 8 ticks (≈2s).
const (
	thinkingTickInterval    = 250 * time.Millisecond
	thinkingVerbRotateTicks = 8
)

// thinkingTickMsg fires once per thinkingTickInterval while an agent run
// is active, prompting View() to refresh the elapsed counter.
type thinkingTickMsg struct{}

// thinkingTickCmd returns a tea.Cmd that emits one thinkingTickMsg.
func thinkingTickCmd() tea.Cmd {
	return tea.Tick(thinkingTickInterval, func(time.Time) tea.Msg { return thinkingTickMsg{} })
}

// thinkingState bundles every field that drives the in-flight agent
// row. The fields always mutate together (set in submitMsg, cleared in
// agentDoneMsg, read in renderThinkingRow) — keeping them on one
// struct makes the lifetime explicit and lets the helpers replace
// 5-line reset/tick sequences with a single call.
type thinkingState struct {
	start     time.Time
	tokens    int
	verbIdx   int
	streaming bool
	tickCount int
}

// reset begins a new in-flight run. Verb index is seeded from the
// nanosecond clock so consecutive runs feel different.
func (t *thinkingState) reset() {
	*t = thinkingState{
		start:   time.Now(),
		verbIdx: int(time.Now().UnixNano()) % len(thinkingVerbs),
	}
	if t.verbIdx < 0 {
		t.verbIdx += len(thinkingVerbs)
	}
}

// tick advances the rotating-verb animation.
func (t *thinkingState) tick() {
	t.tickCount++
	if t.tickCount%thinkingVerbRotateTicks == 0 {
		t.verbIdx = (t.verbIdx + 1) % len(thinkingVerbs)
	}
}

// thinkingSpinnerFrames is the 10-step braille spinner shown at the start
// of the live thinking row. Identical to the spinner most modern CLIs
// (cargo, npm, gh, claude code) use, so the visual cue lands as "agent
// is working" without requiring a legend.
var thinkingSpinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

// thinkingStyle is the soft sky-blue used by the in-flight agent status
// row ("✦ Synthesizing… (3s · 240 tokens)") that sits above the prompt.
var thinkingStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("153"))

// thinkingIdleStyle is the dim grey used by the persistent "· ready"
// row that holds the layout slot while no agent run is in flight. The
// muted shade keeps the eye on the prompt where input belongs.
var thinkingIdleStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240"))

// approxTokens estimates the token count of a streaming chunk using
// the four-chars-per-token rule of thumb. Used until the provider
// emits an authoritative Usage event with the real output count.
func approxTokens(s string) int {
	n := len(s) / 4
	if n < 1 && s != "" {
		return 1
	}
	return n
}

// formatThinkingElapsed returns "3s" / "1m05s" / "1h05m" for the
// thinking row's compact timer.
func formatThinkingElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s/60)%60)
	}
}

// renderThinkingRow returns the row that sits directly above the prompt.
// It is rendered unconditionally from app start so the prompt position
// never jumps when an agent run begins or ends — earlier versions
// returned "" while idle, which made every Enter and every agentDoneMsg
// shove the prompt up/down by a row. Three states:
//
//	· ready                                  -- idle, between turns
//	✦ Synthesizing… (3s · 240 tokens)        -- LLM is thinking, no bytes yet
//	✦ Receiving…   (1m12s · 1240 tokens)     -- bytes arriving, body deferred until Done
//
// The playback typewriter was removed in favour of a single drain on
// agentDoneMsg, so there is no "Typing" state — once Done lands the
// row reverts to idle in the same Update that writes the body.
func (m Model) renderThinkingRow() string {
	if !m.running {
		return thinkingIdleStyle.Render("· ready")
	}
	elapsed := formatThinkingElapsed(time.Since(m.thinking.start))
	verb := thinkingVerbs[m.thinking.verbIdx] + "…"
	if m.thinking.streaming {
		verb = "Receiving…"
	}
	// Braille spinner rotates every tick (~250ms). Using the same tick
	// counter that drives verb rotation keeps the two animations in
	// lockstep — when the agent is alive the spinner is unmistakably
	// moving even during the silent gap between a tool result and the
	// LLM's next token, which used to read as "frozen".
	glyph := thinkingSpinnerFrames[m.thinking.tickCount%len(thinkingSpinnerFrames)]
	line := fmt.Sprintf("%s %s   (%s · %d tokens)", glyph, verb, elapsed, m.thinking.tokens)
	return thinkingStyle.Render(line)
}
