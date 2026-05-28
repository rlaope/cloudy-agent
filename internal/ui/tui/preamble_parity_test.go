package tui

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/agent"
)

// TestPalette_CommandsDocumentedInPreamble guards against drift between the
// two hand-maintained slash-command lists that live in separate packages:
//
//   - palette.builtinItems (this package) — what the operator can pick, and
//   - agent.BasePreamble() — what the LLM is told cloudy supports.
//
// A command offered by the palette but absent from the preamble makes the
// agent answer "I don't know that command" for something cloudy actually
// does — the exact regression the preamble exists to prevent. This test
// caught /use missing from the preamble when the native-scrollback work
// landed; keep it green by documenting every palette command in the
// preamble's "## cloudy slash commands" section.
func TestPalette_CommandsDocumentedInPreamble(t *testing.T) {
	preamble := agent.BasePreamble()
	for _, item := range builtinItems {
		if !strings.Contains(preamble, "/"+item.title) {
			t.Errorf("slash command /%s is offered by the palette but not documented in "+
				"agent.BasePreamble(); add it to the preamble's slash-command list so the "+
				"agent knows the command exists", item.title)
		}
	}
}
