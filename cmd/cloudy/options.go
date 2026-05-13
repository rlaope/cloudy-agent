package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// commonOptions are flags shared across subcommands. Not every subcommand
// honors every field; each subcommand documents what it uses.
type commonOptions struct {
	model      string
	skill      string
	context    string
	namespace  string
	kubeconfig string
	noColor    bool
	asJSON     bool
	prompt     string
	auto       bool
	rescan     bool
	dryRun     bool
	quiet      bool
}

// parseFlags wraps flag.NewFlagSet with the common cloudy flag surface.
// Subcommands call this and then handle the resulting args themselves.
func parseFlags(name string, args []string, stderr io.Writer) (commonOptions, []string, error) {
	var o commonOptions
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.model, "model", "", "model id (e.g. claude-haiku-4-5-20251001, gpt-4o)")
	fs.StringVar(&o.skill, "skill", "", "skill to activate (e.g. jvm-gc)")
	fs.StringVar(&o.context, "context", "", "kubeconfig context name")
	fs.StringVar(&o.namespace, "namespace", "", "default namespace")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: KUBECONFIG / ~/.kube/config)")
	fs.BoolVar(&o.noColor, "no-color", noColorEnv(), "disable ANSI colors")
	fs.BoolVar(&o.asJSON, "json", false, "emit JSON instead of human-readable output")
	fs.StringVar(&o.prompt, "prompt", "", "prompt for one-shot mode (alias for positional)")
	fs.StringVar(&o.prompt, "p", "", "shorthand for --prompt")
	fs.BoolVar(&o.auto, "auto", false, "skip interactive prompts (setup)")
	fs.BoolVar(&o.rescan, "rescan", false, "force re-scan of clusters (setup)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "do not write files (setup)")
	fs.BoolVar(&o.quiet, "quiet", false, "suppress non-essential output")
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	return o, fs.Args(), nil
}

func noColorEnv() bool {
	return os.Getenv("NO_COLOR") != ""
}

// errf is a tiny helper to build a formatted error.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
