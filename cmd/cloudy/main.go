// Command cloudy is a read-only multi-cluster SRE monitoring AI CLI agent.
//
// Running `cloudy` with no arguments enters the full-screen TUI. Subcommands
// (ask / setup / doctor / skills / session / contexts / profile) live in
// internal/cli; main only wires the entry-point.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/cli"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/tui"
	"github.com/rlaope/cloudy/internal/wiring"
)

func main() {
	if err := cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, bootTUI); err != nil {
		fmt.Fprintln(os.Stderr, "cloudy:", err)
		os.Exit(1)
	}
}

// bootTUI launches the full-screen interactive UI. It is the cli.TUIRunner
// hook for `cloudy` invoked with no arguments.
func bootTUI(stdout, stderr io.Writer) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(stdout, `cloudy: not a TTY — run cloudy ask "<prompt>" for one-shot mode`)
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

	provider, modelID, err := wiring.BuildProvider(cfg.DefaultModel)
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", err)
		return nil
	}

	skillReg, err := wiring.BuildSkillRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: skills: %v\n", err)
		return nil
	}

	activeProfile, _ := permission.LoadActive()

	toolReg, warn := wiring.BuildRegistry(wiring.Options{
		Contexts:      cfg.Contexts,
		Profile:       activeProfile,
		PromEndpoints: cfg.Prometheus,
	})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}
	if activeProfile != nil {
		fmt.Fprintf(stderr, "cloudy: profile=%s\n", activeProfile.Name)
	}

	sess, err := session.New("")
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: session: %v\n", err)
		return nil
	}
	defer func() { _ = sess.Close() }()

	ctxName, ns := currentKubeContext()
	deps := tui.Deps{
		Provider:   provider,
		Model:      modelID,
		Skills:     skillReg,
		Tools:      toolReg,
		Session:    sess,
		InitialCtx: ctxName,
		InitialNS:  ns,
	}
	return tui.Run(context.Background(), deps)
}

func currentKubeContext() (ctxName, ns string) {
	if v := os.Getenv("KUBECONTEXT"); v != "" {
		return v, os.Getenv("KUBENAMESPACE")
	}
	return "", ""
}
