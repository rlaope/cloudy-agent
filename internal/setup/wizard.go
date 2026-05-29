package setup

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/secrets"
	"github.com/rlaope/cloudy/internal/wiring"
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
// the flow. On success the wizard writes config.yaml and profile.yaml.
func Run(ctx context.Context, opts WizardOptions) error {
	m := NewWizardModel(ctx, opts)

	if opts.AutoRun {
		// Non-interactive path: select all contexts and run scans/persist
		// without driving a tea.Program.
		m.selectedContexts = append([]string(nil), m.allContexts...)
		return m.runAutomatic()
	}

	p := tea.NewProgram(teaAdapter{m}, tea.WithAltScreen(), tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("setup wizard: %w", err)
	}
	adapter, ok := finalModel.(teaAdapter)
	if !ok || adapter.m.aborted {
		return fmt.Errorf("setup wizard: aborted by user")
	}
	if adapter.m.saveErr != nil {
		return adapter.m.saveErr
	}
	return nil
}

// teaAdapter wraps *WizardModel in the standard tea.Model interface so a
// stand-alone tea.Program can drive it. The exported Update keeps the more
// specific *WizardModel return type for embedders that hold a concrete value.
type teaAdapter struct{ m *WizardModel }

func (a teaAdapter) Init() tea.Cmd { return a.m.Init() }
func (a teaAdapter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	wm, cmd := a.m.Update(msg)
	return teaAdapter{wm}, cmd
}
func (a teaAdapter) View() string { return a.m.View() }

// --- step constants ---

type wizardStep int

const (
	stepKubeconfig wizardStep = iota
	stepScan
	stepDiscovered  // checkbox over discovery.Findings grouped by Kind
	stepCredentials // stream-inline Q&A for each selected backend's auth
	stepHints       // stream-inline "any external URL to add?" loop
	stepFillIn      // LLM model + safety limits only
	stepSkills
	stepSave
	stepDone
)

// --- messages ---

type scanDoneMsg struct {
	results  []ContextResult
	findings []discovery.Finding
	note     string
	cloudIDs []discovery.CloudIdentity
}

type saveDoneMsg struct{ err error }

// --- model ---

// WizardModel is a bubbletea sub-model embedded by the main TUI when
// mode=="setup", and also driven standalone by `cloudy setup` via Run().
type WizardModel struct {
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
	scanning      bool
	scanResults   []ContextResult
	findings      []discovery.Finding
	discoveryNote string
	cloudIDs      []discovery.CloudIdentity

	// Step 3 — discovered findings (grouped checkbox)
	findingGroups      []findingGroup // sorted by Kind
	selectedGroupKinds map[string]bool
	cursorGroup        int

	// Step 4 — credentials (stream-inline Q&A)
	credQueue    []credPrompt
	credIndex    int
	credInput    textinput.Model
	credAnswered []credAnswer
	// authEnvByFindingIdx maps an index into m.selectedFindings to the env-var
	// name that should be threaded into config emission at step 7.
	authEnvByFindingIdx map[int]string

	// Step 5 — hint loop
	hintInput   textinput.Model
	httpHints   []config.HTTPEndpoint
	dbHints     []config.DatabaseEndpoint
	hintError   string
	hintCounter map[string]int

	// Step 6 — fill-in
	inputs       []*textinput.Model
	focusedInput int

	// Step 7 — skills
	recommendations []Recommendation
	enabledSkills   map[string]bool
	cursorSkill     int

	// Selected findings carried into step 7.
	selectedFindings []discovery.Finding

	// Step 8 — save
	profile config.Profile
	cfg     config.Config
}

// findingGroup is one row in the step-3 checkbox view: all Findings sharing the
// same Kind, listed under a single group header.
type findingGroup struct {
	Kind    string
	Group   discovery.Group
	Members []discovery.Finding
}

// credPrompt is one entry in the step-4 stream-Q&A queue.
type credPrompt struct {
	findingIdx int // index into m.selectedFindings
	finding    discovery.Finding
	question   string
}

// credAnswer is a rendered transcript entry for previously-answered prompts.
type credAnswer struct {
	finding discovery.Finding
	envVar  string
	mode    string // "env" or "literal"
}

// NewWizardModel constructs a WizardModel that the parent TUI can embed.
// Init() returns the bubbletea startup command(s). The constructor performs
// the cheap, synchronous setup (kubeconfig listing) so the caller surfaces
// errors before tea.Program is started.
func NewWizardModel(ctx context.Context, opts WizardOptions) *WizardModel {
	builtin, _ := skills.LoadBuiltin()
	contexts, _ := listKubeconfigContexts(opts.KubeconfigPath)
	return &WizardModel{
		ctx:                 ctx,
		opts:                opts,
		builtinSkills:       builtin,
		step:                stepKubeconfig,
		spinner:             spinner.New(spinner.WithSpinner(spinner.Dot)),
		allContexts:         contexts,
		selectedGroupKinds:  map[string]bool{},
		hintCounter:         map[string]int{},
		authEnvByFindingIdx: map[int]string{},
	}
}

// Init returns the bubbletea startup command.
func (m *WizardModel) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update advances the wizard in response to bubbletea messages. The exported
// return type is *WizardModel so callers embedding the wizard can hold a
// concrete pointer through the entire lifecycle.
func (m *WizardModel) Update(msg tea.Msg) (*WizardModel, tea.Cmd) {
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
		m.findings = msg.findings
		m.discoveryNote = msg.note
		m.cloudIDs = msg.cloudIDs
		m.initDiscovered()
		m.step = stepDiscovered
		return m, nil

	case saveDoneMsg:
		m.saveErr = msg.err
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

func (m *WizardModel) handleKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
	switch m.step {
	case stepKubeconfig:
		return m.handleKubeconfigKey(msg)
	case stepDiscovered:
		return m.handleDiscoveredKey(msg)
	case stepCredentials:
		return m.handleCredentialsKey(msg)
	case stepHints:
		return m.handleHintsKey(msg)
	case stepFillIn:
		return m.handleFillInKey(msg)
	case stepSkills:
		return m.handleSkillsKey(msg)
	}
	return m, nil
}

// Done reports whether the wizard has reached the terminal stepDone state.
func (m *WizardModel) Done() bool { return m.step == stepDone }

// Aborted reports whether the user pressed ctrl+c / q before save.
func (m *WizardModel) Aborted() bool { return m.aborted }

// SaveErr returns the persistence error from step 7, if any.
func (m *WizardModel) SaveErr() error { return m.saveErr }

// --- Step 1: kubeconfig context selection ---

func (m *WizardModel) handleKubeconfigKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
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
		if len(m.allContexts) == 0 {
			return m, nil
		}
		ctx := m.allContexts[m.cursorCtx]
		m.selectedContexts = toggleString(m.selectedContexts, ctx)
	case "enter":
		if len(m.selectedContexts) == 0 {
			return m, nil
		}
		m.step = stepScan
		m.scanning = true
		return m, m.startScan()
	case "a":
		if len(m.selectedContexts) == len(m.allContexts) {
			m.selectedContexts = nil
		} else {
			m.selectedContexts = append([]string(nil), m.allContexts...)
		}
	}
	return m, nil
}

func (m *WizardModel) startScan() tea.Cmd {
	ctx := m.ctx
	kubeconfigPath := m.opts.KubeconfigPath
	contexts := append([]string(nil), m.selectedContexts...)
	return func() tea.Msg {
		var (
			results  []ContextResult
			findings []discovery.Finding
			note     string
			cloudIDs []discovery.CloudIdentity
			wg       sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			results = scanContextsConcurrent(ctx, kubeconfigPath, contexts, 30*time.Second)
			findings, note, _ = wiring.RunDiscovery(ctx, wiring.DiscoveryOptions{
				KubeconfigPath: kubeconfigPath,
				Contexts:       contexts,
			})
		}()
		go func() {
			defer wg.Done()
			cloudIDs = discovery.ProbeCloudIdentities(ctx)
		}()
		wg.Wait()
		return scanDoneMsg{results: results, findings: findings, note: note, cloudIDs: cloudIDs}
	}
}

// --- Step 3: discovered findings (grouped checkbox) ---

func (m *WizardModel) initDiscovered() {
	groupsByKind := map[string][]discovery.Finding{}
	groupOf := map[string]discovery.Group{}
	for _, f := range m.findings {
		groupsByKind[f.Kind] = append(groupsByKind[f.Kind], f)
		groupOf[f.Kind] = f.Group
	}
	kinds := make([]string, 0, len(groupsByKind))
	for k := range groupsByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	m.findingGroups = make([]findingGroup, 0, len(kinds))
	for _, k := range kinds {
		m.findingGroups = append(m.findingGroups, findingGroup{
			Kind:    k,
			Group:   groupOf[k],
			Members: groupsByKind[k],
		})
		// Default-select groups that have members.
		m.selectedGroupKinds[k] = true
	}
	m.cursorGroup = 0
}

func (m *WizardModel) handleDiscoveredKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.aborted = true
		return m, tea.Quit
	case "up", "k":
		if m.cursorGroup > 0 {
			m.cursorGroup--
		}
	case "down", "j":
		if m.cursorGroup < len(m.findingGroups)-1 {
			m.cursorGroup++
		}
	case " ":
		if len(m.findingGroups) == 0 {
			return m, nil
		}
		g := m.findingGroups[m.cursorGroup]
		if len(g.Members) == 0 {
			return m, nil // empty groups are not toggleable
		}
		m.selectedGroupKinds[g.Kind] = !m.selectedGroupKinds[g.Kind]
	case "enter":
		m.commitSelectedFindings()
		m.initCredentials()
		if len(m.credQueue) == 0 {
			m.initHints()
			m.step = stepHints
		} else {
			m.step = stepCredentials
		}
		return m, nil
	}
	return m, nil
}

// commitSelectedFindings flattens m.findingGroups by the toggle state and
// stores the result on m.selectedFindings.
func (m *WizardModel) commitSelectedFindings() {
	m.selectedFindings = nil
	for _, g := range m.findingGroups {
		if !m.selectedGroupKinds[g.Kind] {
			continue
		}
		m.selectedFindings = append(m.selectedFindings, g.Members...)
	}
}

// --- Step 4: credentials (stream-inline Q&A) ---

func (m *WizardModel) initCredentials() {
	m.credQueue = m.credQueue[:0]
	for i, f := range m.selectedFindings {
		if f.AuthHint.Kind == discovery.AuthNone || f.AuthHint.Kind == "" {
			continue
		}
		m.credQueue = append(m.credQueue, credPrompt{
			findingIdx: i,
			finding:    f,
			question:   credQuestionFor(f),
		})
	}
	m.credIndex = 0
	m.credAnswered = m.credAnswered[:0]

	if len(m.credQueue) > 0 {
		ti := textinput.New()
		ti.Placeholder = "value or $ENV_VAR"
		ti.Focus()
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '*'
		ti.CharLimit = 256
		m.credInput = ti
	}
}

// credQuestionFor builds the human-readable prompt string for a finding.
func credQuestionFor(f discovery.Finding) string {
	target := f.Source.ServiceName
	if target == "" {
		target = f.Source.ExternalURL
	}
	if target == "" {
		target = f.EndpointURL
	}
	switch f.AuthHint.Kind {
	case discovery.AuthBasic:
		return fmt.Sprintf("%s basic-auth password for %s: ", f.Kind, target)
	case discovery.AuthBearer:
		return fmt.Sprintf("%s bearer token for %s: ", f.Kind, target)
	case discovery.AuthPassword:
		return fmt.Sprintf("%s password for %s: ", f.Kind, target)
	default:
		return fmt.Sprintf("%s credential for %s: ", f.Kind, target)
	}
}

func (m *WizardModel) handleCredentialsKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
	if m.credIndex >= len(m.credQueue) {
		m.initHints()
		m.step = stepHints
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "esc":
		// Skip this credential — leave the auth env empty.
		m.credIndex++
		m.advanceCredOrHints()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.credInput.Value())
		cur := m.credQueue[m.credIndex]
		if raw == "" {
			// Treat blank as skip.
			m.credIndex++
			m.advanceCredOrHints()
			return m, nil
		}
		envName, mode, err := m.recordCredential(cur, raw)
		if err != nil {
			// Surface the error inline by overriding the question text; let
			// the user retry on the same prompt.
			m.credQueue[m.credIndex].question = fmt.Sprintf("error: %v — retry: ", err)
			return m, nil
		}
		m.credAnswered = append(m.credAnswered, credAnswer{
			finding: cur.finding,
			envVar:  envName,
			mode:    mode,
		})
		m.credInput.SetValue("")
		m.credIndex++
		m.advanceCredOrHints()
		return m, nil
	default:
		var cmd tea.Cmd
		m.credInput, cmd = m.credInput.Update(msg)
		return m, cmd
	}
}

func (m *WizardModel) advanceCredOrHints() {
	if m.credIndex >= len(m.credQueue) {
		m.initHints()
		m.step = stepHints
	}
}

// recordCredential applies the input semantics described in the spec:
//
//   - `$NAME`     → store env-var reference; do not call os.Getenv now.
//   - literal     → secrets.Add(generated env name, value); store env-var name.
//
// Returns the env-var name that should be threaded into the emitted config.
func (m *WizardModel) recordCredential(p credPrompt, raw string) (string, string, error) {
	if strings.HasPrefix(raw, "$") {
		env := strings.TrimSpace(strings.TrimPrefix(raw, "$"))
		if env == "" {
			return "", "", fmt.Errorf("empty env name")
		}
		m.authEnvByFindingIdx[p.findingIdx] = env
		return env, "env", nil
	}
	env := generatedEnvVarName(p.finding)
	if err := secrets.Add(env, raw); err != nil {
		return "", "", err
	}
	m.authEnvByFindingIdx[p.findingIdx] = env
	return env, "literal", nil
}

// generatedEnvVarName builds the CLOUDY_<KIND>_<NAME-UPPER>_PWD convention.
func generatedEnvVarName(f discovery.Finding) string {
	name := f.Source.ServiceName
	if name == "" {
		name = f.Source.ExternalURL
	}
	if name == "" {
		name = f.Kind
	}
	upper := strings.ToUpper(sanitizeIdent(name))
	kind := strings.ToUpper(sanitizeIdent(f.Kind))
	return fmt.Sprintf("CLOUDY_%s_%s_PWD", kind, upper)
}

// sanitizeIdent replaces any character outside [A-Za-z0-9_] with `_`.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// --- Step 5: hint loop ---

func (m *WizardModel) initHints() {
	ti := textinput.New()
	ti.Placeholder = "kind URL [bearer-env|basic-user:basic-pass-env] or 'done'"
	ti.Focus()
	ti.CharLimit = 512
	m.hintInput = ti
	m.hintError = ""
}

func (m *WizardModel) handleHintsKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "esc":
		m.advanceHintsToFillIn()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.hintInput.Value())
		if raw == "" || raw == "done" || raw == "skip" {
			m.advanceHintsToFillIn()
			return m, nil
		}
		if err := m.parseAndStoreHint(raw); err != nil {
			m.hintError = err.Error()
		} else {
			m.hintError = ""
		}
		m.hintInput.SetValue("")
		return m, nil
	default:
		var cmd tea.Cmd
		m.hintInput, cmd = m.hintInput.Update(msg)
		return m, cmd
	}
}

func (m *WizardModel) advanceHintsToFillIn() {
	m.step = stepFillIn
	m.initFillIn()
}

// parseAndStoreHint parses a single-line hint of the form
// `kind URL [bearer-env|basic-user:basic-pass-env]` and routes it into either
// httpHints or dbHints depending on Kind.
func (m *WizardModel) parseAndStoreHint(line string) error {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return fmt.Errorf("expected: kind URL [auth]")
	}
	kind := strings.ToLower(fields[0])
	urlOrDSN := fields[1]
	tail := ""
	if len(fields) > 2 {
		tail = strings.Join(fields[2:], " ")
	}

	m.hintCounter[kind]++
	name := fmt.Sprintf("%s-%d", kind, m.hintCounter[kind])

	switch kind {
	case "postgres", "mysql", "redis":
		ep := config.DatabaseEndpoint{Name: name, Kind: kind, DSN: urlOrDSN}
		if tail != "" {
			// Single-token tail = password env var name.
			ep.PasswordEnv = strings.TrimSpace(tail)
		}
		m.dbHints = append(m.dbHints, ep)
		return nil
	case "prom", "prometheus", "loki", "elasticsearch", "tempo", "jaeger", "pprof", "v8":
		ep := config.HTTPEndpoint{Name: name, Kind: kind, URL: urlOrDSN}
		if tail != "" {
			if strings.Contains(tail, ":") {
				parts := strings.SplitN(tail, ":", 2)
				ep.BasicUser = strings.TrimSpace(parts[0])
				ep.BasicPassEnv = strings.TrimSpace(parts[1])
			} else {
				ep.BearerEnv = strings.TrimSpace(tail)
			}
		}
		m.httpHints = append(m.httpHints, ep)
		return nil
	default:
		// Roll back the counter so the next valid line of this kind starts at 1.
		m.hintCounter[kind]--
		return fmt.Errorf("unknown kind %q", kind)
	}
}

// --- Step 6: fill-in ---

func (m *WizardModel) initFillIn() {
	model := textinput.New()
	model.Placeholder = "claude-opus-4-8"
	model.SetValue("claude-opus-4-8")

	tokens := textinput.New()
	tokens.Placeholder = "100000 (0 = unlimited)"
	tokens.SetValue("0")

	usd := textinput.New()
	usd.Placeholder = "0.00 (0 = unlimited)"
	usd.SetValue("0")

	secretsIn := textinput.New()
	secretsIn.Placeholder = "false"
	secretsIn.SetValue("false")

	m.inputs = []*textinput.Model{&model, &tokens, &usd, &secretsIn}
	m.focusedInput = 0
	m.inputs[0].Focus()
}

func (m *WizardModel) handleFillInKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
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
			m.step = stepSkills
			m.initSkills()
		}
	default:
		updated, cmd := m.inputs[m.focusedInput].Update(msg)
		*m.inputs[m.focusedInput] = updated
		return m, cmd
	}
	return m, nil
}

// --- Step 7: skills ---

func (m *WizardModel) initSkills() {
	p := buildProfileFromScans(m.scanResults)
	m.recommendations = Recommend(p, m.builtinSkills)
	m.enabledSkills = make(map[string]bool, len(m.recommendations))
	for _, r := range m.recommendations {
		m.enabledSkills[r.SkillName] = true
	}
	m.cursorSkill = 0
}

func (m *WizardModel) handleSkillsKey(msg tea.KeyMsg) (*WizardModel, tea.Cmd) {
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

func (m *WizardModel) startSave() tea.Cmd {
	return func() tea.Msg {
		err := m.persistConfig()
		return saveDoneMsg{err: err}
	}
}

// --- View ---

// View renders the current step.
func (m *WizardModel) View() string {
	switch m.step {
	case stepKubeconfig:
		return m.viewKubeconfig()
	case stepScan:
		return m.viewScan()
	case stepDiscovered:
		return m.viewDiscovered()
	case stepCredentials:
		return m.viewCredentials()
	case stepHints:
		return m.viewHints()
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

func (m *WizardModel) viewKubeconfig() string {
	var b strings.Builder
	b.WriteString(header("Step 1/7 — Select Kubernetes contexts"))
	b.WriteString("\nUse ↑/↓ to move, SPACE to select, A to toggle all, ENTER to continue.\n\n")
	if len(m.allContexts) == 0 {
		b.WriteString("(no kubeconfig contexts found)\n")
		return b.String()
	}
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

func (m *WizardModel) viewScan() string {
	return fmt.Sprintf("%s\n\n%s  Scanning %d context(s)…\n",
		header("Step 2/7 — Scanning cluster(s)"),
		m.spinner.View(),
		len(m.selectedContexts),
	)
}

func (m *WizardModel) viewDiscovered() string {
	var b strings.Builder
	b.WriteString(header("Step 3/7 — Discovered backends"))
	b.WriteString("\n↑/↓ to move group, SPACE to toggle, ENTER to continue.\n\n")
	if m.discoveryNote != "" {
		b.WriteString("  note: " + m.discoveryNote + "\n\n")
	}
	if len(m.cloudIDs) > 0 {
		b.WriteString("  Cloud identities:\n")
		for _, id := range m.cloudIDs {
			b.WriteString(renderCloudIdentityRow(id))
		}
		b.WriteString("\n")
	}
	if len(m.findingGroups) == 0 {
		b.WriteString("  (no backends discovered)\n")
		return b.String()
	}
	for i, g := range m.findingGroups {
		cursor := "  "
		if i == m.cursorGroup {
			cursor = "> "
		}
		checked := "[ ]"
		if m.selectedGroupKinds[g.Kind] && len(g.Members) > 0 {
			checked = "[x]"
		}
		count := len(g.Members)
		var detail string
		if count == 0 {
			detail = "no candidates"
		} else {
			labels := make([]string, 0, count)
			for _, f := range g.Members {
				if f.Source.External {
					labels = append(labels, f.Source.ExternalURL)
					continue
				}
				if f.Source.Namespace != "" && f.Source.ServiceName != "" {
					labels = append(labels, fmt.Sprintf("%s/%s", f.Source.Namespace, f.Source.ServiceName))
				} else if f.EndpointURL != "" {
					labels = append(labels, f.EndpointURL)
				} else {
					labels = append(labels, f.Kind)
				}
			}
			detail = strings.Join(labels, ", ")
		}
		b.WriteString(fmt.Sprintf("%s%s %-10s (%d)  %s\n", cursor, checked, g.Kind, count, detail))
	}
	// Advisory: if any cloud identity is available, remind the operator that
	// they can add a cloud_aws / cloud_gcp / cloud_azure block to cloudy.yaml.
	for _, id := range m.cloudIDs {
		if id.Available {
			b.WriteString("\n  Tip: one or more cloud identities detected. Add a cloud_aws: / cloud_gcp: / cloud_azure: block to cloudy.yaml to enable cloud tools.\n")
			break
		}
	}
	return b.String()
}

func (m *WizardModel) viewCredentials() string {
	var b strings.Builder
	b.WriteString(header("Step 4/7 — Credentials"))
	b.WriteString("\nType a value (echoed as *) or '$ENV_NAME' to reference an env var. ENTER to confirm, ESC to skip.\n\n")
	for _, ans := range m.credAnswered {
		target := ans.finding.Source.ServiceName
		if target == "" {
			target = ans.finding.Source.ExternalURL
		}
		b.WriteString(fmt.Sprintf("  ✓ %s (%s) → %s [%s]\n", ans.finding.Kind, target, ans.envVar, ans.mode))
	}
	if m.credIndex < len(m.credQueue) {
		cur := m.credQueue[m.credIndex]
		b.WriteString("\n  " + lipgloss.NewStyle().Bold(true).Render(cur.question))
		b.WriteString("\n  " + m.credInput.View() + "\n")
	} else {
		b.WriteString("\n  (no more credentials)\n")
	}
	return b.String()
}

func (m *WizardModel) viewHints() string {
	var b strings.Builder
	b.WriteString(header("Step 5/7 — External endpoints"))
	b.WriteString("\nAdd any external HTTP / DB URL the cluster scan missed. Type 'done' or press ESC to continue.\n\n")
	if len(m.httpHints)+len(m.dbHints) > 0 {
		for _, h := range m.httpHints {
			b.WriteString(fmt.Sprintf("  • %-12s %s  (%s)\n", h.Kind, h.URL, h.Name))
		}
		for _, h := range m.dbHints {
			b.WriteString(fmt.Sprintf("  • %-12s %s  (%s)\n", h.Kind, h.DSN, h.Name))
		}
		b.WriteString("\n")
	}
	if m.hintError != "" {
		b.WriteString("  error: " + m.hintError + "\n")
	}
	b.WriteString("  > " + m.hintInput.View() + "\n")
	return b.String()
}

func (m *WizardModel) viewFillIn() string {
	labels := []string{
		"Default model",
		"Max tokens per session (0=unlimited)",
		"Max USD per day     (0=unlimited)",
		"Allow secrets (true/false)",
	}
	var b strings.Builder
	b.WriteString(header("Step 6/7 — Configuration"))
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

func (m *WizardModel) viewSkills() string {
	var b strings.Builder
	b.WriteString(header("Step 7/7 — Recommended skills"))
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

func (m *WizardModel) viewSave() string {
	return fmt.Sprintf("%s\n\n%s  Saving configuration…\n",
		header("Saving"),
		m.spinner.View(),
	)
}

func (m *WizardModel) viewDone() string {
	if m.saveErr != nil {
		return fmt.Sprintf("\nError saving configuration: %v\n", m.saveErr)
	}
	return "\nSetup complete. Run 'cloudy' to start.\n"
}

// --- persistence ---

func (m *WizardModel) persistConfig() error {
	profile := buildProfileFromScans(m.scanResults)
	profile.GeneratedAt = time.Now()

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

	cfg := config.Default()
	cfg.Contexts = append([]string(nil), m.selectedContexts...)

	// Apply step-6 fill-in values.
	if len(m.inputs) >= 4 {
		if v := strings.TrimSpace(m.inputs[0].Value()); v != "" {
			cfg.DefaultModel = v
		}
		if v := strings.TrimSpace(m.inputs[1].Value()); v != "" {
			fmt.Sscan(v, &cfg.Safety.MaxTokensPerSession)
		}
		if v := strings.TrimSpace(m.inputs[2].Value()); v != "" {
			fmt.Sscan(v, &cfg.Safety.MaxUSDPerDay)
		}
		if strings.TrimSpace(m.inputs[3].Value()) == "true" {
			cfg.Safety.AllowSecrets = true
		}
	}

	// Convert findings + hints → typed config slices.
	logs, traces, proms, pprofEps, nodeEps, dbs := convertFindings(m.selectedFindings, m.authEnvByFindingIdx)
	cfg.Prometheus = append(cfg.Prometheus, proms...)
	cfg.Logs = append(cfg.Logs, logs...)
	cfg.Tracing = append(cfg.Tracing, traces...)
	cfg.Pprof = append(cfg.Pprof, pprofEps...)
	cfg.NodeInspectors = append(cfg.NodeInspectors, nodeEps...)
	cfg.Databases = append(cfg.Databases, dbs...)

	// Layer in step-5 user-supplied hints, dispatched by Kind.
	for _, h := range m.httpHints {
		switch strings.ToLower(h.Kind) {
		case "prom", "prometheus":
			cfg.Prometheus = append(cfg.Prometheus, config.PrometheusEndpoint{
				Name:         h.Name,
				URL:          h.URL,
				BasicUser:    h.BasicUser,
				BasicPassEnv: h.BasicPassEnv,
				BearerEnv:    h.BearerEnv,
			})
		case "loki", "elasticsearch":
			cfg.Logs = append(cfg.Logs, h)
		case "tempo", "jaeger":
			cfg.Tracing = append(cfg.Tracing, h)
		case "pprof":
			cfg.Pprof = append(cfg.Pprof, h)
		case "v8":
			cfg.NodeInspectors = append(cfg.NodeInspectors, h)
		}
	}
	cfg.Databases = append(cfg.Databases, m.dbHints...)

	if err := config.Save(m.opts.ConfigPath, cfg); err != nil {
		return err
	}

	// Rebuild registry from the freshly written config + active profile.
	// Orchestrator owns the BuildRegistry + Replace sequence so this site,
	// cmd/main.go, and tui/setupchat.go cannot drift apart.
	_, _ = wiring.Rebuild(cfg, wiring.RebuildOpts{
		KubeconfigPath: m.opts.KubeconfigPath,
	})
	return nil
}

// convertFindings is the testable seam that maps a slice of selected Findings
// (plus an optional env-var map keyed by findings index) into the typed config
// slices the wizard emits at step 7. It does not touch I/O.
func convertFindings(findings []discovery.Finding, authEnvByIdx map[int]string) (
	logs []config.HTTPEndpoint,
	traces []config.HTTPEndpoint,
	proms []config.PrometheusEndpoint,
	pprofEps []config.HTTPEndpoint,
	nodeEps []config.HTTPEndpoint,
	dbs []config.DatabaseEndpoint,
) {
	counters := map[string]int{}
	nameFor := func(kind string) string {
		counters[kind]++
		return fmt.Sprintf("%s-%d", kind, counters[kind])
	}

	for i, f := range findings {
		envName := ""
		if authEnvByIdx != nil {
			envName = authEnvByIdx[i]
		}
		switch strings.ToLower(f.Kind) {
		case "prometheus", "prom":
			ep := config.PrometheusEndpoint{Name: nameFor("prom"), URL: f.EndpointURL}
			applyAuthToProm(&ep, f.AuthHint.Kind, envName)
			proms = append(proms, ep)
		case "loki", "elasticsearch":
			ep := config.HTTPEndpoint{Name: nameFor(f.Kind), Kind: f.Kind, URL: f.EndpointURL}
			applyAuthToHTTP(&ep, f.AuthHint.Kind, envName)
			logs = append(logs, ep)
		case "tempo", "jaeger":
			ep := config.HTTPEndpoint{Name: nameFor(f.Kind), Kind: f.Kind, URL: f.EndpointURL}
			applyAuthToHTTP(&ep, f.AuthHint.Kind, envName)
			traces = append(traces, ep)
		case "pprof":
			ep := config.HTTPEndpoint{Name: nameFor("pprof"), Kind: "pprof", URL: f.EndpointURL}
			applyAuthToHTTP(&ep, f.AuthHint.Kind, envName)
			pprofEps = append(pprofEps, ep)
		case "v8":
			ep := config.HTTPEndpoint{Name: nameFor("v8"), Kind: "v8", URL: f.EndpointURL}
			applyAuthToHTTP(&ep, f.AuthHint.Kind, envName)
			nodeEps = append(nodeEps, ep)
		case "postgres", "mysql", "redis":
			ep := config.DatabaseEndpoint{
				Name: nameFor(f.Kind),
				Kind: f.Kind,
				DSN:  dsnForFinding(f),
			}
			if envName != "" {
				ep.PasswordEnv = envName
			}
			dbs = append(dbs, ep)
		}
	}
	return
}

// dsnForFinding returns a DSN string for a DB finding. K8s-sourced findings
// use the `k8s://ns/svc:port` placeholder that the DB clients understand.
func dsnForFinding(f discovery.Finding) string {
	if f.EndpointURL != "" {
		return f.EndpointURL
	}
	if f.Source.External {
		return f.Source.ExternalURL
	}
	if f.Source.Namespace != "" && f.Source.ServiceName != "" {
		return fmt.Sprintf("k8s://%s/%s", f.Source.Namespace, f.Source.ServiceName)
	}
	return ""
}

func applyAuthToProm(ep *config.PrometheusEndpoint, kind discovery.AuthKind, env string) {
	if env == "" {
		return
	}
	switch kind {
	case discovery.AuthBearer:
		ep.BearerEnv = env
	case discovery.AuthBasic:
		ep.BasicPassEnv = env
	}
}

func applyAuthToHTTP(ep *config.HTTPEndpoint, kind discovery.AuthKind, env string) {
	if env == "" {
		return
	}
	switch kind {
	case discovery.AuthBearer:
		ep.BearerEnv = env
	case discovery.AuthBasic:
		ep.BasicPassEnv = env
	}
}

// --- AutoRun (non-interactive) ---

// runAutomatic skips steps 3-5 entirely (no findings selection, no credential
// prompts, no hints) and goes straight from scan to save with defaults.
func (m *WizardModel) runAutomatic() error {
	results := scanContextsConcurrent(m.ctx, m.opts.KubeconfigPath, m.selectedContexts, 30*time.Second)
	m.scanResults = results
	// Build the default skills list as if the user had hit enter on step 7.
	p := buildProfileFromScans(results)
	m.recommendations = Recommend(p, m.builtinSkills)
	m.enabledSkills = make(map[string]bool, len(m.recommendations))
	for _, r := range m.recommendations {
		m.enabledSkills[r.SkillName] = true
	}
	// initFillIn populates m.inputs with default values used by persistConfig.
	m.initFillIn()
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

// renderCloudIdentityRow formats one CloudIdentity as an informational line for
// the setup wizard's step-3 view. Available identities show the principal and
// account; unavailable ones show the reason so the operator can diagnose.
func renderCloudIdentityRow(id discovery.CloudIdentity) string {
	provider := strings.ToUpper(string(id.Provider))
	if id.Available {
		return fmt.Sprintf("  ✓ %-5s  %s  (account %s)\n", provider, id.Principal, id.Account)
	}
	return fmt.Sprintf("  · %-5s  not detected (%s)\n", provider, id.Reason)
}

// buildProfileFromScans assembles a config.Profile from scan results.
func buildProfileFromScans(results []ContextResult) config.Profile {
	return config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      results,
	}
}
