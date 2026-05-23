package gitops_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/gitops"
)

func fakeArgoServer(t *testing.T, handlers map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range handlers {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
	}
	return httptest.NewServer(mux)
}

const argoAppsBody = `{
  "items": [
    {
      "metadata": {"name": "guestbook", "namespace": "argocd"},
      "spec": {"project": "default", "source": {"repoURL": "https://github.com/x/y", "path": "manifests", "targetRevision": "HEAD"}},
      "status": {
        "sync":   {"status": "Synced", "revision": "abcdef0123456789"},
        "health": {"status": "Healthy"},
        "operationState": {"finishedAt": "2026-01-01T00:00:00Z"},
        "history": []
      }
    },
    {
      "metadata": {"name": "broken-app", "namespace": "argocd"},
      "spec": {"project": "default", "source": {"repoURL": "https://github.com/x/y", "path": "manifests", "targetRevision": "main"}},
      "status": {
        "sync":   {"status": "OutOfSync", "revision": "deadbeefcafe1234"},
        "health": {"status": "Degraded"},
        "operationState": {"finishedAt": "2026-01-01T01:00:00Z"},
        "history": []
      }
    }
  ]
}`

const argoOneAppBody = `{
  "metadata": {"name": "broken-app", "namespace": "argocd"},
  "spec": {"project": "default", "source": {"repoURL": "https://github.com/x/y", "path": "manifests", "targetRevision": "main"}},
  "status": {
    "sync":   {"status": "OutOfSync", "revision": "deadbeefcafe1234"},
    "health": {"status": "Degraded"},
    "operationState": {"finishedAt": "2026-01-01T01:00:00Z"},
    "history": [
      {"revision": "111111111111", "deployedAt": "2025-12-31T22:00:00Z", "source": {"repoURL": "https://github.com/x/y"}},
      {"revision": "222222222222", "deployedAt": "2026-01-01T01:00:00Z", "source": {"repoURL": "https://github.com/x/y"}}
    ]
  }
}`

// TestListApps_OutOfSyncFirstAndCountedInSummary exercises URL composition
// (GET /api/v1/applications) plus the broken-first sort and counts in the
// summary line.
func TestListApps_OutOfSyncFirstAndCountedInSummary(t *testing.T) {
	t.Parallel()
	srv := fakeArgoServer(t, map[string]string{"/api/v1/applications": argoAppsBody})
	defer srv.Close()

	cs, _ := gitops.BuildClients([]config.ArgoCDEndpoint{{Name: "prod", URL: srv.URL}})
	reg := tools.New()
	gitops.RegisterAll(reg, cs, nil)

	tool, ok := reg.Get("gitops.argo_list_apps")
	if !ok {
		t.Fatal("gitops.argo_list_apps not registered")
	}
	obs, err := tool.Run(context.Background(), []byte(`{"name":"prod"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(obs.Text, "out_of_sync=1") || !strings.Contains(obs.Text, "degraded=1") {
		t.Errorf("expected counts in summary, got %q", obs.Text)
	}
	// broken-app must precede guestbook in the observation text.
	brokenIdx := strings.Index(obs.Text, "broken-app")
	guestIdx := strings.Index(obs.Text, "guestbook")
	if brokenIdx == -1 || guestIdx == -1 || brokenIdx > guestIdx {
		t.Errorf("OutOfSync/Degraded must appear first; broken=%d guest=%d", brokenIdx, guestIdx)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 table rows, got %+v", obs.Table)
	}
	if obs.Table.Rows[0][0] != "broken-app" {
		t.Errorf("first row must be broken-app, got %s", obs.Table.Rows[0][0])
	}
}

// TestAppHistory_NewestFirst verifies /api/v1/applications/{name} read +
// history reversal so newest sync is at index 0.
func TestAppHistory_NewestFirst(t *testing.T) {
	t.Parallel()
	srv := fakeArgoServer(t, map[string]string{
		"/api/v1/applications/broken-app": argoOneAppBody,
	})
	defer srv.Close()

	cs, _ := gitops.BuildClients([]config.ArgoCDEndpoint{{Name: "prod", URL: srv.URL}})
	reg := tools.New()
	gitops.RegisterAll(reg, cs, nil)

	tool, _ := reg.Get("gitops.argo_app_history")
	obs, err := tool.Run(context.Background(), []byte(`{"name":"prod","app":"broken-app"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", obs.Table)
	}
	// Newest (222...) must be row 0 after reversal.
	if obs.Table.Rows[0][1] != "22222222" {
		t.Errorf("expected newest revision first, got %s", obs.Table.Rows[0][1])
	}
	if !strings.Contains(obs.Text, "newest first") {
		t.Errorf("expected ordering hint in text, got %q", obs.Text)
	}
}

// TestBuildClients_EmptyMarksGroupSkipped pins the no-endpoint contract.
func TestBuildClients_EmptyMarksGroupSkipped(t *testing.T) {
	t.Parallel()
	cs, _ := gitops.BuildClients(nil)
	if !cs.Empty() {
		t.Fatal("expected empty clients")
	}
	reg := tools.New()
	gitops.RegisterAll(reg, cs, nil)
	r, ok := reg.Skipped()["gitops"]
	if !ok {
		t.Fatal("expected gitops group skipped")
	}
	if !strings.Contains(r, "no Argo CD endpoint configured") {
		t.Errorf("expected canonical reason, got %q", r)
	}
}
