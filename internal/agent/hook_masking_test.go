package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/tools"
)

// TestMaskingHook_RedactsValueRegexInText pins the regression that
// motivated this hook (security review M-1): permission.Masker was a
// published, tested, documented API with zero production call sites
// until v0.5. A connection string with an embedded password in a
// pg_stat_activity row reached the LLM verbatim. This test confirms
// the hook actually applies ValueRegex redaction to obs.Text on the
// AfterToolCall path.
func TestMaskingHook_RedactsValueRegexInText(t *testing.T) {
	t.Parallel()

	prof := &permission.Profile{
		Name: "test",
		Masking: permission.Masking{
			ValueRegex: []string{`password=\S+`},
		},
	}
	masker, err := permission.NewMasker(prof)
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	hook := agent.NewMaskingHook(masker)

	in := tools.Observation{
		Text: "client=app1 password=correcthorsebatterystaple state=active",
	}
	out, err := hook.AfterToolCall(context.Background(), llm.ToolCall{}, in, nil)
	if err != nil {
		t.Fatalf("AfterToolCall: %v", err)
	}
	if strings.Contains(out.Text, "correcthorsebatterystaple") {
		t.Errorf("password value should be redacted; got: %q", out.Text)
	}
	if !strings.Contains(out.Text, "[REDACTED]") {
		t.Errorf("redaction marker missing; got: %q", out.Text)
	}
	// Sanity: the surrounding non-secret context must survive.
	if !strings.Contains(out.Text, "client=app1") || !strings.Contains(out.Text, "state=active") {
		t.Errorf("non-secret tokens were dropped; got: %q", out.Text)
	}
}

// TestMaskingHook_NilMaskerIsNoop confirms a profile with no patterns
// configured produces a nil *permission.Masker, and that the hook
// passes the observation through unchanged. This is the common case
// (no operator-supplied masking) and the wiring layer should not have
// to branch on profile shape.
func TestMaskingHook_NilMaskerIsNoop(t *testing.T) {
	t.Parallel()

	hook := agent.NewMaskingHook(nil)
	in := tools.Observation{Text: "password=keep_me_visible"}
	out, err := hook.AfterToolCall(context.Background(), llm.ToolCall{}, in, nil)
	if err != nil {
		t.Fatalf("AfterToolCall: %v", err)
	}
	if out.Text != in.Text {
		t.Errorf("nil masker should be a no-op; got: %q want: %q", out.Text, in.Text)
	}
}
