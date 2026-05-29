package cloud

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBin drops an executable shell script named `name` into a temp dir,
// prepends that dir to PATH for the test, and returns nothing — runCloudExec
// resolves the binary via PATH, so this exercises the real exec path without a
// real cloud CLI.
func writeFakeBin(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary exec test is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunCloudExec_RealBinaryStdout(t *testing.T) {
	writeFakeBin(t, "aws", `echo '{"ok":true}'`)
	out, err := runCloudExec(context.Background(), "aws", []string{"cloudwatch", "list-metrics"})
	if err != nil {
		t.Fatalf("runCloudExec error: %v", err)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Errorf("unexpected stdout: %s", out)
	}
}

func TestRunCloudExec_StderrWrapped(t *testing.T) {
	writeFakeBin(t, "aws", `echo "AccessDenied: nope" 1>&2; exit 254`)
	_, err := runCloudExec(context.Background(), "aws", []string{"cloudwatch", "list-metrics"})
	if err == nil {
		t.Fatal("expected error from non-zero exit")
	}
	if !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "aws") {
		t.Errorf("error should wrap stderr and bin name, got: %v", err)
	}
}

func TestRunCloudExec_ByteCap(t *testing.T) {
	// Emit more than maxCloudOutputBytes (NUL bytes are fine — we only assert
	// the length); output must be truncated to the cap.
	writeFakeBin(t, "aws", `head -c 5000000 /dev/zero`)
	out, err := runCloudExec(context.Background(), "aws", []string{"cloudwatch", "list-metrics"})
	if err != nil {
		t.Fatalf("runCloudExec error: %v", err)
	}
	if len(out) != maxCloudOutputBytes {
		t.Errorf("output not capped: got %d bytes, want %d", len(out), maxCloudOutputBytes)
	}
}

// TestCloudExec_EndToEndAllowlistThenExec confirms CloudExec (allowlist gate)
// and runCloudExec (real exec) compose: an allowlisted verb reaches the fake
// binary and returns its stdout.
func TestCloudExec_EndToEndAllowlistThenExec(t *testing.T) {
	writeFakeBin(t, "az", `echo '[]'`)
	out, err := CloudExec(context.Background(), "az",
		[]string{"monitor", "log-analytics", "query", "--workspace", "w", "--analytics-query", "X"})
	if err != nil {
		t.Fatalf("CloudExec end-to-end error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "[]" {
		t.Errorf("unexpected stdout: %q", out)
	}
}
