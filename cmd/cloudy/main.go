// Command cloudy is a read-only multi-cluster SRE monitoring AI CLI agent.
//
// Running `cloudy` with no arguments enters the full-screen TUI. Subcommands
// (`ask`, `setup`, `doctor`, `skills`) cover one-shot and management flows.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/buildinfo"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tui"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cloudy:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runTUI(stdout, stderr)
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

// runTUI enters the full-screen TUI when stdout is a TTY and config is present.
// When stdout is not a TTY (pipe/redirect) it falls back to a hint message.
func runTUI(stdout, stderr io.Writer) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(stdout, "cloudy: not a TTY — run `cloudy ask \"<prompt>\"` for one-shot mode")
		return nil
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: config error: %v\n", err)
		return nil
	}

	if cfg.DefaultModel == "" {
		fmt.Fprintln(stderr, "cloudy: no model configured — run `cloudy setup` to get started")
		return nil
	}

	// Best-effort: read kubeconfig current context.
	ctx, ns := currentKubeContext()

	deps := tui.Deps{
		Model:      cfg.DefaultModel,
		InitialCtx: ctx,
		InitialNS:  ns,
		// Provider, Skills, Tools, Session: wired in tui.Run when non-nil provider available.
	}

	return tui.Run(context.Background(), deps)
}

// currentKubeContext reads the current kubeconfig context and namespace
// from the KUBECONFIG / default kubeconfig, returning empty strings on error.
func currentKubeContext() (ctx, ns string) {
	// Best-effort only; we avoid importing the full k8s client here.
	// If KUBECONTEXT is set, use it.
	if v := os.Getenv("KUBECONTEXT"); v != "" {
		return v, os.Getenv("KUBENAMESPACE")
	}
	return "", ""
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
