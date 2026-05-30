package synthetic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/transport"
)

func runCheck(t *testing.T, args string) tools.Observation {
	t.Helper()
	reg := tools.New()
	RegisterAll(reg)
	tool, ok := reg.Get("synthetic.http_check")
	if !ok {
		t.Fatal("synthetic.http_check not registered")
	}
	obs, err := tool.Run(context.Background(), []byte(args))
	if err != nil {
		t.Fatalf("Run(%s): %v", args, err)
	}
	return obs
}

func TestHTTPCheck_UpOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	obs := runCheck(t, `{"url":"`+srv.URL+`"}`)
	if !strings.Contains(obs.Text, "UP") || !strings.Contains(obs.Text, "status=200") {
		t.Errorf("expected UP/200, got %q", obs.Text)
	}
	if obs.Table == nil || obs.Table.Rows[0][1] != "UP" {
		t.Errorf("expected UP verdict in table, got %+v", obs.Table)
	}
}

func TestHTTPCheck_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Default expectation (any 2xx) → a 500 is UNEXPECTED, but still reachable.
	obs := runCheck(t, `{"url":"`+srv.URL+`"}`)
	if !strings.Contains(obs.Text, "UNEXPECTED") || !strings.Contains(obs.Text, "status=500") {
		t.Errorf("expected UNEXPECTED/500, got %q", obs.Text)
	}
}

func TestHTTPCheck_ExpectStatusMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted) // 202
	}))
	defer srv.Close()

	obs := runCheck(t, `{"url":"`+srv.URL+`","expect_status":202}`)
	if !strings.Contains(obs.Text, "UP") {
		t.Errorf("202 with expect_status=202 should be UP, got %q", obs.Text)
	}
}

func TestHTTPCheck_DownOnUnreachable(t *testing.T) {
	// Closed server → connection refused → DOWN verdict (not a tool error).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	obs := runCheck(t, `{"url":"`+url+`","timeout_seconds":2}`)
	if !strings.Contains(obs.Text, "DOWN") {
		t.Errorf("expected DOWN for an unreachable host, got %q", obs.Text)
	}
}

func TestHTTPCheck_RejectsNonReadMethod(t *testing.T) {
	reg := tools.New()
	RegisterAll(reg)
	tool, _ := reg.Get("synthetic.http_check")
	_, err := tool.Run(context.Background(), []byte(`{"url":"http://example.com","method":"POST"}`))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("POST must be rejected as read-only, got err=%v", err)
	}
}

// TestHTTPCheck_BlocksLinkLocalMetadata is the SSRF regression: a probe aimed
// at the cloud-metadata address must be refused at dial time (DOWN verdict with
// the guard reason), so an LLM-chosen URL can't exfiltrate IAM credentials.
func TestHTTPCheck_BlocksLinkLocalMetadata(t *testing.T) {
	obs := runCheck(t, `{"url":"http://169.254.169.254/latest/meta-data/","timeout_seconds":2}`)
	if !strings.Contains(obs.Text, "DOWN") {
		t.Fatalf("metadata probe must be DOWN, got %q", obs.Text)
	}
	if !strings.Contains(obs.Text, "link-local") {
		t.Errorf("expected the link-local guard reason, got %q", obs.Text)
	}
}

// TestTransportBackstop_RefusesNonReadVerb proves the second layer the package
// doc advertises: even if the tool's GET/HEAD switch were bypassed, the shared
// transport refuses a non-read verb. Exercises the guarded transport directly.
func TestTransportBackstop_RefusesNonReadVerb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	client := &http.Client{Transport: guardedTransport()}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	_, err := client.Do(req)
	if err == nil || !errors.Is(err, transport.ErrReadOnlyViolation) {
		t.Fatalf("transport must refuse POST with ErrReadOnlyViolation, got %v", err)
	}
}

func TestHTTPCheck_RejectsBadURL(t *testing.T) {
	reg := tools.New()
	RegisterAll(reg)
	tool, _ := reg.Get("synthetic.http_check")
	for _, bad := range []string{`{"url":""}`, `{"url":"ftp://x"}`, `{"url":"not a url"}`} {
		if _, err := tool.Run(context.Background(), []byte(bad)); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}
