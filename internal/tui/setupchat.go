package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
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
// optional async command to dispatch, a done-flag, and an optional
// arrow picker the parent should activate. When picker is non-nil the
// parent installs it on the Model and routes ↑/↓/Space/Enter/Esc to
// it until it fires its resolve message; until then no fresh /setup
// step is invoked.
type setupResult struct {
	out    string
	cmd    tea.Cmd
	done   bool
	picker *arrowPicker
}

// newSetupChat lists kubeconfig contexts and emits the first prompt
// together with a Claude-style multi-select picker. When no contexts
// can be enumerated the chat itself is nil and the returned result
// carries an error string for the parent to write inline.
func newSetupChat(ctx context.Context, kubeconfigPath, cfgPath, profPath string) (*setupChat, setupResult) {
	contexts, _ := setup.ListKubeconfigContexts(kubeconfigPath)
	if len(contexts) == 0 {
		return nil, setupResult{
			out: "[/setup] no kubeconfig contexts found. " +
				"Set KUBECONFIG or place a config at ~/.kube/config, then run /setup again.\n",
		}
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
	return s, setupResult{
		out:    s.promptContexts(),
		picker: s.buildContextPicker(),
	}
}

// buildContextPicker materialises step 1 as a checkbox picker. The
// first context is pre-ticked so a single-context setup is one Enter
// away; multi-context operators tab through and space-toggle the
// extras they want.
func (s *setupChat) buildContextPicker() *arrowPicker {
	items := make([]arrowPickerItem, 0, len(s.contexts))
	for _, c := range s.contexts {
		items = append(items, arrowPickerItem{label: c, key: c})
	}
	preselect := []string{}
	if len(s.contexts) > 0 {
		preselect = append(preselect, s.contexts[0])
	}
	return newMultiArrowPicker("Pick one or more kubeconfig contexts:", items, preselect)
}

// buildFindingsPicker materialises step 2 (post-scan) as a checkbox
// picker over discovered backend kinds. Every detected kind is
// pre-ticked because /setup's default has always been "enable
// everything found" — the operator un-ticks anything they want to
// leave out.
func (s *setupChat) buildFindingsPicker() *arrowPicker {
	items := make([]arrowPickerItem, 0, len(s.groupKinds))
	for _, k := range s.groupKinds {
		count := 0
		for _, f := range s.findings {
			if f.Kind == k {
				count++
			}
		}
		items = append(items, arrowPickerItem{
			label: k,
			hint:  fmt.Sprintf("%d candidate(s)", count),
			key:   k,
		})
	}
	return newMultiArrowPicker("Pick which backends to enable:", items, s.groupKinds)
}

// ApplyMulti advances the chat off an arrow-picker multi-select
// confirmation. Step 0 → contexts chosen, kick off the scan.
// Step 2 → backend kinds chosen, kick off the save.
func (s *setupChat) ApplyMulti(keys []string, cancelled bool) setupResult {
	if cancelled {
		return setupResult{out: "[setup cancelled]\n", done: true}
	}
	switch s.step {
	case setupStepCtx:
		if len(keys) == 0 {
			return setupResult{
				out:    "(no context picked) — tick at least one, or Esc to cancel:\n",
				picker: s.buildContextPicker(),
			}
		}
		s.selected = keys
		s.step = setupStepScanning
		return setupResult{
			out: fmt.Sprintf("Scanning %d context(s) for prometheus / loki / postgres / pprof / …\n",
				len(keys)),
			cmd: s.runScan(),
		}
	case setupStepFindings:
		s.selectedKinds = map[string]bool{}
		for _, k := range keys {
			s.selectedKinds[k] = true
		}
		s.step = setupStepDone
		summary := "Saving cluster + RBAC configuration …\n"
		if len(keys) > 0 {
			summary = "Saving cluster + RBAC + selected backends …\n"
		}
		return setupResult{
			out: summary,
			cmd: s.runSave(),
		}
	}
	return setupResult{out: "[setup state confused; aborting]\n", done: true}
}

// Step is the text-input fallback for /setup. The pick steps are
// arrow-picker driven (see ApplyMulti), so the only typed input the
// operator routinely sends is "cancel". Anything else during a
// picker-driven step is a hint that the parent dropped the picker
// somehow; reply with a gentle nudge instead of crashing.
func (s *setupChat) Step(input string) setupResult {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "cancel") {
		return setupResult{out: "[setup cancelled]\n", done: true}
	}

	switch s.step {
	case setupStepScanning:
		return setupResult{out: "(still scanning; hold tight…)\n"}
	case setupStepCtx:
		return setupResult{
			out:    "(use the picker — ↑↓ Space Enter — or type 'cancel'):\n",
			picker: s.buildContextPicker(),
		}
	case setupStepFindings:
		return setupResult{
			out:    "(use the picker — ↑↓ Space Enter — or type 'cancel'):\n",
			picker: s.buildFindingsPicker(),
		}
	}
	return setupResult{out: "[setup state confused; aborting]\n", done: true}
}

// Apply receives async messages (scan, save) and advances state. The
// scan-done branch returns a multi-select picker so the operator picks
// backend kinds with arrow keys instead of typing a comma-separated
// number list; if nothing was discovered it skips straight to the save.
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
		if len(s.groupKinds) == 0 {
			// Nothing discovered — skip the picker, just save K8s.
			s.step = setupStepDone
			return setupResult{
				out: s.promptFindings(m.note) + "Saving cluster + RBAC configuration …\n",
				cmd: s.runSave(),
			}
		}
		return setupResult{
			out:    s.promptFindings(m.note),
			picker: s.buildFindingsPicker(),
		}

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
	return b.String()
}

func (s *setupChat) promptFindings(note string) string {
	var b strings.Builder
	if note != "" {
		fmt.Fprintf(&b, "  note: %s\n", note)
	}
	if len(s.groupKinds) == 0 {
		b.WriteString("Discovered backends: (none)\n")
		return b.String()
	}
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

	_, _ = wiring.Rebuild(cfg, wiring.RebuildOpts{
		KubeconfigPath: s.kubeconfig,
	})

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

// Picker-driven pick steps replaced the obsolete pickByIndexOrAll
// helper; see ApplyMulti and the arrowPicker resolve path in app.go.
