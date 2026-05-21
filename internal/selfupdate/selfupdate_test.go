package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMatches_HandlesVPrefix pins the contract that local snapshots
// like "0.4.0-48-gfa752bc" (no leading "v") still compare cleanly
// against GitHub-style "v0.4.0" tags. Without the prefix strip an
// otherwise-current binary would loop calling itself out of date.
func TestMatches_HandlesVPrefix(t *testing.T) {
	cases := []struct {
		local, remote string
		want          bool
	}{
		{"v0.4.1", "v0.4.1", true},
		{"0.4.1", "v0.4.1", true},
		{"v0.4.1", "0.4.1", true},
		{"0.4.1", "0.4.1", true},
		{"v0.4.0", "v0.4.1", false},
		{"v0.4.0-48-gfa752bc", "v0.4.1", false},
	}
	for _, c := range cases {
		got := matches(c.local, c.remote)
		if got != c.want {
			t.Errorf("matches(%q, %q) = %v, want %v", c.local, c.remote, got, c.want)
		}
	}
}

// TestValidateBinary_RejectsHTML is the regression we explicitly
// care about: GitHub returns an HTML page when an asset is missing,
// and the previous (pre-self-update) install.sh had to grep for
// "<!DOCTYPE" to refuse it. The in-process counterpart needs the
// same guard so an "asset not found" never bricks a running cloudy
// install by getting chmod +x'd over the real binary.
func TestValidateBinary_RejectsHTML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-asset")
	if err := os.WriteFile(path, []byte("<!DOCTYPE html>\n<html><body>Not Found</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(path); err == nil {
		t.Error("validateBinary must reject an HTML payload")
	}
}

// TestValidateBinary_AcceptsELF and TestValidateBinary_AcceptsMachO
// confirm the inverse: real binary magic bytes pass. The magic
// constants are kept inline (rather than imported from debug/elf etc.)
// so this test reads as a single-glance assertion.
func TestValidateBinary_AcceptsELF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-elf")
	// ELF magic + arbitrary padding.
	if err := os.WriteFile(path, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(path); err != nil {
		t.Errorf("ELF header should validate; got %v", err)
	}
}

func TestValidateBinary_AcceptsMachO(t *testing.T) {
	dir := t.TempDir()

	// 64-bit Mach-O, little-endian.
	pathLE := filepath.Join(dir, "fake-macho-le")
	if err := os.WriteFile(pathLE, append([]byte{0xcf, 0xfa, 0xed, 0xfe}, make([]byte, 64)...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(pathLE); err != nil {
		t.Errorf("Mach-O LE header should validate; got %v", err)
	}

	// 64-bit Mach-O, big-endian.
	pathBE := filepath.Join(dir, "fake-macho-be")
	if err := os.WriteFile(pathBE, append([]byte{0xfe, 0xed, 0xfa, 0xcf}, make([]byte, 64)...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(pathBE); err != nil {
		t.Errorf("Mach-O BE header should validate; got %v", err)
	}
}
