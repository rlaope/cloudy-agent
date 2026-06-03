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

// loginChat is a three-step conversation that walks the operator
// through connecting an LLM:
//
//  1. provider     — pick anthropic / openai / google / moonshot (arrow picker)
//  2. key          — paste the API key (text input, saved to ~/.cloudy/secrets)
//  3. model        — pick the specific model id from a curated, current
//     lineup for the chosen provider (arrow picker)
//
// The conversation lives inside the main TUI stream. Each Step returns
// the text to print and (when relevant) a picker or a swapToModel hint
// the parent uses to activate the new provider.
type loginChat struct {
	step     int
	provider string // one of: anthropic, openai, google, moonshot
}

// Named steps for loginChat.step. Matches setupChat's naming style so
// debug prints stay legible.
const (
	loginStepProvider = 0
	loginStepKey      = 1
	loginStepModel    = 2
)

// loginModel is one entry in a provider's model picker. The id is what
// goes on the wire (and into config.yaml's default_model); the hint is
// the trailing dim description the operator sees while picking.
type loginModel struct {
	id   string
	hint string
}

// loginProvider is one row of the provider picker. Order here is the
// order shown on screen. models is the curated list shown at step 3;
// the first entry is the default-selected row so an operator who just
// hits Enter twice ends up on the provider's safe default.
type loginProvider struct {
	key    string // canonical name accepted as input
	envVar string // env-var written to ~/.cloudy/secrets
	hint   string // short "(Claude — …)" trailer in the provider picker
	models []loginModel
}

// loginProviders enumerates every provider /login can wire up, with
// the curated model lineup we surface at step 3. The first model in
// each list is the default-selected one (the cursor lands there and
// Enter takes it). Lists deliberately favour the latest released
// model first — model ids do deprecate, so we don't try to be
// exhaustive, just current-and-useful.
var loginProviders = []loginProvider{
	{
		key:    "anthropic",
		envVar: "ANTHROPIC_API_KEY",
		hint:   "Claude — Opus / Sonnet / Haiku",
		models: []loginModel{
			{"claude-opus-4-8", "Opus 4.8 — most capable, newest"},
			{"claude-opus-4-7", "Opus 4.7 — previous flagship"},
			{"claude-sonnet-4-6", "Sonnet 4.6 — balanced"},
			{"claude-haiku-4-5-20251001", "Haiku 4.5 — fastest, cheapest"},
		},
	},
	{
		key:    "openai",
		envVar: "OPENAI_API_KEY",
		hint:   "GPT — GPT-5.5 / o3",
		models: []loginModel{
			{"gpt-5.5", "GPT-5.5 — most capable, newest"},
			{"gpt-5.4", "GPT-5.4 — more affordable flagship"},
			{"gpt-5.4-mini", "GPT-5.4 mini — fast & cheap"},
			{"o3", "o3 — reasoning"},
		},
	},
	{
		key:    "google",
		envVar: "GOOGLE_API_KEY",
		hint:   "Gemini — 3.1 Pro / 3.5 Flash",
		models: []loginModel{
			{"gemini-3.1-pro-preview", "Gemini 3.1 Pro — most capable, newest"},
			{"gemini-3.5-flash", "Gemini 3.5 Flash — frontier, fast"},
			{"gemini-2.5-pro", "Gemini 2.5 Pro — stable"},
			{"gemini-2.5-flash", "Gemini 2.5 Flash — fast, stable"},
		},
	},
	{
		key:    "moonshot",
		envVar: "MOONSHOT_API_KEY",
		hint:   "Kimi — kimi-k2.6",
		models: []loginModel{
			{"kimi-k2.6", "Kimi K2.6 — newest"},
			{"kimi-k2.5", "Kimi K2.5 — previous"},
		},
	},
}

// loginResult is what every Step returns: stream output to print, an
// optional arrow-picker the parent should activate, a done flag the
// parent uses to clear the active chat, and an optional swapToModel
// the parent feeds into Deps.SwapModel so the just-picked model
// becomes active for the next turn without restarting cloudy.
type loginResult struct {
	out         string
	picker      *arrowPicker
	done        bool
	swapToModel string
}

// newLoginChat starts a fresh login conversation. The greeting is one
// line; the provider menu is an arrow-picker the parent activates from
// the returned loginResult.
func newLoginChat() (*loginChat, loginResult) {
	greeting := "\n--- /login — connect an LLM provider ---\n"
	items := make([]arrowPickerItem, 0, len(loginProviders))
	for _, p := range loginProviders {
		items = append(items, arrowPickerItem{
			label: p.key,
			hint:  p.hint,
			key:   p.key,
		})
	}
	return &loginChat{step: loginStepProvider}, loginResult{
		out:    greeting,
		picker: newArrowPicker("Pick an LLM provider:", items),
	}
}

// buildModelPicker materialises step 3 from the resolved provider's
// curated model list. The first entry is the default-selected one.
func (l *loginChat) buildModelPicker() *arrowPicker {
	p, _ := lookupLoginProvider(l.provider)
	items := make([]arrowPickerItem, 0, len(p.models))
	for _, m := range p.models {
		items = append(items, arrowPickerItem{
			label: m.id,
			hint:  m.hint,
			key:   m.id,
		})
	}
	return newArrowPicker(
		fmt.Sprintf("Pick a %s model (the first is the recommended default):", p.key),
		items,
	)
}

// modelKnown reports whether id matches one of the provider's curated
// model ids. Used at step 3 to reject stray text input.
func (l *loginChat) modelKnown(id string) bool {
	p, _ := lookupLoginProvider(l.provider)
	for _, m := range p.models {
		if m.id == id {
			return true
		}
	}
	return false
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
		l.step = loginStepModel
		return loginResult{
			out:    fmt.Sprintf("✓ Saved as %s\n", p.envVar),
			picker: l.buildModelPicker(),
		}

	case loginStepModel:
		if !l.modelKnown(input) {
			return loginResult{
				out:    fmt.Sprintf("unknown model %q for provider %q — pick from the list or 'cancel':\n", input, l.provider),
				picker: l.buildModelPicker(),
			}
		}
		return loginResult{
			out:         fmt.Sprintf("→ activating %s\n", input),
			done:        true,
			swapToModel: input,
		}
	}

	return loginResult{out: "[login state confused; aborting]\n", done: true}
}
