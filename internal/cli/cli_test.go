package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/cli"
)

// fakeCmd is the minimal Command implementation used to drive the dispatcher
// in isolation from the production commands (which need real config, k8s,
// LLM providers, etc.). Each test instance gets a unique Name() so multiple
// fakes coexist with the production registry without colliding.
type fakeCmd struct {
	name   string
	short  string
	run    func(ctx context.Context, args []string, stdout, stderr io.Writer) error
	called bool
	gotArg []string
}

func (f *fakeCmd) Name() string  { return f.name }
func (f *fakeCmd) Short() string { return f.short }
func (f *fakeCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f.called = true
	f.gotArg = args
	if f.run != nil {
		return f.run(ctx, args, stdout, stderr)
	}
	return nil
}

// TestRun_NoArgsRunsTUI pins the contract that a zero-arg invocation
// (`cloudy` with nothing else) delegates to the TUIRunner the binary
// entry-point supplied. Without this, the top-level binary would have to
// duplicate dispatch logic just to start the TUI.
func TestRun_NoArgsRunsTUI(t *testing.T) {
	t.Parallel()
	var tuiCalled bool
	tui := func(stdout, stderr io.Writer) error {
		tuiCalled = true
		return nil
	}
	if err := cli.Run(context.Background(), nil, io.Discard, io.Discard, tui); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tuiCalled {
		t.Error("TUI runner was not called on zero-arg invocation")
	}
}

// TestRun_NoArgsNoTUIPrintsHelp covers the headless fallback: when the
// caller did not wire a TUI runner (e.g. an ssh session without a tty),
// the zero-arg path should print help rather than crash.
func TestRun_NoArgsNoTUIPrintsHelp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cli.Run(context.Background(), nil, &out, io.Discard, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Errorf("expected help text, got: %q", out.String())
	}
}

// TestRun_VersionVariants pins all three spellings of the version flag —
// `--version` / `-v` / `version` — because operators reach for whichever
// they typed first and any divergence would silently fail.
func TestRun_VersionVariants(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"--version", "-v", "version"} {
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{arg}, &out, io.Discard, nil)
		if err != nil {
			t.Errorf("Run(%q): %v", arg, err)
			continue
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("Run(%q) printed empty version", arg)
		}
	}
}

// TestRun_HelpVariants is the same pinning for the three help spellings.
func TestRun_HelpVariants(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"--help", "-h", "help"} {
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{arg}, &out, io.Discard, nil)
		if err != nil {
			t.Errorf("Run(%q): %v", arg, err)
			continue
		}
		if !strings.Contains(out.String(), "commands:") {
			t.Errorf("Run(%q) output missing 'commands:' section: %q", arg, out.String())
		}
	}
}

// TestRun_UnknownCommandPrintsHelpAndErrors pins the two-part contract for
// typos: the user sees the help banner on stderr (so they know what was
// available) AND the call returns a non-nil error naming the unknown
// command. Returning silently or returning only an error would leave
// shell-script callers without diagnostic context.
func TestRun_UnknownCommandPrintsHelpAndErrors(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	err := cli.Run(context.Background(), []string{"definitely-not-a-real-command"}, io.Discard, &stderr, nil)
	if err == nil {
		t.Fatal("unknown command should return an error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error should mention 'unknown command'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-command") {
		t.Errorf("error should name the unknown command; got: %v", err)
	}
	if !strings.Contains(stderr.String(), "commands:") {
		t.Errorf("unknown command should print help to stderr; got: %q", stderr.String())
	}
}

// TestRun_DispatchesToRegisteredCommand pins the happy path: a registered
// command's Run is called with the args following the command name (the
// command name itself is consumed by the dispatcher).
func TestRun_DispatchesToRegisteredCommand(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{name: "test-dispatch-happy", short: "test"}
	cli.Register(cmd)

	err := cli.Run(context.Background(), []string{"test-dispatch-happy", "x", "y"}, io.Discard, io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !cmd.called {
		t.Error("command Run was not invoked")
	}
	if len(cmd.gotArg) != 2 || cmd.gotArg[0] != "x" || cmd.gotArg[1] != "y" {
		t.Errorf("dispatcher should strip the command name and pass remaining args; got: %v", cmd.gotArg)
	}
}

// TestRun_PropagatesCommandError confirms that an error returned from a
// command's Run flows back through the dispatcher unchanged. The dispatcher
// must not swallow the error or wrap it in a way that hides the original.
func TestRun_PropagatesCommandError(t *testing.T) {
	t.Parallel()
	want := errors.New("subcommand intentional failure")
	cmd := &fakeCmd{
		name:  "test-dispatch-err",
		short: "test",
		run: func(_ context.Context, _ []string, _, _ io.Writer) error {
			return want
		},
	}
	cli.Register(cmd)

	err := cli.Run(context.Background(), []string{"test-dispatch-err"}, io.Discard, io.Discard, nil)
	if !errors.Is(err, want) {
		t.Errorf("dispatcher should propagate command error; got %v want %v", err, want)
	}
}

// TestRegister_DuplicatePanics is the boot-time hygiene check: two commands
// claiming the same Name() must crash at init time rather than silently
// shadow each other (which would make the second registration win and the
// first one go un-routable without diagnostic).
func TestRegister_DuplicatePanics(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{name: "test-dup-name"}
	cli.Register(cmd)

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on duplicate Register, got none")
		}
	}()
	cli.Register(&fakeCmd{name: "test-dup-name"})
}

// TestLookup pins the public lookup API used by tests (and any future
// programmatic dispatch from outside the cli package).
func TestLookup(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{name: "test-lookup-target"}
	cli.Register(cmd)

	got, ok := cli.Lookup("test-lookup-target")
	if !ok {
		t.Fatal("Lookup returned !ok for a freshly registered command")
	}
	if got.Name() != "test-lookup-target" {
		t.Errorf("Lookup returned wrong command: %q", got.Name())
	}

	if _, ok := cli.Lookup("test-lookup-missing"); ok {
		t.Error("Lookup returned ok for an unregistered name")
	}
}

// TestAll_IsSortedAndIncludesProductionCommands pins both the ordering
// contract (alphabetical so help output is stable) and the fact that the
// production commands (ask / setup / doctor / skills / contexts / profile /
// session / tools / update) are all registered via init().
func TestAll_IsSortedAndIncludesProductionCommands(t *testing.T) {
	t.Parallel()
	all := cli.All()
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.Name()
	}
	// Sortedness.
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("All() returned out-of-order: %q before %q", names[i-1], names[i])
		}
	}
	// Production commands present. Listed inline so a future deletion or
	// rename of any of these is caught here, not at the first user report.
	required := []string{"ask", "contexts", "doctor", "profile", "session", "setup", "skills", "tools", "update"}
	have := make(map[string]bool, len(names))
	for _, n := range names {
		have[n] = true
	}
	for _, r := range required {
		if !have[r] {
			t.Errorf("All() missing production command %q (registered names: %v)", r, names)
		}
	}
}

// TestPrintHelp_ShapeContract pins the structural contract of the help
// banner so a casual edit cannot silently drop the section headers
// operators look for.
func TestPrintHelp_ShapeContract(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	cli.PrintHelp(&out)
	s := out.String()

	for _, want := range []string{
		"cloudy",
		"usage:",
		"cloudy <command> [args]",
		"commands:",
		"common flags (per subcommand):",
		"--model",
		"--skill",
		"--kubeconfig",
		"--context",
		"--no-color",
		"--json",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("help output missing expected fragment %q\n--- output ---\n%s", want, s)
		}
	}
}
