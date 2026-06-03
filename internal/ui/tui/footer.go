package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Sentinel values for the footer's state and model segments. Promoted
// from inline strings so any drift in the wording (e.g. translation)
// happens in one place.
const (
	footerStateReady        = "set-up done"
	footerStateUnconfigured = "no set-up"
	footerSeparator         = " | "
)

// FooterModel renders the single-line status bar shown directly below
// the prompt: `cloudy <ver> | state: <state> | model: <id>`.
//
// Styles and the immutable brand segment are precomputed in
// NewFooterModel; View() only does the cheap mutable-segment Render
// calls. The parent owns version (passed in) and the model id so the
// footer never reads buildinfo directly — keeps the seam testable and
// the single source of truth in the parent Model.
type FooterModel struct {
	state  string
	model  string
	cost   float64
	ctxPct int // context-window usage 0-100; rendered as the "ctx N%" segment
	width  int

	brandRendered string // pre-rendered "cloudy <ver>" segment
	sepRendered   string // pre-rendered separator with dim style
	labelStyle    lipgloss.Style
	valueStyle    lipgloss.Style
	noColor       bool
}

// NewFooterModel constructs a FooterModel. Empty state/model both
// fall back to footerStateUnconfigured ("no set-up") so the bar never
// shows a blank segment.
func NewFooterModel(state, model, version string) FooterModel {
	noColor := os.Getenv("NO_COLOR") != ""
	f := FooterModel{
		state:   orUnconfigured(state),
		model:   orUnconfigured(model),
		width:   80,
		noColor: noColor,
	}
	if noColor {
		f.brandRendered = "cloudy " + version
		f.sepRendered = footerSeparator
	} else {
		f.brandRendered = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117")).Bold(true).
			Render("cloudy " + version)
		f.sepRendered = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Render(footerSeparator)
		f.labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		f.valueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	}
	return f
}

// SetWidth lets the parent push the latest terminal width.
func (f *FooterModel) SetWidth(w int) { f.width = w }

// SetState updates the setup-state segment shown by the footer.
func (f *FooterModel) SetState(s string) { f.state = orUnconfigured(s) }

// SetModel updates the model segment.
func (f *FooterModel) SetModel(m string) { f.model = orUnconfigured(m) }

// SetCost updates the cumulative session cost segment. The header used to
// own the live `$<cost>` readout, but the native-scrollback layout prints
// the header once at the top (it scrolls away with the transcript), so the
// always-visible cost moved here to the pinned footer.
func (f *FooterModel) SetCost(c float64) { f.cost = c }

// SetCtxPct updates the context-window usage segment (clamped to 0-100).
// The parent computes it from the latest input-token count over the model's
// context window so the operator can see when to /compact.
func (f *FooterModel) SetCtxPct(p int) {
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	f.ctxPct = p
}

// View renders the single-line footer.
func (f FooterModel) View() string {
	cost := fmt.Sprintf("$%.4f", f.cost)
	ctx := fmt.Sprintf("ctx %d%%", f.ctxPct)
	if f.noColor {
		return f.brandRendered + f.sepRendered +
			"state: " + f.state + f.sepRendered +
			"model: " + f.model + f.sepRendered + cost + f.sepRendered + ctx
	}
	var b strings.Builder
	b.Grow(len(f.brandRendered) + 80)
	b.WriteString(f.brandRendered)
	b.WriteString(f.sepRendered)
	b.WriteString(f.labelStyle.Render("state: "))
	b.WriteString(f.valueStyle.Render(f.state))
	b.WriteString(f.sepRendered)
	b.WriteString(f.labelStyle.Render("model: "))
	b.WriteString(f.valueStyle.Render(f.model))
	b.WriteString(f.sepRendered)
	b.WriteString(f.valueStyle.Render(cost))
	b.WriteString(f.sepRendered)
	// ctx gauge tints amber once it crosses the advise threshold so the
	// footer itself echoes the below-prompt /compact hint.
	ctxStyle := f.valueStyle
	if f.ctxPct >= compactAdviseThreshold {
		ctxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	}
	b.WriteString(ctxStyle.Render(ctx))
	return b.String()
}

// orUnconfigured maps empty input to footerStateUnconfigured so every
// footer setter goes through the same fallback rule.
func orUnconfigured(s string) string {
	if s == "" {
		return footerStateUnconfigured
	}
	return s
}

// footerClusterState renders the state segment from the configured cluster
// list. The rule:
//
//   - 0 clusters and no current-ctx fallback → "no set-up"
//   - 1 cluster → just the name (e.g. "prod")
//   - N>1 clusters → "<default> +<N-1>" (e.g. "prod +2")
//
// defaultCtx is used as a single-element fallback when Contexts is empty —
// for users who never ran the setup wizard but have a kubeconfig
// current-context, we still want the footer to say which cluster they hit
// rather than the bland "set-up done" placeholder it used to show.
func footerClusterState(contexts []string, defaultCtx string) string {
	names := dedupe(nonEmpty(contexts))
	if len(names) == 0 {
		if defaultCtx != "" {
			return defaultCtx
		}
		return footerStateUnconfigured
	}
	if len(names) == 1 {
		return names[0]
	}
	primary := names[0]
	matched := false
	if defaultCtx != "" {
		// If the kubeconfig current-context is one of the configured ones,
		// surface it as the headline so the footer matches what tools
		// without an explicit `context` argument will target.
		for _, n := range names {
			if n == defaultCtx {
				primary = defaultCtx
				matched = true
				break
			}
		}
	}
	// If defaultCtx is set but does NOT appear in the configured list, the
	// kubeconfig current-context is steering tools to a cluster the user
	// did not configure here. Surface it explicitly so the footer cannot
	// silently lie about which cluster bare-word tool calls hit.
	if defaultCtx != "" && !matched {
		return fmt.Sprintf("%s* (configured: %s +%d)", defaultCtx, names[0], len(names)-1)
	}
	return fmt.Sprintf("%s +%d", primary, len(names)-1)
}

// nonEmpty returns a copy of in with empty strings removed.
func nonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// dedupe returns a copy of in with duplicate entries removed, preserving
// first-seen order. Without this a kubeconfig that merged the same context
// twice (or a copy-paste in config.yaml) would render `prod +1` and imply
// the agent talks to two distinct clusters when it only has one.
func dedupe(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
