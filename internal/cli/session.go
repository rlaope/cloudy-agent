package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/session"
)

func init() { Register(&sessionCmd{}) }

type sessionCmd struct{}

func (sessionCmd) Name() string  { return "session" }
func (sessionCmd) Short() string { return `list / show / replay session logs` }

func (sessionCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy session <list|show|replay> [id]")
	}
	dir := filepath.Join(filepath.Dir(config.Path()), "logs")
	sub := args[0]
	rest := args[1:]
	_ = stderr

	switch sub {
	case "list":
		metas, err := session.List(dir)
		if err != nil {
			return errf("session list: %w", err)
		}
		fmt.Fprintf(stdout, "%-32s  %-25s  %5s  %s\n", "ID", "STARTED", "EVENTS", "MODEL")
		for _, m := range metas {
			fmt.Fprintf(stdout, "%-32s  %-25s  %5d  %s\n",
				m.ID, m.Started.Format("2006-01-02 15:04:05"), m.EventCount, m.Model)
		}
		return nil

	case "show", "replay":
		if len(rest) < 1 {
			return errf("usage: cloudy session %s <id>", sub)
		}
		path := filepath.Join(dir, rest[0]+".jsonl")
		r, err := session.Open(path)
		if err != nil {
			return errf("session %s: %w", sub, err)
		}
		ch := make(chan session.Event, 16)
		go r.Stream(ch)
		for ev := range ch {
			fmt.Fprintf(stdout, "[%s] %s %s\n", ev.Time.Format("15:04:05"), ev.Kind, truncate(ev.Text, 200))
		}
		return nil

	default:
		return errf("unknown session subcommand: %s", sub)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
