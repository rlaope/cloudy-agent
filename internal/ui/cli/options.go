package cli

import (
	"flag"
	"io"
	"os"
)

// baseFlags is the small set of options shared by most subcommands. Commands
// embed it (as a value, not a pointer) and call bind() inside their flag
// parser. Subcommand-specific flags live on the subcommand's own option type.
type baseFlags struct {
	kubeconfig string
	context    string
	noColor    bool
	asJSON     bool
}

func (b *baseFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&b.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: KUBECONFIG / ~/.kube/config)")
	fs.StringVar(&b.context, "context", "", "kubeconfig context name")
	fs.BoolVar(&b.noColor, "no-color", noColorEnv(), "disable ANSI colors")
	fs.BoolVar(&b.asJSON, "json", false, "emit JSON instead of human-readable output")
}

// flagSet wraps *flag.FlagSet so subcommand option types can bind through one
// helper instead of taking the stdlib pointer directly.
type flagSet struct{ *flag.FlagSet }

// binder is implemented by every subcommand option struct. parseInto wires its
// flags and parses args, returning whatever positional values remain.
type binder interface {
	bind(fs *flagSet)
}

// parseInto allocates a flag.FlagSet under name, lets b register its flags via
// b.bind, parses args, and returns the trailing positional arguments.
func parseInto(b binder, name string, args []string, stderr io.Writer) ([]string, error) {
	fs := &flagSet{flag.NewFlagSet(name, flag.ContinueOnError)}
	fs.SetOutput(stderr)
	b.bind(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func noColorEnv() bool { return os.Getenv("NO_COLOR") != "" }
