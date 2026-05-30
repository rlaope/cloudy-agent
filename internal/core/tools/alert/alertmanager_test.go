package alert_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/alert"
)

// fakeAMServer returns an httptest.Server whose handlers map path -> body.
func fakeAMServer(t *testing.T, handlers map[string]string) *httptest.Server {
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

const v2AlertsBody = `[
  {
    "labels": {"alertname": "HighErrorRate", "severity": "critical", "service": "checkout"},
    "annotations": {"summary": "5xx ratio > 5%"},
    "startsAt": "2026-01-01T00:00:00Z",
    "endsAt":   "2026-01-01T00:30:00Z",
    "status":   {"state": "active", "silencedBy": [], "inhibitedBy": []}
  },
  {
    "labels": {"alertname": "DiskFillingUp", "severity": "warning", "instance": "node-1"},
    "annotations": {"summary": "Disk > 80%"},
    "startsAt": "2026-01-01T00:10:00Z",
    "endsAt":   "2026-01-01T01:00:00Z",
    "status":   {"state": "suppressed", "silencedBy": ["abcd"], "inhibitedBy": []}
  }
]`

const v2SilencesBody = `[
  {
    "id": "abcd",
    "matchers": [
      {"name": "alertname", "value": "DiskFillingUp", "isRegex": false, "isEqual": true}
    ],
    "startsAt": "2026-01-01T00:05:00Z",
    "endsAt":   "2026-01-01T02:00:00Z",
    "createdBy": "alice",
    "comment":   "planned maintenance",
    "status":    {"state": "active"}
  }
]`

// TestListActive_ParsesV2AlertsAndOrdersFiringFirst exercises URL composition
// (GET /api/v2/alerts) plus the firing-first sort and label flattening.
func TestListActive_ParsesV2AlertsAndOrdersFiringFirst(t *testing.T) {
	t.Parallel()
	srv := fakeAMServer(t, map[string]string{"/api/v2/alerts": v2AlertsBody})
	defer srv.Close()

	cs, skips := alert.BuildClients(
		[]config.AlertmanagerEndpoint{{Name: "test", URL: srv.URL}},
		nil,
	)
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	reg := tools.New()
	alert.RegisterAll(reg, cs, nil)

	tool, ok := reg.Get("alert.list_active")
	if !ok {
		t.Fatal("alert.list_active not registered")
	}
	args, _ := json.Marshal(map[string]any{"name": "test", "limit": 10})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(obs.Text, "firing=1") || !strings.Contains(obs.Text, "suppressed=1") {
		t.Errorf("expected counts in text, got %q", obs.Text)
	}
	if !strings.Contains(obs.Text, "HighErrorRate") {
		t.Errorf("expected active alert name in text, got %q", obs.Text)
	}
	// Firing must appear before suppressed in the observation text.
	highIdx := strings.Index(obs.Text, "HighErrorRate")
	diskIdx := strings.Index(obs.Text, "DiskFillingUp")
	if highIdx == -1 || diskIdx == -1 || highIdx > diskIdx {
		t.Errorf("firing must appear before suppressed; high=%d disk=%d", highIdx, diskIdx)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 table rows, got %+v", obs.Table)
	}
	if obs.Table.Rows[0][0] != "active" {
		t.Errorf("first row state must be active, got %s", obs.Table.Rows[0][0])
	}
}

// TestListSilences_RendersMatchers verifies /api/v2/silences URL composition
// plus the {name="value"} matcher rendering.
func TestListSilences_RendersMatchers(t *testing.T) {
	t.Parallel()
	srv := fakeAMServer(t, map[string]string{"/api/v2/silences": v2SilencesBody})
	defer srv.Close()

	cs, _ := alert.BuildClients(
		[]config.AlertmanagerEndpoint{{Name: "test", URL: srv.URL}},
		nil,
	)
	reg := tools.New()
	alert.RegisterAll(reg, cs, nil)

	tool, _ := reg.Get("alert.list_silences")
	obs, err := tool.Run(context.Background(), []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(obs.Text, `alertname="DiskFillingUp"`) {
		t.Errorf("expected matcher rendering in text, got %q", obs.Text)
	}
	if !strings.Contains(obs.Text, "alice") || !strings.Contains(obs.Text, "planned maintenance") {
		t.Errorf("expected creator and comment in text, got %q", obs.Text)
	}
}

// TestBuildClients_EmptyMarksGroupSkipped pins the conditional-registration
// contract: no Alertmanager + no Prometheus = group skipped with the
// canonical reason the architect spec calls for.
func TestBuildClients_EmptyMarksGroupSkipped(t *testing.T) {
	t.Parallel()
	cs, _ := alert.BuildClients(nil, nil)
	if !cs.Empty() {
		t.Fatalf("expected empty clients")
	}
	reg := tools.New()
	alert.RegisterAll(reg, cs, nil)
	r, ok := reg.Skipped()["alert"]
	if !ok {
		t.Fatal("expected alert group skipped")
	}
	if !strings.Contains(r, "no Alertmanager endpoint configured") {
		t.Errorf("expected canonical reason, got %q", r)
	}
}

// TestRegisterAll_PromOnlyMarksAMSubGroupSkipped pins the partial-wiring fix:
// with Prometheus configured but no Alertmanager, alert.list_rules registers
// while the absent Alertmanager backend is marked skipped under the `alert-am`
// sub-group key — so skill validation's isInSkippedGroup treats
// alert.list_active / alert.list_silences as configurably-absent instead of
// leaking "references unknown tool" on every prom-only startup.
func TestRegisterAll_PromOnlyMarksAMSubGroupSkipped(t *testing.T) {
	t.Parallel()
	cs, _ := alert.BuildClients(
		nil, // no Alertmanager
		[]config.PrometheusEndpoint{{Name: "prom", URL: "http://prom:9090"}},
	)
	if cs.Empty() {
		t.Fatal("clients should not be empty (prom is wired)")
	}
	reg := tools.New()
	alert.RegisterAll(reg, cs, nil)

	// The prom-backed tool is present; the group is NOT wholesale-skipped.
	if _, ok := reg.Get("alert.list_rules"); !ok {
		t.Error("alert.list_rules should be registered when prom is wired")
	}
	if _, ok := reg.Skipped()["alert"]; ok {
		t.Error("alert group should not be wholesale-skipped when prom is wired")
	}
	// The absent Alertmanager backend carries a sub-group skip key.
	if _, ok := reg.Skipped()["alert-am"]; !ok {
		t.Error("expected alert-am sub-group skip key when Alertmanager is absent")
	}
}
