package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddLoadRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	// Start fresh
	loaded = make(map[string]bool)
	os.Unsetenv("FOO")

	// Add a secret
	if err := Add("FOO", "bar"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Check it's in the process environment
	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("os.Getenv(FOO) = %q, want %q", got, "bar")
	}

	// Reset process environment
	os.Unsetenv("FOO")
	loaded = make(map[string]bool)

	// Load it back
	if err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Check environment was set
	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("os.Getenv(FOO) after Load = %q, want %q", got, "bar")
	}

	// Check Has() works
	if !Has("FOO") {
		t.Errorf("Has(FOO) = false, want true")
	}
}

func TestAtomicMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	if err := Add("KEY", "value"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	path := Path()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s failed: %v", path, err)
	}

	// Check mode is 0600
	mode := stat.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func TestRejectBadKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	err := Add("with space", "x")
	if err == nil {
		t.Error("Add with bad key should return error")
	}

	// Also test other invalid keys
	badKeys := []string{"123", "_lowercase", "with-dash", "with.dot", "with/slash"}
	for _, key := range badKeys {
		err := Add(key, "x")
		if err == nil {
			t.Errorf("Add with key %q should return error", key)
		}
	}
}

func TestRejectMultilineValue(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	err := Add("X", "a\nb")
	if err == nil {
		t.Error("Add with multiline value should return error")
	}

	err = Add("X", "a\rb")
	if err == nil {
		t.Error("Add with carriage return should return error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	// File does not exist
	if err := Load(); err != nil {
		t.Errorf("Load on missing file returned error: %v", err)
	}
}

func TestPathResolution(t *testing.T) {
	tests := []struct {
		name       string
		cloudyHome string
		xdgConfig  string
		expected   string
	}{
		{
			name:       "CLOUDY_HOME takes precedence",
			cloudyHome: "/opt/cloudy",
			xdgConfig:  "/etc/xdg",
			expected:   "/opt/cloudy/secrets",
		},
		{
			name:       "XDG_CONFIG_HOME second precedence",
			cloudyHome: "",
			xdgConfig:  "/etc/xdg",
			expected:   "/etc/xdg/cloudy/secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment
			os.Unsetenv("CLOUDY_HOME")
			os.Unsetenv("XDG_CONFIG_HOME")

			if tt.cloudyHome != "" {
				t.Setenv("CLOUDY_HOME", tt.cloudyHome)
			}
			if tt.xdgConfig != "" {
				t.Setenv("XDG_CONFIG_HOME", tt.xdgConfig)
			}

			got := Path()
			if got != tt.expected {
				t.Errorf("Path() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMultipleEntries(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	// Add multiple secrets
	if err := Add("KEY1", "value1"); err != nil {
		t.Fatalf("Add KEY1 failed: %v", err)
	}
	if err := Add("KEY2", "value2"); err != nil {
		t.Fatalf("Add KEY2 failed: %v", err)
	}

	// Reset environment
	os.Unsetenv("KEY1")
	os.Unsetenv("KEY2")
	loaded = make(map[string]bool)

	// Load
	if err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Check both are loaded
	if got := os.Getenv("KEY1"); got != "value1" {
		t.Errorf("os.Getenv(KEY1) = %q, want %q", got, "value1")
	}
	if got := os.Getenv("KEY2"); got != "value2" {
		t.Errorf("os.Getenv(KEY2) = %q, want %q", got, "value2")
	}

	if !Has("KEY1") || !Has("KEY2") {
		t.Errorf("Has() returned false for loaded keys")
	}
}

func TestAddOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	loaded = make(map[string]bool)

	// Add initial value
	if err := Add("KEY", "old"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Overwrite with new value
	if err := Add("KEY", "new"); err != nil {
		t.Fatalf("Add overwrite failed: %v", err)
	}

	// Reset environment
	os.Unsetenv("KEY")
	loaded = make(map[string]bool)

	// Load and verify
	if err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got := os.Getenv("KEY"); got != "new" {
		t.Errorf("os.Getenv(KEY) = %q, want %q", got, "new")
	}
}

func TestValidKeyFormats(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	validKeys := []string{"A", "FOO", "FOO_BAR", "FOO123", "_PRIVATE", "A_B_C_D_E"}

	for _, key := range validKeys {
		loaded = make(map[string]bool)
		err := Add(key, "value")
		if err != nil {
			t.Errorf("Add with valid key %q failed: %v", key, err)
		}
	}
}

func TestCommentsAndBlankLines(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CLOUDY_HOME", tmpDir)

	// Manually write a file with comments and blank lines
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	content := `# This is a comment
FOO=bar

# Another comment
BAZ=qux
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	loaded = make(map[string]bool)
	os.Unsetenv("FOO")
	os.Unsetenv("BAZ")

	if err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("os.Getenv(FOO) = %q, want %q", got, "bar")
	}
	if got := os.Getenv("BAZ"); got != "qux" {
		t.Errorf("os.Getenv(BAZ) = %q, want %q", got, "qux")
	}
}
