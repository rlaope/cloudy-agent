package correlate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestLokiSelector_NamespaceScoping verifies the LogQL selector folds in
// namespace=%q only when ChangeQuery.Namespace is set.
func TestLokiSelector_NamespaceScoping(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"data":{"result":[]}}`))
	}))
	defer srv.Close()

	c, err := httpapi.NewClient("loki-1", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	logs := tlog.Clients{Loki: map[string]*tlog.LokiClient{"loki-1": {Client: c}}}
	src := newLogSource(logs, nil)

	if _, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "api"}); err != nil {
		t.Fatalf("RecentChanges (no ns): %v", err)
	}
	if !strings.HasPrefix(gotQuery, `{app="api"} |~`) {
		t.Errorf("no-namespace selector = %q, want {app=\"api\"} prefix", gotQuery)
	}

	if _, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "api", Namespace: "prod"}); err != nil {
		t.Fatalf("RecentChanges (ns): %v", err)
	}
	if !strings.HasPrefix(gotQuery, `{app="api", namespace="prod"} |~`) {
		t.Errorf("namespace selector = %q, want app+namespace prefix", gotQuery)
	}
}

// TestESErrorQuery verifies the Elasticsearch URI query string includes the
// time range and error-level clause always, and the namespace clause only when
// set.
func TestESErrorQuery(t *testing.T) {
	start := time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	q := esErrorQuery("checkout", "", start, end)
	if !strings.Contains(q, "@timestamp:[2026-05-28T11:00:00Z TO 2026-05-28T12:00:00Z]") {
		t.Errorf("missing time range: %q", q)
	}
	if !strings.Contains(q, `"checkout"`) || !strings.Contains(q, "level:ERROR") {
		t.Errorf("missing workload/level clause: %q", q)
	}
	if strings.Contains(q, "kubernetes.namespace_name") {
		t.Errorf("namespace clause leaked without namespace: %q", q)
	}

	qns := esErrorQuery("checkout", "prod", start, end)
	if !strings.Contains(qns, `kubernetes.namespace_name:"prod"`) {
		t.Errorf("missing namespace clause: %q", qns)
	}
}

func TestParseESTimestamps(t *testing.T) {
	body := []byte(`{"hits":{"hits":[
		{"_source":{"@timestamp":"2026-05-28T12:00:05Z"}},
		{"_source":{"@timestamp":"2026-05-28T12:00:01Z"}},
		{"_source":{"@timestamp":"not-a-time"}},
		{"_source":{"message":"no timestamp field"}}
	]}}`)
	times, err := parseESTimestamps(body, "@timestamp")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(times) != 2 {
		t.Fatalf("len = %d, want 2 (bad/absent timestamps skipped)", len(times))
	}
}

func TestESLogErrorEvents(t *testing.T) {
	t0 := time.Date(2026, 5, 28, 12, 0, 5, 0, time.UTC)
	t1 := time.Date(2026, 5, 28, 12, 0, 1, 0, time.UTC) // earliest
	got := esLogErrorEvents([]time.Time{t0, t1}, "checkout")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	e := got[0]
	if e.Kind != "log_error" || e.Source != "log" || e.Target != "checkout" {
		t.Errorf("event = %+v, want log_error/log/checkout", e)
	}
	if !e.Time.Equal(t1) {
		t.Errorf("Time = %v, want earliest %v", e.Time, t1)
	}
	if e.Summary != "2 error log(s) in window" {
		t.Errorf("Summary = %q", e.Summary)
	}
	if got := esLogErrorEvents(nil, "checkout"); len(got) != 0 {
		t.Errorf("nil input: want 0 events, got %d", len(got))
	}
}

// TestLogSource_ESOnly drives the full ES path against a canned _search
// response, verifying an ES-only deployment now yields log symptoms.
func TestLogSource_ESOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":{"hits":[
			{"_source":{"@timestamp":"2026-05-28T12:00:01Z"}},
			{"_source":{"@timestamp":"2026-05-28T12:00:09Z"}}
		]}}`))
	}))
	defer srv.Close()

	c, err := httpapi.NewClient("es-1", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	logs := tlog.Clients{ES: map[string]*tlog.ESClient{"es-1": {Client: c}}}
	src := newLogSource(logs, nil)
	if src == nil {
		t.Fatal("newLogSource returned nil for ES-only clients")
	}

	events, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "log_error" {
		t.Fatalf("events = %+v, want one log_error", events)
	}
	if !events[0].Time.Equal(time.Date(2026, 5, 28, 12, 0, 1, 0, time.UTC)) {
		t.Errorf("Time = %v, want earliest", events[0].Time)
	}
}
