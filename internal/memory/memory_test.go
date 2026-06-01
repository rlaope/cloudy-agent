package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withHome points cloudy's state directory at a fresh temp dir so each test
// gets an isolated memory.md. CLOUDY_HOME wins in config.Path's resolution
// order, which is what Path derives from.
func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLOUDY_HOME", dir)
	return dir
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	withHome(t)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got != "" {
		t.Fatalf("Load on missing file = %q, want empty", got)
	}
}

func TestAppendThenLoadRoundTrip(t *testing.T) {
	dir := withHome(t)
	if err := Append("ctx prod-east is production"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Append("payments lives in the payments namespace"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, want := range []string{"ctx prod-east is production", "payments lives in the payments namespace"} {
		if !strings.Contains(got, want) {
			t.Errorf("Load missing %q\n--- got ---\n%s", want, got)
		}
	}
	// Stored as dated markdown bullets at the resolved path.
	if Path() != filepath.Join(dir, "memory.md") {
		t.Errorf("Path = %q, want %q", Path(), filepath.Join(dir, "memory.md"))
	}
	if !strings.HasPrefix(got, "- (") {
		t.Errorf("entries should be dated bullets, got prefix %q", got[:min(8, len(got))])
	}
}

func TestAppend_IgnoresBlankAndCollapsesWhitespace(t *testing.T) {
	withHome(t)
	if err := Append("   \n  "); err != nil {
		t.Fatalf("Append blank: %v", err)
	}
	if got, _ := Load(); got != "" {
		t.Fatalf("blank fact should record nothing, got %q", got)
	}
	if err := Append("multi\n   line\tfact"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Load()
	if !strings.Contains(got, "multi line fact") {
		t.Errorf("internal whitespace not collapsed to one bullet: %q", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("one fact must be one line, got %d newlines: %q", strings.Count(got, "\n"), got)
	}
}

func TestLoad_TailTrimsOversizedFileOnLineBoundary(t *testing.T) {
	withHome(t)
	// Write well past maxInjectBytes; the earliest entries must be dropped and
	// the result must start at a whole bullet (no partial leading line).
	for i := 0; i < 2000; i++ {
		if err := Append("filler fact number that takes up some bytes"); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) > maxInjectBytes {
		t.Errorf("Load returned %d bytes, want <= %d", len(got), maxInjectBytes)
	}
	if !strings.HasPrefix(got, "- (") {
		t.Errorf("tail-trim must start at a clean bullet, got prefix %q", got[:min(8, len(got))])
	}

	// Sanity: the raw file is larger than what Load injects.
	raw, _ := os.ReadFile(Path())
	if len(raw) <= maxInjectBytes {
		t.Fatalf("test precondition: raw file %d bytes not larger than cap %d", len(raw), maxInjectBytes)
	}
}
