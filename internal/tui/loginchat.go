package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/secrets"
)

// lookupLoginProvider resolves either a 1-based index ("1", "2", …)
// or a canonical provider name ("anthropic", "openai", …) into the
// matching loginProviders entry. Returns ok=false on any miss.
func lookupLoginProvider(input string) (loginProvider, bool) {
	input = strings.ToLower(strings.TrimSpace(input))
	if n, err := strconv.Atoi(input); err == nil {
		if n >= 1 && n <= len(loginProviders) {
			return loginProviders[n-1], true
		}
		return loginProvider{}, false
	}
	for _, p := range loginProviders {
		if p.key == input {
			return p, true
		}
	}
	return loginProvider{}, false
}

// loginChat is a tiny two-question conversation that walks the operator
// through saving an LLM-provider API key. Step 0 asks for the provider
// (anthropic / openai / google / moonshot), step 1 takes the key and
// persists it via internal/secrets — same dotenv file the /setup wizard
// writes to, so the rest of cloudy sees the credential after a restart
// or model switch.
//
// The conversation lives inside the main TUI stream — no separate
// screen, no popup. Each Step returns the text to print and a
// done-flag the parent uses to clear the active conversation.
type loginChat struct {
	step     int
	provider string // one of: anthropic, openai, google, moonshot
}

// Named steps for loginChat.step. Matches setupChat's naming style so
// debug prints stay legible.
const (
	loginStepProvider = 0
	loginStepKey      = 1
)

// loginProvider is one row of the numbered picker the operator sees on
// step 0. Order here is the order shown on screen and the order numeric
// inputs (1, 2, 3, 4) map back to.
type loginProvider struct {
	key       string // canonical name accepted as input
	envVar    string // env-var written to ~/.cloudy/secrets
	hint      string // short "(Claude — …)" trailer in the picker
	suggested string // model id printed on save
}

var loginProviders = []loginProvider{
	{"anthropic", "ANTHROPIC_API_KEY", "Claude — claude-3-5-sonnet, claude-3-opus, …", "claude-3-5-sonnet-20241022"},
	{"openai", "OPENAI_API_KEY", "GPT — gpt-4o, gpt-4o-mini, …", "gpt-4o-mini"},
	{"google", "GOOGLE_API_KEY", "Gemini — gemini-2.5-flash, gemini-2.0-flash, …", "gemini-2.5-flash"},
	{"moonshot", "MOONSHOT_API_KEY", "Kimi — kimi-k2-instruct, …", "kimi-k2-instruct"},
}

// loginResult is what every Step returns: stream output to print, an
// optional arrow-picker the parent should activate, and a done flag
// the parent uses to clear the active chat.
type loginResult struct {
	out    string
	picker *arrowPicker
	done   bool
}

// newLoginChat starts a fresh login conversation. The greeting is one
// line; the actual provider menu is an arrow-picker the parent
// activates from the returned loginResult.
func newLoginChat() (*loginChat, loginResult) {
	greeting := "\n--- /login — add an LLM API key ---\n"
	items := make([]arrowPickerItem, 0, len(loginProviders))
	for _, p := range loginProviders {
		items = append(items, arrowPickerItem{
			label: p.key,
			hint:  p.hint,
			key:   p.key,
		})
	}
	return &loginChat{step: 0}, loginResult{
		out:    greeting,
		picker: newArrowPicker("Pick an LLM provider:", items),
	}
}

// Step advances the conversation by one operator answer.
func (l *loginChat) Step(input string) loginResult {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "cancel") {
		return loginResult{out: "[login cancelled]\n", done: true}
	}

	switch l.step {
	case loginStepProvider:
		p, ok := lookupLoginProvider(input)
		if !ok {
			return loginResult{
				out: fmt.Sprintf("unknown provider %q. Type a number 1-%d, a name "+
					"(anthropic / openai / google / moonshot), or 'cancel':\n",
					input, len(loginProviders)),
			}
		}
		l.provider = p.key
		l.step = loginStepKey
		return loginResult{
			out: fmt.Sprintf("Paste your %s API key (will be saved to ~/.cloudy/secrets as %s):\n",
				p.key, p.envVar),
		}

	case loginStepKey:
		p, _ := lookupLoginProvider(l.provider)
		if input == "" {
			return loginResult{out: "(empty) — paste the key, or type 'cancel':\n"}
		}
		if err := secrets.Add(p.envVar, input); err != nil {
			return loginResult{
				out:  fmt.Sprintf("[error: %v]\n", err),
				done: true,
			}
		}
		out := fmt.Sprintf("✓ Saved as %s\n", p.envVar)
		if p.suggested != "" {
			out += fmt.Sprintf("  Next: /model %s (or pick another id from your provider)\n", p.suggested)
		}
		return loginResult{out: out, done: true}
	}

	return loginResult{out: "[login state confused; aborting]\n", done: true}
}
