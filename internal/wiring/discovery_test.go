package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
)

// TestRunDiscovery_NoKubeconfig verifies that RunDiscovery degrades gracefully
// when no kubeconfig is reachable: it still returns (possibly empty) findings,
// a non-empty note describing why the hub is missing, and a nil error.
func TestRunDiscovery_NoKubeconfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts := DiscoveryOptions{
		KubeconfigPath: "/nonexistent/kubeconfig",
	}

	findings, note, err := RunDiscovery(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunDiscovery returned err = %v, want nil", err)
	}
	if findings == nil {
		// Coordinator returns nil rather than an empty slice when no
		// detectors emit; both are acceptable here.
		findings = []discovery.Finding{}
	}
	if note == "" {
		t.Fatalf("RunDiscovery note = %q, want non-empty when hub unavailable", note)
	}
	for _, f := range findings {
		// Without a hub and without hints, no External-or-cluster finding
		// should be produced for non-hint backends. Hint-backed findings
		// are tested separately.
		if !f.Source.External {
			t.Errorf("unexpected non-external finding %+v with no hub and no hints", f)
		}
	}
}

// TestRunDiscovery_PropagatesHints checks that HTTPHints with Kind="loki" are
// turned into External Loki findings by the log detector (registered via the
// side-effect import in discovery.go).
func TestRunDiscovery_PropagatesHints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts := DiscoveryOptions{
		KubeconfigPath: "/nonexistent/kubeconfig",
		HTTPHints: []config.HTTPEndpoint{
			{Name: "loki-prod", Kind: "loki", URL: "https://loki.example.com"},
		},
	}

	findings, _, err := RunDiscovery(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunDiscovery err = %v, want nil", err)
	}

	var found bool
	for _, f := range findings {
		if f.Group == discovery.GroupLog && f.Kind == "loki" &&
			f.Source.External && f.Source.ExternalURL == "https://loki.example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RunDiscovery did not propagate loki HTTPHint; got %+v", findings)
	}
}

// TestRunDiscovery_DBHints checks that DBHints with Kind="postgres" are turned
// into External postgres findings by the db detector.
func TestRunDiscovery_DBHints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dsn := "postgres://ro@db.example.com:5432/app?sslmode=require"
	opts := DiscoveryOptions{
		KubeconfigPath: "/nonexistent/kubeconfig",
		DBHints: []config.DatabaseEndpoint{
			{Name: "pg-prod", Kind: "postgres", DSN: dsn},
		},
	}

	findings, _, err := RunDiscovery(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunDiscovery err = %v, want nil", err)
	}

	var found bool
	for _, f := range findings {
		if f.Group == discovery.GroupDB && f.Kind == "postgres" &&
			f.Source.External && strings.Contains(f.Source.ExternalURL, "db.example.com") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RunDiscovery did not propagate postgres DBHint; got %+v", findings)
	}
}
