package tui

import (
	"testing"
)

// TestFooterClusterState locks the rendering rule for the footer's state
// segment after this PR replaced the placeholder "set-up done":
//
//   - 0 contexts and no default → "no set-up"
//   - 0 contexts but a kubeconfig current-context → that name
//   - 1 context → just the name
//   - N>1 contexts → "<default> +<N-1>"
//
// The N>1 case also confirms that when the kubeconfig current-context
// matches one of the configured names, that name becomes the headline
// (not just the first slice element) — otherwise the footer would
// disagree with which cluster a tool call without `context: ...` actually
// targets.
func TestFooterClusterState(t *testing.T) {
	cases := []struct {
		name       string
		contexts   []string
		defaultCtx string
		want       string
	}{
		{"empty_no_default", nil, "", footerStateUnconfigured},
		{"empty_with_default", nil, "prod", "prod"},
		{"single", []string{"prod"}, "prod", "prod"},
		{"single_ignores_default", []string{"prod"}, "staging", "prod"},
		{"multi_default_first", []string{"prod", "staging", "dev"}, "prod", "prod +2"},
		{"multi_default_middle", []string{"prod", "staging", "dev"}, "staging", "staging +2"},
		// When the kubeconfig current-context is NOT in the configured list,
		// the footer surfaces that mismatch explicitly — silently picking
		// the first configured name would lie about which cluster bare-word
		// tool calls actually hit.
		{"multi_default_missing", []string{"prod", "staging", "dev"}, "qa", "qa* (configured: prod +2)"},
		{"multi_empty_default", []string{"prod", "staging"}, "", "prod +1"},
		{"strips_empty_entries", []string{"", "prod", "", "staging"}, "prod", "prod +1"},
		{"dedupes_duplicates", []string{"prod", "prod"}, "prod", "prod"},
		{"dedupes_with_extras", []string{"prod", "staging", "prod"}, "prod", "prod +1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := footerClusterState(tc.contexts, tc.defaultCtx)
			if got != tc.want {
				t.Errorf("footerClusterState(%v, %q) = %q, want %q",
					tc.contexts, tc.defaultCtx, got, tc.want)
			}
		})
	}
}

// TestMouseCaptureDisabled pins the env-var parsing for CLOUDY_NO_MOUSE so a
// future shape change ("only 1 is on" vs "any value is on") is caught.
// Treating `0`/`false`/`""` as off keeps the common shell idioms working
// without a parser.
func TestMouseCaptureDisabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"False", false},
		// case-insensitive + whitespace tolerant — closes the symmetric-
		// shape gap that the first version of this PR shipped.
		{"FaLsE", false},
		{"no", false},
		{"No", false},
		{"off", false},
		{"OFF", false},
		{" false", false},
		{"false ", false},
		{"  0  ", false},
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
		{"anything_truthy", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv("CLOUDY_NO_MOUSE", tc.val)
			if got := mouseCaptureDisabled(); got != tc.want {
				t.Errorf("mouseCaptureDisabled with %q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
