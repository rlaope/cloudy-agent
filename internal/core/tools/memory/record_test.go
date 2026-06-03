package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/tools"
	memstore "github.com/rlaope/cloudy/internal/memory"
)

func TestRecordTool_AppendsAndConfirms(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	tool := newRecordTool()
	if tool.Name() != "memory.record" {
		t.Fatalf("Name = %q, want memory.record", tool.Name())
	}

	obs, err := tool.Run(context.Background(), []byte(`{"fact":"ctx prod-east is production"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "ctx prod-east is production") {
		t.Errorf("observation should confirm the fact, got %q", obs.Text)
	}

	stored, err := memstore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(stored, "ctx prod-east is production") {
		t.Errorf("fact not persisted to memory store, got %q", stored)
	}
}

func TestRecordTool_RedactsSecretBeforePersisting(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	// memory.md is injected into the system prompt unmasked, so a secret must be
	// redacted at the write. The default masking patterns catch AWS access keys.
	obs, err := newRecordTool().Run(context.Background(), []byte(`{"fact":"prod creds AKIAIOSFODNN7EXAMPLE for the east cluster"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(obs.Text, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret leaked into observation: %q", obs.Text)
	}
	stored, _ := memstore.Load()
	if strings.Contains(stored, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret persisted to memory.md in clear text: %q", stored)
	}
	// The surrounding non-secret context is still recorded.
	if !strings.Contains(stored, "east cluster") {
		t.Errorf("non-secret context was lost: %q", stored)
	}
}

func TestRecordTool_EmptyFactRecordsNothing(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	obs, err := newRecordTool().Run(context.Background(), []byte(`{"fact":"   "}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "Nothing recorded") {
		t.Errorf("empty fact should report nothing recorded, got %q", obs.Text)
	}
	if stored, _ := memstore.Load(); stored != "" {
		t.Errorf("empty fact must not write, got %q", stored)
	}
}

func TestRecordToolRequiresApproval(t *testing.T) {
	if got := tools.RiskOf(newRecordTool()); got != tools.RiskHigh {
		t.Fatalf("memory.record risk = %s, want high so TUI shows y/n HITL before writing local memory", got)
	}
}
