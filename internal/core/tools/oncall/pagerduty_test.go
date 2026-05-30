package oncall

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// fakePD returns an httptest.Server mapping path -> JSON body, asserting GET.
func fakePD(t *testing.T, handlers map[string]string) *httptest.Server {
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

func pdClient(t *testing.T, url string) map[string]*PagerDutyClient {
	t.Helper()
	hc, err := httpapi.NewClient("test", url, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return map[string]*PagerDutyClient{"test": {Client: hc}}
}

const incidentsBody = `{
  "incidents": [
    {
      "incident_number": 42, "title": "checkout 5xx surge", "status": "triggered",
      "urgency": "high", "created_at": "2026-05-30T01:00:00Z",
      "service": {"summary": "checkout"},
      "assignments": [{"assignee": {"summary": "Alice"}}]
    },
    {
      "incident_number": 41, "title": "disk filling", "status": "acknowledged",
      "urgency": "low", "created_at": "2026-05-30T00:30:00Z",
      "service": {"summary": "node-1"},
      "assignments": [{"assignee": {"summary": "Bob"}}]
    }
  ]
}`

func TestListIncidents_ParsesAndRanksTriggeredFirst(t *testing.T) {
	srv := fakePD(t, map[string]string{"/incidents": incidentsBody})
	defer srv.Close()

	reg := tools.New()
	RegisterAll(reg, Clients{PagerDuty: pdClient(t, srv.URL)}, nil)

	tool, ok := reg.Get("oncall.list_incidents")
	if !ok {
		t.Fatal("oncall.list_incidents not registered")
	}
	obs, err := tool.Run(context.Background(), []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "triggered=1") || !strings.Contains(obs.Text, "acknowledged=1") {
		t.Errorf("expected status counts, got %q", obs.Text)
	}
	// Triggered/high must sort above acknowledged/low.
	hi := strings.Index(obs.Text, "checkout 5xx surge")
	lo := strings.Index(obs.Text, "disk filling")
	if hi == -1 || lo == -1 || hi > lo {
		t.Errorf("triggered incident must rank first; hi=%d lo=%d", hi, lo)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 table rows, got %+v", obs.Table)
	}
	if obs.Table.Rows[0][1] != "triggered" {
		t.Errorf("first row should be the triggered incident, got %q", obs.Table.Rows[0][1])
	}
}

const oncallsBody = `{
  "oncalls": [
    {"escalation_level": 2, "user": {"summary": "Carol"},
     "escalation_policy": {"summary": "Payments"}, "schedule": {"summary": "Secondary"}},
    {"escalation_level": 1, "user": {"summary": "Dave"},
     "escalation_policy": {"summary": "Payments"}, "schedule": {"summary": "Primary"}}
  ]
}`

func TestWhoIsOnCall_OrdersByLevelAndFiltersMaxLevel(t *testing.T) {
	srv := fakePD(t, map[string]string{"/oncalls": oncallsBody})
	defer srv.Close()

	reg := tools.New()
	RegisterAll(reg, Clients{PagerDuty: pdClient(t, srv.URL)}, nil)
	tool, _ := reg.Get("oncall.who_is_oncall")

	obs, err := tool.Run(context.Background(), []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Level 1 (primary) must render before level 2 within the same policy.
	primary := strings.Index(obs.Text, "Dave")
	secondary := strings.Index(obs.Text, "Carol")
	if primary == -1 || secondary == -1 || primary > secondary {
		t.Errorf("primary (L1) should sort first; dave=%d carol=%d", primary, secondary)
	}

	// max_level=2 drops the level-1 responder.
	obs2, err := tool.Run(context.Background(), []byte(`{"name":"test","max_level":2}`))
	if err != nil {
		t.Fatalf("Run max_level: %v", err)
	}
	if strings.Contains(obs2.Text, "Dave") {
		t.Errorf("max_level=2 must drop the L1 on-call, got %q", obs2.Text)
	}
	if !strings.Contains(obs2.Text, "Carol") {
		t.Errorf("max_level=2 must keep the L2 on-call, got %q", obs2.Text)
	}
}

func TestBuildClients_EmptyMarksGroupSkipped(t *testing.T) {
	cs, _ := BuildClients(nil)
	if !cs.Empty() {
		t.Fatal("expected empty clients")
	}
	reg := tools.New()
	RegisterAll(reg, cs, nil)
	if r, ok := reg.Skipped()["oncall"]; !ok || !strings.Contains(r, "no PagerDuty account configured") {
		t.Errorf("expected oncall group skipped with canonical reason, got %q", r)
	}
}

func TestIncidentStatuses(t *testing.T) {
	cases := map[string][]string{
		"":          {"triggered", "acknowledged"},
		"open":      {"triggered", "acknowledged"},
		"triggered": {"triggered"},
		"acked":     {"acknowledged"},
		"all":       nil,
		"nonsense":  {"triggered", "acknowledged"},
	}
	for in, want := range cases {
		got := incidentStatuses(in)
		if len(got) != len(want) {
			t.Errorf("incidentStatuses(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("incidentStatuses(%q) = %v, want %v", in, got, want)
			}
		}
	}
}
