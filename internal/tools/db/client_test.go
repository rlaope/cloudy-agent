package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools/db"
)

// ---------------------------------------------------------------------------
// TestParseK8sDSN
// ---------------------------------------------------------------------------

func TestParseK8sDSN(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		dsn     string
		wantCtx string
		wantNS  string
		wantSvc string
		wantPort int
		wantOK  bool
	}{
		{
			name:     "full ctx",
			dsn:      "k8s://my-ctx/my-ns/my-svc:5432",
			wantCtx:  "my-ctx",
			wantNS:   "my-ns",
			wantSvc:  "my-svc",
			wantPort: 5432,
			wantOK:   true,
		},
		{
			name:     "empty ctx (default context)",
			dsn:      "k8s:///my-ns/my-svc:5432",
			wantCtx:  "",
			wantNS:   "my-ns",
			wantSvc:  "my-svc",
			wantPort: 5432,
			wantOK:   true,
		},
		{
			name:     "redis port",
			dsn:      "k8s://prod/default/redis-master:6379",
			wantCtx:  "prod",
			wantNS:   "default",
			wantSvc:  "redis-master",
			wantPort: 6379,
			wantOK:   true,
		},
		{
			name:   "malformed — missing port",
			dsn:    "k8s://ctx/ns/svc",
			wantOK: false,
		},
		{
			name:   "malformed — no path segments",
			dsn:    "k8s://ctx/",
			wantOK: false,
		},
		{
			name:   "malformed — wrong scheme",
			dsn:    "postgres://host:5432/db",
			wantOK: false,
		},
		{
			name:   "malformed — garbage",
			dsn:    "not-a-url",
			wantOK: false,
		},
		{
			name:   "malformed — port out of range",
			dsn:    "k8s://ctx/ns/svc:99999",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotCtx, gotNS, gotSvc, gotPort, gotOK := db.ParseK8sDSN(tc.dsn)
			if gotOK != tc.wantOK {
				t.Fatalf("ParseK8sDSN(%q) ok=%v, want %v", tc.dsn, gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotCtx != tc.wantCtx {
				t.Errorf("ctx: got %q, want %q", gotCtx, tc.wantCtx)
			}
			if gotNS != tc.wantNS {
				t.Errorf("namespace: got %q, want %q", gotNS, tc.wantNS)
			}
			if gotSvc != tc.wantSvc {
				t.Errorf("svc: got %q, want %q", gotSvc, tc.wantSvc)
			}
			if gotPort != tc.wantPort {
				t.Errorf("port: got %d, want %d", gotPort, tc.wantPort)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestBuildClients_NonK8sDirectPath
// ---------------------------------------------------------------------------

// TestBuildClients_NonK8sDirectPath verifies that non-k8s DSNs are attempted
// via the direct-dial path regardless of whether hub is nil. The test passes
// a nil hub: that must not cause a panic, and any skip reason must NOT mention
// "k8s DSN requires" (it will mention a dial failure instead, which is fine in
// a test environment with no real DB).
func TestBuildClients_NonK8sDirectPath(t *testing.T) {
	t.Parallel()

	eps := []config.DatabaseEndpoint{
		{Name: "pg-direct", Kind: "postgres", DSN: "postgres://localhost:5432/db?sslmode=disable"},
	}
	_, skips := db.BuildClients(context.Background(), nil, eps)

	for _, s := range skips {
		if strings.Contains(s, "k8s DSN requires") {
			t.Errorf("non-k8s endpoint generated a k8s-hub skip reason: %q", s)
		}
	}
	// The dial will fail (no real DB) — that is expected. What matters is
	// that no panic occurred and no false k8s-hub skip reason was recorded.
}

// ---------------------------------------------------------------------------
// TestBuildClients_K8sDSNWithoutHub
// ---------------------------------------------------------------------------

// TestBuildClients_K8sDSNWithoutHub verifies that a k8s:// DSN with a nil hub
// produces a skip reason containing "k8s DSN requires" and does not produce
// any client entry.
func TestBuildClients_K8sDSNWithoutHub(t *testing.T) {
	t.Parallel()

	eps := []config.DatabaseEndpoint{
		{Name: "pg-k8s", Kind: "postgres", DSN: "k8s://my-ctx/default/postgres-svc:5432"},
	}
	cs, skips := db.BuildClients(context.Background(), nil, eps)

	if !cs.Empty() {
		t.Errorf("expected empty clients when hub is nil, got non-empty: %+v", cs)
	}

	found := false
	for _, s := range skips {
		if strings.Contains(s, "k8s DSN requires") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected skip reason containing \"k8s DSN requires\", got: %v", skips)
	}
}
