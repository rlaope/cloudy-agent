package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/setup"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/wiring"
)

// setupChat is the stream-inline replacement for the full-screen setup
// wizard. It walks four prompts — context, findings, model, save — and
// reuses the wiring/discovery primitives the wizard already exercises
// (RunDiscovery, BuildRegistry, Replace, config persistence). Anything
// that requires per-finding interaction (credentials, hints, skill
// curation) is intentionally cut from the inline flow; the operator
// can edit cloudy.yaml directly for those advanced cases.
type setupChat struct {
	step       int
	ctx        context.Context
	kubeconfig string
	cfgPath    string
	profPath   string
	builtin    []*skills.Skill

	contexts      []string
	selected      []string
	findings      []discovery.Finding
	groupKinds    []string // unique kinds in detection order
	selectedKinds map[string]bool
}

const (
	setupStepCtx      = 0
	setupStepScanning = 1
	setupStepFindings = 2
	setupStepDone     = 3
)

// setupScanDoneMsg arrives after the async discovery scan finishes.
type setupScanDoneMsg struct {
	findings []discovery.Finding
	note     string
	err      error
}

// setupSaveDoneMsg arrives after the registry hot-swap and config
// persistence finish.
type setupSaveDoneMsg struct {
	summary string
	err     error
}

// setupResult mirrors loginResult: text to write to the stream, an
// optional async command to dispatch, and a done-flag.
type setupResult struct {
	out  string
	cmd  tea.Cmd
	done bool
}

// newSetupChat lists kubeconfig contexts and emits the first prompt.
// Returns nil when no contexts can be enumerated — caller writes an
// error to the stream and aborts entry.
func newSetupChat(ctx context.Context, kubeconfigPath, cfgPath, profPath string) (*setupChat, string) {
	contexts, _ := setup.ListKubeconfigContexts(kubeconfigPath)
	if len(contexts) == 0 {
		return nil, "[/setup] no kubeconfig contexts found. " +
			"Set KUBECONFIG or place a config at ~/.kube/config, then run /setup again.\n"
	}
	builtin, _ := skills.LoadBuiltin()

	s := &setupChat{
		step:          setupStepCtx,
		ctx:           ctx,
		kubeconfig:    kubeconfigPath,
		cfgPath:       cfgPath,
		profPath:      profPath,
		builtin:       builtin,
		contexts:      contexts,
		selectedKinds: map[string]bool{},
	}
	return s, s.promptContexts()
}

// Step processes the operator's typed answer for the current step.
func (s *setupChat) Step(input string) setupResult {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "cancel") {
		return setupResult{out: "[setup cancelled]\n", done: true}
	}

	switch s.step {
	case setupStepCtx:
		picked, err := pickByIndexOrAll(input, s.contexts)
		if err != nil {
			return setupResult{out: fmt.Sprintf("(%v) — try again or 'cancel':\n", err)}
		}
		s.selected = picked
		s.step = setupStepScanning
		return setupResult{
			out: fmt.Sprintf("Scanning %d context(s) for prometheus / loki / postgres / pprof / …\n",
				len(picked)),
			cmd: s.runScan(),
		}

	case setupStepScanning:
		// Operator typed while a scan was running; remind them to wait.
		return setupResult{out: "(still scanning; hold tight…)\n"}

	case setupStepFindings:
		if len(s.findings) == 0 {
			// Nothing discovered — operator's enter saves the K8s-only
			// configuration and finishes setup.
			s.step = setupStepDone
			return setupResult{
				out: "Saving cluster + RBAC configuration …\n",
				cmd: s.runSave(),
			}
		}
		if !strings.EqualFold(input, "skip") && !strings.EqualFold(input, "none") {
			picked, err := pickByIndexOrAll(input, s.groupKinds)
			if err != nil {
				return setupResult{out: fmt.Sprintf("(%v) — try again, 'skip', or 'cancel':\n", err)}
			}
			s.selectedKinds = map[string]bool{}
			for _, k := range picked {
				s.selectedKinds[k] = true
			}
		} else {
			s.selectedKinds = map[string]bool{}
		}
		s.step = setupStepDone
		return setupResult{
			out: "Saving cluster + RBAC + selected backends …\n",
			cmd: s.runSave(),
		}
	}

	return setupResult{out: "[setup state confused; aborting]\n", done: true}
}

// Apply receives async messages (scan, save) and advances state.
func (s *setupChat) Apply(msg tea.Msg) setupResult {
	switch m := msg.(type) {
	case setupScanDoneMsg:
		if m.err != nil {
			return setupResult{out: fmt.Sprintf("[scan error: %v]\n", m.err), done: true}
		}
		s.findings = m.findings
		s.groupKinds = uniqueKinds(m.findings)
		// Default-select every detected kind.
		for _, k := range s.groupKinds {
			s.selectedKinds[k] = true
		}
		s.step = setupStepFindings
		return setupResult{out: s.promptFindings(m.note)}

	case setupSaveDoneMsg:
		if m.err != nil {
			return setupResult{out: fmt.Sprintf("[save error: %v]\n", m.err), done: true}
		}
		return setupResult{out: m.summary, done: true}
	}
	return setupResult{}
}

// --- step prompts ---

func (s *setupChat) promptContexts() string {
	var b strings.Builder
	b.WriteString("\n--- /setup — analyse infrastructure & establish access ---\n")
	b.WriteString("(use /login separately to connect an LLM provider)\n\n")
	b.WriteString("Available kubeconfig contexts:\n")
	for i, c := range s.contexts {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, c)
	}
	b.WriteString("\nType a number, comma-separated list (e.g. 1,2), 'all', or 'cancel':\n")
	return b.String()
}

func (s *setupChat) promptFindings(note string) string {
	var b strings.Builder
	if note != "" {
		fmt.Fprintf(&b, "  note: %s\n", note)
	}
	if len(s.groupKinds) == 0 {
		b.WriteString("Discovered backends: (none)\n")
		b.WriteString("Press Enter to continue with K8s tools only.\n")
		return b.String()
	}
	b.WriteString("Discovered backends:\n")
	for i, k := range s.groupKinds {
		count := 0
		for _, f := range s.findings {
			if f.Kind == k {
				count++
			}
		}
		fmt.Fprintf(&b, "  %d. %-12s (%d candidates)\n", i+1, k, count)
	}
	b.WriteString("Type numbers (e.g. 1,2), 'all', 'skip', or 'cancel'. " +
		"Default: all detected backends.\n")
	return b.String()
}

// --- async commands ---

func (s *setupChat) runScan() tea.Cmd {
	ctx := s.ctx
	kubeconfigPath := s.kubeconfig
	contexts := append([]string(nil), s.selected...)
	return func() tea.Msg {
		findings, note, err := wiring.RunDiscovery(ctx, wiring.DiscoveryOptions{
			KubeconfigPath: kubeconfigPath,
			Contexts:       contexts,
		})
		return setupScanDoneMsg{findings: findings, note: note, err: err}
	}
}

func (s *setupChat) runSave() tea.Cmd {
	return func() tea.Msg {
		summary, err := s.persistAndSwap()
		return setupSaveDoneMsg{summary: summary, err: err}
	}
}

// persistAndSwap writes profile.yaml + cloudy.yaml, rebuilds the
// registry from the new config + active profile, and swaps it in.
// Returns the human summary printed back to the stream.
func (s *setupChat) persistAndSwap() (string, error) {
	scanResults := setup.ScanResultsForContexts(s.ctx, s.kubeconfig, s.selected, 30*time.Second)
	profile := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      scanResults,
		GeneratedAt:   time.Now(),
	}
	// Auto-enable every recommended skill — same default the wizard
	// applies in non-interactive mode.
	recs := setup.Recommend(profile, s.builtin)
	for _, r := range recs {
		profile.RecommendedSkills = append(profile.RecommendedSkills, r.SkillName)
	}
	if err := config.SaveProfile(s.profPath, profile); err != nil {
		return "", fmt.Errorf("save profile: %w", err)
	}

	// Preserve any existing DefaultModel from a prior cloudy.yaml; /setup
	// only touches infrastructure (contexts + backends), not the AI
	// connection. The /login conversation owns model selection.
	cfg := config.Default()
	if prev, err := config.Load(s.cfgPath); err == nil {
		cfg.DefaultModel = prev.DefaultModel
	}
	cfg.Contexts = append([]string(nil), s.selected...)
	// Project selected findings into typed config slices.
	pickedFindings := s.filterFindings()
	logs, traces, proms, pprofEps, nodeEps, dbs := setup.ConvertFindingsForChat(pickedFindings)
	cfg.Prometheus = append(cfg.Prometheus, proms...)
	cfg.Logs = append(cfg.Logs, logs...)
	cfg.Tracing = append(cfg.Tracing, traces...)
	cfg.Pprof = append(cfg.Pprof, pprofEps...)
	cfg.NodeInspectors = append(cfg.NodeInspectors, nodeEps...)
	cfg.Databases = append(cfg.Databases, dbs...)
	if err := config.Save(s.cfgPath, cfg); err != nil {
		return "", fmt.Errorf("save cloudy.yaml: %w", err)
	}

	activeProfile, _ := permission.LoadActive()
	newReg, _ := wiring.BuildRegistry(wiring.Options{
		KubeconfigPath: s.kubeconfig,
		Contexts:       cfg.Contexts,
		Profile:        activeProfile,
		PromEndpoints:  cfg.Prometheus,
		Databases:      cfg.Databases,
		Logs:           cfg.Logs,
		Tracing:        cfg.Tracing,
		Pprof:          cfg.Pprof,
		NodeInspectors: cfg.NodeInspectors,
	})
	wiring.Replace(newReg)

	enabled := len(pickedFindings)
	nextStep := "ask cloudy a question, or /tools to inspect the new registry."
	modelLine := cfg.DefaultModel
	if modelLine == "" {
		modelLine = "(unset)"
		nextStep = "/login to connect an LLM, then ask cloudy a question."
	}
	return fmt.Sprintf("✓ infrastructure ready\n"+
		"  contexts : %s\n"+
		"  backends : %d enabled (of %d discovered)\n"+
		"  skills   : %d auto-enabled\n"+
		"  model    : %s\n"+
		"Next: %s\n",
		strings.Join(cfg.Contexts, ", "),
		enabled, len(s.findings),
		len(recs),
		modelLine,
		nextStep,
	), nil
}

func (s *setupChat) filterFindings() []discovery.Finding {
	if len(s.selectedKinds) == 0 {
		return nil
	}
	out := make([]discovery.Finding, 0, len(s.findings))
	for _, f := range s.findings {
		if s.selectedKinds[f.Kind] {
			out = append(out, f)
		}
	}
	return out
}

// --- helpers ---

func uniqueKinds(fs []discovery.Finding) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range fs {
		if !seen[f.Kind] {
			seen[f.Kind] = true
			out = append(out, f.Kind)
		}
	}
	sort.Strings(out)
	return out
}

// pickByIndexOrAll parses "1", "1,3", or "all" against a candidate list
// and returns the picked subset. Returns an error for empty input or
// any out-of-range index.
func pickByIndexOrAll(input string, candidates []string) ([]string, error) {
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}
	if strings.EqualFold(input, "all") {
		return append([]string(nil), candidates...), nil
	}
	parts := strings.Split(input, ",")
	picked := make([]string, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("not a number: %q", p)
		}
		if n < 1 || n > len(candidates) {
			return nil, fmt.Errorf("index %d out of range (1..%d)", n, len(candidates))
		}
		picked = append(picked, candidates[n-1])
	}
	return picked, nil
}
