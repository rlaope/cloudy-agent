package tui

import (
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scopeNamespacesMsg carries the result of an async kubectl namespace
// fetch. err is non-nil when the lookup failed (no kubeconfig, missing
// permissions, …); the picker path surfaces it to the operator as a
// chrome line instead of opening an empty picker.
type scopeNamespacesMsg struct {
	context    string
	namespaces []string
	err        error
}

// fetchScopeNamespacesCmd runs `kubectl get ns -o name` in a goroutine
// and returns the result as a scopeNamespacesMsg. When ctxName is
// non-empty the lookup targets that kubeconfig context so a single TUI
// session can switch clusters via /use and still scope correctly.
//
// We shell out to kubectl rather than reaching into k8s.Client because
// the TUI does not currently embed a Client (it lives behind the agent
// runner) and kubectl already honours every auth plugin / proxy / token
// helper the operator has configured. The lookup runs in a goroutine
// (returned tea.Cmd) so the bubbletea loop stays responsive while the
// API round-trip is in flight.
func fetchScopeNamespacesCmd(ctxName string) tea.Cmd {
	return func() tea.Msg {
		args := []string{"get", "namespaces", "-o", "name"}
		if ctxName != "" {
			args = append([]string{"--context", ctxName}, args...)
		}
		cmd := exec.Command("kubectl", args...)
		// Modest timeout: namespace listing is cheap. A slower cluster
		// is the operator's signal that something is wrong, not a
		// reason to block the TUI forever.
		stop := time.AfterFunc(15*time.Second, func() {
			_ = cmd.Process.Kill()
		})
		out, err := cmd.Output()
		stop.Stop()
		if err != nil {
			return scopeNamespacesMsg{context: ctxName, err: err}
		}
		var names []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "namespace/")
			if line == "" {
				continue
			}
			names = append(names, line)
		}
		return scopeNamespacesMsg{context: ctxName, namespaces: names}
	}
}

// scopeResetKey is the sentinel item key in the namespace picker that
// means "reset to all namespaces". The arrowPickerMultiResolveMsg
// handler intercepts this before treating the selection as a literal
// namespace list, so it is safe to use a value that could not be a
// real Kubernetes namespace (kebab + leading underscore + brackets).
const scopeResetKey = "__all_namespaces__"

// buildScopeNamespacePicker materialises the multi-select picker for
// /scope. The label IS the namespace name so what the operator ticks
// is exactly what gets passed to parseScope. A synthetic top row
// ("↺  All namespaces (reset scope)") lets the operator restore the
// "no scope" state without having to un-tick every namespace one by
// one — useful when the current scope was set on a different cluster
// or simply needs to be cleared. Pre-selection mirrors the current
// scope so re-opening the picker without changes is a no-op instead
// of starting from scratch.
func buildScopeNamespacePicker(names []string, preselected []string) *arrowPicker {
	items := make([]arrowPickerItem, 0, len(names)+1)
	items = append(items, arrowPickerItem{
		label: "↺ All namespaces",
		hint:  "reset scope — agent sees every namespace",
		key:   scopeResetKey,
	})
	for _, n := range names {
		items = append(items, arrowPickerItem{
			label: n,
			key:   n,
		})
	}
	return newMultiArrowPicker(
		"Pick namespaces to scope this session (space to toggle, Enter to confirm, Esc to cancel):",
		items, preselected,
	)
}
