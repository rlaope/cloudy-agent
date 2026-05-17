package tui

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/secrets"
)

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
	step     int    // 0: pick provider, 1: paste key
	provider string // one of: anthropic, openai, google, moonshot
}

// loginEnvVars maps a provider name to the environment variable cloudy
// reads at boot. Keep in sync with internal/llm/*/<provider>.go.
var loginEnvVars = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"google":    "GOOGLE_API_KEY",
	"moonshot":  "MOONSHOT_API_KEY",
}

// loginSuggestedModels is the model id printed back to the operator
// after a successful save, so they can copy/paste into /model.
var loginSuggestedModels = map[string]string{
	"anthropic": "claude-3-5-sonnet-20241022",
	"openai":    "gpt-4o-mini",
	"google":    "gemini-2.5-flash",
	"moonshot":  "kimi-k2-instruct",
}

// loginResult is what every Step returns: stream output to print, plus
// whether the conversation is finished (parent clears the active chat).
type loginResult struct {
	out  string
	done bool
}

// newLoginChat starts a fresh login conversation. The returned greeting
// is the first stream-write the caller should append.
func newLoginChat() (*loginChat, string) {
	greeting := "\n--- /login — add an LLM API key ---\n" +
		"Which provider? Type one of: anthropic, openai, google, moonshot\n" +
		"(or 'cancel' to abort)\n"
	return &loginChat{step: 0}, greeting
}

// Step advances the conversation by one operator answer.
func (l *loginChat) Step(input string) loginResult {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "cancel") {
		return loginResult{out: "[login cancelled]\n", done: true}
	}

	switch l.step {
	case 0:
		provider := strings.ToLower(input)
		envVar, ok := loginEnvVars[provider]
		if !ok {
			return loginResult{
				out: fmt.Sprintf("unknown provider %q. Try anthropic / openai / google / moonshot:\n", input),
			}
		}
		l.provider = provider
		l.step = 1
		return loginResult{
			out: fmt.Sprintf("Paste your %s API key (will be saved to ~/.cloudy/secrets as %s):\n",
				provider, envVar),
		}

	case 1:
		envVar := loginEnvVars[l.provider]
		if input == "" {
			return loginResult{out: "(empty) — paste the key, or type 'cancel':\n"}
		}
		if err := secrets.Add(envVar, input); err != nil {
			return loginResult{
				out:  fmt.Sprintf("[error: %v]\n", err),
				done: true,
			}
		}
		suggested := loginSuggestedModels[l.provider]
		out := fmt.Sprintf("✓ Saved as %s\n", envVar)
		if suggested != "" {
			out += fmt.Sprintf("  Next: /model %s (or pick another id from your provider)\n", suggested)
		}
		return loginResult{out: out, done: true}
	}

	return loginResult{out: "[login state confused; aborting]\n", done: true}
}
