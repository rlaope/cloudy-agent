// Package cli is cloudy's command dispatcher. Commands self-register via
// init() and the binary entry-point delegates to Run.
//
// Adding a new command:
//
//	package cli
//	func init() { Register(&myCmd{}) }
//	type myCmd struct{}
//	func (myCmd) Name() string  { return "mycmd" }
//	func (myCmd) Short() string { return "what it does" }
//	func (myCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error { ... }
package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

// Command is the contract every cloudy subcommand satisfies.
type Command interface {
	// Name is the dispatch keyword (e.g. "ask").
	Name() string
	// Short is a one-line description shown in help.
	Short() string
	// Run executes the command. args excludes the command name itself.
	Run(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

// TUIRunner is the optional fallback invoked when cloudy is launched with no
// arguments. The cmd/cloudy main package supplies it; cli has no opinion on
// the TUI's implementation.
type TUIRunner func(stdout, stderr io.Writer) error

var (
	mu       sync.RWMutex
	commands = map[string]Command{}
)

// Register adds c to the global command table. It panics on duplicate names
// to surface mistakes at init time.
func Register(c Command) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := commands[c.Name()]; exists {
		panic(fmt.Sprintf("cli: command %q already registered", c.Name()))
	}
	commands[c.Name()] = c
}

// Lookup returns the registered command for name and whether it exists.
func Lookup(name string) (Command, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := commands[name]
	return c, ok
}

// All returns every registered command sorted by name.
func All() []Command {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Command, 0, len(commands))
	for _, c := range commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Run dispatches args to a registered command. With no args it invokes tui.
// Returns an error suitable for printing to stderr; the caller decides the
// exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, tui TUIRunner) error {
	if len(args) == 0 {
		if tui == nil {
			PrintHelp(stdout)
			return nil
		}
		return tui(stdout, stderr)
	}
	switch args[0] {
	case "--version", "-v", "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return nil
	case "--help", "-h", "help":
		PrintHelp(stdout)
		return nil
	}
	c, ok := Lookup(args[0])
	if !ok {
		PrintHelp(stderr)
		return fmt.Errorf("unknown command: %s", args[0])
	}
	return c.Run(ctx, args[1:], stdout, stderr)
}

// PrintHelp renders the top-level usage banner. The command list is built
// dynamically from the registered commands so adding a command is enough to
// have it appear here.
func PrintHelp(w io.Writer) {
	fmt.Fprintln(w, `cloudy — read-only multi-cluster SRE monitoring AI CLI agent

usage:
  cloudy                          enter full-screen TUI
  cloudy <command> [args]         run a subcommand
  cloudy --version                print version
  cloudy --help                   this help

commands:`)
	for _, c := range All() {
		fmt.Fprintf(w, "  %-22s  %s\n", c.Name(), c.Short())
	}
	fmt.Fprintln(w, `
common flags (per subcommand):
  --model <id>     override the default model
  --skill <name>   activate a skill (filters tool catalogue)
  --kubeconfig …   path to kubeconfig (default: KUBECONFIG / ~/.kube/config)
  --context <ctx>  kubeconfig context to use
  --no-color       disable ANSI colors (also via NO_COLOR env)
  --json           emit JSON instead of human text (where supported)`)
}

// errf is the canonical error helper used by every subcommand in this package.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
