// Command cloudy is a read-only multi-cluster SRE monitoring AI CLI agent.
//
// Running `cloudy` with no arguments enters the full-screen TUI. Subcommands
// (`ask`, `setup`, `doctor`, `skills`) cover one-shot and management flows.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cloudy:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		// TUI entry. Implemented in internal/tui (next milestone).
		fmt.Fprintln(stdout, "cloudy: TUI entry not yet implemented (v0.1 in progress)")
		fmt.Fprintln(stdout, "use 'cloudy --help' for available subcommands")
		return nil
	}

	switch args[0] {
	case "--version", "-v", "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return nil
	case "--help", "-h", "help":
		printHelp(stdout)
		return nil
	case "ask", "setup", "doctor", "skills", "session", "profile", "contexts":
		fmt.Fprintf(stderr, "cloudy: subcommand %q not yet implemented (v0.1 in progress)\n", args[0])
		return nil
	default:
		printHelp(stderr)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `cloudy — read-only multi-cluster SRE monitoring AI CLI agent

usage:
  cloudy                   enter full-screen TUI
  cloudy ask "<prompt>"    one-shot natural-language query
  cloudy setup             discover clusters, write ~/.cloudy/{config,profile}.yaml
  cloudy doctor            verify setup and reachability
  cloudy skills list       list installed skills
  cloudy --version         print version`)
}
