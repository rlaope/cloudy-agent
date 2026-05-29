package cloud

import (
	"context"
	"strings"
	"testing"
)

func TestGCPLoggingRead_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"timestamp":"2026-05-29T00:00:00Z","severity":"ERROR","textPayload":"boom","resource":{"type":"gce_instance"}}]`)

	tool := newGCPLoggingReadTool(oneGCP())
	filter := "resource.type=gce_instance AND severity>=ERROR"
	obs := runTool(t, tool, `{"filter":"`+filter+`","limit":10,"freshness":"1h","order":"desc"}`)

	if args[0] != "logging" || args[1] != "read" {
		t.Errorf("command path = %v, want logging read", args[:2])
	}
	if !hasFlag(args, "--project", "proj-id") || !hasFlag(args, "--format", "json") {
		t.Errorf("per-project flags missing: %v", args)
	}
	if !hasFlag(args, "--limit", "10") || !hasFlag(args, "--freshness", "1h") || !hasFlag(args, "--order", "desc") {
		t.Errorf("query flags missing: %v", args)
	}
	// The filter is gcloud's trailing positional: it must be the LAST token and
	// survive as a single argv value (it contains spaces and operators).
	if args[len(args)-1] != filter {
		t.Errorf("filter not the trailing positional: %v", args)
	}
	// Critical: despite the positional filter, the allowlist prefix must stay
	// exactly "logging read" or CloudExec would refuse the call.
	if got := subcommandPrefix(args); got != "logging read" {
		t.Errorf("allowlist prefix = %q, want %q", got, "logging read")
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	if row := obs.Table.Rows[0]; row[1] != "ERROR" || row[2] != "gce_instance" || row[3] != "boom" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestGCPLoggingRead_DefaultsAndEmptyFilter(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `[]`)

	tool := newGCPLoggingReadTool(oneGCP())
	runTool(t, tool, `{}`) // no filter, no limit

	if !hasFlag(args, "--limit", "50") {
		t.Errorf("default limit 50 missing: %v", args)
	}
	// With no filter there is no trailing positional; prefix still resolves.
	if got := subcommandPrefix(args); got != "logging read" {
		t.Errorf("allowlist prefix = %q, want %q", got, "logging read")
	}
	// The last token must be a flag value, never a bare filter token.
	if args[len(args)-1] == "" {
		t.Errorf("unexpected empty trailing token: %v", args)
	}
}

func TestGCPLoggingRead_JSONPayloadFallback(t *testing.T) {
	stubRunner(t, nil, nil,
		`[{"timestamp":"2026-05-29T00:00:00Z","severity":"WARNING","jsonPayload":{"message":"structured msg"},"resource":{"type":"k8s_container"}}]`)
	tool := newGCPLoggingReadTool(oneGCP())
	obs := runTool(t, tool, `{"filter":"severity>=WARNING"}`)
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][3] != "structured msg" {
		t.Errorf("jsonPayload.message fallback failed: %+v", obs.Table)
	}
}

func TestGCPLoggingRead_BadOrder(t *testing.T) {
	stubRunner(t, nil, nil, `[]`)
	tool := newGCPLoggingReadTool(oneGCP())
	err := runToolErr(t, tool, `{"order":"sideways"}`)
	if !strings.Contains(err.Error(), "asc") {
		t.Errorf("want order validation error, got %v", err)
	}
}

func TestGCPLoggingRead_RejectsFlagInjectionFilter(t *testing.T) {
	stubRunner(t, nil, nil, `[]`)
	tool := newGCPLoggingReadTool(oneGCP())
	// A filter beginning with '-' would be mis-parsed as a flag by gcloud; the
	// safeArg guard must reject it before exec.
	err := runToolErr(t, tool, `{"filter":"--project=evil"}`)
	if !strings.Contains(err.Error(), "must not start with '-'") {
		t.Errorf("want safeArg rejection, got %v", err)
	}
}

// TestGCPAllowlist_RefusesMutatingVerb proves the gcloud allowlist is a real
// boundary: only `logging read` is permitted, so a mutating compute verb is
// refused before exec even though gcloud is now an allowed binary.
func TestGCPAllowlist_RefusesMutatingVerb(t *testing.T) {
	_, err := CloudExec(context.Background(), "gcloud",
		[]string{"compute", "instances", "delete", "vm1"})
	if err == nil {
		t.Fatal("expected a mutating gcloud verb to be refused")
	}
	if !strings.Contains(err.Error(), "not a read-only allowlisted sub-command") {
		t.Errorf("want allowlist refusal, got %v", err)
	}
}
