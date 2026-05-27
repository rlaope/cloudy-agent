package permission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// stubTool implements tools.Tool for filter tests.
type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return s.name }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) ReadOnly() bool          { return true }
func (s stubTool) Run(ctx context.Context, _ json.RawMessage) (tools.Observation, error) {
	return tools.Observation{}, nil
}

func newRegistry(names ...string) *tools.Registry {
	r := tools.New()
	for _, n := range names {
		r.MustRegister(stubTool{name: n})
	}
	return r
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	in := &Profile{
		Name:        "payments",
		Description: "payments SRE",
		Contexts:    []string{"prod-eu", "prod-us"},
		Tools: Tools{
			Allow: []string{"k8s.*", "prom.*"},
			Deny:  []string{"jvm.async_profile"},
		},
		Limits: Limits{MaxLogLines: 2000},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load("payments")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Name != in.Name || len(out.Tools.Allow) != 2 || out.Limits.MaxLogLines != 2000 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestLoad_MissingReturnsErrNotFound(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	_, err := Load("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestList_EmptyDir(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	names, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty, got %v", names)
	}
}

func TestList_AlphaOrder(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	for _, n := range []string{"z-last", "a-first", "m-mid"} {
		if err := Save(&Profile{Name: n}); err != nil {
			t.Fatalf("Save %s: %v", n, err)
		}
	}
	names, _ := List()
	want := []string{"a-first", "m-mid", "z-last"}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("order mismatch: want %v, got %v", want, names)
		}
	}
}

func TestActive_EnvWins(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	t.Setenv("CLOUDY_PROFILE", "from-env")
	got, err := Active()
	if err != nil || got != "from-env" {
		t.Fatalf("want from-env, got %q err=%v", got, err)
	}
}

func TestActive_FileFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOUDY_HOME", dir)
	t.Setenv("CLOUDY_PROFILE", "")
	if err := Save(&Profile{Name: "marker"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := SetActive("marker"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	got, _ := Active()
	if got != "marker" {
		t.Fatalf("want marker, got %q", got)
	}
	// Sanity: the marker file lives under CLOUDY_HOME, not Dir().
	if _, err := os.Stat(activeFile()); err != nil {
		t.Fatalf("active marker not written: %v", err)
	}
}

func TestSetActive_RejectsMissing(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	if err := SetActive("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClearActive_Idempotent(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	if err := ClearActive(); err != nil {
		t.Fatalf("clear (missing): %v", err)
	}
}

func TestFilterRegistry_NilProfileReturnsSame(t *testing.T) {
	reg := newRegistry("k8s.list_pods", "prom.query")
	got := FilterRegistry(reg, nil)
	if got != reg {
		t.Fatalf("nil profile must return registry unchanged")
	}
}

func TestFilterRegistry_AllowGlob(t *testing.T) {
	reg := newRegistry("k8s.list_pods", "k8s.logs", "prom.query", "jvm.jcmd_gc")
	out := FilterRegistry(reg, &Profile{Tools: Tools{Allow: []string{"k8s.*"}}})
	if _, ok := out.Get("k8s.list_pods"); !ok {
		t.Errorf("k8s.list_pods should pass")
	}
	if _, ok := out.Get("prom.query"); ok {
		t.Errorf("prom.query should be filtered")
	}
	if _, ok := out.Get("jvm.jcmd_gc"); ok {
		t.Errorf("jvm.jcmd_gc should be filtered")
	}
}

func TestFilterRegistry_DenyWins(t *testing.T) {
	reg := newRegistry("jvm.jcmd_gc", "jvm.async_profile")
	out := FilterRegistry(reg, &Profile{Tools: Tools{
		Allow: []string{"jvm.*"},
		Deny:  []string{"jvm.async_profile"},
	}})
	if _, ok := out.Get("jvm.jcmd_gc"); !ok {
		t.Errorf("jvm.jcmd_gc should pass")
	}
	if _, ok := out.Get("jvm.async_profile"); ok {
		t.Errorf("jvm.async_profile must be denied")
	}
}

func TestFilterRegistry_BothEmptyReturnsSame(t *testing.T) {
	reg := newRegistry("k8s.list_pods")
	if got := FilterRegistry(reg, &Profile{}); got != reg {
		t.Errorf("empty allow+deny must return registry unchanged")
	}
}

func TestMatchNamespace_NilProfile(t *testing.T) {
	if err := MatchNamespace(nil, "anything"); err != nil {
		t.Errorf("nil profile must permit any namespace, got %v", err)
	}
}

func TestMatchNamespace_EmptyNamespace(t *testing.T) {
	p := &Profile{Namespaces: Namespaces{Allow: []string{"prod-*"}}}
	if err := MatchNamespace(p, ""); err != nil {
		t.Errorf("empty namespace must be permissive, got %v", err)
	}
}

func TestMatchNamespace_DenyWins(t *testing.T) {
	p := &Profile{Namespaces: Namespaces{
		Allow: []string{"*"},
		Deny:  []string{"kube-system"},
	}}
	if err := MatchNamespace(p, "kube-system"); !errors.Is(err, ErrNamespaceDenied) {
		t.Errorf("want ErrNamespaceDenied, got %v", err)
	}
}

func TestMatchNamespace_AllowGlob(t *testing.T) {
	p := &Profile{Namespaces: Namespaces{Allow: []string{"prod-*"}}}
	if err := MatchNamespace(p, "prod-eu"); err != nil {
		t.Errorf("prod-eu should match allow glob, got %v", err)
	}
	if err := MatchNamespace(p, "staging"); !errors.Is(err, ErrNamespaceNotAllowed) {
		t.Errorf("staging should be rejected, got %v", err)
	}
}

func TestMatchNamespace_EmptyAllowAndDeny(t *testing.T) {
	p := &Profile{}
	if err := MatchNamespace(p, "anything"); err != nil {
		t.Errorf("empty allow+deny must permit, got %v", err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*", "anything", true},
		{"k8s.*", "k8s.list_pods", true},
		{"k8s.*", "prom.query", false},
		{"prom.query", "prom.query", true},
		{"prom.query", "prom.query_range", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.name); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", c.pat, c.name, got, c.want)
		}
	}
}
