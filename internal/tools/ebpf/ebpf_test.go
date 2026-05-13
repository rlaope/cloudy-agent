package ebpf

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

func TestRegisterAll_NonLinuxMarksGroupSkipped(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("only meaningful on non-linux hosts")
	}
	reg := tools.New()
	RegisterAll(reg)
	r, ok := reg.Skipped()["ebpf"]
	if !ok {
		t.Fatalf("expected group ebpf skipped on %s", runtime.GOOS)
	}
	if !strings.Contains(r, "linux") {
		t.Errorf("expected linux mention in reason, got %q", r)
	}
	if len(reg.List()) != 0 {
		t.Errorf("expected no tools registered on %s, got %d", runtime.GOOS, len(reg.List()))
	}
}

func TestBpftraceCatalog_LookupAndKeys(t *testing.T) {
	t.Parallel()
	keys := catalogKeys()
	if len(keys) == 0 {
		t.Fatal("empty bpftrace catalog")
	}
	for _, k := range keys {
		e, ok := catalogEntryByKey(k)
		if !ok || e.Prog == "" || e.Desc == "" {
			t.Errorf("catalog key %q malformed: %+v", k, e)
		}
	}
	if _, ok := catalogEntryByKey("not-a-real-key"); ok {
		t.Error("expected unknown key to return false")
	}
}

func TestBpftraceOneliner_RejectsUnknownKey(t *testing.T) {
	t.Parallel()
	tool := newBpftraceOnelinerTool("/usr/bin/true")
	_, err := tool.Run(context.Background(), json.RawMessage(`{"script_key":"definitely-not-real"}`))
	if err == nil {
		t.Fatal("expected error for unknown script_key")
	}
	if !strings.Contains(err.Error(), "unknown script_key") {
		t.Errorf("expected unknown-script_key error, got %v", err)
	}
}

func TestBpftraceOneliner_StubbedRunner(t *testing.T) {
	prev := runner
	captured := []string{}
	runner = func(ctx context.Context, bin string, args ...string) (string, string, error) {
		captured = append([]string{bin}, args...)
		return "@[curl]: 42\n", "", nil
	}
	defer func() { runner = prev }()

	tool := newBpftraceOnelinerTool("/usr/bin/bpftrace")
	obs, err := tool.Run(context.Background(), json.RawMessage(`{"script_key":"syscall_counts","duration_seconds":3}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "@[curl]: 42") {
		t.Errorf("expected stubbed output in Text, got %q", obs.Text)
	}
	if !strings.Contains(obs.Text, "syscall_counts") {
		t.Errorf("expected catalog key in summary footer, got %q", obs.Text)
	}
	// Verify the program flag was passed as -e <prog>.
	if !contains(captured, "-e") {
		t.Errorf("expected -e in argv, got %v", captured)
	}
	prog, _ := catalogEntryByKey("syscall_counts")
	if !contains(captured, prog.Prog) {
		t.Errorf("expected catalog program in argv, got %v", captured)
	}
}

func TestBoundDuration(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{0, 5}, {-1, 5}, {1, 1}, {60, 60}, {61, 60}, {1000, 60},
	}
	for _, c := range cases {
		got := boundDuration(c.in)
		if got != c.want {
			t.Errorf("boundDuration(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
