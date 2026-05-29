package correlate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	tlog "github.com/rlaope/cloudy/internal/core/tools/log"
)

// TestLogSource_MultiEndpointPicksDefault verifies that with more than one Loki
// endpoint configured, RecentChanges no longer errors (it used to pass
// q.Context as the endpoint name) and queries the deterministic default — the
// sorted-first key.
func TestLogSource_MultiEndpointPicksDefault(t *testing.T) {
	emptyResult := `{"data":{"result":[]}}`

	hit := ""
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "loki-1"
		_, _ = w.Write([]byte(emptyResult))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "loki-2"
		_, _ = w.Write([]byte(emptyResult))
	}))
	defer srv2.Close()

	c1, err := httpapi.NewClient("loki-1", srv1.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient loki-1: %v", err)
	}
	c2, err := httpapi.NewClient("loki-2", srv2.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient loki-2: %v", err)
	}

	logs := tlog.Clients{Loki: map[string]*tlog.LokiClient{
		"loki-2": {Client: c2},
		"loki-1": {Client: c1},
	}}
	src := newLogSource(logs, nil)
	if src == nil {
		t.Fatal("newLogSource returned nil")
	}

	// q.Context is the k8s context, NOT an endpoint name — this used to error.
	_, err = src.RecentChanges(context.Background(), change.ChangeQuery{Context: "kind-cloudy-test", Workload: "api"})
	if err != nil {
		t.Fatalf("RecentChanges errored on multi-endpoint map: %v", err)
	}
	if hit != "loki-1" {
		t.Errorf("queried endpoint = %q, want sorted-first %q", hit, "loki-1")
	}
}

func TestIsErrorLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"all good", false},
		{"level=error msg=something", true},
		{"", false},
		{"Fatal: out of memory", true},
		{"panic: runtime error", true},
		{"ERROR: connection refused", true},
		{"   ", false},
		{"info: starting server", false},
	}
	for _, c := range cases {
		got := isErrorLine(c.line)
		if got != c.want {
			t.Errorf("isErrorLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestLogErrorEvents_WithErrors(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0) // later
	t2 := time.Unix(500, 0)  // earliest

	lines := []TimestampedLine{
		{Time: t0, Text: "info: all good"},
		{Time: t1, Text: "ERROR x"},
		{Time: t2, Text: "panic y"},
		{Time: t0, Text: "debug: nothing here"},
	}

	events := logErrorEvents(lines, "myapp")

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Kind != "log_error" {
		t.Errorf("Kind = %q, want %q", e.Kind, "log_error")
	}
	if e.Source != "log" {
		t.Errorf("Source = %q, want %q", e.Source, "log")
	}
	if e.Target != "myapp" {
		t.Errorf("Target = %q, want %q", e.Target, "myapp")
	}
	if !e.Time.Equal(t2) {
		t.Errorf("Time = %v, want earliest error time %v", e.Time, t2)
	}
	// Summary must mention the count (2 error lines: "ERROR x" and "panic y")
	if e.Summary != "2 error line(s) in window" {
		t.Errorf("Summary = %q, want %q", e.Summary, "2 error line(s) in window")
	}
}

func TestLogErrorEvents_NoErrors(t *testing.T) {
	lines := []TimestampedLine{
		{Time: time.Unix(1000, 0), Text: "info: server started"},
		{Time: time.Unix(2000, 0), Text: "debug: request received"},
	}
	events := logErrorEvents(lines, "myapp")
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestLogErrorEvents_Empty(t *testing.T) {
	events := logErrorEvents(nil, "myapp")
	if len(events) != 0 {
		t.Errorf("expected 0 events for nil input, got %d", len(events))
	}
}
