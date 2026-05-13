package setup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/skills"
)

// WizardOptions configures the setup wizard.
type WizardOptions struct {
	// Theme controls colour output.
	Theme render.Theme

	// ConfigPath is the target path for config.yaml.
	ConfigPath string

	// ProfilePath is the target path for profile.yaml.
	ProfilePath string

	// KubeconfigPath is the path to the user's kubeconfig file. Empty = default.
	KubeconfigPath string

	// AutoRun skips interactive prompts: all contexts are selected and defaults
	// are accepted. Useful for CI and scripted setup.
	AutoRun bool
}

// Run launches the setup wizard and blocks until the user completes or aborts
// all five steps. On success the wizard writes config.yaml and profile.yaml.
func Run(ctx context.Context, opts WizardOptions) error {
	// Load available builtin skills for the recommendation step.
	builtinSkills, _ := skills.LoadBuiltin()

	m := &wizardModel{
		ctx:           ctx,
		opts:          opts,
		builtinSkills: builtinSkills,
		step:          stepKubeconfig,
		spinner:       spinner.New(spinner.WithSpinner(spinner.Dot)),
	}

	// Discover kubeconfig contexts before launching the TUI.
	contexts, err := listKubeconfigContexts(opts.KubeconfigPath)
	if err != nil {
		return fmt.Errorf("setup wizard: list kubeconfig contexts: %w", err)
	}
	m.allContexts = contexts

	if opts.AutoRun {
		// Non-interactive path: select all contexts and run scans.
		m.selectedContexts = contexts
		return m.runAutomatic()
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("setup wizard: %w", err)
	}
	wm, ok := finalModel.(*wizardModel)
	if !ok || wm.aborted {
		return fmt.Errorf("setup wizard: aborted by user")
	}
	if wm.saveErr != nil {
		return wm.saveErr
	}
	return nil
}

// --- step constants ---

type wizardStep int

const (
	stepKubeconfig wizardStep = iota
	stepScan
	stepFillIn
	stepSkills
	stepSave
	stepDone
)

// --- messages ---

type scanDoneMsg struct {
	results []ContextResult
}

type saveDoneMsg struct{ err error }

// --- model ---

type wizardModel struct {
	ctx           context.Context
	opts          WizardOptions
	builtinSkills []*skills.Skill

	step    wizardStep
	spinner spinner.Model
	aborted bool
	saveErr error

	// Step 1 — kubeconfig
	allContexts      []string
	selectedContexts []string
	cursorCtx        int

	// Step 2 — scan
	scanning    bool
	scanResults []ContextResult

	// Step 3 — fill-in
	inputs        []*textinput.Model
	focusedInput  int
	filledProm    string
	filledModel   string
	filledTokens  string
	filledUSD     string
	filledSecrets string

	// Step 4 — skills
	recommendations []Recommendation
	enabledSkills   map[string]bool
	cursorSkill     int

	// Step 5 — save
	profile config.Profile
	cfg     config.Config
}

func (m *wizardModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m *wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case scanDoneMsg:
		m.scanning = false
		m.scanResults = msg.results
		m.step = stepFillIn
		m.initFillIn()
		return m, nil

	case saveDoneMsg:
		m.saveErr = msg.err
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

func (m *wizardModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepKubeconfig:
		return m.handleKubeconfigKey(msg)
	case stepFillIn:
		return m.handleFillInKey(msg)
	case stepSkills:
		return m.handleSkillsKey(msg)
	}
	return m, nil
}

// --- Step 1: kubeconfig context selection ---

func (m *wizardModel) handleKubeconfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.aborted = true
		return m, tea.Quit
	case "up", "k":
		if m.cursorCtx > 0 {
			m.cursorCtx--
		}
	case "down", "j":
		if m.cursorCtx < len(m.allContexts)-1 {
			m.cursorCtx++
		}
	case " ":
		ctx := m.allContexts[m.cursorCtx]
		m.selectedContexts = toggleString(m.selectedContexts, ctx)
	case "enter":
		if len(m.selectedContexts) == 0 {
			// Nothing selected — require at least one.
			return m, nil
		}
		m.step = stepScan
		m.scanning = true
		return m, m.startScan()
	case "a":
		// Select / deselect all.
		if len(m.selectedContexts) == len(m.allContexts) {
			m.selectedContexts = nil
		} else {
			m.selectedContexts = append([]string(nil), m.allContexts...)
		}
	}
	return m, nil
}

func (m *wizardModel) startScan() tea.Cmd {
	return func() tea.Msg {
		results := scanContextsConcurrent(m.ctx, m.opts.KubeconfigPath, m.selectedContexts, 30*time.Second)
		return scanDoneMsg{results: results}
	}
}

// --- Step 3: fill-in ---

func (m *wizardModel) initFillIn() {
	// Build suggested prom URL from scan results.
	var promSuggestion string
	for _, r := range m.scanResults {
		if len(r.PrometheusURLs) > 0 {
			promSuggestion = r.PrometheusURLs[0]
			break
		}
	}

	prom := textinput.New()
	prom.Placeholder = "http://prometheus.monitoring.svc:9090"
	prom.SetValue(promSuggestion)

	model := textinput.New()
	model.Placeholder = "claude-3-5-sonnet-20241022"
	model.SetValue("claude-3-5-sonnet-20241022")

	tokens := textinput.New()
	tokens.Placeholder = "100000 (0 = unlimited)"
	tokens.SetValue("0")

	usd := textinput.New()
	usd.Placeholder = "0.00 (0 = unlimited)"
	usd.SetValue("0")

	secrets := textinput.New()
	secrets.Placeholder = "false"
	secrets.SetValue("false")

	m.inputs = []*textinput.Model{&prom, &model, &tokens, &usd, &secrets}
	m.focusedInput = 0
	m.inputs[0].Focus()
}

func (m *wizardModel) handleFillInKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "tab", "down":
		m.inputs[m.focusedInput].Blur()
		m.focusedInput = (m.focusedInput + 1) % len(m.inputs)
		m.inputs[m.focusedInput].Focus()
	case "shift+tab", "up":
		m.inputs[m.focusedInput].Blur()
		m.focusedInput = (m.focusedInput - 1 + len(m.inputs)) % len(m.inputs)
		m.inputs[m.focusedInput].Focus()
	case "enter":
		if m.focusedInput < len(m.inputs)-1 {
			m.inputs[m.focusedInput].Blur()
			m.focusedInput++
			m.inputs[m.focusedInput].Focus()
		} else {
			// Advance to skills step.
			m.step = stepSkills
			m.initSkills()
		}
	default:
		// Forward to focused input.
		updated, cmd := m.inputs[m.focusedInput].Update(msg)
		*m.inputs[m.focusedInput] = updated
		return m, cmd
	}
	return m, nil
}

// --- Step 4: skills ---

func (m *wizardModel) initSkills() {
	// Build profile from scan results for recommendation logic.
	p := buildProfileFromScans(m.scanResults)
	m.recommendations = Recommend(p, m.builtinSkills)
	m.enabledSkills = make(map[string]bool, len(m.recommendations))
	for _, r := range m.recommendations {
		m.enabledSkills[r.SkillName] = true
	}
	m.cursorSkill = 0
}

func (m *wizardModel) handleSkillsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "up", "k":
		if m.cursorSkill > 0 {
			m.cursorSkill--
		}
	case "down", "j":
		if m.cursorSkill < len(m.recommendations)-1 {
			m.cursorSkill++
		}
	case " ":
		if len(m.recommendations) > 0 {
			name := m.recommendations[m.cursorSkill].SkillName
			m.enabledSkills[name] = !m.enabledSkills[name]
		}
	case "enter":
		m.step = stepSave
		return m, m.startSave()
	}
	return m, nil
}

func (m *wizardModel) startSave() tea.Cmd {
	return func() tea.Msg {
		err := m.persistConfig()
		return saveDoneMsg{err: err}
	}
}

// --- View ---

func (m *wizardModel) View() string {
	switch m.step {
	case stepKubeconfig:
		return m.viewKubeconfig()
	case stepScan:
		return m.viewScan()
	case stepFillIn:
		return m.viewFillIn()
	case stepSkills:
		return m.viewSkills()
	case stepSave:
		return m.viewSave()
	case stepDone:
		return m.viewDone()
	}
	return ""
}

func (m *wizardModel) viewKubeconfig() string {
	var b strings.Builder
	b.WriteString(header("Step 1/5 — Select Kubernetes contexts"))
	b.WriteString("\nUse ↑/↓ to move, SPACE to select, A to toggle all, ENTER to continue.\n\n")
	for i, ctx := range m.allContexts {
		cursor := "  "
		if i == m.cursorCtx {
			cursor = "> "
		}
		checked := "[ ]"
		if contains(m.selectedContexts, ctx) {
			checked = "[x]"
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, checked, ctx))
	}
	if len(m.selectedContexts) == 0 {
		b.WriteString("\n(select at least one context)\n")
	}
	return b.String()
}

func (m *wizardModel) viewScan() string {
	return fmt.Sprintf("%s\n\n%s  Scanning %d context(s)…\n",
		header("Step 2/5 — Scanning cluster(s)"),
		m.spinner.View(),
		len(m.selectedContexts),
	)
}

func (m *wizardModel) viewFillIn() string {
	labels := []string{
		"Prometheus URL",
		"Default model",
		"Max tokens per session (0=unlimited)",
		"Max USD per day     (0=unlimited)",
		"Allow secrets (true/false)",
	}
	var b strings.Builder
	b.WriteString(header("Step 3/5 — Configuration"))
	b.WriteString("\nTAB/↑↓ to move between fields, ENTER to advance, ENTER on last field to continue.\n\n")
	for i, inp := range m.inputs {
		label := labels[i]
		if i == m.focusedInput {
			label = lipgloss.NewStyle().Bold(true).Render(label)
		}
		b.WriteString(fmt.Sprintf("  %s\n  %s\n\n", label, inp.View()))
	}
	return b.String()
}

func (m *wizardModel) viewSkills() string {
	var b strings.Builder
	b.WriteString(header("Step 4/5 — Recommended skills"))
	b.WriteString("\nSPACE to toggle, ENTER to confirm.\n\n")
	for i, rec := range m.recommendations {
		cursor := "  "
		if i == m.cursorSkill {
			cursor = "> "
		}
		checked := "[ ]"
		if m.enabledSkills[rec.SkillName] {
			checked = "[x]"
		}
		b.WriteString(fmt.Sprintf("%s%s %-20s  %s\n", cursor, checked, rec.SkillName, rec.Reason))
	}
	return b.String()
}

func (m *wizardModel) viewSave() string {
	return fmt.Sprintf("%s\n\n%s  Saving configuration…\n",
		header("Step 5/5 — Saving"),
		m.spinner.View(),
	)
}

func (m *wizardModel) viewDone() string {
	if m.saveErr != nil {
		return fmt.Sprintf("\nError saving configuration: %v\n", m.saveErr)
	}
	return "\nSetup complete. Run 'cloudy' to start.\n"
}

// --- persistence ---

func (m *wizardModel) persistConfig() error {
	profile := buildProfileFromScans(m.scanResults)
	profile.GeneratedAt = time.Now()

	// Collect enabled skill names.
	var enabled []string
	for _, r := range m.recommendations {
		if m.enabledSkills[r.SkillName] {
			enabled = append(enabled, r.SkillName)
		}
	}
	profile.RecommendedSkills = enabled

	if err := config.SaveProfile(m.opts.ProfilePath, profile); err != nil {
		return err
	}

	// Build config.
	cfg := config.Default()
	if m.inputs != nil && len(m.inputs) >= 5 {
		promURL := strings.TrimSpace(m.inputs[0].Value())
		if promURL != "" {
			cfg.Prometheus = []config.PrometheusEndpoint{{Name: "default", URL: promURL}}
		}
		if v := strings.TrimSpace(m.inputs[1].Value()); v != "" {
			cfg.DefaultModel = v
		}
		if v := strings.TrimSpace(m.inputs[2].Value()); v != "" {
			fmt.Sscan(v, &cfg.Safety.MaxTokensPerSession)
		}
		if v := strings.TrimSpace(m.inputs[3].Value()); v != "" {
			fmt.Sscan(v, &cfg.Safety.MaxUSDPerDay)
		}
		if strings.TrimSpace(m.inputs[4].Value()) == "true" {
			cfg.Safety.AllowSecrets = true
		}
	}

	return config.Save(m.opts.ConfigPath, cfg)
}

// --- AutoRun (non-interactive) ---

func (m *wizardModel) runAutomatic() error {
	results := scanContextsConcurrent(m.ctx, m.opts.KubeconfigPath, m.selectedContexts, 30*time.Second)
	m.scanResults = results
	return m.persistConfig()
}

// --- helpers ---

func header(title string) string {
	return lipgloss.NewStyle().Bold(true).Render(title)
}

func toggleString(slice []string, s string) []string {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return append(slice, s)
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// listKubeconfigContexts returns the names of all contexts in the kubeconfig.
func listKubeconfigContexts(kubeconfigPath string) ([]string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{},
	).RawConfig()
	if err != nil {
		// If no kubeconfig exists at all, return empty without error.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	return names, nil
}

// scanContextsConcurrent runs ScanContext for each context concurrently with a
// per-context timeout. Results are returned in the same order as contexts.
func scanContextsConcurrent(ctx context.Context, kubeconfigPath string, contexts []string, perCtxTimeout time.Duration) []ContextResult {
	results := make([]ContextResult, len(contexts))
	var wg sync.WaitGroup
	for i, ctxName := range contexts {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			scanCtx, cancel := context.WithTimeout(ctx, perCtxTimeout)
			defer cancel()
			r, _ := ScanContext(scanCtx, kubeconfigPath, name)
			results[idx] = r
		}(i, ctxName)
	}
	wg.Wait()
	return results
}

// buildProfileFromScans assembles a config.Profile from scan results.
func buildProfileFromScans(results []ContextResult) config.Profile {
	return config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      results,
	}
}
