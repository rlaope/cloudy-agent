// Command cloudy is a read-only multi-cluster SRE monitoring AI CLI agent.
//
// Running `cloudy` with no arguments enters the full-screen TUI. Subcommands
// (ask / setup / doctor / skills / session / contexts / profile) cover one-shot
// and management flows.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/buildinfo"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/tui"
	"github.com/rlaope/cloudy/internal/wiring"
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
	case "ask":
		return runAsk(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "skills":
		return runSkills(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "contexts":
		return runContexts(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	default:
		printHelp(stderr)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

// runTUI enters the full-screen TUI when stdout is a TTY and config is present.
// When stdout is not a TTY (pipe/redirect) it prints a hint instead.
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

	// Resolve active Permission Profile up front so wiring can install the
	// namespace allow/deny middleware and apply tool-name filters in one pass.
	activeProfile, _ := permission.LoadActive()

	toolReg, warn := wiring.BuildRegistry(wiring.Options{
		Contexts:      cfg.Contexts,
		Profile:       activeProfile,
		PromEndpoints: cfg.Prometheus,
		EnableJVM:     true,
		EnablePython:  true,
		EnableGPU:     true,
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
	defer sess.Close()

	ctx, ns := currentKubeContext()
	deps := tui.Deps{
		Provider:   provider,
		Model:      modelID,
		Skills:     skillReg,
		Tools:      toolReg,
		Session:    sess,
		InitialCtx: ctx,
		InitialNS:  ns,
	}

	return tui.Run(context.Background(), deps)
}

// currentKubeContext returns the current kubeconfig context and namespace
// using KUBECONTEXT / KUBENAMESPACE env vars (best-effort).
func currentKubeContext() (ctx, ns string) {
	if v := os.Getenv("KUBECONTEXT"); v != "" {
		return v, os.Getenv("KUBENAMESPACE")
	}
	return "", ""
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `cloudy — read-only multi-cluster SRE monitoring AI CLI agent

usage:
  cloudy                          enter full-screen TUI
  cloudy ask "<prompt>"           one-shot natural-language query
  cloudy setup                    discover clusters, write ~/.cloudy/{config,profile}.yaml
  cloudy doctor                   verify setup and reachability
  cloudy skills list              list installed skills
  cloudy skills show <name>       show a skill definition
  cloudy session list             list past sessions
  cloudy session show <id>        print stored session events
  cloudy session replay <id>      replay a stored session
  cloudy contexts                 list kubeconfig contexts
  cloudy profile list             show the discovered cluster profile
  cloudy --version                print version
  cloudy --help                   this help

flags (per subcommand):
  --model <id>     override the default model
  --skill <name>   activate a skill (filters tool catalogue)
  --kubeconfig …   path to kubeconfig (default: KUBECONFIG / ~/.kube/config)
  --context <ctx>  kubeconfig context to use
  --no-color       disable ANSI colors (also via NO_COLOR env)
  --json           emit JSON instead of human text (where supported)`)
}
