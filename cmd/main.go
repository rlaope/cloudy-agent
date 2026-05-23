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
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/cli"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/secrets"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/tools"
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

	// Export any persisted credentials before building provider clients so
	// that *_env config fields resolve correctly.
	_ = secrets.Load()

	// Detect first run: config file is absent when os.Stat returns not-exist.
	cfgPath := config.Path()
	_, statErr := os.Stat(cfgPath)
	firstRun := os.IsNotExist(statErr)

	cfg, _ := config.Load(cfgPath) // missing file returns Default() + nil err

	var (
		provider    llm.Provider
		modelID     string
		providerErr error
	)
	if cfg.DefaultModel != "" {
		provider, modelID, providerErr = wiring.BuildProvider(cfg.DefaultModel)
		if providerErr != nil {
			fmt.Fprintf(stderr, "cloudy: %v\n", providerErr)
			// Do NOT return — proceed into TUI with provider=nil so the user
			// can run /setup to fix the configuration.
		}
	}

	skillReg, err := wiring.BuildSkillRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: skills: %v\n", err)
		// Non-fatal: continue without user skills.
	}

	toolReg, warn := wiring.Rebuild(cfg, wiring.RebuildOpts{})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}
	// Re-load the active profile here even though wiring.Rebuild already
	// loaded it internally: cfg.Safety knobs below are dampened through
	// permission.EffectiveLogLines / EffectiveProfileSeconds, both of which
	// need the *permission.Profile value, and a one-line stderr surface
	// reminds the operator which profile shaped this session.
	activeProfile, _ := permission.LoadActive()
	if activeProfile != nil {
		fmt.Fprintf(stderr, "cloudy: profile=%s\n", activeProfile.Name)
	}

	// Inventory banner: surface what got wired and what got silently
	// skipped so the operator never has to type /tools just to discover
	// that, say, the trace group was dropped because no Tempo/Jaeger
	// endpoint was discovered. Quiet when everything wired cleanly to
	// stay out of the way on healthy startups.
	if line := wiringInventoryLine(toolReg); line != "" {
		fmt.Fprintln(stderr, line)
	}

	// Warn-only: a user skill under ~/.cloudy/skills/ may legitimately
	// reference tools not wired in the current cluster shape. Built-in
	// refs are pinned by TestSkillToolRefsAreValid in CI.
	if err := wiring.ValidateSkillToolRefs(skillReg, toolReg); err != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", err)
	}

	sess, err := session.New("")
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: session: %v\n", err)
		return nil
	}
	defer func() { _ = sess.Close() }()

	ctxName, ns := currentKubeContext()
	deps := tui.Deps{
		Provider:                 provider,
		Model:                    modelID,
		Skills:                   skillReg,
		Tools:                    toolReg,
		Session:                  sess,
		InitialCtx:               ctxName,
		InitialNS:                ns,
		FirstRun:                 firstRun,
		MaxTokensPerSession:      cfg.Safety.MaxTokensPerSession,
		MaxUSDPerDay:             cfg.Safety.MaxUSDPerDay,
		MaxConversationSeconds:   cfg.Safety.MaxConversationSeconds,
		MaxLogLinesPerCall:       permission.EffectiveLogLines(activeProfile, cfg.Safety.MaxLogLines),
		MaxProfileSecondsPerCall: permission.EffectiveProfileSeconds(activeProfile, cfg.Safety.MaxProfileSeconds),
		MaxLogResponseBytes:      cfg.Safety.MaxLogResponseBytes,
	}
	return tui.Run(context.Background(), deps)
}

func currentKubeContext() (ctxName, ns string) {
	if v := os.Getenv("KUBECONTEXT"); v != "" {
		return v, os.Getenv("KUBENAMESPACE")
	}
	return "", ""
}

// wiringInventoryLine returns a single-line startup summary of which tool
// groups were wired and which were skipped (with the first-line reason).
// Returns "" when every probed group succeeded, so the cleanly-configured
// case stays quiet. Operators previously had to type /tools to discover
// that, say, the trace group had been dropped because no Tempo/Jaeger
// endpoint was discovered — this surfaces that signal at boot.
func wiringInventoryLine(reg *tools.Registry) string {
	if reg == nil {
		return ""
	}
	inv := reg.Inventory()
	var wired, skipped []string
	for _, g := range inv.Groups {
		if g.Skipped {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", g.Name, g.Reason))
		} else {
			wired = append(wired, g.Name)
		}
	}
	if len(skipped) == 0 {
		return ""
	}
	return fmt.Sprintf("cloudy: %d tool groups wired (%s); %d skipped — %s. type /tools for detail.",
		len(wired), strings.Join(wired, ", "), len(skipped), strings.Join(skipped, "; "))
}
