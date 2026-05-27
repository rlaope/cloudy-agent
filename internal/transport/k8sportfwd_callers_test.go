package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// allowedPortforwardCallers is the explicit allow-list of production files
// (non-test, non-worktree, non-self) that may call
// `transport.OpenPortForward`. The function is the ONE documented exception
// to the read-only HTTP contract: the SPDY upgrade handshake POSTs to
// `pods/portforward` outside `ReadOnlyRoundTripper` (see docs/SAFETY.md
// "Documented exception: SPDY portforward upgrade").
//
// This test fails the build if a new production caller appears. The intent:
// any future contributor who wants to add a second OpenPortForward call
// MUST update this slice AND has been forced to read the SAFETY.md
// exception block to do so — which is the closest thing to a human-loop
// review the test layer can enforce.
//
// Allow-list entries are repo-relative POSIX paths.
var allowedPortforwardCallers = []string{
	"internal/core/tools/db/client.go",
}

// TestOpenPortForward_CallerAllowList is the bounded-exception regression
// flagged by the v0.5 security review (H-1). It walks every production .go
// file under internal/ and asserts the call-site set has not expanded.
func TestOpenPortForward_CallerAllowList(t *testing.T) {
	repoRoot, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	want := make(map[string]struct{}, len(allowedPortforwardCallers))
	for _, p := range allowedPortforwardCallers {
		want[p] = struct{}{}
	}

	got := map[string]struct{}{}
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(filepath.Join(repoRoot, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip subagent worktrees IF they appear inside the walked
			// tree. We compare the basename, not the full path — using
			// strings.Contains(path, ".claude/worktrees") used to fire
			// on the walk's root itself when the test ran from a git
			// worktree at .claude/worktrees/agent-*/internal, causing
			// SkipDir at root and the entire walk to return zero files.
			if d.Name() == "worktrees" || strings.HasSuffix(path, "/.claude") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Skip the package that defines OpenPortForward itself.
		if strings.HasSuffix(path, "internal/transport/k8sportfwd.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			// A parse error here would be flagged by the regular Go build
			// long before this test runs, but propagate it just in case.
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "transport" && sel.Sel.Name == "OpenPortForward" {
				rel, _ := filepath.Rel(repoRoot, path)
				got[filepath.ToSlash(rel)] = struct{}{}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}

	missing := setSubtract(want, got)
	unexpected := setSubtract(got, want)

	if len(missing) > 0 {
		t.Errorf("allow-list lists callers that no longer exist; remove from allowedPortforwardCallers: %v", missing)
	}
	if len(unexpected) > 0 {
		t.Errorf(
			"new caller(s) of transport.OpenPortForward detected: %v.\n"+
				"This is the documented SPDY exception to the read-only contract.\n"+
				"Before adding the file(s) to allowedPortforwardCallers, read\n"+
				"docs/SAFETY.md \"Documented exception: SPDY portforward upgrade\"\n"+
				"and confirm the new call site cannot be reached from an LLM-supplied tool argument.",
			unexpected,
		)
	}
}

// repoRoot walks up from this test's working directory until it finds a
// go.mod, so the test is robust to running from any package directory.
func repoRoot() (string, error) {
	dir, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}
	for {
		if _, err := filepath.Glob(filepath.Join(dir, "go.mod")); err == nil {
			if matches, _ := filepath.Glob(filepath.Join(dir, "go.mod")); len(matches) > 0 {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", filepath.ErrBadPattern // no go.mod found anywhere up the tree
		}
		dir = parent
	}
}

func setSubtract(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, in := b[k]; !in {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
