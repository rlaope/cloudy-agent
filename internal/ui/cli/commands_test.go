package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseInto pins the parseInto helper that every subcommand uses to
// translate (args, name) → (positional remainder, error). Without this
// covered, any future refactor of the flag wiring could silently change
// what each subcommand interprets as its positional args.
func TestParseInto(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	type opts struct {
		base baseFlags
	}
	bind := func(o *opts) binder {
		return binderFn(func(fs *flagSet) { o.base.bind(fs.FlagSet) })
	}

	cases := []struct {
		name        string
		args        []string
		wantPos     []string
		wantNoColor bool
		wantJSON    bool
		wantContext string
	}{
		{
			name:        "no args",
			args:        nil,
			wantPos:     nil,
			wantNoColor: false,
			wantJSON:    false,
		},
		{
			name:    "positional only",
			args:    []string{"alpha", "beta"},
			wantPos: []string{"alpha", "beta"},
		},
		{
			name:        "flags then positionals",
			args:        []string{"--context", "prod", "--json", "alpha"},
			wantPos:     []string{"alpha"},
			wantContext: "prod",
			wantJSON:    true,
		},
		{
			name:        "no-color flag",
			args:        []string{"--no-color"},
			wantNoColor: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var o opts
			pos, err := parseInto(bind(&o), "test", c.args, io.Discard)
			if err != nil {
				t.Fatalf("parseInto: %v", err)
			}
			if !equalStrings(pos, c.wantPos) {
				t.Errorf("positional args: got %v want %v", pos, c.wantPos)
			}
			if o.base.noColor != c.wantNoColor {
				t.Errorf("noColor: got %v want %v", o.base.noColor, c.wantNoColor)
			}
			if o.base.asJSON != c.wantJSON {
				t.Errorf("asJSON: got %v want %v", o.base.asJSON, c.wantJSON)
			}
			if o.base.context != c.wantContext {
				t.Errorf("context: got %q want %q", o.base.context, c.wantContext)
			}
		})
	}
}

// TestParseInto_UnknownFlagErrors confirms an unrecognised flag surfaces
// as an error rather than crashing the process — `flag.ContinueOnError`
// must be honoured by parseInto.
func TestParseInto_UnknownFlagErrors(t *testing.T) {
	t.Parallel()

	type opts struct{ base baseFlags }
	o := &opts{}
	b := binderFn(func(fs *flagSet) { o.base.bind(fs.FlagSet) })

	var stderr bytes.Buffer
	_, err := parseInto(b, "test", []string{"--definitely-not-a-real-flag"}, &stderr)
	if err == nil {
		t.Fatal("expected an error for unknown flag, got nil")
	}
}

// TestNoColorEnv pins the env-var hook so a future refactor cannot drop
// the NO_COLOR=1 honouring that operators depend on for piped output.
func TestNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if noColorEnv() {
		t.Error("noColorEnv should be false when NO_COLOR is unset")
	}
	t.Setenv("NO_COLOR", "1")
	if !noColorEnv() {
		t.Error("noColorEnv should be true when NO_COLOR is set")
	}
}

func TestSetupCmd_DryRunFlagParses(t *testing.T) {
	t.Parallel()
	var o setupOptions
	pos, err := parseInto(&o, "setup", []string{"--auto", "--dry-run"}, io.Discard)
	if err != nil {
		t.Fatalf("parseInto setup: %v", err)
	}
	if len(pos) != 0 {
		t.Fatalf("positional args = %v, want none", pos)
	}
	if !o.auto {
		t.Error("--auto should set auto=true")
	}
	if !o.dryRun {
		t.Error("--dry-run should set dryRun=true")
	}
}

func TestSkillsCmd_ListJSON(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	var out bytes.Buffer
	if err := (skillsCmd{}).Run(context.Background(), []string{"--json"}, &out, io.Discard); err != nil {
		t.Fatalf("skills --json: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("skills list output is not JSON: %v\n%s", err, out.String())
	}
	if len(rows) == 0 {
		t.Fatal("skills list --json returned no skills")
	}
	if _, ok := rows[0]["name"]; !ok {
		t.Fatalf("first skill row missing name field: %#v", rows[0])
	}
}

func TestSkillsCmd_ShowJSON(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	var out bytes.Buffer
	if err := (skillsCmd{}).Run(context.Background(), []string{"--json", "show", "cluster-recon"}, &out, io.Discard); err != nil {
		t.Fatalf("skills show --json: %v", err)
	}

	var row map[string]any
	if err := json.Unmarshal(out.Bytes(), &row); err != nil {
		t.Fatalf("skills show output is not JSON: %v\n%s", err, out.String())
	}
	if row["name"] != "cluster-recon" {
		t.Fatalf("skill name = %v, want cluster-recon", row["name"])
	}
}

func TestSkillsCmd_ListJSONRejectsUnexpectedArgs(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	var out bytes.Buffer
	err := (skillsCmd{}).Run(context.Background(), []string{"--json", "unexpected"}, &out, io.Discard)
	if err == nil {
		t.Fatal("skills --json unexpected should return an error")
	}
	if !strings.Contains(err.Error(), "unexpected skills list argument") {
		t.Fatalf("error = %v, want unexpected skills list argument", err)
	}
}

func TestSkillsCmd_ShowRejectsUnexpectedArgs(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	var out bytes.Buffer
	err := (skillsCmd{}).Run(context.Background(), []string{"show", "cluster-recon", "extra"}, &out, io.Discard)
	if err == nil {
		t.Fatal("skills show cluster-recon extra should return an error")
	}
	if !strings.Contains(err.Error(), "unexpected skills show argument") {
		t.Fatalf("error = %v, want unexpected skills show argument", err)
	}
}

func TestSkillsCmd_FlagValueNamedShowDoesNotBecomeSubcommand(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	var out bytes.Buffer
	if err := (skillsCmd{}).Run(context.Background(), []string{"--context", "show", "--json"}, &out, io.Discard); err != nil {
		t.Fatalf("skills --context show --json: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("skills list output is not JSON: %v\n%s", err, out.String())
	}
	if len(rows) == 0 {
		t.Fatal("skills list --json returned no skills")
	}
}

func TestSetupRun_DryRunDoesNotWriteConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUDY_HOME", dir)

	kubeconfig := filepath.Join(dir, "missing-kubeconfig")
	err := (setupCmd{}).Run(context.Background(), []string{"--auto", "--dry-run", "--kubeconfig", kubeconfig}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("setup --auto --dry-run: %v", err)
	}

	for _, name := range []string{"config.yaml", "profile.yaml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should not be written during --dry-run; stat err=%v", path, err)
		}
	}
}

// TestUpdateCmd_RejectsArgs pins the contract that `cloudy update` takes
// no positional arguments — the most common typo would be `cloudy update
// --force` or similar, and silently ignoring trailing junk would surprise.
func TestUpdateCmd_RejectsArgs(t *testing.T) {
	t.Parallel()
	c := updateCmd{}
	err := c.Run(context.Background(), []string{"--force"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("update should reject positional/flag args")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Errorf("error should mention 'unexpected argument'; got: %v", err)
	}
}

// TestUpdateCmd_NameShort pins the dispatcher-facing identity of the
// update command. If either Name() or Short() drifts, the help banner
// and `cloudy update` invocation break in lockstep.
func TestUpdateCmd_NameShort(t *testing.T) {
	t.Parallel()
	c := updateCmd{}
	if c.Name() != "update" {
		t.Errorf("Name(): got %q want %q", c.Name(), "update")
	}
	if c.Short() == "" {
		t.Error("Short() must not be empty (used by help banner)")
	}
}

// TestDoctorCmd_NameShort is the same identity pin for the doctor command.
// The Run path needs a live config/profile so we don't exercise it here;
// the dispatcher tests already cover the registry/dispatch surface for
// every registered command via TestAll_IsSortedAndIncludesProductionCommands.
func TestDoctorCmd_NameShort(t *testing.T) {
	t.Parallel()
	c := doctorCmd{}
	if c.Name() != "doctor" {
		t.Errorf("Name(): got %q want %q", c.Name(), "doctor")
	}
	if c.Short() == "" {
		t.Error("Short() must not be empty")
	}
}

// TestEnvironmentChecks_Shape pins the structural contract of
// environmentChecks() — two checks (cloudy home, egress proxy), both
// always OK (they are informational, not pass/fail). A future refactor
// that drops either row, or flips one to OK=false, gets caught here.
func TestEnvironmentChecks_Shape(t *testing.T) {
	t.Setenv("CLOUDY_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")

	checks := environmentChecks()
	if len(checks) != 2 {
		t.Fatalf("environmentChecks: got %d checks, want 2", len(checks))
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = true
		if !c.OK {
			t.Errorf("environmentChecks row %q: OK=false (these rows are informational only)", c.Name)
		}
	}
	for _, want := range []string{"cloudy home", "egress proxy"} {
		if !names[want] {
			t.Errorf("environmentChecks missing required row %q", want)
		}
	}
}

// TestEnvironmentChecks_RespectsHomeOverrides confirms the CLOUDY_HOME /
// XDG_CONFIG_HOME env-var overrides actually surface in the detail
// string, so a bastion operator inspecting `cloudy doctor` sees the
// per-shell override they configured.
func TestEnvironmentChecks_RespectsHomeOverrides(t *testing.T) {
	t.Setenv("CLOUDY_HOME", "/opt/cloudy-override")
	t.Setenv("XDG_CONFIG_HOME", "")
	checks := environmentChecks()
	var home string
	for _, c := range checks {
		if c.Name == "cloudy home" {
			home = c.Detail
		}
	}
	if !strings.Contains(home, "/opt/cloudy-override") {
		t.Errorf("CLOUDY_HOME override should appear in detail; got %q", home)
	}
}

// binderFn is a function adapter for the binder interface so test cases
// can express their bind body inline without declaring a one-shot type.
type binderFn func(fs *flagSet)

func (f binderFn) bind(fs *flagSet) { f(fs) }

// equalStrings is a slice comparator local to this test file so we do not
// pull in a dep just for one comparison.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guard unused imports if a future refactor removes test cases.
var _ = flag.ContinueOnError
