package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/selfupdate"
)

// selfUpdateDoneMsg carries the result of an in-process /update run.
// The captured log (every line selfupdate.Run wrote to its writer) is
// dumped into the stream so the operator sees the same blow-by-blow
// they would have seen at the CLI; the result + err drive the final
// status line.
type selfUpdateDoneMsg struct {
	log    string
	result selfupdate.Result
	err    error
}

// selfUpdateCmd runs the self-update in a tea.Cmd goroutine so the
// TUI stays responsive during the GitHub API roundtrip + binary
// download (~5–15s on a reasonable connection). All progress
// messages are captured into a buffer and replayed into the stream
// when the cmd resolves — interleaving them live would require a
// channel-pump similar to the agent stream, which is overkill for a
// rarely-invoked maintenance command.
func selfUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		var buf strings.Builder
		res, err := selfupdate.Run(context.Background(), &buf)
		return selfUpdateDoneMsg{
			log:    buf.String(),
			result: res,
			err:    err,
		}
	}
}
