package agent

import (
	"context"

	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/tools"
)

// MaskingHook applies the active permission profile's key/value redaction
// patterns to every tool observation before it is fed back to the LLM.
//
// Why this exists: prior to the v0.5 security review (M-1),
// permission.Masker was published, tested, and documented but had zero
// production call sites. A connection string with embedded credentials in
// a pg_stat_activity row, or an Argo CD repo URL with an inline token,
// reached the model verbatim. This hook closes that loop by sitting on
// AfterToolCall — the agent's single chokepoint between tool result and
// LLM prompt — and applying Masker.MaskString to obs.Text plus
// Masker.MaskJSON to obs.Raw when present.
//
// A nil *permission.Masker is the "no patterns configured" signal from
// permission.NewMasker; in that case this hook returns the observation
// unchanged, so the wiring layer can pass the masker unconditionally.
type MaskingHook struct {
	NoopHook
	masker *permission.Masker
}

// NewMaskingHook constructs a MaskingHook bound to masker. masker may be
// nil — in that case AfterToolCall is a no-op (the wiring layer does not
// need to branch on profile presence).
func NewMaskingHook(masker *permission.Masker) *MaskingHook {
	return &MaskingHook{masker: masker}
}

// AfterToolCall redacts the observation text + raw payload using the
// bound masker. Returns the original observation when the masker is nil
// or when no patterns matched. The hook does not consult the tool name
// or arguments — masking is universal, so a misconfigured tool cannot
// quietly opt out by being added without a per-tool branch here.
func (h *MaskingHook) AfterToolCall(_ context.Context, _ llm.ToolCall, obs tools.Observation, _ error) (tools.Observation, error) {
	if h == nil || h.masker == nil {
		return obs, nil
	}
	obs.Text = h.masker.MaskString(obs.Text)
	// obs.Raw is typed `any` because tools may stash either raw bytes
	// (JSON blob from an HTTP backend) or a typed struct. We only mask
	// the bytes form — typed structs are not exposed to the LLM through
	// the prompt, only Text is. Use a defensive type switch so a future
	// tool stashing a different shape does not panic the hook chain.
	if raw, ok := obs.Raw.([]byte); ok && len(raw) > 0 {
		masked, err := h.masker.MaskJSON(raw)
		if err == nil {
			obs.Raw = masked
		}
		// MaskJSON returns the original bytes on non-JSON input with a
		// nil error, so an err here is genuinely unexpected. Swallowed
		// rather than aborting the run — masking is best-effort and
		// failing the whole tool dispatch because a regex hit an edge
		// case in a multi-megabyte log dump would be a worse outcome.
	}
	return obs, nil
}
