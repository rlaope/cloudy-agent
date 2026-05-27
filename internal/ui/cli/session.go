package cli

import (
	"context"
	"errors"
	"flag"
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

// sessionOptions exists so each subcommand can route through parseInto and get
// `--help` / `--no-color` handling for free. Without it, `cloudy session show
// --help` used to be treated as an id and tried to open `--help.jsonl`.
type sessionOptions struct {
	base baseFlags
}

func (o *sessionOptions) bind(fs *flagSet) { o.base.bind(fs.FlagSet) }

func (sessionCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy session <list|show|replay> [id]")
	}
	dir := filepath.Join(filepath.Dir(config.Path()), "logs")
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		var opts sessionOptions
		if _, err := parseInto(&opts, "session list", rest, stderr); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
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
		var opts sessionOptions
		parsed, err := parseInto(&opts, "session "+sub, rest, stderr)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if len(parsed) < 1 {
			return errf("usage: cloudy session %s <id>", sub)
		}
		path := filepath.Join(dir, parsed[0]+".jsonl")
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
